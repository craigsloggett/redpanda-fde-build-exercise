package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	labelConstructive = "constructive"
	labelVandalism    = "vandalism"
	labelSpam         = "spam"
	labelUnsourced    = "unsourced_claim"
	labelUnclear      = "unclear"
)

var labels = []string{labelConstructive, labelVandalism, labelSpam, labelUnsourced, labelUnclear}

type modelReply struct {
	Label      string
	Confidence float64
	Reason     string
	Evidence   string
}

type rawReply struct {
	Label      json.RawMessage `json:"label"`
	Confidence json.RawMessage `json:"confidence"`
	Reason     string          `json:"reason"`
	Evidence   string          `json:"evidence"`
}

var thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

func parseReply(content string) (modelReply, error) {
	content = thinkBlock.ReplaceAllString(content, "")

	var raw rawReply

	found := false

	for start := strings.IndexByte(content, '{'); start >= 0; {
		obj, ok := balancedObject(content[start:])
		if ok && json.Unmarshal([]byte(obj), &raw) == nil {
			found = true
			break
		}

		next := strings.IndexByte(content[start+1:], '{')
		if next < 0 {
			break
		}

		start += 1 + next
	}

	if !found {
		return modelReply{}, errors.New("no JSON object found in reply")
	}

	var labelText string
	if err := json.Unmarshal(raw.Label, &labelText); err != nil {
		return modelReply{}, fmt.Errorf("label is not a string: %s", truncate(string(raw.Label), 40))
	}

	label, ok := normalizeLabel(labelText)
	if !ok {
		return modelReply{}, fmt.Errorf("label %q is not one of %s", labelText, strings.Join(labels, ", "))
	}

	confidence, err := parseConfidence(raw.Confidence)
	if err != nil {
		return modelReply{}, err
	}

	return modelReply{
		Label:      label,
		Confidence: confidence,
		Reason:     strings.TrimSpace(raw.Reason),
		Evidence:   strings.TrimSpace(raw.Evidence),
	}, nil
}

func balancedObject(s string) (string, bool) {
	depth, inString, escaped := 0, false, false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}

			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i+1], true
			}
		}
	}

	return "", false
}

// labelSynonyms lists the words a model reaches for when it drifts off the label set.
var labelSynonyms = map[string][]string{
	labelConstructive: {"good", "good_faith", "benign", "legitimate", "improvement", "helpful", "ok", "fine", "valid", "productive"},
	labelVandalism: {
		"vandal", "vandalized", "vandalised", "damaging", "nonsense", "test", "test_edit", "blanking",
		"disruptive", "malicious", "hoax", "trolling",
	},
	labelSpam: {"promotional", "promotion", "advertising", "advert", "advertisement", "promo", "self_promotion", "link_spam", "linkspam"},
	labelUnsourced: {
		"unsourced", "uncited", "unverified", "unreferenced", "citation_needed", "needs_citation", "unsupported_claim",
		"original_research",
	},
	labelUnclear: {"uncertain", "unknown", "unsure", "ambiguous", "indeterminate", "cannot_tell"},
}

var (
	nonAlnum    = regexp.MustCompile(`[^a-z0-9]+`)
	hedgePrefix = regexp.MustCompile(`^(likely|probably|probable|possibly|possible|mostly|mild|minor|clear|clearly|obvious|obviously)_`)
)

func normalizeLabel(s string) (string, bool) {
	s = nonAlnum.ReplaceAllString(strings.ToLower(s), "_")
	s = strings.Trim(s, "_")
	s = hedgePrefix.ReplaceAllString(s, "")

	for label, synonyms := range labelSynonyms {
		if s == label || slices.Contains(synonyms, s) {
			return label, true
		}
	}

	return "", false
}

var confidenceWords = map[string]float64{"high": 0.9, "medium": 0.6, "moderate": 0.6, "low": 0.3}

func parseConfidence(raw json.RawMessage) (float64, error) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)

	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "null" {
		return 0, errors.New("confidence is missing")
	}

	if f, ok := confidenceWords[s]; ok {
		return f, nil
	}

	percent := strings.HasSuffix(s, "%")

	f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
	if err != nil {
		return 0, fmt.Errorf("confidence %q is not a number", s)
	}

	if percent || f > 1 {
		f /= 100
	}

	if f < 0 || f > 1 {
		return 0, fmt.Errorf("confidence %v is outside 0..1", f)
	}

	return f, nil
}
