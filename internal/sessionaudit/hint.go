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
	// Caution stands on its own axis: it fires for a command gk does NOT cover
	// but that destroys uncommitted work, so Covered stays false and no
	// replacement is claimed. Silence is the right answer for an ordinary gap;
	// it is the wrong answer for one that is irreversible.
	Caution string `json:"caution,omitempty"`
	// CautionMatched is the segment that raised Caution.
	CautionMatched string `json:"caution_matched,omitempty"`
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
	cautionMatched := ""

	consider := func(kind, matched string) {
		spec, ok := findingSpecs[kind]
		if !ok {
			return
		}
		// A hint answers "run this instead". A kind with status "gap" (e.g.
		// raw-history-search: gk has no --grep/-S/pathspec log) has no such
		// answer, so the hint — and the PreToolUse hook built on it — must stay
		// SILENT rather than nag with an empty replacement. Nagging an agent
		// toward a command that cannot answer its question is exactly the
		// over-claim this classifier split exists to remove.
		if spec.status != "covered" || len(spec.coveredBy) == 0 {
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
			// Independent of the replacement search: a command can be both
			// uncovered and irreversible, and that is exactly the case where
			// staying silent reads as approval. Held aside rather than written
			// into best, which consider() replaces wholesale on a better match.
			if cautionMatched == "" && isDestructiveDiscard(subcmd, args) {
				cautionMatched = strings.TrimSpace(seg.Text)
			}
		case "gk":
			consider("gk-short-alias", seg.Text)
		}
	}
	if cautionMatched != "" {
		best.Caution = discardCaution
		best.CautionMatched = cautionMatched
	}
	return best
}
