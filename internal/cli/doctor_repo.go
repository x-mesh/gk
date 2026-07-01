package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// errSkipFix marks a fix attempt that should *not* re-trigger the
// multi-pass loop — e.g. user skipped, or the operation can't proceed
// without manual intervention.
var errSkipFix = errors.New("skip fix")

// resolveGitDir returns the absolute .git directory path (or work-tree
// gitdir for a worktree) — empty string when the cwd isn't a git repo.
func resolveGitDir(ctx context.Context, runner git.Runner) string {
	out, _, err := runner.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return ""
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	repoDir := RepoFlag()
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}
	return filepath.Join(repoDir, dir)
}

// indexLockFreshWindow is the age under which an index.lock is presumed to
// belong to a live git process. git does not write a pid into index.lock,
// so mtime is the only portable staleness signal — a parallel agent that
// died mid-commit leaves a lock that only ages, while a real in-flight git
// operation holds it for seconds.
const indexLockFreshWindow = 2 * time.Minute

// indexLockAge returns whether .git/index.lock exists and how old it is.
func indexLockAge(gitDir string) (exists bool, age time.Duration) {
	info, err := os.Stat(filepath.Join(gitDir, "index.lock"))
	if err != nil {
		return false, 0
	}
	return true, time.Since(info.ModTime())
}

// checkRepoLockFile flags a stale .git/index.lock — the most common
// reason "git could not write the index" surfaces during everyday use.
// A fresh lock (younger than indexLockFreshWindow) is only a WARN: a git
// process may legitimately be holding it, and deleting it under a live
// writer corrupts the index.
func checkRepoLockFile(gitDir string) doctorCheck {
	exists, age := indexLockAge(gitDir)
	if !exists {
		return doctorCheck{
			Name:   "repo: index.lock",
			Status: statusPass,
			Detail: "no stale lock",
		}
	}
	if age < indexLockFreshWindow {
		return doctorCheck{
			Name:   "repo: index.lock",
			Status: statusWarn,
			Detail: fmt.Sprintf("present but recent (%s old) — a git process may still be running", age.Round(time.Second)),
			Fix:    "wait for the running git operation; if it crashed, rerun `gk doctor --fix` in a couple of minutes",
		}
	}
	return doctorCheck{
		Name:   "repo: index.lock",
		Status: statusFail,
		Detail: fmt.Sprintf("stale (%s old)", age.Round(time.Second)),
		Fix:    "remove if no git process is running: rm .git/index.lock  (or run `gk doctor --fix`)",
	}
}

// mergeHeadOrphan reports a MERGE_HEAD with zero unmerged paths — a merge
// that was interrupted after conflicts were resolved (or never had any).
// Aborting such a merge cannot lose conflict work, so the fix prompt can
// say so explicitly.
func mergeHeadOrphan(ctx context.Context, runner git.Runner, gitDir string) bool {
	if _, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD")); err != nil {
		return false
	}
	out, _, err := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	return err == nil && strings.TrimSpace(string(out)) == ""
}

// checkPrunableWorktrees flags worktree registrations whose directories are
// gone — agents that delete a worktree directory without `gk wt remove`
// leave these behind, and they block re-adding a worktree at the same path.
func checkPrunableWorktrees(ctx context.Context, runner git.Runner) doctorCheck {
	out, _, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return doctorCheck{Name: "repo: prunable worktrees", Status: statusWarn, Detail: "could not query: " + err.Error()}
	}
	count := 0
	for _, e := range parseWorktreePorcelain(string(out)) {
		if e.Prunable {
			count++
		}
	}
	if count == 0 {
		return doctorCheck{Name: "repo: prunable worktrees", Status: statusPass, Detail: "none"}
	}
	return doctorCheck{
		Name:   "repo: prunable worktrees",
		Status: statusWarn,
		Detail: fmt.Sprintf("%d stale registration(s)", count),
		Fix:    "clean with `git worktree prune` (or run `gk doctor --fix`)",
	}
}

