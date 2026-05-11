package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/branchparent"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// worktreeInfo describes the worktree the current invocation runs from.
// IsPrimary is true when the worktree is the original (non-linked) one —
// which is the only case where we suppress the BRANCH section's worktree
// annotation, since the primary worktree is the implicit default.
type worktreeInfo struct {
	Name      string
	Path      string
	IsPrimary bool
}

// currentWorktreeInfo returns the worktree the runner points at. Returns
// nil + error when not in a git repo or git's worktree machinery fails;
// callers should treat any error as "skip the worktree annotation" rather
// than aborting the BRANCH section render.
//
// Determines primary by position: `git worktree list --porcelain` always
// emits the original worktree first.
func currentWorktreeInfo(ctx context.Context, runner *git.ExecRunner) (*worktreeInfo, error) {
	out, _, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, strings.TrimSpace(rest))
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("no worktrees")
	}

	topOut, _, err := runner.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(string(topOut)))
	if err != nil {
		top = strings.TrimSpace(string(topOut))
	}

	for i, p := range paths {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			resolved = p
		}
		if filepath.Clean(resolved) == filepath.Clean(top) {
			return &worktreeInfo{
				Name:      filepath.Base(top),
				Path:      top,
				IsPrimary: i == 0,
			}, nil
		}
	}
	// Fall through: cwd doesn't match any reported worktree (rare —
	// could happen with bind-mounts or symlinked repos that don't
	// EvalSymlinks the same way). Treat as unknown.
	return nil, errors.New("current dir not found in worktree list")
}

// renderBranchSection produces the BRANCH section for rich-mode output.
// Replaces the legacy approach (extract the first line of the captured
// body and shove it into the section's summary slot), which lost the
// branch name to dim-wrapping at the section chrome and never surfaced
// worktree context at all.
//
// Layout:
//
//	█  BRANCH
//	   feature/tmux ← main  @ tmux  ⇄ origin/feature/tmux  ↑0 ↓0  · last commit 22m abc1234
//	   wt: ~/work/.../gk/tmux
//
// The `← main` segment names the fork parent (resolved through
// branchparent so per-branch metadata wins over origin/HEAD); `@ tmux`
// and `wt:` are suppressed on the primary worktree to keep the common
// case terse. base="" or base==displayBranch suppress the parent tag.
func renderBranchSection(cmd *cobra.Command, runner *git.ExecRunner, st *git.Status, layout ui.SectionLayout, displayBranch, displayUpstream, baseTrunk string) string {
	// Resolve the fork parent through branchparent so per-branch metadata
	// (gk-parent ref) wins over the configured trunk. ResolveBase is the
	// silent variant — issues are surfaced once by renderBaseDivergence in
	// the WORKING TREE flow, so duplicating them here would just spam.
	var base string
	if st.Branch != "" && st.Branch != "(detached)" {
		base = branchparent.NewResolver(git.NewClient(runner)).ResolveBase(cmd.Context(), st.Branch, baseTrunk)
	}
	bold := color.New(color.Bold).SprintFunc()
	cyan := color.CyanString
	faint := color.New(color.Faint).SprintFunc()

	wt, _ := currentWorktreeInfo(cmd.Context(), runner)
	showWT := wt != nil && !wt.IsPrimary

	var head strings.Builder
	detached := st.Branch == "" || st.Branch == "(detached)"
	if detached {
		head.WriteString(color.YellowString("⚠ detached"))
		if sha := detachedShortSHA(cmd.Context(), runner); sha != "" {
			head.WriteString(" at " + sha)
		}
	} else {
		head.WriteString(bold(displayBranch))
		// Fork parent: surface the branch this one was cut from. Skip
		// when on the trunk itself (base == current) or when the
		// resolver couldn't pin one down — false confidence here would
		// mislead a stacked workflow more than no info at all.
		if base != "" && base != displayBranch {
			head.WriteString(" " + faint("←") + " " + cyan(base))
		}
		if showWT {
			head.WriteString("  " + faint("@") + " " + cyan(wt.Name))
		}
		if displayUpstream != "" {
			head.WriteString("  " + faint("⇄") + " " + cyan(displayUpstream))
			if st.Ahead != 0 || st.Behind != 0 {
				fmt.Fprintf(&head, "  ↑%d ↓%d", st.Ahead, st.Behind)
			}
		}
	}

	// Trailing informational suffixes — staleness + since-push. Re-use the
	// same helpers the legacy renderer called so output stays in lock-step
	// with `gk status` (compact mode), which still goes through the older
	// path.
	if statusVisEnabled("staleness") {
		if ago, sha := lastCommitAgo(cmd, runner); ago != "" {
			text := "· last commit " + ago
			if sha != "" {
				text += " " + sha
			}
			head.WriteString("  " + faint(text))
		}
	}
	if statusVisEnabled("since-push") && !detached {
		if unpushed, ok := sincePushSuffix(cmd.Context(), runner); ok && unpushed != "" {
			head.WriteString("  " + faint("· "+unpushed))
		}
	}

	lines := []string{head.String()}
	if showWT {
		lines = append(lines, faint("wt:")+" "+wt.Path)
	}
	return renderSection("branch", "", lines, layout)
}
