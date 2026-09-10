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

type scriptedLLM struct {
	replies  []string
	err      error
	calls    [][]Message
	jsonMode []bool
}

func (s *scriptedLLM) Chat(_ context.Context, msgs []Message, jsonMode bool) (Reply, error) {
	if s.err != nil {
		return Reply{}, s.err
	}

	i := len(s.calls)
	s.calls = append(s.calls, msgs)
	s.jsonMode = append(s.jsonMode, jsonMode)

	if i >= len(s.replies) {
		return Reply{}, fmt.Errorf("unexpected model call number %d", i+1)
	}

	return Reply{Content: s.replies[i], PromptTokens: 10, CompletionTokens: 5}, nil
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
		in             Input
		replies        []string
		wantLabel      string
		wantRoute      string
		wantConfidence float64
		wantCalls      int
		wantSteps      []string
		check          func(t *testing.T, llm *scriptedLLM, v Verdict)
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
			check: func(t *testing.T, llm *scriptedLLM, _ Verdict) {
				retry := llm.calls[1]
				if len(retry) != 4 || retry[2].Role != "assistant" || !strings.Contains(lastMessage(retry), "could not be used") {
					t.Errorf("retry should replay the bad reply and explain the problem, got %+v", retry)
				}
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
			check: func(t *testing.T, llm *scriptedLLM, v Verdict) {
				if v.Grounded {
					t.Error("verdict should be marked ungrounded")
				}

				if !strings.Contains(lastMessage(llm.calls[1]), "does not appear verbatim") {
					t.Errorf("retry should ask for a verbatim quote, got %q", lastMessage(llm.calls[1]))
				}
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
			check: func(t *testing.T, _ *scriptedLLM, v Verdict) {
				if !v.Grounded {
					t.Error("a near-verbatim long quote should be grounded")
				}
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
			wantSteps:      []string{"triage", "challenge"},
			check: func(t *testing.T, llm *scriptedLLM, _ Verdict) {
				challenge := llm.calls[1]
				if len(challenge) != 2 || !strings.Contains(lastMessage(challenge), `labelled this edit "unsourced_claim"`) {
					t.Errorf("challenge should be a fresh conversation quoting the first verdict, got %+v", challenge)
				}
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
			wantSteps:      []string{"triage", "challenge"},
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
			check: func(t *testing.T, llm *scriptedLLM, v Verdict) {
				if v.Attempts != 4 {
					t.Errorf("attempts = %d, want 4", v.Attempts)
				}
			},
		},
		{
			name:      "no diff never calls the model",
			in:        Input{RevID: 1, Title: "Test village", EnrichError: "http status 503"},
			wantLabel: labelUnreviewed,
			wantRoute: routeSkipped,
			wantCalls: 0,
			wantSteps: []string{"gate:no_diff"},
			check: func(t *testing.T, _ *scriptedLLM, v Verdict) {
				if !strings.Contains(v.Reason, "http status 503") {
					t.Errorf("reason should carry the fetch error, got %q", v.Reason)
				}
			},
		},
		{
			name:      "unusable output after the whole budget lands as unreviewed with json mode tried last",
			replies:   []string{"nope", "nope", "nope"},
			wantLabel: labelUnreviewed,
			wantRoute: routeReview,
			wantCalls: 3,
			wantSteps: []string{"triage:parse_retry", "triage:parse_retry", "triage:parse_retry", "triage:unusable"},
			check: func(t *testing.T, llm *scriptedLLM, v Verdict) {
				if llm.jsonMode[0] || llm.jsonMode[1] || !llm.jsonMode[2] {
					t.Errorf("json mode should be forced only on the last attempt, got %v", llm.jsonMode)
				}

				if v.Attempts != 3 {
					t.Errorf("attempts = %d, want 3", v.Attempts)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			if in.RevID == 0 {
				in = testInput()
			}

			llm := &scriptedLLM{replies: tc.replies}

			v, err := newTestReasoner(llm).Reason(context.Background(), in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if v.Label != tc.wantLabel || v.Route != tc.wantRoute {
				t.Errorf("got label=%q route=%q, want label=%q route=%q", v.Label, v.Route, tc.wantLabel, tc.wantRoute)
			}

			if tc.wantConfidence != 0 && v.Confidence != tc.wantConfidence {
				t.Errorf("confidence = %v, want %v", v.Confidence, tc.wantConfidence)
			}

			if len(llm.calls) != tc.wantCalls {
				t.Errorf("model calls = %d, want %d", len(llm.calls), tc.wantCalls)
			}

			if got, want := strings.Join(v.Steps, ","), strings.Join(tc.wantSteps, ","); got != want {
				t.Errorf("steps = %s, want %s", got, want)
			}

			if tc.check != nil {
				tc.check(t, llm, v)
			}
		})
	}
}

func TestReasonReturnsErrorWhenModelUnreachable(t *testing.T) {
	llm := &scriptedLLM{err: errors.New("connection refused")}

	v, err := newTestReasoner(llm).Reason(context.Background(), testInput())
	if err == nil {
		t.Fatalf("expected an error so the record is retried, got verdict %+v", v)
	}

	if v.Label != "" {
		t.Errorf("no verdict should be produced on a transport failure, got %+v", v)
	}
}