// promptFixPrunable prunes stale worktree registrations after confirmation.
// Prune only drops bookkeeping for directories that no longer exist — it
// never touches a live worktree — so the prompt is a courtesy, not a guard.
func promptFixPrunable(ctx context.Context, cmd *cobra.Command, runner git.Runner) error {
	choice, err := ui.ScrollSelectTUI(ctx, "fix: prunable worktrees",
		"Stale worktree registrations point at directories that no longer exist.\nPruning removes only the bookkeeping under .git/worktrees.",
		[]ui.ScrollSelectOption{
			{Key: "p", Value: "prune", Display: "prune — git worktree prune -v", IsDefault: true},
			{Key: "s", Value: "skip", Display: "skip — keep registrations"},
		})
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	out, errOut, runErr := runner.Run(ctx, "worktree", "prune", "-v")
	if runErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "worktree prune: %s: %v\n", strings.TrimSpace(string(errOut)), runErr)
		return nil
	}
	pruned := strings.TrimSpace(string(out))
	if pruned == "" {
		pruned = "nothing to prune"
	}
	fmt.Fprintln(cmd.OutOrStdout(), pruned)
	return nil
}

// checkInProgressOp surfaces an interrupted rebase / merge / cherry-pick
// / bisect / revert. Each leaves a marker under .git that blocks new
// operations like stash and reset.
func checkInProgressOp(gitDir string) doctorCheck {
	type op struct {
		marker string
		label  string
		abort  string
	}
	ops := []op{
		{"rebase-merge", "rebase (interactive)", "git rebase --abort"},
		{"rebase-apply", "rebase", "git rebase --abort"},
		{"MERGE_HEAD", "merge", "git merge --abort"},
		{"CHERRY_PICK_HEAD", "cherry-pick", "git cherry-pick --abort"},
		{"REVERT_HEAD", "revert", "git revert --abort"},
		{"BISECT_LOG", "bisect", "git bisect reset"},
	}
	for _, o := range ops {
		if _, err := os.Stat(filepath.Join(gitDir, o.marker)); err == nil {
			return doctorCheck{
				Name:   "repo: in-progress op",
				Status: statusFail,
				Detail: o.label + " is in progress",
				Fix:    "abort with `" + o.abort + "` (or `gk abort`), or finish with the matching --continue",
			}
		}
	}
	return doctorCheck{
		Name:   "repo: in-progress op",
		Status: statusPass,
		Detail: "none",
	}
}

// checkUnmergedPaths flags conflicts left from a merge/rebase that
// neither completed nor aborted.
func checkUnmergedPaths(ctx context.Context, runner git.Runner) doctorCheck {
	out, _, err := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return doctorCheck{Name: "repo: unmerged paths", Status: statusWarn, Detail: "could not query: " + err.Error()}
	}
	names := strings.Fields(strings.TrimSpace(string(out)))
	if len(names) == 0 {
		return doctorCheck{Name: "repo: unmerged paths", Status: statusPass, Detail: "none"}
	}
	preview := strings.Join(names, ", ")
	if len(preview) > 60 {
		preview = preview[:57] + "..."
	}
	return doctorCheck{
		Name:   "repo: unmerged paths",
		Status: statusFail,
		Detail: fmt.Sprintf("%d file(s): %s", len(names), preview),
		Fix:    "resolve with `gk resolve` or edit conflict markers, then `git add` + continue",
	}
}

// checkDirtyTree is a soft warning — many gk subcommands (undo, pull,
// sync) refuse a dirty tree by default. Surfacing it on `gk doctor`
// keeps the user informed without blocking anything.
func checkDirtyTree(ctx context.Context, runner git.Runner) doctorCheck {
	out, _, err := runner.Run(ctx, "status", "--porcelain")
	if err != nil {
		return doctorCheck{Name: "repo: working tree", Status: statusWarn, Detail: "could not query: " + err.Error()}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	count := 0
	for _, l := range lines {
		if l != "" {
			count++
		}
	}
	if count == 0 {
		return doctorCheck{Name: "repo: working tree", Status: statusPass, Detail: "clean"}
	}
	return doctorCheck{
		Name:   "repo: working tree",
		Status: statusWarn,
		Detail: fmt.Sprintf("%d uncommitted change(s)", count),
		Fix:    "stash with `gk stash push -u` or commit with `gk commit`",
	}
}

// commitGraphPaths returns the absolute paths of the commit-graph cache in
// both forms — the single-file `commit-graph` and the split-chain
// `commit-graphs/` directory. Resolved via `git rev-parse --git-path` so it
// is correct inside linked worktrees too, where the object store (and thus
// the commit-graph) lives under the common dir, not the worktree's gitdir.
func commitGraphPaths(ctx context.Context, runner git.Runner) []string {
	var out []string
	for _, rel := range []string{
		"objects/info/commit-graph",
		"objects/info/commit-graphs",
	} {
		raw, _, err := runner.Run(ctx, "rev-parse", "--git-path", rel)
		if err != nil {
			continue
		}
		p := strings.TrimSpace(string(raw))
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			base := RepoFlag()
			if base == "" {
				base, _ = os.Getwd()
			}
			p = filepath.Join(base, p)
		}
		out = append(out, p)
	}
	return out
}

