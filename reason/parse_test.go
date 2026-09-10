package main

import (
	"errors"
	"testing"
)

const goodReply = `{"label": "vandalism", "confidence": 0.95, "reason": "Adds a joke to a factual sentence.", "evidence": "haunted by aliens lol"}`

func TestParseReply(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    modelReply
		wantErr error
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
		{name: "missing confidence", content: `{"label": "spam", "reason": "x", "evidence": "y"}`, wantErr: errConfidence},
		{name: "unknown label", content: `{"label": "meh", "confidence": 0.5}`, wantErr: errLabel},
		{name: "no object at all", content: "vandalism, 0.9", wantErr: errNoObject},
		{name: "unterminated object", content: `{"label": "spam", "confidence": 0.9`, wantErr: errNoObject},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseReply(test.content)

			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("parseReply(%q) error = %v, want %v", test.content, err, test.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("parseReply(%q) unexpected error: %v", test.content, err)
			}

			if got != test.want {
				t.Errorf("parseReply(%q) = %+v, want %+v", test.content, got, test.want)
			}
		})
	}
}
