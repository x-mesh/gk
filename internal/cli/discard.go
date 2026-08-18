package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:   "discard <path>...",
		Short: "Discard uncommitted changes to paths — a safety snapshot is saved first",
		Long: `Discards working-tree changes to the given paths (tracked files only), after
automatically saving a refs/wip snapshot of the whole working tree — untracked
files included — so the discarded work stays recoverable.

The discard itself is what 'git checkout -- <path>' does: the working tree is
restored from the INDEX. Staged changes stay staged; untracked files are never
touched (remove those explicitly if you mean to). Bring the pre-discard state
back any time with 'gk snapshot restore'.

Because the snapshot makes the operation recoverable, there is no confirmation
prompt — this is the verb to reach for where a raw 'git checkout -- <path>'
would destroy work with no way back. A failed snapshot aborts the discard: the
command never throws work away without the net that justifies its lack of a
prompt.

  gk discard src/a.go          # discard edits to one file
  gk discard src/ pkg/util.go  # pathspecs work
  gk discard --dry-run .       # show what would be discarded`,
		Args: cobra.MinimumNArgs(1),
		RunE: runDiscard,
	}
	cmd.Flags().Bool("dry-run", false, "list what would be discarded without touching anything")
	rootCmd.AddCommand(cmd)
}

// discardResultJSON backs `GK_AGENT=1 gk discard` (and --json).
type discardResultJSON struct {
	Schema        int      `json:"schema"`
	Result        string   `json:"result"` // "discarded" | "nothing-to-discard" | "dry-run"
	Discarded     []string `json:"discarded"`
	UntrackedKept []string `json:"untracked_kept,omitempty"`
	SnapshotRef   string   `json:"snapshot_ref,omitempty"`
	SnapshotSHA   string   `json:"snapshot_sha,omitempty"`
}

// discardTargets classifies the status entries under the user's pathspecs into
// what discard acts on and what it deliberately leaves alone.
type discardTargets struct {
	files     []string // tracked paths with worktree changes (Y in M/D/T)
	untracked []string // reported, never touched
	unmerged  []string // blocked: conflict resolution owns these
}

func runDiscard(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	w := cmd.OutOrStdout()

	targets, err := collectDiscardTargets(ctx, runner, args)
	if err != nil {
		return err
	}
	if len(targets.unmerged) > 0 {
		return WithBlocked(
			fmt.Errorf("discard: unmerged path(s) under the given pathspecs: %s",
				strings.Join(targets.unmerged, ", ")),
			"unmerged-paths",
			"conflicted files belong to conflict resolution, not discard — pick sides with 'gk resolve', then finish with 'gk continue' (or cancel with 'gk abort')",
		)
	}
	if len(targets.files) == 0 {
		if JSONOut() {
			return emitAgentResult(w, discardResultJSON{
				Schema: 1, Result: "nothing-to-discard",
				Discarded: []string{}, UntrackedKept: targets.untracked,
			})
		}
		fmt.Fprintln(w, "nothing to discard — no tracked changes under the given paths")
		if len(targets.untracked) > 0 {
			fmt.Fprintln(w, cellFaint("  untracked files are never touched: "+strings.Join(targets.untracked, ", ")))
		}
		return nil
	}

	if dryRun {
		if JSONOut() {
			return emitAgentResult(w, discardResultJSON{
				Schema: 1, Result: "dry-run",
				Discarded: targets.files, UntrackedKept: targets.untracked,
			})
		}
		fmt.Fprintf(w, "would discard %d file(s):\n", len(targets.files))
		for _, f := range targets.files {
			fmt.Fprintf(w, "  %s\n", f)
		}
		if len(targets.untracked) > 0 {
			fmt.Fprintln(w, cellFaint("  untracked files would be left in place: "+strings.Join(targets.untracked, ", ")))
		}
		return nil
	}

	// The snapshot is the whole point: it is what makes a promptless discard
	// defensible. A failed snapshot therefore aborts the discard — the command
	// must never throw work away without the net.
	ref, sha, created, err := createWorkingTreeSnapshot(ctx, runner, "auto-backup before discard")
	if err != nil {
		return fmt.Errorf("discard: snapshot before discarding: %w", err)
	}
	if !created {
		// Defensive: status just reported changes, so the tree is dirty and a
		// snapshot must have been written. Refuse rather than discard bare.
		return fmt.Errorf("discard: snapshot unexpectedly empty — refusing to discard without a safety net")
	}

	checkoutArgs := make([]string, 0, len(targets.files)+2)
	checkoutArgs = append(checkoutArgs, "checkout", "--")
	for _, f := range targets.files {
		// :(top,literal) pins each status path to the repo root, byte for
		// byte — status speaks root-relative while pathspecs default to
		// cwd-relative, and a glob character in a filename must not expand.
		checkoutArgs = append(checkoutArgs, ":(top,literal)"+f)
	}
	if _, stderr, e := runner.Run(ctx, checkoutArgs...); e != nil {
		return fmt.Errorf("discard: restore from index: %s: %w", strings.TrimSpace(string(stderr)), e)
	}

	if JSONOut() {
		return emitAgentResult(w, discardResultJSON{
			Schema: 1, Result: "discarded",
			Discarded: targets.files, UntrackedKept: targets.untracked,
			SnapshotRef: ref, SnapshotSHA: sha,
		})
	}
	fmt.Fprintln(w, successLinef("discarded", "%d file(s) — snapshot saved first: %s (%s)",
		len(targets.files), ref, shortSHA(sha)))
	for _, f := range targets.files {
		fmt.Fprintf(w, "  %s\n", f)
	}
	if len(targets.untracked) > 0 {
		fmt.Fprintln(w, cellFaint("  untracked files left in place: "+strings.Join(targets.untracked, ", ")))
	}
	fmt.Fprintln(w, stylizeHintLine("hint: gk snapshot restore   # bring the discarded changes back"))
	return nil
}

// collectDiscardTargets scopes `git status --porcelain -z` to the user's
// pathspecs (cwd-relative, like every other git command they type) and sorts
// the entries into discardable / untracked / unmerged. The returned paths are
// root-relative — that is what porcelain emits regardless of cwd.
func collectDiscardTargets(ctx context.Context, runner git.Runner, pathspecs []string) (discardTargets, error) {
	statusArgs := append([]string{"status", "--porcelain", "-z", "--"}, pathspecs...)
	out, stderr, err := runner.Run(ctx, statusArgs...)
	if err != nil {
		return discardTargets{}, fmt.Errorf("discard: git status: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	var t discardTargets
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		x, y, path := entry[0], entry[1], entry[3:]
		if x == 'R' || x == 'C' {
			i++ // the rename/copy origin travels as its own NUL field
		}
		switch {
		case x == '?' && y == '?':
			t.untracked = append(t.untracked, path)
		case isUnmergedXY(x, y):
			t.unmerged = append(t.unmerged, path)
		case y == 'M' || y == 'D' || y == 'T':
			t.files = append(t.files, path)
		}
	}
	return t, nil
}

// isUnmergedXY matches the porcelain conflict states (DD/AU/UD/UA/DU/AA/UU).
func isUnmergedXY(x, y byte) bool {
	return x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D')
}