// commitGraphPresent reports whether any commit-graph cache file exists.
// There is nothing to verify (or corrupt) when git has never written one,
// and some git versions make `commit-graph verify` error loudly on a
// missing graph — which would otherwise read as a false FAIL.
func commitGraphPresent(ctx context.Context, runner git.Runner) bool {
	for _, p := range commitGraphPaths(ctx, runner) {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// checkCommitGraph flags a corrupt commit-graph cache — the failure that
// strands `gk sync` / `gk pull` mid-rebase with "fatal: invalid commit
// position. commit-graph is likely corrupt". The cache is a pure speed
// optimisation rebuilt from the object store, so repair never risks
// history. PASS when no cache exists (nothing to corrupt).
func checkCommitGraph(ctx context.Context, runner git.Runner) doctorCheck {
	if !commitGraphPresent(ctx, runner) {
		return doctorCheck{Name: "repo: commit-graph", Status: statusPass, Detail: "no cache"}
	}
	_, stderr, err := runner.Run(ctx, "commit-graph", "verify")
	if err == nil {
		return doctorCheck{Name: "repo: commit-graph", Status: statusPass, Detail: "valid"}
	}
	detail := strings.TrimSpace(string(stderr))
	if i := strings.IndexByte(detail, '\n'); i >= 0 {
		detail = detail[:i]
	}
	if detail == "" {
		detail = "verify failed"
	}
	if len(detail) > 70 {
		detail = detail[:67] + "..."
	}
	return doctorCheck{
		Name:   "repo: commit-graph",
		Status: statusFail,
		Detail: "corrupt — " + detail,
		Fix:    "rebuild it: rm -rf .git/objects/info/commit-graph .git/objects/info/commit-graphs && git commit-graph write --reachable  (or run `gk doctor --fix`)",
	}
}

// removeCommitGraph deletes every commit-graph cache path. os.RemoveAll
// copes with the read-only mode git assigns graph files — on Unix unlink
// needs write on the parent dir, not on the file itself.
func removeCommitGraph(ctx context.Context, runner git.Runner) error {
	for _, p := range commitGraphPaths(ctx, runner) {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// hardenCommitGraph writes the --local git config that stops the cache from
// being auto-(re)written (gc / fetch) and from being read at all, so a
// future race between concurrent git processes can never corrupt an
// operation. Scoped to this repo; the trade-off is slightly slower history
// traversal, negligible outside very large repos.
func hardenCommitGraph(ctx context.Context, runner git.Runner) error {
	for _, kv := range [][2]string{
		{"gc.writeCommitGraph", "false"},
		{"fetch.writeCommitGraph", "false"},
		{"core.commitGraph", "false"},
	} {
		if _, errOut, err := runner.Run(ctx, "config", "--local", kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s: %w (%s)", kv[0], err, strings.TrimSpace(string(errOut)))
		}
	}
	return nil
}

// promptFixCommitGraph repairs a corrupt commit-graph cache after
// confirmation. Both options delete the corrupt cache; "repair" regenerates
// a fresh one, "harden" also disables auto-write/read so the corruption
// cannot recur (the durable fix when parallel agents/worktrees keep racing
// on the same repo's cache). Neither can lose commits — the cache is rebuilt
// from the object store.
func promptFixCommitGraph(ctx context.Context, cmd *cobra.Command, runner git.Runner) error {
	body := "git's commit-graph cache is corrupt — it desynced from the object store\n" +
		"(an interrupted write, or two git processes racing on the same repo).\n\n" +
		"The cache is a pure speed optimisation rebuilt from your objects, so repair\n" +
		"cannot lose commits. Hardening also stops git from auto-writing/reading it."
	choice, err := ui.ScrollSelectTUI(ctx, "fix: corrupt commit-graph", body, []ui.ScrollSelectOption{
		{Key: "r", Value: "repair", Display: "repair — delete cache & regenerate (git commit-graph write --reachable)", IsDefault: true},
		{Key: "h", Value: "harden", Display: "repair + harden — delete cache & disable auto-write/read so it can't recur"},
		{Key: "s", Value: "skip", Display: "skip — leave it"},
	})
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	if rmErr := removeCommitGraph(ctx, runner); rmErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "could not remove commit-graph cache: %v\n", rmErr)
		return errSkipFix
	}
	switch choice {
	case "repair":
		if _, errOut, wErr := runner.Run(ctx, "commit-graph", "write", "--reachable"); wErr != nil {
			// The corrupt cache is already gone — git falls back to walking
			// the object store, so the repo is fully usable even if the
			// rebuild failed. Don't fail the fix over a lost optimisation.
			fmt.Fprintf(cmd.ErrOrStderr(),
				"removed the corrupt cache, but regeneration failed: %s\n"+
					"git will use the object store directly (slower history walks); retry later with `git commit-graph write --reachable`.\n",
				strings.TrimSpace(string(errOut)))
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "commit-graph repaired (deleted + regenerated)")
	case "harden":
		if hErr := hardenCommitGraph(ctx, runner); hErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "removed the corrupt cache, but hardening config failed: %v\n", hErr)
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(),
			"commit-graph removed and disabled for this repo "+
				"(gc.writeCommitGraph=false, fetch.writeCommitGraph=false, core.commitGraph=false) — corruption can't recur")
	}
	return nil
}

