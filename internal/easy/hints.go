package easy

import (
	"fmt"
	"strings"

	"github.com/x-mesh/gk/internal/i18n"
)

// HintLevel controls the verbosity of contextual hints.
type HintLevel string

const (
	// HintVerbose includes full command descriptions and examples.
	HintVerbose HintLevel = "verbose"
	// HintMinimal shows only the command, brief.
	HintMinimal HintLevel = "minimal"
	// HintOff suppresses all hints.
	HintOff HintLevel = "off"
)

// HintGenerator produces contextual next-action hints based on the
// current hint level, message catalog, and emoji mapper.
type HintGenerator struct {
	level   HintLevel
	catalog *i18n.Catalog
	emoji   *EmojiMapper
}

// NewHintGenerator creates a HintGenerator with the given verbosity
// level, message catalog, and emoji mapper.
func NewHintGenerator(level HintLevel, catalog *i18n.Catalog, emoji *EmojiMapper) *HintGenerator {
	return &HintGenerator{
		level:   level,
		catalog: catalog,
		emoji:   emoji,
	}
}

// Generate looks up a hint key from the catalog and formats it with
// the supplied arguments. Returns "" when the level is HintOff or the
// generator/catalog is nil.
func (g *HintGenerator) Generate(key string, args ...interface{}) string {
	if g == nil || g.level == HintOff || g.catalog == nil {
		return ""
	}

	// For minimal mode, try the ".minimal" suffixed key first.
	if g.level == HintMinimal {
		minKey := key + ".minimal"
		if g.catalog.Has(minKey) {
			return g.catalog.Getf(minKey, args...)
		}
		// Fall through to the regular key if no minimal variant exists.
	}

	return g.catalog.Getf(key, args...)
}

// statusHintKeys defines the priority order for status hints.
// Conflict has the highest priority, then staged, unstaged, untracked.
var statusHintKeys = []struct {
	flag bool   // placeholder — replaced at call time
	key  string // catalog key for the hint
}{
	{key: "hint.status.has_conflict"},
	{key: "hint.status.has_staged"},
	{key: "hint.status.has_unstaged"},
	{key: "hint.status.has_untracked"},
}

// StatusHint generates a contextual next-step hint based on the
// current git working tree state. Priority order:
// conflict > staged > unstaged > untracked.
//
// Returns "" when the level is HintOff, no flags are true, or the
// generator is nil.
func (g *HintGenerator) StatusHint(hasStaged, hasUnstaged, hasUntracked, hasConflict bool) string {
	if g == nil || g.level == HintOff {
		return ""
	}

	flags := []bool{hasConflict, hasStaged, hasUnstaged, hasUntracked}

	for i, f := range flags {
		if f {
			return g.Generate(statusHintKeys[i].key)
		}
	}

	return ""
}

// SyncHint generates a contextual hint when the working tree is
// otherwise clean, based purely on upstream divergence. Priority order:
// diverged (both ahead and behind) > behind > ahead > in sync.
//
// hasUpstream reports whether the current branch tracks an upstream;
// without one we cannot speak to "in sync" so the function returns "".
//
// Returns "" when the level is HintOff or the generator is nil.
func (g *HintGenerator) SyncHint(ahead, behind int, hasUpstream bool) string {
	if g == nil || g.level == HintOff {
		return ""
	}
	if !hasUpstream {
		return ""
	}
	switch {
	case ahead > 0 && behind > 0:
		return g.Generate("hint.status.diverged", ahead, behind)
	case behind > 0:
		return g.Generate("hint.status.behind", behind)
	case ahead > 0:
		return g.Generate("hint.status.ahead", ahead)
	default:
		return g.Generate("hint.status.clean_synced")
	}
}

// Level returns the current hint verbosity level.
func (g *HintGenerator) Level() HintLevel {
	if g == nil {
		return HintOff
	}
	return g.level
}

// MergeIntoNextHint produces the next-step hints emitted right after a
// successful `gk merge --into <receiver>`. Always returns a push hint;
// when the source branch is fully merged into receiver, also returns a
// cleanup hint nudging the user to delete the source branch. Result is
// returned as a slice so callers can decide whether to print one per
// line or join them.
//
// Returns nil when the level is HintOff or the generator is nil.
func (g *HintGenerator) MergeIntoNextHint(receiver string, sourceFullyMerged bool, source string) []string {
	if g == nil || g.level == HintOff {
		return nil
	}
	out := make([]string, 0, 2)
	if h := g.Generate("hint.merge.into.next_push", receiver); h != "" {
		out = append(out, h)
	}
	if sourceFullyMerged && source != "" && source != receiver {
		if h := g.Generate("hint.merge.into.cleanup_source", source, source); h != "" {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PushSummaryHint returns a one-line summary for a successful `gk push`.
// When n > 0 it reports how many commits were uploaded; n == 0 falls
// back to the up-to-date message (same wording as a no-op push).
//
// Returns "" when the level is HintOff or the generator is nil.
func (g *HintGenerator) PushSummaryHint(n int, remote, branch, short string) string {
	if g == nil || g.level == HintOff {
		return ""
	}
	if n <= 0 {
		return g.Generate("hint.push.up_to_date", remote, branch, short)
	}
	return g.Generate("hint.push.summary", n, remote, branch, short)
}

// WorktreeWorkItem describes a single worktree's pending work for the
// cross-worktree status hint. Branch is the short branch name (e.g.
// "feat/x"); Detail is a free-form short label like "↑3", "↓2", "↑1 ↓4",
// "dirty", or "stale". Empty Detail means "nothing to flag".
type WorktreeWorkItem struct {
	Branch string
	Detail string
}

// StatusCrossWorktreeHint produces a one-line summary of pending work
// across other worktrees for `gk st` when the current worktree is clean
// and in sync. Items with empty Detail are filtered out. When more than
// 3 items have work, only the first 3 are listed and an extra "+N more"
// suffix is appended.
//
// total is the total number of worktrees inspected (used for the
// "all clean" message when items is empty).
//
// Returns "" when the level is HintOff or the generator is nil.
func (g *HintGenerator) StatusCrossWorktreeHint(items []WorktreeWorkItem, total int) string {
	if g == nil || g.level == HintOff {
		return ""
	}
	withWork := make([]WorktreeWorkItem, 0, len(items))
	for _, it := range items {
		if it.Detail != "" {
			withWork = append(withWork, it)
		}
	}
	if len(withWork) == 0 {
		return g.Generate("hint.status.all_clean_worktrees", total)
	}
	limit := 3
	if len(withWork) < limit {
		limit = len(withWork)
	}
	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		parts = append(parts, g.Generate("hint.status.cross_worktree", withWork[i].Branch, withWork[i].Detail))
	}
	out := strings.Join(parts, "  ·  ")
	if extra := len(withWork) - limit; extra > 0 {
		out += fmt.Sprintf("  · +%d more", extra)
	}
	return out
}
