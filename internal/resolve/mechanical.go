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
//  2. trailing-whitespace / line-ending difference only — same line count,
//     every line equal after trimming trailing spaces/tabs/CR. This is the
//     only whitespace class that is meaning-free in EVERY language; internal
//     spacing (string literals) and indentation (Python, Makefile, YAML)
//     carry meaning and stay out (cross-vendor review, 2 vendors).
//  3. one side equals base (diff3 markers only) — the other side is the only
//     real change; taking it is what the merge would have produced had the
//     edits not been adjacent.
//  4. union files, only when provably additive: with diff3 info the base
//     block must be empty (both sides ADDED lines); a non-empty base means a
//     rewrite/delete conflict where concatenation is wrong. go.sum is
//     additionally sorted/deduplicated, and refuses when both sides carry a
//     DIFFERENT hash for the same module@version — that mismatch is the
//     exact tampering signal go.sum exists to surface.
func mechanicalHunkResolution(path string, h *ConflictHunk, unionFiles []string) (HunkResolution, bool) {
	if equalLines(h.Ours, h.Theirs) {
		return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: h.Ours, Rationale: "both sides identical"}, true
	}
	if equalLinesTrailingWS(h.Ours, h.Theirs) {
		return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: h.Ours, Rationale: "sides differ only in trailing whitespace / line endings"}, true
	}
	if h.Base != nil {
		if equalLines(h.Ours, h.Base) {
			return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: h.Theirs, Rationale: "ours unchanged from base — theirs is the only change"}, true
		}
		if equalLines(h.Theirs, h.Base) {
			return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: h.Ours, Rationale: "theirs unchanged from base — ours is the only change"}, true
		}
	}
	if isUnionFile(path, unionFiles) && unionAdditive(h) {
		if filepath.Base(path) == "go.sum" {
			lines, ok := goSumUnion(h.Ours, h.Theirs)
			if !ok {
				return HunkResolution{}, false // same module@version, different hash
			}
			return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: lines, Rationale: "go.sum union — extra entries are harmless"}, true
		}
		return HunkResolution{Strategy: StrategyMechanical, ResolvedLines: append(append([]string{}, h.Ours...), h.Theirs...), Rationale: "union merge — both sides added"}, true
	}
	return HunkResolution{}, false
}

// unionAdditive reports whether the hunk is safe to union: with diff3 base
// info, both sides must be pure additions (empty base block). Without diff3
// markers the base is unknown — union proceeds, a documented limitation.
func unionAdditive(h *ConflictHunk) bool {
	if h.Base == nil {
		return true
	}
	for _, l := range h.Base {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// isUnionFile matches the path's basename against the configured union list.
// nil means "use the defaults"; an explicit EMPTY list disables union merging
// entirely (`resolve.union_files: []`).
func isUnionFile(path string, unionFiles []string) bool {
	if unionFiles == nil {
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

// equalLinesTrailingWS reports line-by-line equality after trimming ONLY
// trailing spaces/tabs/CR. Line count must match — a blank-line difference
// is a content difference (markdown paragraphs), not noise.
func equalLinesTrailingWS(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimRight(a[i], " \t\r") != strings.TrimRight(b[i], " \t\r") {
			return false
		}
	}
	return true
}

// goSumUnion merges both sides as a sorted, deduplicated line set — go.sum's
// own format. It refuses (ok=false) when the two sides carry DIFFERENT
// hashes for the same "module version" key: a checksum mismatch is the
// tampering signal go.sum exists to raise, never something to merge away.
func goSumUnion(a, b []string) ([]string, bool) {
	byKey := map[string]string{}
	seen := map[string]bool{}
	var out []string
	for _, l := range append(append([]string{}, a...), b...) {
		if l == "" || seen[l] {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) >= 2 {
			key := fields[0] + " " + fields[1]
			if prev, dup := byKey[key]; dup && prev != l {
				return nil, false
			}
			byKey[key] = l
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Strings(out)
	return out, true
}