// runDoctorFix walks the repo-state findings (FAIL/WARN) and offers
// in-line repair via ScrollSelectTUI. After each potential mutation
// the repo is re-checked so a fix that uncovers (or unblocks) the next
// finding is acted on too — e.g. resolving conflicts unblocks the
// stash step that follows.
func runDoctorFix(ctx context.Context, cmd *cobra.Command, runner git.Runner, gitDir, remote string, _ []doctorCheck) error {
	// Callers (runDoctor) gate this behind promptAllowed(); this helper
	// assumes an interactive terminal and opens repair TUIs directly.
	if remote == "" {
		remote = "origin"
	}

	// Cap passes so a stubborn finding can't loop forever. The branch-
	// tracking handler walks one offender per pass, so allow extra room
	// when many local branches need re-tracking — most repos have <10
	// local branches, so 8 is plenty without being unbounded.
	const maxPasses = 8
	for pass := 0; pass < maxPasses; pass++ {
		fresh := []doctorCheck{
			checkRepoLockFile(gitDir),
			checkInProgressOp(gitDir),
			checkUnmergedPaths(ctx, runner),
			checkCommitGraph(ctx, runner),
			checkDirtyTree(ctx, runner),
			checkPrunableWorktrees(ctx, runner),
			checkBranchTracking(ctx, runner, remote),
		}
		acted := false
		stop := false
		for _, c := range fresh {
			if c.Status != statusFail && c.Status != statusWarn {
				continue
			}
			var err error
			switch c.Name {
			case "repo: index.lock":
				// A fresh lock may belong to a live git process — git puts
				// no pid in index.lock, so age is the only safe signal.
				// Never offer deletion inside the fresh window.
				if exists, age := indexLockAge(gitDir); exists && age < indexLockFreshWindow {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"index.lock is only %s old — a git process may be running; not touching it\n",
						age.Round(time.Second))
					err = errSkipFix
				} else {
					err = promptFixLock(ctx, cmd, gitDir)
				}
			case "repo: in-progress op":
				detail := c.Detail
				if mergeHeadOrphan(ctx, runner, gitDir) {
					detail += " (no conflicted files — aborting cannot lose conflict work)"
				}
				err = promptFixInProgress(ctx, cmd, runner, detail)
			case "repo: commit-graph":
				err = promptFixCommitGraph(ctx, cmd, runner)
			case "repo: prunable worktrees":
				err = promptFixPrunable(ctx, cmd, runner)
			case "repo: unmerged paths":
				err = promptFixUnmerged(ctx, cmd, runner)
			case "repo: working tree":
				err = promptFixDirty(ctx, cmd, runner)
			case "repo: branch tracking":
				err = promptFixBranchTracking(ctx, cmd, runner, remote)
			}
			if errors.Is(err, errSkipFix) {
				// User skipped or the op can't proceed without manual
				// help — abandon the loop instead of spamming the same
				// prompt every pass.
				stop = true
			} else if err == nil {
				acted = true
			}
			// Re-check from the top of `fresh` after every fix attempt
			// so dependent findings (e.g. dirty-tree unblocked by
			// resolve) are reconsidered immediately.
			break
		}
		if !acted || stop {
			break
		}
	}
	return nil
}

