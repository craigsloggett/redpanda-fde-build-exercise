package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type Route string

const (
	routeFlagged Route = "flagged"
	routeReview  Route = "review"
	routeOK      Route = "ok"
	routeSkipped Route = "skipped"
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
	Label      Label    `json:"label"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason"`
	Evidence   string   `json:"evidence"`
	Grounded   bool     `json:"grounded"`
	Route      Route    `json:"route"`
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

func (r *Reasoner) Reason(ctx context.Context, input Input) (Verdict, error) {
	if strings.TrimSpace(input.Diff) == "" {
		reason := "no diff to review"
		if input.EnrichError != "" {
			reason += ": " + input.EnrichError
		}

		return Verdict{Label: labelUnreviewed, Route: routeSkipped, Reason: reason, Steps: []string{"gate:no_diff"}}, nil
	}

	verdict, err := r.assess(ctx, input, triageMessages(input), "triage")
	if err != nil {
		return Verdict{}, err
	}

	if verdict.Label == labelUnreviewed {
		verdict.Route = routeReview

		return verdict, nil
	}

	if verdict.Grounded && (verdict.Label == labelUnclear || verdict.Confidence < r.HighConfidence) {
		second, err := r.assess(ctx, input, challengeMessages(input, verdict), "challenge")
		if err != nil {
			return Verdict{}, err
		}

		second.Steps = append(
			verdict.Steps,
			second.Steps...,
		)
		second.Attempts += verdict.Attempts
		second.Tokens += verdict.Tokens

		if second.Label != labelUnreviewed {
			verdict = second
		} else {
			verdict.Steps = second.Steps
			verdict.Attempts = second.Attempts
			verdict.Tokens = second.Tokens
		}
	}

	verdict.Route = r.route(verdict)

	return verdict, nil
}

func (r *Reasoner) assess(ctx context.Context, input Input, msgs []Message, stage string) (Verdict, error) {
	var (
		steps       []string
		attempts    int
		tokens      int
		lastProblem string
		groundRetry bool
	)

	for attempts < r.MaxAttempts {
		attempts++

		format := FormatText
		if attempts == r.MaxAttempts {
			format = FormatJSON
		}

		reply, err := r.LLM.Chat(ctx, msgs, format)
		if err != nil {
			return Verdict{}, fmt.Errorf("%s attempt %d: %w", stage, attempts, err)
		}

		tokens += reply.PromptTokens + reply.CompletionTokens

		parsed, err := parseReply(reply.Content)
		if err != nil {
			steps = append(
				steps,
				stage+":parse_retry",
			)
			lastProblem = err.Error()
			msgs = append(
				msgs,
				Message{
					Role:    "assistant",
					Content: reply.Content,
				},
				Message{
					Role:    "user",
					Content: fmt.Sprintf(repairPrompt, err),
				},
			)
			r.Log.Debug(
				"unparseable reply",
				"stage", stage,
				"attempt", attempts,
				"err", err,
				"finish", reply.FinishReason,
				"reply", truncate(reply.Content, 200),
			)

			continue
		}

		verdict := Verdict{
			Label:      parsed.Label,
			Confidence: parsed.Confidence,
			Reason:     parsed.Reason,
			Evidence:   parsed.Evidence,
			Grounded:   grounded(parsed.Evidence, input.Diff),
			Steps:      append(steps, stage),
			Attempts:   attempts,
			Tokens:     tokens,
		}

		if verdict.Grounded {
			return verdict, nil
		}

		if !groundRetry && attempts < r.MaxAttempts {
			steps = append(
				steps,
				stage+":ground_retry",
			)
			msgs = append(
				msgs,
				Message{
					Role:    "assistant",
					Content: reply.Content,
				},
				Message{
					Role:    "user",
					Content: groundPrompt,
				},
			)
			groundRetry = true

			continue
		}

		verdict.Steps = append(
			steps,
			stage+":ungrounded",
		)
		verdict.Confidence = min(verdict.Confidence, r.LowConfidence)

		return verdict, nil
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
	quote, text := collapseSpace(evidence), collapseSpace(diff)
	if len(quote) < minEvidenceLen {
		return false
	}

	if strings.Contains(text, quote) {
		return true
	}

	return containsRun(text, quote, runNeeded(len(quote)))
}

func runNeeded(quoteLen int) int {
	return max(minPartialRun, (quoteLen*minPartialPercent+99)/100)
}

func containsRun(text, quote string, n int) bool {
	for i := 0; i+n <= len(quote); i++ {
		if strings.Contains(text, quote[i:i+n]) {
			return true
		}
	}

	return false
}

func collapseSpace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func (r *Reasoner) route(verdict Verdict) Route {
	switch {
	case !verdict.Grounded:
		return routeReview
	case verdict.Label.damaging() && verdict.Confidence >= r.HighConfidence:
		return routeFlagged
	case verdict.Label == labelConstructive && verdict.Confidence >= r.LowConfidence:
		return routeOK
	default:
		return routeReview
	}
}
