package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed index.html
var indexHTML string

// Row is one verdict as the Connect sink stored it.
type Row struct {
	RevID         int64     `db:"rev_id" json:"rev_id"`
	RevParentID   int64     `db:"rev_parent_id" json:"rev_parent_id"`
	Title         string    `db:"title" json:"title"`
	Editor        string    `db:"editor" json:"editor"`
	EditorIsTemp  bool      `db:"editor_is_temp" json:"editor_is_temp"`
	Comment       string    `db:"comment" json:"comment"`
	BytesDelta    int64     `db:"bytes_delta" json:"bytes_delta"`
	EventTS       time.Time `db:"event_ts" json:"event_ts"`
	DiffURL       string    `db:"diff_url" json:"diff_url"`
	Tier          string    `db:"tier" json:"tier"`
	Diff          string    `db:"diff" json:"diff"`
	DiffTruncated bool      `db:"diff_truncated" json:"diff_truncated"`
	Label         string    `db:"label" json:"label"`
	Confidence    float64   `db:"confidence" json:"confidence"`
	Reason        string    `db:"reason" json:"reason"`
	Evidence      string    `db:"evidence" json:"evidence"`
	Grounded      bool      `db:"grounded" json:"grounded"`
	Route         string    `db:"route" json:"route"`
	Steps         []string  `db:"steps" json:"steps"`
	Model         string    `db:"model" json:"model"`
	Attempts      int       `db:"attempts" json:"attempts"`
	Tokens        int       `db:"tokens" json:"tokens"`
	LatencyMS     int64     `db:"latency_ms" json:"latency_ms"`
	ReasonedAt    time.Time `db:"reasoned_at" json:"reasoned_at"`
}

type Stats struct {
	Total   int            `json:"total"`
	ByRoute map[string]int `json:"by_route"`
	ByLabel map[string]int `json:"by_label"`
}

type Filter struct {
	Route         string
	Label         string
	MinConfidence float64
	Limit         int
	After         time.Time
}

type Server struct {
	DB   *pgxpool.Pool
	Log  *slog.Logger
	tmpl *template.Template
}

func NewServer(db *pgxpool.Pool, log *slog.Logger) *Server {
	funcs := template.FuncMap{
		"pct":    func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
		"since":  func(t time.Time) string { return time.Since(t).Round(time.Second).String() + " ago" },
		"cursor": func(t time.Time) string { return t.Format(time.RFC3339Nano) },
	}
	return &Server{DB: db, Log: log, tmpl: template.Must(template.New("index").Funcs(funcs).Parse(indexHTML))}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/stats", s.apiStats)
	mux.HandleFunc("GET /api/verdicts", s.apiList)
	mux.HandleFunc("GET /api/verdicts/{rev_id}", s.apiGet)
	mux.HandleFunc("GET /fragments/stats", s.fragmentStats)
	mux.HandleFunc("GET /fragments/rows", s.fragmentRows)
	mux.HandleFunc("GET /{$}", s.index)
	return mux
}

func parseFilter(q url.Values) (Filter, error) {
	f := Filter{Route: q.Get("route"), Label: q.Get("label"), Limit: 50}
	if v := q.Get("min_confidence"); v != "" {
		c, err := strconv.ParseFloat(v, 64)
		if err != nil || c < 0 || c > 1 {
			return f, fmt.Errorf("min_confidence must be a number from 0 to 1, got %q", v)
		}
		f.MinConfidence = c
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return f, fmt.Errorf("limit must be from 1 to 500, got %q", v)
		}
		f.Limit = n
	}
	if v := q.Get("after"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return f, fmt.Errorf("after must be an RFC 3339 timestamp, got %q", v)
		}
		f.After = t
	}
	return f, nil
}

const selectRows = `SELECT rev_id, rev_parent_id, title, editor, editor_is_temp, comment, bytes_delta, event_ts, diff_url, tier,
	diff, diff_truncated, label, confidence, reason, evidence, grounded, route, steps, model, attempts, tokens, latency_ms, reasoned_at
	FROM verdicts`

func (s *Server) list(ctx context.Context, f Filter) ([]Row, error) {
	rows, err := s.DB.Query(ctx, selectRows+`
		WHERE ($1 = '' OR route = $1) AND ($2 = '' OR label = $2) AND confidence >= $3 AND reasoned_at > $4
		ORDER BY reasoned_at DESC LIMIT $5`, f.Route, f.Label, f.MinConfidence, f.After, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Row])
}

func (s *Server) get(ctx context.Context, revID int64) (Row, error) {
	rows, err := s.DB.Query(ctx, selectRows+` WHERE rev_id = $1`, revID)
	if err != nil {
		return Row{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Row])
}

func (s *Server) stats(ctx context.Context) (Stats, error) {
	st := Stats{ByRoute: map[string]int{}, ByLabel: map[string]int{}}
	rows, err := s.DB.Query(ctx, `SELECT route, label, count(*) FROM verdicts GROUP BY route, label`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var route, label string
		var n int
		if err := rows.Scan(&route, &label, &n); err != nil {
			return st, err
		}
		st.Total += n
		st.ByRoute[route] += n
		st.ByLabel[label] += n
	}
	return st, rows.Err()
}

func (s *Server) apiStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.stats(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, st)
}

func (s *Server) apiList(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.list(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	if rows == nil {
		rows = []Row{}
	}
	writeJSON(w, rows)
}

func (s *Server) apiGet(w http.ResponseWriter, r *http.Request) {
	revID, err := strconv.ParseInt(r.PathValue("rev_id"), 10, 64)
	if err != nil {
		http.Error(w, "rev_id must be an integer", http.StatusBadRequest)
		return
	}
	row, err := s.get(r.Context(), revID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "no verdict for that revision", http.StatusNotFound)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, row)
}

type page struct {
	Filter Filter
	Stats  Stats
	Rows   []Row
	Cursor string
	Error  string
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := page{Filter: f}
	if p.Stats, err = s.stats(r.Context()); err == nil {
		p.Rows, err = s.list(r.Context(), f)
	}
	if err != nil {
		// Before the sink has connected once the table does not exist yet; the page should say so, not 500.
		s.Log.Warn("query failed", "err", err)
		p.Error = err.Error()
	}
	if len(p.Rows) > 0 {
		p.Cursor = p.Rows[0].ReasonedAt.Format(time.RFC3339Nano)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.Execute(w, p); err != nil {
		s.Log.Error("render", "err", err)
	}
}

// The fragments render the same templates the page does, so a row looks the same whether it arrived with
// the page or was inserted later.
func (s *Server) fragmentStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.stats(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "stats", st); err != nil {
		s.Log.Error("render", "err", err)
	}
}

func (s *Server) fragmentRows(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.list(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "rows", rows); err != nil {
		s.Log.Error("render", "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.Log.Error("query failed", "err", err)
	http.Error(w, "database query failed: "+err.Error(), http.StatusServiceUnavailable)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