// inProgressOpActive returns true when .git contains a marker for an
// active merge/rebase/cherry-pick. `gk resolve` requires one; without
// it, only manual `git add` (after editing markers) clears unmerged
// paths.
func inProgressOpActive(gitDir string) bool {
	for _, m := range []string{
		"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD",
		"rebase-merge", "rebase-apply",
	} {
		if _, err := os.Stat(filepath.Join(gitDir, m)); err == nil {
			return true
		}
	}
	return false
}

// promptFixUnmerged offers to launch `gk resolve` (when an op is active)
// or to mark files resolved via `git add` (when only stale markers are
// left). Returns errSkipFix on user skip OR unrecoverable failure so
// the multi-pass loop in runDoctorFix knows not to retry.
func promptFixUnmerged(ctx context.Context, cmd *cobra.Command, runner git.Runner) error {
	out, _, _ := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	files := strings.Fields(strings.TrimSpace(string(out)))
	if len(files) == 0 {
		return nil
	}

	gitDir := resolveGitDir(ctx, runner)
	hasOp := gitDir != "" && inProgressOpActive(gitDir)

	body := "Files with unmerged paths:\n  • " + strings.Join(files, "\n  • ")
	options := []ui.ScrollSelectOption{}
	if hasOp {
		body += "\n\nA merge/rebase/cherry-pick is in progress — `gk resolve` can walk each hunk."
		options = append(options,
			ui.ScrollSelectOption{Key: "r", Value: "resolve", Display: "launch `gk resolve` now (auto-stages on success)", IsDefault: true})
	} else {
		body += "\n\nNo merge/rebase/cherry-pick is active — only `git add` (after editing the conflict markers) will clear these.\n" +
			"Open the files in your editor, fix the markers, then choose `mark resolved` here."
		options = append(options,
			ui.ScrollSelectOption{Key: "a", Value: "addall", Display: "mark resolved — `git add` every unmerged file (run only after editing!)", IsDefault: true})
	}
	options = append(options,
		ui.ScrollSelectOption{Key: "s", Value: "skip", Display: "skip — handle manually with editor + `git add`"})

	choice, err := ui.ScrollSelectTUI(ctx, "fix: unmerged paths", body, options)
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}

	switch choice {
	case "resolve":
		binary, eErr := os.Executable()
		if eErr != nil || binary == "" {
			binary = "gk"
		}
		resolve := exec.CommandContext(ctx, binary, "resolve")
		resolve.Stdin = os.Stdin
		resolve.Stdout = os.Stdout
		resolve.Stderr = os.Stderr
		if rErr := resolve.Run(); rErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"`gk resolve` exited with error: %v\n"+
					"resolve the conflicts manually (edit + `git add`), then re-run `gk doctor --fix`.\n",
				rErr)
			return errSkipFix
		}
	case "addall":
		args := append([]string{"add", "--"}, files...)
		if _, errOut, aErr := runner.Run(ctx, args...); aErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"git add failed: %s\n"+
					"some files still contain conflict markers — fix them in your editor first.\n",
				strings.TrimSpace(string(errOut)))
			return errSkipFix
		}
		fmt.Fprintf(cmd.OutOrStdout(), "marked resolved: %s\n", strings.Join(files, ", "))
	}
	return nil
}

func promptFixLock(ctx context.Context, cmd *cobra.Command, gitDir string) error {
	lock := filepath.Join(gitDir, "index.lock")
	body := "Stale .git/index.lock blocks every write (commit, stash, reset, merge).\n\n" +
		"Path: " + lock + "\n\n" +
		"Make sure no other git process is running before deleting:\n" +
		"  pgrep -fl '^git ' || echo \"no git running\""
	choice, err := ui.ScrollSelectTUI(ctx, "fix: stale index.lock", body, []ui.ScrollSelectOption{
		{Key: "r", Value: "remove", Display: "remove the lock file (no git process must be running)", IsDefault: true},
		{Key: "s", Value: "skip", Display: "skip — fix manually"},
	})
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	if err := os.Remove(lock); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "remove %s: %v\n", lock, err)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", lock)
	return nil
}

