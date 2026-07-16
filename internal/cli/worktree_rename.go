package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/branchparent"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// worktreeRenameJSON is the machine-readable result of `gk worktree rename`.
type worktreeRenameJSON struct {
	OldPath    string `json:"old_path"`
	NewPath    string `json:"new_path"`
	OldBranch  string `json:"old_branch,omitempty"`
	NewBranch  string `json:"new_branch,omitempty"`
	WithBranch bool   `json:"with_branch"`
	Managed    bool   `json:"managed"`
	DryRun     bool   `json:"dry_run,omitempty"`
}

func newWorktreeRenameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rename <worktree> <new-name>",
		Aliases: []string{"mv"},
		Short:   "Move a worktree to a new managed name/path (optionally rename its branch)",
		Long: `Move a linked worktree to a new location and, optionally, rename the
branch it holds.

<worktree> matches by managed name (the last path segment), by an absolute
path, by a path relative to the current directory, or by the checked-out
branch name. <new-name> is resolved through the same managed layout as
` + "`gk worktree add`" + ` — a plain name lands under <worktree.base>/<project>/<name>;
an absolute path is used verbatim.

By default only the directory moves (via ` + "`git worktree move`" + `); the branch,
its upstream, and its gk-parent metadata are left untouched. Pass
--with-branch to also rename the checked-out branch to <new-name>
(` + "`git branch -m`" + `). A protected branch (main/master/develop) is refused —
drop --with-branch to move the directory only. Child branches whose
gk-parent pointed at the old branch name are rewritten to the new one.

The main worktree cannot be renamed. A locked worktree is refused unless its
lock holder is gone (--force) or you override a live holder (--force-locked),
mirroring ` + "`gk worktree remove`" + `; the lock is restored at the new path.

Examples:
  gk worktree rename ai-commit ai-commit-v2           # move dir only
  gk worktree rename feat-x feat-y --with-branch      # move dir + rename branch
  gk worktree rename feat-x /tmp/exp                  # move to an absolute path
  gk worktree rename ai-commit ai-commit-v2 --dry-run # preview
`,
		Args: cobra.ExactArgs(2),
		RunE: runWorktreeRename,
	}
	cmd.Flags().Bool("with-branch", false, "also rename the checked-out branch to <new-name> (git branch -m)")
	cmd.Flags().BoolP("force", "f", false, "unlock and move a worktree whose lock holder is no longer running")
	cmd.Flags().Bool("force-locked", false, "move even when the lock holder is still running (dangerous: may be in active use)")
	cmd.Flags().Bool("print-path", false, "print the new absolute path on stdout (human output moves to stderr) for `cd $(…)` wrappers")
	return cmd
}

