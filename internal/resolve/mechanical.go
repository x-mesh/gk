package resolve

import (
	"path/filepath"
	"sort"
	"strings"
)

// Mechanical resolution: deterministic answers for conflict shapes that need
// no judgment. It runs as a pre-pass before AI (strategy "ai") and is the
// entirety of strategy "safe". The contract is: a hunk either has a PROVABLY
// safe resolution or it is left alone — this tier never guesses, so enabling
// it cannot introduce the silent-semantic-corruption failure mode that keeps
// AI resolution opt-in.

// StrategyMechanical marks hunks resolved by the deterministic pre-pass.
const StrategyMechanical Strategy = "mechanical"

// StrategySafe resolves only the mechanical tier and leaves every other
// conflict untouched (still marked, still unmerged) for AI or a human.
const StrategySafe Strategy = "safe"

// DefaultUnionFiles are basenames whose conflicts are line-set additions from
// both sides: keeping both is the correct merge. CHANGELOG entries are
// append-only prose; go.sum is a hash database where extra entries are
// harmless and missing ones fail loudly at build time.
var DefaultUnionFiles = []string{"CHANGELOG.md", "go.sum"}

// mechanicalFileResolutions returns deterministic resolutions for EVERY hunk
// in cf, or ok=false when any hunk needs judgment. All-or-nothing per file:
// mixing mechanical and AI hunks inside one file is Phase 2 territory.
func mechanicalFileResolutions(cf ConflictFile, unionFiles []string) ([]HunkResolution, bool) {
	var out []HunkResolution
	for _, seg := range cf.Segments {
		if seg.Hunk == nil {
			continue
		}
		hr, ok := mechanicalHunkResolution(cf.Path, seg.Hunk, unionFiles)
		if !ok {
			return nil, false
		}
		out = append(out, hr)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// mechanicalHunkResolution classifies one hunk. The rules, in order of
// certainty:
//
//  1. identical sides — any answer is the same answer.
//  2. whitespace-only difference — content is identical after collapsing
//     runs of whitespace; take ours (the difference cannot change meaning).
//  3. one side equals base (diff3 markers only) — the other side is the only
//     real change; taking it is what the merge would have produced had the
//     edits not been adjacent.
//  4. union files — both sides' lines are kept (go.sum additionally sorted
//     and deduplicated, matching its generated format).
func mechanicalHunkResolution(path string, h *ConflictHunk, unionFiles []string) (HunkResolution, bool) {
	if equalLines(h.Ours, h.Theirs) {
		return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: h.Ours, Rationale: "both sides identical"}, true
	}
	if equalLinesLoose(h.Ours, h.Theirs) {
		return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: h.Ours, Rationale: "sides differ only in whitespace"}, true
	}
	if h.Base != nil {
		if equalLines(h.Ours, h.Base) {
			return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: h.Theirs, Rationale: "ours unchanged from base — theirs is the only change"}, true
		}
		if equalLines(h.Theirs, h.Base) {
			return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: h.Ours, Rationale: "theirs unchanged from base — ours is the only change"}, true
		}
	}
	if isUnionFile(path, unionFiles) {
		if filepath.Base(path) == "go.sum" {
			return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: sortedUniqueUnion(h.Ours, h.Theirs), Rationale: "go.sum union — extra entries are harmless"}, true
		}
		return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: append(append([]string{}, h.Ours...), h.Theirs...), Rationale: "union merge — append-both file"}, true
	}
	return HunkResolution{}, false
}

// isUnionFile matches the path's basename against the configured union list.
func isUnionFile(path string, unionFiles []string) bool {
	if len(unionFiles) == 0 {
		unionFiles = DefaultUnionFiles
	}
	base := filepath.Base(path)
	for _, u := range unionFiles {
		if base == u {
			return true
		}
	}
	return false
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// equalLinesLoose compares after collapsing all whitespace runs inside each
// line and dropping blank lines — the strictest normalization that still
// counts indentation-only and trailing-space-only edits as "the same".
func equalLinesLoose(a, b []string) bool {
	na, nb := normalizeLines(a), normalizeLines(b)
	return equalLines(na, nb)
}

func normalizeLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		n := strings.Join(strings.Fields(l), " ")
		if n == "" {
			continue
		}
		out = append(out, n)
	}
	return out
}

// sortedUniqueUnion merges both sides as a sorted, deduplicated line set —
// go.sum's own format, so the result reads as if `go mod tidy` had written it.
func sortedUniqueUnion(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range append(append([]string{}, a...), b...) {
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}