func promptFixInProgress(ctx context.Context, cmd *cobra.Command, runner git.Runner, detail string) error {
	body := "An interrupted git operation is recorded in .git.\n\n" +
		"Detail: " + detail + "\n\n" +
		"Choose abort to roll the working tree back to before the operation, or skip to handle it manually with the matching --continue."
	choice, err := ui.ScrollSelectTUI(ctx, "fix: in-progress operation", body, []ui.ScrollSelectOption{
		{Key: "a", Value: "abort", Display: "abort — roll back via `gk abort` (works for rebase/merge/cherry-pick)", IsDefault: true},
		{Key: "s", Value: "skip", Display: "skip — fix manually"},
	})
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	// `gk abort` is preferred over the per-op git command because it
	// already routes to the correct --abort variant.
	if _, errOut, aErr := runner.Run(ctx, "merge", "--abort"); aErr == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "git merge --abort succeeded")
		return nil
	} else if !strings.Contains(string(errOut), "MERGE_HEAD") {
		// Fall through to other op types.
		_ = aErr
	}
	if _, _, aErr := runner.Run(ctx, "rebase", "--abort"); aErr == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "git rebase --abort succeeded")
		return nil
	}
	if _, _, aErr := runner.Run(ctx, "cherry-pick", "--abort"); aErr == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "git cherry-pick --abort succeeded")
		return nil
	}
	if _, _, aErr := runner.Run(ctx, "revert", "--abort"); aErr == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "git revert --abort succeeded")
		return nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "no abort handled the in-progress op — try `gk abort` directly")
	return nil
}

// promptFixBranchTracking offers to repair the first untracked-divergent
// branch found by scanUntrackedDivergent. The multi-pass loop in
// runDoctorFix re-scans after each fix, so multiple offenders are walked
// one prompt at a time.
//
// Safety model:
//   - Ahead == 0 (origin moved, local stayed): offer "set tracking + ff" — no
//     local commit can be lost.
//   - Ahead > 0 (local has commits not on origin): offer "set tracking only"
//     — never reset, so the user's commits stay intact. They can then run
//     `gk pull` to rebase/merge.
//
// All actions are explicit user choices (TTY prompt) — no implicit git
// config writes happen on a non-interactive `gk doctor` run.
func promptFixBranchTracking(ctx context.Context, cmd *cobra.Command, runner git.Runner, remote string) error {
	if remote == "" {
		remote = "origin"
	}
	offenders := scanUntrackedDivergent(ctx, runner, remote)
	if len(offenders) == 0 {
		return nil
	}
	o := offenders[0]
	cur, _ := currentBranchName(ctx, runner)
	isCurrent := cur == o.Branch

	body := fmt.Sprintf("Branch %q has no upstream configured.\nremote: %s differs from local by ↑%d ↓%d.\n",
		o.Branch, o.Implicit, o.Ahead, o.Behind)
	if len(offenders) > 1 {
		body += fmt.Sprintf("\n(+%d more untracked-divergent branch(es) — they'll be offered one at a time.)", len(offenders)-1)
	}

	options := []ui.ScrollSelectOption{}
	if o.Ahead == 0 {
		options = append(options, ui.ScrollSelectOption{
			Key:       "f",
			Value:     "ff",
			Display:   fmt.Sprintf("set tracking + fast-forward to %s (no local commits to lose)", o.Implicit),
			IsDefault: true,
		})
	} else {
		body += fmt.Sprintf("\n⚠ %d local commit(s) not on %s — fast-forward would discard them. Suggested: set tracking only, then `gk pull` to rebase/merge.", o.Ahead, o.Implicit)
	}
	options = append(options,
		ui.ScrollSelectOption{
			Key:       "t",
			Value:     "track",
			Display:   fmt.Sprintf("set tracking only — `git branch --set-upstream-to=%s %s`", o.Implicit, o.Branch),
			IsDefault: o.Ahead > 0,
		},
		ui.ScrollSelectOption{
			Key:     "s",
			Value:   "skip",
			Display: "skip this branch",
		},
	)

	choice, err := ui.ScrollSelectTUI(ctx, "fix: branch tracking", body, options)
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	if applyErr := applyBranchTrackingFix(ctx, runner, choice, o, isCurrent); applyErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "branch tracking fix failed: %v\n", applyErr)
		return errSkipFix
	}
	switch choice {
	case "ff":
		fmt.Fprintf(cmd.OutOrStdout(), "set tracking + fast-forwarded: %s → %s\n", o.Branch, o.Implicit)
	case "track":
		fmt.Fprintf(cmd.OutOrStdout(), "set tracking: %s → %s (run `gk pull` to integrate origin)\n", o.Branch, o.Implicit)
	}
	return nil
}

