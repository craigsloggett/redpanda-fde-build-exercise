package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed web
var webFS embed.FS

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
	DB     *pgxpool.Pool
	Events *Broker
	Log    *slog.Logger
	tmpl   *template.Template
}

func NewServer(pool *pgxpool.Pool, log *slog.Logger) *Server {
	funcs := template.FuncMap{
		"pct":    func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
		"since":  func(t time.Time) string { return since(time.Since(t)) },
		"cursor": func(t time.Time) string { return t.Format(time.RFC3339Nano) },
	}

	return &Server{DB: pool, Events: newBroker(pool, log), Log: log, tmpl: template.Must(template.New("index.html").Funcs(funcs).ParseFS(webFS, "web/index.html"))}
}

const day = 24 * time.Hour

func since(age time.Duration) string {
	age = age.Round(time.Second)

	switch {
	case age <= 0:
		return "now"
	case age < time.Minute:
		return ago(age, time.Second, "second")
	case age < time.Hour:
		return ago(age, time.Minute, "minute")
	case age < day:
		return ago(age, time.Hour, "hour")
	case age < 2*day:
		return "yesterday"
	default:
		return ago(age, day, "day")
	}
}

func ago(age, unit time.Duration, name string) string {
	count := int64(age / unit)
	if count == 1 {
		return "1 " + name + " ago"
	}

	return fmt.Sprintf("%d %ss ago", count, name)
}

func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("GET /api/stats", s.apiStats)
	mux.HandleFunc("GET /api/verdicts", s.apiList)
	mux.HandleFunc("GET /api/verdicts/{rev_id}", s.apiGet)
	mux.HandleFunc("GET /fragments/stats", s.fragmentStats)
	mux.HandleFunc("GET /fragments/rows", s.fragmentRows)
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	return mux
}

var errFilter = errors.New("invalid filter")

func parseFilter(query url.Values) (Filter, error) {
	filter := Filter{Route: query.Get("route"), Label: query.Get("label"), Limit: 50}

	if text := query.Get("min_confidence"); text != "" {
		confidence, err := strconv.ParseFloat(text, 64)
		if err != nil || confidence < 0 || confidence > 1 {
			return filter, fmt.Errorf("%w: min_confidence must be a number from 0 to 1, got %q", errFilter, text)
		}

		filter.MinConfidence = confidence
	}

	if text := query.Get("limit"); text != "" {
		limit, err := strconv.Atoi(text)
		if err != nil || limit < 1 || limit > 500 {
			return filter, fmt.Errorf("%w: limit must be from 1 to 500, got %q", errFilter, text)
		}

		filter.Limit = limit
	}

	if text := query.Get("after"); text != "" {
		after, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return filter, fmt.Errorf("%w: after must be an RFC 3339 timestamp, got %q", errFilter, text)
		}

		filter.After = after
	}

	return filter, nil
}

const selectRows = `SELECT rev_id, rev_parent_id, title, editor, editor_is_temp, comment, bytes_delta, event_ts, diff_url, tier,
	diff, diff_truncated, label, confidence, reason, evidence, grounded, route, steps, model, attempts, tokens, latency_ms, reasoned_at
	FROM verdicts`

func (s *Server) list(ctx context.Context, filter Filter) ([]Row, error) {
	rows, err := s.DB.Query(ctx, selectRows+`
		WHERE ($1 = '' OR route = $1) AND ($2 = '' OR label = $2) AND confidence >= $3 AND reasoned_at > $4
		ORDER BY reasoned_at DESC LIMIT $5`, filter.Route, filter.Label, filter.MinConfidence, filter.After, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("query verdicts: %w", err)
	}

	result, err := pgx.CollectRows(rows, pgx.RowToStructByName[Row])
	if err != nil {
		return nil, fmt.Errorf("scan verdicts: %w", err)
	}

	return result, nil
}

func (s *Server) get(ctx context.Context, revID int64) (Row, error) {
	rows, err := s.DB.Query(ctx, selectRows+` WHERE rev_id = $1`, revID)
	if err != nil {
		return Row{}, fmt.Errorf("query verdict: %w", err)
	}

	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[Row])
	if err != nil {
		return Row{}, fmt.Errorf("scan verdict: %w", err)
	}

	return row, nil
}

func (s *Server) stats(ctx context.Context) (Stats, error) {
	stats := Stats{ByRoute: make(map[string]int), ByLabel: make(map[string]int)}

	rows, err := s.DB.Query(ctx, `SELECT route, label, count(*) FROM verdicts GROUP BY route, label`)
	if err != nil {
		return stats, fmt.Errorf("query stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			route, label string
			count        int
		)

		if err := rows.Scan(&route, &label, &count); err != nil {
			return stats, fmt.Errorf("scan stats: %w", err)
		}

		stats.Total += count
		stats.ByRoute[route] += count
		stats.ByLabel[label] += count
	}

	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("read stats: %w", err)
	}

	return stats, nil
}

func (s *Server) apiStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.stats(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}

	writeJSON(w, stats)
}

func (s *Server) apiList(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rows, err := s.list(r.Context(), filter)
	if err != nil {
		s.fail(w, err)
		return
	}

	if rows == nil {
		rows = []Row{} //nolint:revive // Clients get [] for an empty page, where a nil slice encodes as null.
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
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	view := page{Filter: filter}
	if view.Stats, err = s.stats(r.Context()); err == nil {
		view.Rows, err = s.list(r.Context(), filter)
	}

	if err != nil {
		s.Log.Warn("query failed", "err", err)
		view.Error = err.Error()
	}

	if len(view.Rows) > 0 {
		view.Cursor = view.Rows[0].ReasonedAt.Format(time.RFC3339Nano)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := s.tmpl.Execute(w, view); err != nil {
		s.Log.Error("render", "err", err)
	}
}

func (s *Server) fragmentStats(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := s.stats(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := s.tmpl.ExecuteTemplate(w, "stats", page{Filter: filter, Stats: stats}); err != nil {
		s.Log.Error("render", "err", err)
	}
}

func (s *Server) fragmentRows(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rows, err := s.list(r.Context(), filter)
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

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	if err := enc.Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
