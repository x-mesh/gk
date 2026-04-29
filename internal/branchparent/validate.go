package branchparent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// maxParentDepth caps how deep the parent chain may go before we refuse to
// follow it. The number is large enough to accommodate realistic stacked
// workflows (a 5-deep stack is already exotic) and small enough that cycle
// detection terminates in O(1) for any pathological config — even without
// the visited-set check, depth alone would unwind us within microseconds.
const maxParentDepth = 10

// ValidateSet checks whether `branch` may have its parent set to `parent`.
// Returns nil when the assignment is safe; a descriptive error otherwise.
//
// Validations (in order — earliest cheapest):
//  1. parent is non-empty
//  2. parent != branch (no self-parent)
//  3. parent contains no '/' that resembles a remote prefix (origin/main)
//  4. parent's ref name is well-formed (`git check-ref-format --branch`)
//  5. refs/heads/<parent> exists locally (not a tag, not a remote-tracking)
//  6. assigning parent does not create a cycle (walk existing chain)
//
// The CLI write path runs this BEFORE Config.SetParent. Validation lives
// here, not in Config, so unit-tested business rules stay decoupled from
// the dumb git-config wrapper.
func ValidateSet(ctx context.Context, c *git.Client, branch, parent string) error {
	if parent == "" {
		return fmt.Errorf("parent must be a non-empty branch name")
	}
	if parent == branch {
		return fmt.Errorf("cannot set %q as its own parent", branch)
	}
	if isRemoteLike(parent) {
		hint := ""
		if i := strings.IndexByte(parent, '/'); i > 0 {
			hint = " (use the local branch name " + parent[i+1:] + ")"
		}
		return fmt.Errorf("parent must be a local branch, not a remote-tracking ref %q%s", parent, hint)
	}
	if err := c.CheckRefFormat(ctx, parent); err != nil {
		return fmt.Errorf("invalid parent name %q: %w", parent, err)
	}
	if !branchExists(ctx, c, parent) {
		// Distinguish "doesn't exist" from "exists as a tag" — both are
		// rejections, but the user-facing message helps debugging.
		if tagExists(ctx, c, parent) {
			return fmt.Errorf("%q is a tag, not a branch", parent)
		}
		// Best-effort fuzzy suggestion. Non-fatal if listing fails.
		if suggestion := suggestSimilarBranch(ctx, c, parent); suggestion != "" {
			return fmt.Errorf("branch %q does not exist; did you mean %q?", parent, suggestion)
		}
		return fmt.Errorf("branch %q does not exist", parent)
	}
	if cycle := detectCycle(ctx, c, branch, parent); cycle != "" {
		return fmt.Errorf("setting parent %q on %q would create a cycle: %s", parent, branch, cycle)
	}
	return nil
}

// isRemoteLike returns true when parent looks like a remote-tracking ref
// (e.g., "origin/main"). We reject these because divergence semantics
// against a remote ref are subtly wrong: the value drifts as the remote
// updates without any local action, leading to silent behavior changes.
//
// The heuristic is intentionally permissive: any value containing '/'.
// Local branches with slashes ("feat/x") share the syntax, so we accept
// those — the actual disambiguation happens in branchExists, which only
// returns true for refs/heads. The pre-check here just yields a clearer
// error message for the common origin/* mistake.
func isRemoteLike(parent string) bool {
	// Reject only when the segment before the first '/' looks like a
	// known remote name. This is heuristic — checking remotes for real
	// means another git spawn we don't want — so we use the convention
	// that "origin", "upstream", "fork" are common remote names.
	idx := strings.IndexByte(parent, '/')
	if idx <= 0 {
		return false
	}
	prefix := parent[:idx]
	switch prefix {
	case "origin", "upstream", "fork":
		return true
	}
	return false
}

func branchExists(ctx context.Context, c *git.Client, name string) bool {
	return git.RefExists(ctx, c.Raw(), "refs/heads/"+name)
}

func tagExists(ctx context.Context, c *git.Client, name string) bool {
	return git.RefExists(ctx, c.Raw(), "refs/tags/"+name)
}

// detectCycle walks parent's existing chain to see whether assigning it to
// branch would create a loop. Returns "" when safe, otherwise a string
// describing the cycle for inclusion in the error message.
//
// Walks at most maxParentDepth hops; longer chains are rejected as a
// safety net even when they aren't strictly cyclical (status would have
// to dereference each link, which is wasteful and a likely user error).
func detectCycle(ctx context.Context, c *git.Client, branch, parent string) string {
	cfg := NewConfig(c)
	visited := map[string]bool{branch: true}
	cur := parent
	chain := []string{branch, parent}
	for i := 0; i < maxParentDepth; i++ {
		if visited[cur] {
			return strings.Join(chain, " → ") + " → " + cur
		}
		visited[cur] = true
		next, err := cfg.GetParent(ctx, cur)
		if err != nil || next == "" {
			return ""
		}
		chain = append(chain, next)
		cur = next
	}
	return strings.Join(chain, " → ") + " (depth > " + fmt.Sprintf("%d", maxParentDepth) + ")"
}

// suggestSimilarBranch returns the closest local branch name to `parent`
// (Levenshtein, case-insensitive) when the distance is small enough to be
// useful — typically a 1- or 2-character typo. Returns "" when the listing
// fails or no plausible candidate exists, so callers can omit the hint.
func suggestSimilarBranch(ctx context.Context, c *git.Client, parent string) string {
	out, _, err := c.Raw().Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return ""
	}
	want := strings.ToLower(parent)
	type cand struct {
		name string
		dist int
	}
	var best cand
	best.dist = len(parent) // upper bound
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		d := levenshtein(strings.ToLower(name), want)
		if d < best.dist {
			best = cand{name: name, dist: d}
		}
	}
	// Only suggest when the distance is small relative to the length —
	// otherwise we'd be guessing.
	threshold := len(parent) / 3
	if threshold < 2 {
		threshold = 2
	}
	if best.dist > threshold {
		return ""
	}
	return best.name
}

// levenshtein is a small, allocation-light implementation. We only call it
// against local branch names (typically <50 entries × <30 chars), so an
// O(n*m) loop is cheaper than pulling in a dependency.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// localBranchNames is exposed for callers that want a deterministic,
// alphabetically-sorted list (e.g., for help output). Currently used only
// by tests; the production fuzzy-match path consumes raw `for-each-ref`
// output without re-sorting.
func localBranchNames(ctx context.Context, c *git.Client) []string {
	out, _, err := c.Raw().Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
