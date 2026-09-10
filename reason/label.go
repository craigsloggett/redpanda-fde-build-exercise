package main

// Label is the model's classification of an edit.
type Label string

const (
	labelConstructive Label = "constructive"
	labelVandalism    Label = "vandalism"
	labelSpam         Label = "spam"
	labelUnsourced    Label = "unsourced_claim"
	labelUnclear      Label = "unclear"
	labelUnreviewed   Label = "unreviewed"
)

// reviewLabels lists the labels the model may answer with, in the order the prompt presents them.
func reviewLabels() []Label {
	return []Label{labelConstructive, labelVandalism, labelSpam, labelUnsourced, labelUnclear}
}

// damaging reports whether a confident verdict with this label should be flagged.
func (l Label) damaging() bool {
	switch l {
	case labelVandalism, labelSpam, labelUnsourced:
		return true
	default:
		return false
	}
}

// synonyms lists the words a model reaches for when it drifts off the label set.
func (l Label) synonyms() []string {
	switch l {
	case labelConstructive:
		return []string{"good", "good_faith", "benign", "legitimate", "improvement", "helpful", "ok", "fine", "valid", "productive"}
	case labelVandalism:
		return []string{
			"vandal", "vandalized", "vandalised", "damaging", "nonsense", "test", "test_edit", "blanking",
			"disruptive", "malicious", "hoax", "trolling",
		}
	case labelSpam:
		return []string{"promotional", "promotion", "advertising", "advert", "advertisement", "promo", "self_promotion", "link_spam", "linkspam"}
	case labelUnsourced:
		return []string{
			"unsourced", "uncited", "unverified", "unreferenced", "citation_needed", "needs_citation", "unsupported_claim",
			"original_research",
		}
	case labelUnclear:
		return []string{"uncertain", "unknown", "unsure", "ambiguous", "indeterminate", "cannot_tell"}
	default:
		return nil
	}
}