// applyBranchTrackingFix performs the chosen git operation. Split out from
// the prompt so the git plumbing is testable without a TTY.
//   - choice == "track": only `git branch --set-upstream-to=<implicit> <branch>`
//   - choice == "ff":    set-upstream-to, then fast-forward — `merge --ff-only`
//     when fixing the current branch (working-tree consistency), or
//     `update-ref` when fixing a non-current branch.
func applyBranchTrackingFix(ctx context.Context, runner git.Runner, choice string, o untrackedDivergent, isCurrent bool) error {
	if _, _, err := runner.Run(ctx, "branch", "--set-upstream-to="+o.Implicit, o.Branch); err != nil {
		return fmt.Errorf("set-upstream-to %s: %w", o.Implicit, err)
	}
	if choice != "ff" {
		return nil
	}
	if isCurrent {
		if _, stderr, err := runner.Run(ctx, "merge", "--ff-only", o.Implicit); err != nil {
			return fmt.Errorf("merge --ff-only %s: %w (%s)", o.Implicit, err, strings.TrimSpace(string(stderr)))
		}
		return nil
	}
	sha, _, err := runner.Run(ctx, "rev-parse", o.Implicit)
	if err != nil {
		return fmt.Errorf("rev-parse %s: %w", o.Implicit, err)
	}
	if _, stderr, err := runner.Run(ctx, "update-ref", "refs/heads/"+o.Branch, strings.TrimSpace(string(sha))); err != nil {
		return fmt.Errorf("update-ref %s: %w (%s)", o.Branch, err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// currentBranchName returns the short branch name HEAD points at, or ""
// when HEAD is detached (or the call fails).
func currentBranchName(ctx context.Context, runner git.Runner) (string, error) {
	out, _, err := runner.Run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func promptFixDirty(ctx context.Context, cmd *cobra.Command, runner git.Runner) error {
	// If conflicts are still present, git refuses to stash. Surface the
	// dependency before the user picks an option that's bound to fail.
	unmergedOut, _, _ := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	unmerged := strings.TrimSpace(string(unmergedOut)) != ""

	statusOut, _, _ := runner.Run(ctx, "status", "--short")
	body := strings.TrimRight(string(statusOut), "\n")
	if body == "" {
		body = "(no diff against HEAD)"
	}
	if unmerged {
		body += "\n\n⚠ unmerged paths are present — git will refuse to stash until they're resolved.\n" +
			"  resolve first: `gk resolve` (interactive) or edit conflict markers + `git add`."
	}
	choice, err := ui.ScrollSelectTUI(ctx, "fix: working tree is dirty", body, []ui.ScrollSelectOption{
		{Key: "s", Value: "stash", Display: "stash & continue (recoverable with `git stash pop`)", IsDefault: true},
		{Key: "k", Value: "skip", Display: "skip — keep changes as-is"},
	})
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	if unmerged {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"can't stash while conflicts remain. resolve them first:\n"+
				"  gk resolve              # interactive AI-assisted picker\n"+
				"  # or edit the conflict markers manually, then: git add <file>\n"+
				"then re-run `gk doctor --fix`.")
		return nil
	}
	if _, errOut, sErr := runner.Run(ctx, "stash", "push", "--include-untracked", "-m", "gk-doctor-stash"); sErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"stash failed: %s\n"+
				"hint: see `gk doctor` for the underlying repo state, or check `.git/index.lock` and any in-progress merge/rebase.\n",
			strings.TrimSpace(string(errOut)))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), "stashed — restore with `gk stash` or `git stash pop`")
	return nil
}