func runWorktreeRename(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	cfg, _ := config.Load(cmd.Flags())
	client := git.NewClient(runner)

	target, newName := args[0], args[1]
	withBranch, _ := cmd.Flags().GetBool("with-branch")
	force, _ := cmd.Flags().GetBool("force")
	forceLocked, _ := cmd.Flags().GetBool("force-locked")
	printPath, _ := cmd.Flags().GetBool("print-path")

	entries, err := listWorktreeEntries(ctx, runner)
	if err != nil {
		return err
	}
	oldEntry, ok := resolveWorktreeTarget(ctx, runner, cfg, target, entries)
	if !ok {
		return WithHint(fmt.Errorf("no worktree matches %q", target),
			"list worktrees with `gk worktree list`")
	}

	// The main worktree is the repository root — git refuses to move it, and
	// so do we, with a clearer message. git lists it first in --porcelain.
	if len(entries) > 0 && canonPath(oldEntry.Path) == canonPath(entries[0].Path) {
		return WithHint(fmt.Errorf("refusing to rename the main worktree (%s)", oldEntry.Path),
			"only linked worktrees can be renamed; the main worktree is the repo root")
	}

	newPath, err := resolveWorktreePath(ctx, runner, cfg, newName)
	if err != nil {
		return err
	}
	managed := newPath != newName
	Dbg("worktree rename: target=%q old=%q new-raw=%q new=%q managed=%v", target, oldEntry.Path, newName, newPath, managed)

	if canonPath(newPath) == canonPath(oldEntry.Path) {
		return fmt.Errorf("worktree is already at %s", newPath)
	}
	// Lstat (not Stat) so a dangling symlink already occupying newPath is
	// caught as "exists" instead of being followed to a phantom ENOENT.
	if _, statErr := os.Lstat(newPath); statErr == nil {
		return WithHint(fmt.Errorf("destination already exists: %s", newPath),
			"pick a different name, or remove the existing path first")
	}

	// Branch-rename target, fully pre-validated so the destructive move is
	// only attempted once the branch rename is known to succeed too — git's
	// move-then-branch-m is two commands, but validating up front keeps them
	// effectively atomic (no half-renamed state on a foreseeable failure).
	oldBranch := oldEntry.Branch
	newBranch := ""
	if withBranch {
		if oldEntry.Detached || oldBranch == "" {
			return WithHint(fmt.Errorf("--with-branch: worktree has no branch (detached HEAD)"),
				"drop --with-branch to move the directory only")
		}
		if filepath.IsAbs(newName) {
			return WithHint(fmt.Errorf("--with-branch: cannot derive a branch name from an absolute path"),
				"pass a plain name (e.g. `feat-x`) so it doubles as the branch name")
		}
		if isProtectedBranchName(oldBranch, cfgProtected(cfg)) {
			return WithHint(fmt.Errorf("--with-branch: refusing to rename protected branch %q", oldBranch),
				"drop --with-branch to move the directory only, or rename via `git branch -m` deliberately")
		}
		if newName == oldBranch {
			return fmt.Errorf("branch is already named %q", oldBranch)
		}
		if err := client.CheckRefFormat(ctx, newName); err != nil {
			return WithHint(fmt.Errorf("--with-branch: %q is not a valid branch name", newName),
				"pick a name git accepts (no spaces, no trailing `.lock`, no `..`)")
		}
		if branchExists(ctx, runner, newName) {
			return WithHint(fmt.Errorf("--with-branch: branch %q already exists", newName),
				"pick a different name, or delete/rename the existing branch first")
		}
		newBranch = newName
	}

	// Lock policy is read-only here (no mutation), so evaluating it before
	// the dry-run preview is safe — dry-run stays a true no-op.
	lock := worktreeLockInfo(ctx, runner, oldEntry.Path)
	if err := checkWorktreeLockGate(lock, force, forceLocked); err != nil {
		return err
	}

	res := worktreeRenameJSON{
		OldPath: oldEntry.Path, NewPath: newPath,
		OldBranch: oldBranch, NewBranch: newBranch,
		WithBranch: withBranch, Managed: managed,
	}

	// Detect whether the shell is standing inside the worktree being moved
	// from the *process cwd*, not the --repo-pinned runner — otherwise a
	// `gk --repo <mainrepo> wt rename …` from inside the worktree would miss
	// the stale-cwd warning.
	insideTarget := canonPath(currentWorktreePath(ctx, &git.ExecRunner{})) == canonPath(oldEntry.Path)

	if DryRun() {
		res.DryRun = true
		if JSONOut() {
			return emitAgentResult(cmd.OutOrStdout(), res)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "would move worktree %s → %s\n", oldEntry.Path, newPath)
		if newBranch != "" {
			fmt.Fprintf(out, "would rename branch %s → %s\n", oldBranch, newBranch)
		}
		return nil
	}

	// Only create intermediate dirs for a managed destination; an absolute
	// path is the user's responsibility (matches `gk worktree add`).
	if managed {
		if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
			return fmt.Errorf("ensure worktree base: %w", err)
		}
	}

	// `git worktree move` refuses a locked worktree, so unlock first — then
	// restore the lock at the new path so an in-use marker (e.g. a running
	// agent) is never silently dropped.
	if lock.Locked {
		if _, stderr, uerr := runner.Run(ctx, "worktree", "unlock", oldEntry.Path); uerr != nil {
			return fmt.Errorf("worktree unlock: %s: %w", strings.TrimSpace(string(stderr)), uerr)
		}
	}

	if _, stderr, err := runner.Run(ctx, "worktree", "move", oldEntry.Path, newPath); err != nil {
		// Move failed — put the lock back where we found it so the worktree
		// is left exactly as before.
		if lock.Locked {
			relockWorktree(ctx, runner, oldEntry.Path, lock.Reason)
		}
		return fmt.Errorf("worktree move: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if lock.Locked {
		relockWorktree(ctx, runner, newPath, lock.Reason)
	}

	if newBranch != "" {
		// Refs live in the common git dir, but run against the moved worktree
		// so everything resolves at its final home.
		mover := &git.ExecRunner{Dir: newPath}
		if _, stderr, err := mover.Run(ctx, "branch", "-m", oldBranch, newBranch); err != nil {
			// Pre-validated above, so this only fires on an exotic race. The
			// move already landed — report the partial state honestly.
			return WithHint(
				fmt.Errorf("moved worktree to %s but branch rename failed: %s", newPath, strings.TrimSpace(string(stderr))),
				fmt.Sprintf("finish manually: git -C %s branch -m %s %s", newPath, oldBranch, newBranch))
		}
		// `git branch -m` carried THIS branch's own config across, but leaves
		// child branches recording `gk-parent = <old>` dangling — rewrite them.
		rewriteChildParents(ctx, client, oldBranch, newBranch)
	}

	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), res)
	}
	// With --print-path, human lines go to stderr so stdout carries only the
	// path for `cd $(gk wt rename … --print-path)`.
	humanW := cmd.OutOrStdout()
	if printPath {
		humanW = cmd.ErrOrStderr()
	}
	fmt.Fprintln(humanW, successLinef("renamed worktree", "%s → %s", oldEntry.Path, newPath))
	if newBranch != "" {
		fmt.Fprintln(humanW, successLinef("renamed branch", "%s → %s", oldBranch, newBranch))
	}
	if insideTarget {
		fmt.Fprintln(humanW, stylizeHintLine(fmt.Sprintf("hint: your shell is still in the old path — cd %s", newPath)))
	}
	if printPath {
		fmt.Fprintln(cmd.OutOrStdout(), newPath)
	}
	return nil
}

