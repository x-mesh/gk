package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// syncPullBase implements `gk pull --with-base`: after pull's own fetch it
// fast-forwards the repo's base branch from the remote without checking it
// out, so the morning multi-branch sync ("pull develop, switch, pull main")
// collapses into one command. It runs before integration on purpose — a
// conflict on the current branch must not strand the base update.
//
// base is the already-resolved --base/.gk.yaml value ("" → auto-detect);
// current is the branch being pulled; fetchBranch names the branch pull has
// just fetched so the same round-trip is not repeated when it is the base.
//
// Returns the branch-column labeler sized to cover both branch names, so the
// caller's own result lines (already-up-to-date, integrating, updated) align
// with the base lines printed here. When the base does not participate, the
// labeler covers just the current branch.
func syncPullBase(ctx context.Context, client *git.Client, runner git.Runner, w io.Writer, remote, base, current, fetchBranch string) func(string) string {
	curLabel := current
	if curLabel == "" {
		curLabel = "HEAD"
	}
	name := base
	if name == "" {
		detected, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			printNote(w, "--with-base: could not detect the base branch — set it with --base or `base` in .gk.yaml")
			return branchLabeler(curLabel)
		}
		name = detected
	}
	if current != "" && current == name {
		// Pulling the base itself — the regular pull already covers it.
		return branchLabeler(curLabel)
	}
	labeler := branchLabeler(curLabel, name)
	ffSyncBranches(ctx, runner, w, remote, []string{name}, fetchBranch, labeler)
	return labeler
}

// branchLabeler renders the branch-name column that prefixes every per-branch
// status line of a pull. The column is padded to the longest participating
// name so multi-branch output (--with-base) aligns vertically:
//
//	main     ✓ fast-forwarded 4ce31df → 3c33feeb
//	develop  updated 6a84c6ad → 2527b1fd  (+1 commit · ff-only)
//
// Names render cyan-bold; padding is computed on the raw name so ANSI codes
// cannot skew the column.
func branchLabeler(names ...string) func(string) string {
	width := 0
	for _, n := range names {
		if l := utf8.RuneCountInString(n); l > width {
			width = l
		}
	}
	return func(name string) string {
		pad := width - utf8.RuneCountInString(name)
		if pad < 0 {
			pad = 0
		}
		return cellCyanBold(name) + strings.Repeat(" ", pad+2)
	}
}

// ffSyncBranches fast-forwards each named local branch to <remote>/<branch>
// without a checkout. Strictly FF-only: every ambiguous state — diverged
// branch, branch checked out in some worktree, missing local or remote ref —
// is skipped with a NOTE rather than resolved automatically, so no local
// commit can ever be lost and no working tree is ever touched.
//
// Kept list-shaped so a future `pull.sync_branches` config can reuse it
// unchanged; today the only caller passes the single base branch.
func ffSyncBranches(ctx context.Context, runner git.Runner, w io.Writer, remote string, branches []string, skipFetchFor string, label func(string) string) {
	for _, b := range branches {
		ffSyncBranch(ctx, runner, w, remote, b, b == skipFetchFor, label)
	}
}

func ffSyncBranch(ctx context.Context, runner git.Runner, w io.Writer, remote, branch string, alreadyFetched bool, label func(string) string) {
	remoteRef := remote + "/" + branch

	if !alreadyFetched {
		stop := ui.StartBubbleSpinner(fmt.Sprintf("fetching %s (base)", remoteRef))
		_, stderr, err := runner.Run(ctx, "fetch", remote, branch)
		stop()
		if err != nil {
			printNote(w, fmt.Sprintf("base '%s' not synced — fetch failed: %s",
				branch, strings.TrimSpace(string(stderr))))
			return
		}
	}

	// The local branch must already exist — creating refs behind the
	// user's back is out of scope for a convenience sync.
	oldOut, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		printNote(w,
			fmt.Sprintf("base '%s' not synced — no local branch", branch),
			fmt.Sprintf("create it with: gk sw %s", branch),
		)
		return
	}
	oldSHA := strings.TrimSpace(string(oldOut))

	newOut, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", remoteRef)
	if err != nil {
		printNote(w, fmt.Sprintf("base '%s' not synced — '%s' does not exist after fetch", branch, remoteRef))
		return
	}
	newSHA := strings.TrimSpace(string(newOut))

	if oldSHA == newSHA {
		fmt.Fprintln(w, label(branch)+cellFaint("already up to date at "+shortSHA(oldSHA)))
		return
	}

	// A branch checked out anywhere must not have its ref moved under the
	// working tree. The current worktree holds the branch being pulled, so
	// a hit here means the primary checkout or a linked worktree owns it.
	if entry, werr := findWorktreeForBranch(ctx, runner, branch); werr == nil && entry != nil {
		printNote(w,
			fmt.Sprintf("base '%s' not synced — checked out in %s", branch, entry.Path),
			"run gk pull there to update it",
		)
		return
	}

	// FF-only gate: the local tip must be a strict ancestor of the remote
	// tip, which makes losing local commits structurally impossible.
	if _, _, err := runner.Run(ctx, "merge-base", "--is-ancestor", oldSHA, newSHA); err != nil {
		printNote(w, appendEasyLine([]string{
			fmt.Sprintf("base '%s' not synced — it has local commits not on %s", branch, remoteRef),
			fmt.Sprintf("integrate them with: gk sw %s && gk pull", branch),
		}, "pull.note.with_base_diverged", branch, remoteRef)...)
		return
	}

	// Compare-and-swap on the old SHA so a concurrent ref update (another
	// gk/git process) fails loudly instead of being silently overwritten.
	reason := fmt.Sprintf("gk pull --with-base: fast-forward to %s", remoteRef)
	if _, stderr, err := runner.Run(ctx, "update-ref", "-m", reason, "refs/heads/"+branch, newSHA, oldSHA); err != nil {
		printNote(w, fmt.Sprintf("base '%s' not synced — update-ref failed: %s",
			branch, strings.TrimSpace(string(stderr))))
		return
	}
	fmt.Fprintln(w, label(branch)+successLinef("fast-forwarded", "%s → %s", shortSHA(oldSHA), shortSHA(newSHA)))
}
