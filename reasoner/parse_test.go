package main

import (
	"strings"
	"testing"
)

const goodReply = `{"label": "vandalism", "confidence": 0.95, "reason": "Adds a joke to a factual sentence.", "evidence": "haunted by aliens lol"}`

func TestParseReply(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    modelReply
		wantErr string
	}{
		{
			name:    "thinking block before the object",
			content: "<think>Is this a joke? {maybe}</think>\n" + goodReply,
			want:    modelReply{Label: labelVandalism, Confidence: 0.95, Reason: "Adds a joke to a factual sentence.", Evidence: "haunted by aliens lol"},
		},
		{
			name:    "braces in prose before the object are skipped",
			content: "The removed line was {{Infobox settlement}} and then {fs end}. " + goodReply,
			want:    modelReply{Label: labelVandalism, Confidence: 0.95, Reason: "Adds a joke to a factual sentence.", Evidence: "haunted by aliens lol"},
		},
		{
			name:    "escaped brace inside a string does not end the object",
			content: `{"label": "spam", "confidence": 0.7, "reason": "adds \"}\" and a link", "evidence": "x"}`,
			want:    modelReply{Label: labelSpam, Confidence: 0.7, Reason: `adds "}" and a link`, Evidence: "x"},
		},
		{
			name:    "hedged label and confidence word",
			content: `{"label": "Likely spam", "confidence": "high", "reason": "", "evidence": ""}`,
			want:    modelReply{Label: labelSpam, Confidence: 0.9},
		},
		{
			name:    "confidence given out of 100",
			content: `{"label": "constructive", "confidence": 85, "reason": "", "evidence": ""}`,
			want:    modelReply{Label: labelConstructive, Confidence: 0.85},
		},
		{
			name:    "synonym maps onto the set",
			content: `{"label": "good faith", "confidence": 0.8, "reason": "", "evidence": ""}`,
			want:    modelReply{Label: labelConstructive, Confidence: 0.8},
		},
		{name: "missing confidence", content: `{"label": "spam", "reason": "x", "evidence": "y"}`, wantErr: "confidence is missing"},
		{name: "unknown label", content: `{"label": "meh", "confidence": 0.5}`, wantErr: "not one of"},
		{name: "no object at all", content: "vandalism, 0.9", wantErr: "no JSON object"},
		{name: "unterminated object", content: `{"label": "spam", "confidence": 0.9`, wantErr: "no JSON object"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReply(tc.content)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
