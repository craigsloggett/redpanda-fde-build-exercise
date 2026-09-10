package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

const labelUnreviewed = "unreviewed"

const (
	routeFlagged = "flagged"
	routeReview  = "review"
	routeOK      = "ok"
	routeSkipped = "skipped"
)

type Input struct {
	RevID         int64  `json:"rev_id"`
	RevParentID   int64  `json:"rev_parent_id"`
	RcID          int64  `json:"rc_id"`
	Title         string `json:"title"`
	User          string `json:"user"`
	UserIsTemp    bool   `json:"user_is_temp"`
	Minor         bool   `json:"minor"`
	Comment       string `json:"comment"`
	BytesOld      int64  `json:"bytes_old"`
	BytesNew      int64  `json:"bytes_new"`
	BytesDelta    int64  `json:"bytes_delta"`
	EventTS       string `json:"event_ts"`
	ServerURL     string `json:"server_url"`
	DiffURL       string `json:"diff_url"`
	Tier          string `json:"tier"`
	Diff          string `json:"diff"`
	DiffSize      int64  `json:"diff_size"`
	DiffTruncated bool   `json:"diff_truncated"`
	EnrichError   string `json:"enrich_error,omitempty"`
}

type Verdict struct {
	Label      string   `json:"label"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason"`
	Evidence   string   `json:"evidence"`
	Grounded   bool     `json:"grounded"`
	Route      string   `json:"route"`
	Steps      []string `json:"steps"`
	Attempts   int      `json:"attempts"`
	Tokens     int      `json:"tokens"`
}

type Reasoner struct {
	LLM            Chatter
	MaxAttempts    int
	HighConfidence float64
	LowConfidence  float64
	Log            *slog.Logger
}

var damagingLabels = map[string]bool{labelVandalism: true, labelSpam: true, labelUnsourced: true}

func (r *Reasoner) Reason(ctx context.Context, in Input) (Verdict, error) {
	if strings.TrimSpace(in.Diff) == "" {
		reason := "no diff to review"
		if in.EnrichError != "" {
			reason += ": " + in.EnrichError
		}

		return Verdict{Label: labelUnreviewed, Route: routeSkipped, Reason: reason, Steps: []string{"gate:no_diff"}}, nil
	}

	v, err := r.assess(ctx, in, triageMessages(in), "triage")
	if err != nil {
		return Verdict{}, err
	}

	if v.Label == labelUnreviewed {
		v.Route = routeReview
		return v, nil
	}

	if v.Grounded && (v.Label == labelUnclear || v.Confidence < r.HighConfidence) {
		second, err := r.assess(ctx, in, challengeMessages(in, v), "challenge")
		if err != nil {
			return Verdict{}, err
		}

		second.Steps = append(v.Steps, second.Steps...)
		second.Attempts += v.Attempts
		second.Tokens += v.Tokens

		if second.Label != labelUnreviewed {
			v = second
		} else {
			v.Steps = second.Steps
			v.Attempts = second.Attempts
			v.Tokens = second.Tokens
		}
	}

	v.Route = r.route(v)
	return v, nil
}

func (r *Reasoner) assess(ctx context.Context, in Input, msgs []Message, stage string) (Verdict, error) {
	var (
		steps       []string
		attempts    int
		tokens      int
		lastProblem string
		groundRetry bool
	)
	for attempts < r.MaxAttempts {
		attempts++
		jsonMode := attempts == r.MaxAttempts
		reply, err := r.LLM.Chat(ctx, msgs, jsonMode)
		if err != nil {
			return Verdict{}, fmt.Errorf("%s attempt %d: %w", stage, attempts, err)
		}

		tokens += reply.PromptTokens + reply.CompletionTokens

		parsed, err := parseReply(reply.Content)
		if err != nil {
			lastProblem = err.Error()
			steps = append(steps, stage+":parse_retry")
			r.Log.Debug("unparseable reply", "stage", stage, "attempt", attempts, "err", err, "finish", reply.FinishReason, "reply", truncate(reply.Content, 200))
			msgs = append(msgs, Message{Role: "assistant", Content: reply.Content}, Message{Role: "user", Content: fmt.Sprintf(repairPrompt, err)})
			continue
		}

		v := Verdict{
			Label:      parsed.Label,
			Confidence: parsed.Confidence,
			Reason:     parsed.Reason,
			Evidence:   parsed.Evidence,
			Grounded:   grounded(parsed.Evidence, in.Diff),
			Steps:      append(steps, stage),
			Attempts:   attempts,
			Tokens:     tokens,
		}

		if v.Grounded {
			return v, nil
		}

		if !groundRetry && attempts < r.MaxAttempts {
			groundRetry = true
			steps = append(steps, stage+":ground_retry")
			msgs = append(msgs, Message{Role: "assistant", Content: reply.Content}, Message{Role: "user", Content: groundPrompt})
			continue
		}

		v.Steps = append(steps, stage+":ungrounded")
		v.Confidence = min(v.Confidence, r.LowConfidence)
		return v, nil
	}

	return Verdict{
		Label:    labelUnreviewed,
		Reason:   fmt.Sprintf("model output unusable after %d attempts: %s", attempts, lastProblem),
		Steps:    append(steps, stage+":unusable"),
		Attempts: attempts,
		Tokens:   tokens,
	}, nil
}

const (
	minEvidenceLen    = 3
	minPartialRun     = 24
	minPartialPercent = 60
)

func grounded(evidence, diff string) bool {
	e, d := collapseSpace(evidence), collapseSpace(diff)

	if len(e) < minEvidenceLen {
		return false
	}

	if strings.Contains(d, e) {
		return true
	}

	run := longestCommonRun(e, d)

	return run >= minPartialRun && run*100 >= len(e)*minPartialPercent
}

func longestCommonRun(a, b string) int {
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	best := 0

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
				best = max(best, cur[j])
			} else {
				cur[j] = 0
			}
		}
		prev, cur = cur, prev
	}

	return best
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (r *Reasoner) route(v Verdict) string {
	switch {
	case !v.Grounded:
		return routeReview
	case damagingLabels[v.Label] && v.Confidence >= r.HighConfidence:
		return routeFlagged
	case v.Label == labelConstructive && v.Confidence >= r.LowConfidence:
		return routeOK
	default:
		return routeReview
	}
}
