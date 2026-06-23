package sessionaudit

import "strings"

// HintResult is the git-kit guidance for a single shell command: the covered
// raw-git pattern it matched and how to replace it. Covered is false when the
// command needs no nudge (already git-kit, read-only plumbing, or non-git).
type HintResult struct {
	Covered    bool     `json:"covered"`
	Kind       string   `json:"kind,omitempty"`
	Severity   string   `json:"severity,omitempty"`
	CoveredBy  []string `json:"covered_by,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
	// Matched is the raw-git segment that triggered the hint, for the message.
	Matched string `json:"matched,omitempty"`
}

// Hint inspects a single shell command and returns the git-kit guidance for the
// highest-severity covered raw-git pattern it contains. It reuses the exact
// classifiers the session audit uses (gitSegmentFinding plus the gk short
// alias), so the audit and any PreToolUse hook share one source of truth — add
// a classifier once and both improve.
//
// tag/push are intentionally not matched here: raw-release-sequence is a
// cross-segment aggregate (it needs both), so a lone push is left for the
// ship/push verbs rather than nagged inline.
func Hint(command string) HintResult {
	class := classifyCommand(command)
	best := HintResult{}
	bestRank := -1

	consider := func(kind, matched string) {
		spec, ok := findingSpecs[kind]
		if !ok {
			return
		}
		if r := severityRank(spec.severity); r > bestRank {
			bestRank = r
			best = HintResult{
				Covered:    true,
				Kind:       kind,
				Severity:   spec.severity,
				CoveredBy:  append([]string(nil), spec.coveredBy...),
				Suggestion: spec.recommendation,
				Matched:    strings.TrimSpace(matched),
			}
		}
	}

	for _, seg := range class.Segments {
		switch seg.Tool {
		case "git":
			subcmd, args, ok := gitSubcommand(seg.Text)
			if !ok {
				continue
			}
			if kind := gitSegmentFinding(subcmd, args); kind != "" {
				consider(kind, seg.Text)
			}
		case "gk":
			consider("gk-short-alias", seg.Text)
		}
	}
	return best
}