// checkWorktreeLockGate enforces the lock policy shared by `gk worktree
// remove` and `rename`: a live lock holder needs --force-locked, a stale one
// needs --force. It is read-only — it never unlocks — so callers evaluate it
// before deciding to mutate. Returns nil (proceed) for an unlocked worktree
// or when the appropriate force flag is present.
func checkWorktreeLockGate(lock worktreeLock, force, forceLocked bool) error {
	if !lock.Locked {
		return nil
	}
	switch {
	case lock.Alive && !forceLocked:
		return WithHint(
			fmt.Errorf("worktree is locked and still in use: %s", lock.Reason),
			"the lock holder is still running — stop it first, or pass --force-locked to override")
	case !lock.Alive && !force && !forceLocked:
		return WithHint(
			fmt.Errorf("worktree is locked by a stale holder: %s", lock.Reason),
			"the holder is no longer running — rerun with --force to unlock")
	}
	return nil
}

// relockWorktree re-applies a lock (with its original reason) to the worktree
// at path. Best-effort: a failure to restore the lock must not fail the
// rename that already succeeded — the worst case is a dropped advisory marker.
func relockWorktree(ctx context.Context, runner git.Runner, path, reason string) {
	args := []string{"worktree", "lock"}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	args = append(args, path)
	_, _, _ = runner.Run(ctx, args...)
}

// rewriteChildParents repoints every branch whose recorded gk-parent equals
// oldBranch at newBranch, after a `git branch -m` renamed oldBranch (which
// git does not propagate to other branches' gk-parent values). Best-effort:
// the rename has already succeeded, so a config-read failure just leaves the
// stale references for `gk doctor`/manual fixup rather than failing the op.
func rewriteChildParents(ctx context.Context, client *git.Client, oldBranch, newBranch string) {
	pcfg := branchparent.NewConfig(client)
	parents, err := pcfg.AllParents(ctx)
	if err != nil {
		return
	}
	for child, parent := range parents {
		if parent == oldBranch {
			_ = pcfg.SetParent(ctx, child, newBranch)
		}
	}
}

// listWorktreeEntries returns the parsed `git worktree list --porcelain`
// rows. git always lists the main worktree first.
func listWorktreeEntries(ctx context.Context, runner git.Runner) ([]WorktreeEntry, error) {
	out, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return parseWorktreePorcelain(string(out)), nil
}

// resolveWorktreeTarget maps a user-supplied <worktree> reference to a
// registered entry. It tries, in order: an exact path match (absolute, or a
// managed name resolved through resolveWorktreePath, or relative-to-cwd),
// then the managed "name" (the path's basename), then the checked-out branch
// name. Returns ok=false when nothing matches.
func resolveWorktreeTarget(ctx context.Context, runner git.Runner, cfg *config.Config, target string, entries []WorktreeEntry) (WorktreeEntry, bool) {
	var candidates []string
	if filepath.IsAbs(target) {
		candidates = append(candidates, target)
	} else {
		if mp, err := resolveWorktreePath(ctx, runner, cfg, target); err == nil {
			candidates = append(candidates, mp)
		}
		if abs, err := filepath.Abs(target); err == nil {
			candidates = append(candidates, abs)
		}
	}
	for _, c := range candidates {
		cc := canonPath(c)
		for _, e := range entries {
			if canonPath(e.Path) == cc {
				return e, true
			}
		}
	}
	// Fall back to the managed name (basename) — what the user typed to
	// `gk worktree add`.
	for _, e := range entries {
		if filepath.Base(e.Path) == target {
			return e, true
		}
	}
	// Last resort: the checked-out branch name, so `gk wt rename feat/x …`
	// works and `gk wt rename main …` resolves the main worktree (which the
	// caller then refuses with a clear message rather than "no match").
	for _, e := range entries {
		if e.Branch != "" && e.Branch == target {
			return e, true
		}
	}
	return WorktreeEntry{}, false
}
