package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/branchparent"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// This file holds the opt-in `gk status` layers added in the layer-priority
// round (.xm/op/tournament-2026-06-11-gk-status-log-layer-priority.json):
// wip, squash, ancestry, collision. Each renders one line (or a short list)
// and returns "" / nil when it has nothing to say — silence is the default,
// matching the stash/base layers.

// renderWIPDebtLine reports the contiguous WIP chain sitting at HEAD:
//
//	wip: ×3 at HEAD ("save") · unwraps on gk commit
//
// Reuses the same detection (patterns + chain walk) as `gk commit`'s WIP
// unwrap, so the badge never disagrees with what commit would actually fold.
func renderWIPDebtLine(ctx context.Context, runner git.Runner, cfg *config.Config) string {
	var custom []string
	maxChain := 0
	if cfg != nil {
		custom = cfg.AI.Commit.WIPPatterns
		maxChain = cfg.AI.Commit.WIPMaxChain
	}
	patterns, err := aicommit.CompileWIPPatterns(custom)
	if err != nil {
		return ""
	}
	chain, _, err := aicommit.DetectWIPChain(ctx, runner, aicommit.DetectWIPChainOptions{
		MaxChain: maxChain,
		Patterns: patterns,
	})
	if err != nil || len(chain) == 0 {
		return ""
	}
	faint := color.New(color.Faint).SprintFunc()
	subj := chain[0].Subject
	if r := []rune(subj); len(r) > 36 {
		subj = string(r[:35]) + "…"
	}
	return fmt.Sprintf("%s %s %s",
		faint("wip:"),
		color.YellowString("×%d", len(chain)),
		faint(fmt.Sprintf("at HEAD (%q) · unwraps on gk commit", subj)))
}

// renderSquashDebtLine counts squash candidates (fixup!/squash!/WIP
// subjects) in the not-yet-landed range:
//
//	squash debt: ◈ 3 (2 fixup · 1 wip) · gk rebase --plan-template
//
// Range is @{upstream}..HEAD when an upstream exists, else <base>..HEAD —
// for a never-pushed branch everything above the base is debt-eligible.
// Capped at 50 commits so a long-lived branch can't blow the time budget.
func renderSquashDebtLine(ctx context.Context, runner git.Runner, cfg *config.Config, upstream, baseResolved, branch string) string {
	var rangeRef string
	switch {
	case upstream != "":
		rangeRef = "@{upstream}..HEAD"
	case baseResolved != "" && baseResolved != branch:
		rangeRef = baseResolved + "..HEAD"
	default:
		return ""
	}
	out, _, err := runner.Run(ctx, "log", "-n", "50", "--format=%s", rangeRef)
	if err != nil {
		return ""
	}
	var custom []string
	if cfg != nil {
		custom = cfg.AI.Commit.WIPPatterns
	}
	patterns, perr := aicommit.CompileWIPPatterns(custom)
	if perr != nil {
		return ""
	}
	isWIP := func(s string) bool { return aicommit.IsWIPSubject(s, patterns) }

	fixup, wip := countSquashDebt(string(out), isWIP)
	total := fixup + wip
	if total == 0 {
		return ""
	}
	var parts []string
	if fixup > 0 {
		parts = append(parts, fmt.Sprintf("%d fixup", fixup))
	}
	if wip > 0 {
		parts = append(parts, fmt.Sprintf("%d wip", wip))
	}
	faint := color.New(color.Faint).SprintFunc()
	return fmt.Sprintf("%s %s %s",
		faint("squash debt:"),
		color.YellowString("◈ %d", total),
		faint("("+strings.Join(parts, " · ")+") · folds via gk rebase --plan"))
}

// countSquashDebt buckets the subjects of a `git log --format=%s` dump into
// autosquash markers (fixup!/squash!) and WIP-style subjects. A subject that
// is both (e.g. "fixup! WIP") counts once, as fixup — the more specific kind.
func countSquashDebt(subjects string, isWIP func(string) bool) (fixup, wip int) {
	for _, s := range strings.Split(subjects, "\n") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		switch {
		case strings.HasPrefix(s, "fixup!") || strings.HasPrefix(s, "squash!"):
			fixup++
		case isWIP(s):
			wip++
		}
	}
	return fixup, wip
}

// ancestryMaxHops caps the parent-chain walk: deeper stacks almost certainly
// mean a metadata cycle or abandoned config, not a real 7-branch stack.
const ancestryMaxHops = 6

