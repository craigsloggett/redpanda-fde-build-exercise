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

var (
	errNoObject   = errors.New("no JSON object found in reply")
	errLabel      = errors.New("label is unusable")
	errConfidence = errors.New("confidence is unusable")
)

type modelReply struct {
	Label      Label
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
		return modelReply{}, errNoObject
	}

	var labelText string
	if err := json.Unmarshal(raw.Label, &labelText); err != nil {
		return modelReply{}, fmt.Errorf("%w: %s is not a string", errLabel, truncate(string(raw.Label), 40))
	}

	label, ok := normalizeLabel(labelText)
	if !ok {
		return modelReply{}, fmt.Errorf("%w: %q is not one of %v", errLabel, labelText, reviewLabels())
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

func balancedObject(text string) (string, bool) {
	depth, inString, escaped := 0, false, false

	for i := range len(text) {
		char := text[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}

			continue
		}

		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[:i+1], true
			}
		}
	}

	return "", false
}

var (
	nonAlnum    = regexp.MustCompile(`[^a-z0-9]+`)
	hedgePrefix = regexp.MustCompile(`^(likely|probably|probable|possibly|possible|mostly|mild|minor|clear|clearly|obvious|obviously)_`)
)

func normalizeLabel(text string) (Label, bool) {
	text = nonAlnum.ReplaceAllString(strings.ToLower(text), "_")
	text = strings.Trim(text, "_")
	text = hedgePrefix.ReplaceAllString(text, "")

	for _, label := range reviewLabels() {
		if text == string(label) || slices.Contains(label.synonyms(), text) {
			return label, true
		}
	}

	return "", false
}

func parseConfidence(raw json.RawMessage) (float64, error) {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	text = strings.ToLower(strings.TrimSpace(text))

	if text == "" || text == "null" {
		return 0, fmt.Errorf("%w: missing", errConfidence)
	}

	if value, ok := confidenceWord(text); ok {
		return value, nil
	}

	percent := strings.HasSuffix(text, "%")

	value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(text, "%")), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a number", errConfidence, text)
	}

	if percent || value > 1 {
		value /= 100
	}

	if value < 0 || value > 1 {
		return 0, fmt.Errorf("%w: %v is outside 0..1", errConfidence, value)
	}

	return value, nil
}

func confidenceWord(text string) (float64, bool) {
	switch text {
	case "high":
		return 0.9, true
	case "medium", "moderate":
		return 0.6, true
	case "low":
		return 0.3, true
	default:
		return 0, false
	}
}
