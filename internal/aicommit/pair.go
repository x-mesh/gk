package aicommit

import (
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// PairTestProdGroups merges Go test files into their production-file
// group when the heuristic (or LLM) parked them in a separate "test"
// group. Returns the input slice with empty groups dropped.
//
// Why: heuristicGroups buckets files purely on heuristicType — so
// `switch.go` (type "chore") and `switch_test.go` (type "test") always
// split, even when co-changed. The LLM Classify path can also miss
// the pairing when the prod file lacks strong type signal. Merging
// here lets one Compose call see both diffs and emit one cohesive
// message; it also stops --dry-run from showing the worst-case split.
//
// Scope deliberately conservative (debate verdict — Option A):
//
//   - Go only: `foo_test.go` ↔ `foo.go`. TS/Py/Rust skipped — basename
//     ambiguity (`.ts`/`.tsx`, `test_foo.py` vs `tests/foo_test.py`,
//     in-file `#[cfg(test)]`) costs more in maintenance than it gains.
//   - Pair only when the production counterpart sits in the same
//     changeset. Orphan tests (integration_test.go, helpers_test.go,
//     or foo_test.go with no foo.go in the diff) stay in the test
//     group as before.
//   - The merged group inherits the production group's Type/Scope —
//     no chore → feat promotion. Compose still sees the original
//     label; it just gets richer diff context.
func PairTestProdGroups(groups []provider.Group) []provider.Group {
	if len(groups) < 2 {
		return groups
	}

	// path → index of the non-test group that contains it. Used to
	// look up where each test's production counterpart lives.
	prodLoc := map[string]int{}
	for i, g := range groups {
		if g.Type == "test" {
			continue
		}
		for _, f := range g.Files {
			prodLoc[f] = i
		}
	}
	if len(prodLoc) == 0 {
		return groups
	}

	out := make([]provider.Group, len(groups))
	copy(out, groups)

	for ti := range out {
		if out[ti].Type != "test" {
			continue
		}
		kept := out[ti].Files[:0:0]
		for _, tf := range out[ti].Files {
			counterpart := goProdCounterpart(tf)
			if counterpart == "" {
				kept = append(kept, tf)
				continue
			}
			pi, ok := prodLoc[counterpart]
			if !ok {
				kept = append(kept, tf)
				continue
			}
			out[pi].Files = append(out[pi].Files, tf)
		}
		out[ti].Files = kept
	}

	// Single-prod fallback: when only one non-test group survives the
	// pair pass, orphan tests have no ambiguity about where they
	// belong — the user is clearly committing one logical change.
	// Merge them in. With multiple prod groups we play it safe and
	// leave orphans in their test group (the C3 case from the debate
	// verdict — integration_test.go etc. across cross-cutting changes).
	out = mergeOrphansIntoSoleProdGroup(out)

	// Drop now-empty groups.
	res := out[:0]
	for _, g := range out {
		if len(g.Files) == 0 {
			continue
		}
		res = append(res, g)
	}
	return res
}

// mergeOrphansIntoSoleProdGroup absorbs every remaining test-group
// file into the single non-test group, when exactly one such group
// exists. No-op otherwise.
func mergeOrphansIntoSoleProdGroup(groups []provider.Group) []provider.Group {
	prodIdx := -1
	for i, g := range groups {
		if len(g.Files) == 0 || g.Type == "test" {
			continue
		}
		if prodIdx >= 0 {
			return groups // multiple prod groups — fallback doesn't apply
		}
		prodIdx = i
	}
	if prodIdx < 0 {
		return groups // no prod group at all — nothing to merge into
	}
	for i, g := range groups {
		if i == prodIdx || g.Type != "test" || len(g.Files) == 0 {
			continue
		}
		groups[prodIdx].Files = append(groups[prodIdx].Files, g.Files...)
		groups[i].Files = nil
	}
	return groups
}

// goProdCounterpart returns the production-file path that a Go test
// file would target (`internal/cli/switch_test.go` →
// `internal/cli/switch.go`). Empty when the input isn't a Go test
// file.
func goProdCounterpart(testPath string) string {
	if !strings.HasSuffix(testPath, "_test.go") {
		return ""
	}
	dir := filepath.Dir(testPath)
	base := filepath.Base(testPath)
	stem := strings.TrimSuffix(base, "_test.go")
	return filepath.Join(dir, stem+".go")
}