// renderAncestryLine walks the fork-parent chain (branch.<name>.gk-parent)
// upward and renders the branch's position in its stack:
//
//	depth: feat/ui → develop → main (2 hops · +18c vs develop)
//
// Silent when the branch has no parent metadata — the layer only speaks for
// stacked-branch users, everyone else sees nothing new. The +Nc count is
// against the immediate parent (the hop that `gk land --promote` walks
// first).
func renderAncestryLine(ctx context.Context, runner git.Runner, client *git.Client, branch string) string {
	pc := branchparent.NewConfig(client)
	chain := []string{branch}
	seen := map[string]bool{branch: true}
	cur := branch
	for hop := 0; hop < ancestryMaxHops; hop++ {
		parent, err := pc.GetParent(ctx, cur)
		if err != nil || parent == "" {
			break
		}
		if seen[parent] { // cycle in metadata — stop, render what we have
			break
		}
		chain = append(chain, parent)
		seen[parent] = true
		cur = parent
	}
	if len(chain) < 2 {
		return ""
	}

	ahead := ""
	if out, _, err := runner.Run(ctx, "rev-list", "--count", chain[1]+"..HEAD"); err == nil {
		if n := strings.TrimSpace(string(out)); n != "" && n != "0" {
			ahead = fmt.Sprintf(" · +%sc vs %s", n, chain[1])
		}
	}
	hops := len(chain) - 1
	faint := color.New(color.Faint).SprintFunc()
	return fmt.Sprintf("%s %s %s",
		faint("depth:"),
		strings.Join(chain, faint(" → ")),
		faint(fmt.Sprintf("(%d %s%s)", hops, pluralize(hops, "hop", "hops"), ahead)))
}

// collisionWorktreeCap bounds the per-status subprocess cost: one
// `git status --porcelain` per other worktree, at most this many.
const collisionWorktreeCap = 8

// renderWorktreeCollisions warns when a file dirty HERE is also dirty in
// another linked worktree — the situation that later explodes as a merge
// conflict between the two branches:
//
//	⊠ 2 files also dirty in develop (app.go, lib.go)
//
// One line per colliding worktree; clean overlaps render nothing.
func renderWorktreeCollisions(ctx context.Context, runner *git.ExecRunner, entries []git.StatusEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	others := listOtherWorktrees(ctx, runner)
	if len(others) == 0 {
		return nil
	}
	if len(others) > collisionWorktreeCap {
		others = others[:collisionWorktreeCap]
	}
	dirty := make(map[string]bool, len(entries))
	for _, e := range entries {
		dirty[e.Path] = true
	}

	var lines []string
	for _, wt := range others {
		wtRunner := &git.ExecRunner{Dir: wt.Path}
		out, _, err := wtRunner.Run(ctx, "status", "--porcelain")
		if err != nil {
			continue
		}
		overlap := porcelainPathOverlap(string(out), dirty)
		if len(overlap) == 0 {
			continue
		}
		label := wt.Branch
		if label == "" {
			label = condenseHomePath(wt.Path)
		}
		lines = append(lines, formatCollisionLine(label, overlap))
	}
	return lines
}

// porcelainPathOverlap extracts the paths from `git status --porcelain`
// output and returns (sorted, deduplicated) the ones present in dirty.
// Rename entries contribute both sides; git's quoting of special-char
// paths is stripped naively (good enough for an advisory line).
func porcelainPathOverlap(porcelain string, dirty map[string]bool) []string {
	seen := map[string]bool{}
	for _, ln := range strings.Split(porcelain, "\n") {
		if len(ln) < 4 {
			continue
		}
		for _, p := range strings.SplitN(ln[3:], " -> ", 2) {
			p = strings.TrimSpace(p)
			p = strings.Trim(p, `"`)
			if p != "" && dirty[p] {
				seen[p] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	hits := make([]string, 0, len(seen))
	for p := range seen {
		hits = append(hits, p)
	}
	sort.Strings(hits)
	return hits
}

// formatCollisionLine renders one worktree's collision summary, listing at
// most three file names with a "+N" tail for the rest.
func formatCollisionLine(label string, files []string) string {
	show := files
	more := 0
	if len(show) > 3 {
		more = len(show) - 3
		show = show[:3]
	}
	names := strings.Join(show, ", ")
	if more > 0 {
		names += fmt.Sprintf(" +%d", more)
	}
	return fmt.Sprintf("%s %d %s also dirty in %s %s",
		color.New(color.FgYellow, color.Bold).Sprint("⊠"),
		len(files), pluralize(len(files), "file", "files"),
		color.CyanString(label),
		color.New(color.Faint).Sprint("("+names+")"))
}
