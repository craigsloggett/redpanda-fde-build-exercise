package main

import (
	"fmt"
	"strings"
)

const systemPrompt = `You are an English Wikipedia recent-changes patroller reviewing one edit.
You will see the edit as a unified diff of the page's wikitext. Lines starting with "-" were removed, lines starting with "+" were added, other lines are unchanged context. Wikitext uses [[links]], {{templates}}, <ref> citations, and == headings ==.

Classify the edit with exactly one label:
- constructive: improves the article in good faith (fixing errors, adding sourced content, copy editing, formatting, cleanup).
- vandalism: deliberately damages the article (nonsense, insults, obscenity, blanking, jokes, deliberately false facts or numbers, test edits like "asdf").
- spam: adds promotional wording or external links whose purpose is advertising.
- unsourced_claim: adds or changes a factual claim, especially about a living person, a number, or a date, with no citation.
- unclear: the diff does not contain enough to tell.

Reply with only a JSON object in exactly this shape and nothing else:
{"label": "<one label>", "confidence": <number from 0 to 1>, "reason": "<one sentence>", "evidence": "<short quote copied exactly from a changed line of the diff>"}
The evidence must be copied character for character from the diff. Never paraphrase or shorten it with "...".`

const repairPrompt = `Your reply could not be used: %s. Reply again with only the JSON object in the required shape.`

const groundPrompt = `The "evidence" you quoted does not appear verbatim in the diff. Reply again with the same JSON shape, and copy the evidence exactly, character for character, from a changed line of the diff.`

func triageMessages(in Input) []Message {
	return []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: describeEdit(in)},
	}
}

func challengeMessages(in Input, first Verdict) []Message {
	user := fmt.Sprintf(`%s

A first review labelled this edit %q with confidence %.2f because: %s

Make the strongest case that this label is wrong, then decide for yourself. Reply with only a JSON object in this shape and nothing else:
{"counterargument": "<one or two sentences>", "label": "<one label>", "confidence": <number from 0 to 1>, "reason": "<one sentence>", "evidence": "<short quote copied exactly from a changed line of the diff>"}`,
		describeEdit(in), first.Label, first.Confidence, first.Reason)
	return []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: user},
	}
}

func describeEdit(in Input) string {
	editor := "registered account"
	if in.UserIsTemp {
		editor = "logged-out editor with a temporary account"
	}

	comment := in.Comment
	if strings.TrimSpace(comment) == "" {
		comment = "(none)"
	}

	diffLabel := "Diff"
	if in.DiffTruncated {
		diffLabel = "Diff (truncated, the edit continues beyond this)"
	}

	return fmt.Sprintf("Article: %s\nEditor: %s (%s)\nEdit summary: %s\nSize change: %+d bytes\n\n%s:\n%s",
		in.Title, in.User, editor, comment, in.BytesDelta, diffLabel, in.Diff)
}
