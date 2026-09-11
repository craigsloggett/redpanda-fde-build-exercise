package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

const testDiff = `@@ -12,3 +12,3 @@
 The village has a population of 1,204.
-The parish church dates from the 14th century.
+The parish church dates from the 14th century and is haunted by aliens lol.
 It is served by a bus route.`

type modelCall struct {
	msgs   []Message
	format Format
}

type scriptedLLM struct {
	replies []string
	err     error
	calls   []modelCall
}

func (s *scriptedLLM) Chat(_ context.Context, msgs []Message, format Format) (Reply, error) {
	if s.err != nil {
		return Reply{}, s.err
	}

	s.calls = append(
		s.calls,
		modelCall{
			msgs:   msgs,
			format: format,
		},
	)

	call := len(s.calls) - 1
	if call >= len(s.replies) {
		return Reply{}, fmt.Errorf("unexpected model call number %d", call+1)
	}

	return Reply{Content: s.replies[call], PromptTokens: 10, CompletionTokens: 5}, nil
}

func newTestReasoner(llm Chatter) *Reasoner {
	return &Reasoner{LLM: llm, MaxAttempts: 3, HighConfidence: 0.8, LowConfidence: 0.5, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func testInput() Input {
	return Input{RevID: 1, RevParentID: 0, Title: "Test village", User: "~2026-1-1", UserIsTemp: true, BytesDelta: 25, Diff: testDiff}
}

func lastMessage(msgs []Message) string { return msgs[len(msgs)-1].Content }

func TestReason(t *testing.T) {
	tests := []struct {
		name           string
		input          Input
		replies        []string
		wantLabel      Label
		wantRoute      Route
		wantConfidence float64
		wantCalls      int
		wantSteps      []string
		check          func(llm *scriptedLLM, verdict Verdict) error
	}{
		{
			name:           "confident grounded verdict stands after one call",
			replies:        []string{goodReply},
			wantLabel:      labelVandalism,
			wantRoute:      routeFlagged,
			wantConfidence: 0.95,
			wantCalls:      1,
			wantSteps:      []string{"triage"},
		},
		{
			name: "dirty output is salvaged without a retry",
			replies: []string{"Sure! Here is my review of the edit:\n```json\n" +
				`{"label": "Vandalism.", "confidence": "95%", "reason": "Adds a joke.", "evidence": "haunted by aliens lol"}` +
				"\n```\nLet me know if you need anything else."},
			wantLabel:      labelVandalism,
			wantRoute:      routeFlagged,
			wantConfidence: 0.95,
			wantCalls:      1,
			wantSteps:      []string{"triage"},
		},
		{
			name:           "unusable reply is sent back with the problem and repaired",
			replies:        []string{"I'm not able to help with that request.", goodReply},
			wantLabel:      labelVandalism,
			wantRoute:      routeFlagged,
			wantConfidence: 0.95,
			wantCalls:      2,
			wantSteps:      []string{"triage:parse_retry", "triage"},
			check: func(llm *scriptedLLM, _ Verdict) error {
				retry := llm.calls[1].msgs
				if len(retry) != 4 || retry[2].Role != "assistant" || !strings.Contains(lastMessage(retry), "could not be used") {
					return fmt.Errorf("retry should replay the bad reply and explain the problem, got %+v", retry)
				}

				return nil
			},
		},
		{
			name:      "label outside the set counts as unusable",
			replies:   []string{`{"label": "not vandalism", "confidence": 0.9, "reason": "x", "evidence": "haunted by aliens lol"}`, goodReply},
			wantLabel: labelVandalism,
			wantRoute: routeFlagged,
			wantCalls: 2,
			wantSteps: []string{"triage:parse_retry", "triage"},
		},
		{
			name: "evidence the diff does not contain is retried once then distrusted",
			replies: []string{
				`{"label": "vandalism", "confidence": 0.95, "reason": "x", "evidence": "the aliens landed in 1999"}`,
				`{"label": "vandalism", "confidence": 0.95, "reason": "x", "evidence": "still not in the diff"}`,
			},
			wantLabel:      labelVandalism,
			wantRoute:      routeReview,
			wantConfidence: 0.5,
			wantCalls:      2,
			wantSteps:      []string{"triage:ground_retry", "triage:ungrounded"},
			check: func(llm *scriptedLLM, verdict Verdict) error {
				if verdict.Grounded {
					return errors.New("verdict should be marked ungrounded")
				}

				if retry := lastMessage(llm.calls[1].msgs); !strings.Contains(retry, "does not appear verbatim") {
					return fmt.Errorf("retry should ask for a verbatim quote, got %q", retry)
				}

				return nil
			},
		},
		{
			name: "long quote mangled by one character still counts as grounded",
			replies: []string{`{"label": "vandalism", "confidence": 0.95, "reason": "x", ` +
				`"evidence": "The parish church dates from the 14th century and is haunted by aliens lol!"}`},
			wantLabel:      labelVandalism,
			wantRoute:      routeFlagged,
			wantConfidence: 0.95,
			wantCalls:      1,
			wantSteps:      []string{"triage"},
			check: func(_ *scriptedLLM, verdict Verdict) error {
				if !verdict.Grounded {
					return errors.New("a near-verbatim long quote should be grounded")
				}

				return nil
			},
		},
		{
			name: "ungrounded constructive verdict is not trusted as ok",
			replies: []string{
				`{"label": "constructive", "confidence": 0.9, "reason": "x", "evidence": "fixed the citation"}`,
				`{"label": "constructive", "confidence": 0.9, "reason": "x", "evidence": "fixed the citation"}`,
			},
			wantLabel:      labelConstructive,
			wantRoute:      routeReview,
			wantConfidence: 0.5,
			wantCalls:      2,
			wantSteps:      []string{"triage:ground_retry", "triage:ungrounded"},
		},
		{
			name: "mid confidence is challenged in a fresh conversation and the challenge decides",
			replies: []string{
				`{"label": "unsourced_claim", "confidence": 0.6, "reason": "Adds a claim without a source.", "evidence": "haunted by aliens lol"}`,
				`{"counterargument": "This is a joke, not a claim anyone meant.", "label": "vandalism", "confidence": 0.9, "reason": "Joke inserted into a factual sentence.", "evidence": "haunted by aliens lol"}`,
			},
			wantLabel:      labelVandalism,
			wantRoute:      routeFlagged,
			wantConfidence: 0.9,
			wantCalls:      2,
			wantSteps:      []string{"triage", "challenge", "challenge:overturned"},
			check: func(llm *scriptedLLM, _ Verdict) error {
				challenge := llm.calls[1].msgs
				if len(challenge) != 2 || !strings.Contains(lastMessage(challenge), `labelled this edit "unsourced_claim"`) {
					return fmt.Errorf("challenge should be a fresh conversation quoting the first verdict, got %+v", challenge)
				}

				return nil
			},
		},
		{
			name: "unclear first pass is challenged and can settle on constructive",
			replies: []string{
				`{"label": "unclear", "confidence": 0.9, "reason": "x", "evidence": "haunted by aliens lol"}`,
				`{"label": "constructive", "confidence": 0.7, "reason": "x", "evidence": "haunted by aliens lol"}`,
			},
			wantLabel:      labelConstructive,
			wantRoute:      routeOK,
			wantConfidence: 0.7,
			wantCalls:      2,
			wantSteps:      []string{"triage", "challenge", "challenge:overturned"},
		},
		{
			name: "challenge that agrees leaves no overturned step",
			replies: []string{
				`{"label": "vandalism", "confidence": 0.6, "reason": "x", "evidence": "haunted by aliens lol"}`,
				`{"label": "vandalism", "confidence": 0.7, "reason": "x", "evidence": "haunted by aliens lol"}`,
			},
			wantLabel:      labelVandalism,
			wantRoute:      routeReview,
			wantConfidence: 0.7,
			wantCalls:      2,
			wantSteps:      []string{"triage", "challenge"},
		},
		{
			name: "challenge that lifts the same label over the high threshold overturns the route",
			replies: []string{
				`{"label": "vandalism", "confidence": 0.6, "reason": "x", "evidence": "haunted by aliens lol"}`,
				`{"label": "vandalism", "confidence": 0.9, "reason": "x", "evidence": "haunted by aliens lol"}`,
			},
			wantLabel:      labelVandalism,
			wantRoute:      routeFlagged,
			wantConfidence: 0.9,
			wantCalls:      2,
			wantSteps:      []string{"triage", "challenge", "challenge:overturned"},
		},
		{
			name: "challenge that never parses leaves the first verdict in review",
			replies: []string{
				`{"label": "vandalism", "confidence": 0.6, "reason": "x", "evidence": "haunted by aliens lol"}`,
				"no", "no", "no",
			},
			wantLabel:      labelVandalism,
			wantRoute:      routeReview,
			wantConfidence: 0.6,
			wantCalls:      4,
			wantSteps:      []string{"triage", "challenge:parse_retry", "challenge:parse_retry", "challenge:parse_retry", "challenge:unusable"},
			check: func(_ *scriptedLLM, verdict Verdict) error {
				if verdict.Attempts != 4 {
					return fmt.Errorf("attempts = %d, want 4", verdict.Attempts)
				}

				return nil
			},
		},
		{
			name:      "no diff never calls the model",
			input:     Input{RevID: 1, Title: "Test village", EnrichError: "http status 503"},
			wantLabel: labelUnreviewed,
			wantRoute: routeSkipped,
			wantCalls: 0,
			wantSteps: []string{"gate:no_diff"},
			check: func(_ *scriptedLLM, verdict Verdict) error {
				if !strings.Contains(verdict.Reason, "http status 503") {
					return fmt.Errorf("reason should carry the fetch error, got %q", verdict.Reason)
				}

				return nil
			},
		},
		{
			name:      "unusable output after the whole budget lands as unreviewed with json format tried last",
			replies:   []string{"nope", "nope", "nope"},
			wantLabel: labelUnreviewed,
			wantRoute: routeReview,
			wantCalls: 3,
			wantSteps: []string{"triage:parse_retry", "triage:parse_retry", "triage:parse_retry", "triage:unusable"},
			check: func(llm *scriptedLLM, verdict Verdict) error {
				if llm.calls[0].format != FormatText || llm.calls[1].format != FormatText || llm.calls[2].format != FormatJSON {
					return fmt.Errorf("json format should be forced only on the last attempt, got %+v", llm.calls)
				}

				if verdict.Attempts != 3 {
					return fmt.Errorf("attempts = %d, want 3", verdict.Attempts)
				}

				return nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.input
			if input.RevID == 0 {
				input = testInput()
			}

			llm := &scriptedLLM{replies: test.replies}

			verdict, err := newTestReasoner(llm).Reason(t.Context(), input)
			if err != nil {
				t.Fatalf("Reason() unexpected error: %v", err)
			}

			if verdict.Label != test.wantLabel || verdict.Route != test.wantRoute {
				t.Errorf("Reason() label = %q route = %q, want label %q route %q", verdict.Label, verdict.Route, test.wantLabel, test.wantRoute)
			}

			if test.wantConfidence != 0 && verdict.Confidence != test.wantConfidence {
				t.Errorf("Reason() confidence = %v, want %v", verdict.Confidence, test.wantConfidence)
			}

			if len(llm.calls) != test.wantCalls {
				t.Errorf("Reason() made %d model calls, want %d", len(llm.calls), test.wantCalls)
			}

			if got, want := strings.Join(verdict.Steps, ","), strings.Join(test.wantSteps, ","); got != want {
				t.Errorf("Reason() steps = %s, want %s", got, want)
			}

			if test.check != nil {
				if err := test.check(llm, verdict); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestReasonReturnsErrorWhenModelUnreachable(t *testing.T) {
	llm := &scriptedLLM{err: errors.New("connection refused")}

	verdict, err := newTestReasoner(llm).Reason(t.Context(), testInput())
	if err == nil {
		t.Fatalf("Reason() expected an error so the record is retried, got verdict %+v", verdict)
	}

	if verdict.Label != "" {
		t.Errorf("Reason() should produce no verdict on a transport failure, got %+v", verdict)
	}
}
