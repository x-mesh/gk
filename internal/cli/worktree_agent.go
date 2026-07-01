package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/branchparent"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

type worktreeAcquireJSON struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Parent  string `json:"parent,omitempty"`
	Created bool   `json:"created"`
	Reused  bool   `json:"reused"`
	Init    string `json:"init,omitempty"`
}

type worktreeFinishJSON struct {
	Mode          string                  `json:"mode"`
	Branch        string                  `json:"branch"`
	To            string                  `json:"to"`
	Path          string                  `json:"path"`
	Cleanup       bool                    `json:"cleanup"`
	Removed       bool                    `json:"removed"`
	DeleteBranch  bool                    `json:"delete_branch,omitempty"`
	BranchDeleted bool                    `json:"branch_deleted,omitempty"`
	DryRun        bool                    `json:"dry_run,omitempty"`
	Gate          *worktreeGateResultJSON `json:"gate,omitempty"`
}

// agentState surfaces the envelope state "paused" when the after gate failed:
// the merge succeeded but the finish is suspended awaiting an accept/revert
// decision, so a parent agent branches on state and reads Gate.recover for the
// resume/abort pair. Any other outcome stays "ok" (before-gate failure and a
// live lock take the error path via WithBlocked instead).
func (r worktreeFinishJSON) agentState() string {
	if r.Gate != nil && r.Gate.Paused {
		return envStatePaused
	}
	return ""
}

type worktreeCleanupJSON struct {
	DryRun     bool                   `json:"dry_run"`
	Candidates []worktreeCleanupEntry `json:"candidates"`
	Removed    []worktreeCleanupEntry `json:"removed,omitempty"`
	Skipped    []worktreeCleanupEntry `json:"skipped,omitempty"`
	Failed     []worktreeCleanupEntry `json:"failed,omitempty"`
}

type worktreeCleanupEntry struct {
	Path          string            `json:"path"`
	Branch        string            `json:"branch,omitempty"`
	Target        string            `json:"target,omitempty"`
	Action        string            `json:"action,omitempty"`
	Reasons       []string          `json:"reasons,omitempty"`
	Age           string            `json:"age,omitempty"`
	Dirty         *contextDirtyJSON `json:"dirty,omitempty"`
	Locked        bool              `json:"locked,omitempty"`
	BranchDeleted bool              `json:"branch_deleted,omitempty"`
	Error         string            `json:"error,omitempty"`
}

func newWorktreeAcquireCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "acquire <branch>",
		Short: "Create or reuse an initialized worktree and print its path",
		Long: `Create or reuse a managed worktree for <branch> and return the path.

This is the agent-friendly setup command: if the branch already has a
worktree, gk reuses it; otherwise gk creates one under the managed worktree
base, records gk-parent metadata for newly created branches, and runs
worktree.init by default. Pass --no-init to skip bootstrap.`,
		Args: cobra.ExactArgs(1),
		RunE: runWorktreeAcquire,
	}
	c.Flags().String("from", "", "base ref when creating a new branch (default: HEAD)")
	c.Flags().Bool("init", true, "run worktree init after create/reuse")
	c.Flags().Bool("no-init", false, "skip worktree init")
	return c
}

func runWorktreeAcquire(cmd *cobra.Command, args []string) error {
	ref := args[0]
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	cfg, _ := config.Load(cmd.Flags())
	jsonMode := JSONOut()
	w := cmd.OutOrStdout()

	path, created, createdBranch, initStatus, err := ensureRunWorktree(ctx, cmd, runner, cfg, ref, jsonMode, w)
	if err != nil {
		return err
	}
	res := worktreeAcquireJSON{
		Path: path, Branch: ref, Created: created, Reused: !created, Init: initStatus,
	}
	if createdBranch {
		from, _ := cmd.Flags().GetString("from")
		res.Parent = predictWorktreeParent(ctx, runner, from)
	}
	if jsonMode {
		return emitAgentResult(w, res)
	}
	fmt.Fprintf(w, "worktree ready: %s (%s", path, ref)
	if created {
		fmt.Fprint(w, ", created")
	} else {
		fmt.Fprint(w, ", reused")
	}
	if initStatus != "" {
		fmt.Fprintf(w, ", init %s", initStatus)
	}
	fmt.Fprintln(w, ")")
	return nil
}

func newWorktreeFinishCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "finish",
		Short: "Commit and merge this worktree's branch into its parent/base",
		Long: `Finish the current worktree branch.

By default this runs the local path: gk promote (commit, then merge one hop
into the branch's gk-parent or base) without pushing. Pass --push to use
gk land --to <target> instead. With --cleanup, the linked worktree is removed
after a successful finish; --delete-branch also deletes the finished branch
after the worktree is removed.`,
		Args: cobra.NoArgs,
		RunE: runWorktreeFinish,
	}
	c.Flags().String("to", "parent", "integration target: parent, base, or a branch")
	c.Flags().Bool("push", false, "use gk land --to <target> instead of local gk promote")
	c.Flags().Bool("cleanup", false, "remove the current linked worktree after a successful finish")
	c.Flags().Bool("delete-branch", false, "after --cleanup, delete the finished branch with git branch -d")
	c.Flags().Bool("autostash", false, "pass --autostash to promote/land for dirty receiver worktrees")
	c.Flags().String("gate", "", "quality-gate command template run against the merge patch (e.g. \"xm panel {patch} --json\"); whitespace-tokenized, no shell")
	c.Flags().StringArray("gate-arg", nil, "gate command as explicit argv tokens (repeatable); the canonical alternative to --gate for precise quoting")
	c.Flags().Bool("panel-review", false, "alias for --gate \"xm panel {patch} --json\"")
	c.Flags().String("gate-phase", "before", "when to run the gate: before, after, or both")
	c.Flags().Duration("gate-timeout", 0, "kill the gate command after this duration (e.g. 10m); 0 = no timeout")
	c.Flags().Bool("gate-keep-patch", false, "keep the temporary gate patch file instead of deleting it after the gate")
	c.Flags().Bool("resume-accept", false, "accept a prior after-gate pause: skip merge/gate and run cleanup only")
	return c
}

func runWorktreeFinish(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	runner := &git.ExecRunner{Dir: RepoFlag()}
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}
	cfg, _ := config.Load(cmd.Flags())
	jsonMode := JSONOut()

	branch, err := git.NewClient(runner).CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("worktree finish: determine current branch: %w", err)
	}
	path := currentWorktreePath(ctx, runner)
	if path == "" {
		return fmt.Errorf("worktree finish: cannot determine current worktree path")
	}
	to, _ := cmd.Flags().GetString("to")
	push, _ := cmd.Flags().GetBool("push")
	cleanup, _ := cmd.Flags().GetBool("cleanup")
	deleteBranch, _ := cmd.Flags().GetBool("delete-branch")
	autostash, _ := cmd.Flags().GetBool("autostash")
	resumeAccept, _ := cmd.Flags().GetBool("resume-accept")
	if deleteBranch && !cleanup {
		return fmt.Errorf("worktree finish: --delete-branch requires --cleanup")
	}

	gateStr, _ := cmd.Flags().GetString("gate")
	gateArgs, _ := cmd.Flags().GetStringArray("gate-arg")
	panelReview, _ := cmd.Flags().GetBool("panel-review")
	gatePhase, _ := cmd.Flags().GetString("gate-phase")
	gateTimeout, _ := cmd.Flags().GetDuration("gate-timeout")
	gateKeepPatch, _ := cmd.Flags().GetBool("gate-keep-patch")
	gate, gerr := parseGateSpec(gateStr, gateArgs, panelReview, gatePhase, gateTimeout, gateKeepPatch)
	if gerr != nil {
		return gerr
	}
	// An after gate reviews the integration AFTER it lands, but --push (land)
	// publishes the target before the gate runs — a failing after gate could
	// then only be undone with a force-push, which the paused abort does not do.
	// Reject the combination so the gate is not silently defeated.
	if gate != nil && push && gate.runsAfter() {
		return fmt.Errorf("worktree finish: --gate-phase %s cannot combine with --push (the after gate reviews an integration that --push already published; gate before push, or drop --push)", gate.phase)
	}

	mode, childArgs, effectiveTo, err := finishChildArgs(ctx, runner, cfg, to, push, autostash)
	if err != nil {
		return err
	}
	res := worktreeFinishJSON{
		Mode: mode, Branch: branch, To: effectiveTo, Path: path,
		Cleanup: cleanup, DeleteBranch: deleteBranch,
	}

	// --resume-accept resumes a prior run whose after gate merged then paused:
	// accepting skips the merge/gate entirely and performs only the held-back
	// cleanup, so the operator moves forward without re-merging.
	if resumeAccept {
		res.Mode = "resume-accept"
		if cleanup {
			// Data-loss guard: cleanup removes the worktree (and optionally deletes
			// the branch), which is only safe if a prior run already merged the
			// branch into its target. Without a completed merge there is nothing to
			// accept — refuse rather than silently discard unmerged work.
			acceptTarget, terr := resolveGateTarget(ctx, runner, cfg, effectiveTo, branch)
			if terr != nil {
				return terr
			}
			if !isAncestor(ctx, runner, branch, acceptTarget) {
				return WithBlocked(
					fmt.Errorf("worktree finish: --resume-accept but %q is not merged into %q", branch, acceptTarget),
					"worktree_resume_not_merged",
					"nothing to accept — run the gated finish first, or drop --resume-accept",
				)
			}
			res.To = acceptTarget
			if cerr := finishCleanup(ctx, runner, &res, path, branch, deleteBranch); cerr != nil {
				return cerr
			}
		}
		return finishEmitOK(cmd, res)
	}

	if DryRun() {
		res.DryRun = true
		if jsonMode {
			return emitAgentResult(cmd.OutOrStdout(), res)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "would run: gk %s\n", strings.Join(childArgs, " "))
		if gate != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "would run gate (%s): %s\n", gate.phase, gate.commandTemplate())
		}
		if cleanup {
			fmt.Fprintf(cmd.OutOrStdout(), "would remove worktree %s\n", path)
		}
		return nil
	}

	gkPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("worktree finish: locate gk binary: %w", err)
	}

	if gate == nil {
		// Unchanged (gate-free) path: byte-identical to pre-gate behavior.
		if err := landRunChild(ctx, gkPath, path, jsonMode, childArgs...); err != nil {
			return fmt.Errorf("worktree finish: %s failed: %w", mode, err)
		}
	} else {
		gateTarget, terr := resolveGateTarget(ctx, runner, cfg, effectiveTo, branch)
		if terr != nil {
			return terr
		}
		res.To = gateTarget
		gateRes, eerr := executeGatedFinish(ctx, runner, gate, gkPath, path, branch, gateTarget, mode, jsonMode, cleanup, deleteBranch, childArgs)
		if gateRes != nil {
			res.Gate = gateRes
		}
		if eerr != nil {
			return eerr
		}
		if gateRes != nil && gateRes.Paused {
			// After gate failed: the merge stands, cleanup is held. Emit the
			// paused contract (result carries gate.recover[]) and exit 3.
			if jsonMode {
				_ = emitAgentResult(cmd.OutOrStdout(), res)
			} else {
				renderFinishPaused(cmd, res)
			}
			return pausedExitIf(res)
		}
	}

	if cleanup {
		if cerr := finishCleanup(ctx, runner, &res, path, branch, deleteBranch); cerr != nil {
			return cerr
		}
	}
	return finishEmitOK(cmd, res)
}

func finishChildArgs(ctx context.Context, runner git.Runner, cfg *config.Config, to string, push, autostash bool) (mode string, args []string, effectiveTo string, err error) {
	to = strings.TrimSpace(to)
	if to == "" {
		to = "parent"
	}
	if push {
		args = []string{"land", "--to", to}
		if autostash {
			args = append(args, "--autostash")
		}
		// Report the resolved branch name for "base" so result.to matches the
		// local promote path (which resolves it too); land still resolves the
		// keyword itself.
		effectiveTo = to
		if to == "base" {
			base := finishBaseBranch(ctx, runner, cfg)
			if base == "" {
				return "", nil, "", fmt.Errorf("worktree finish: cannot determine base branch")
			}
			effectiveTo = base
		}
		return "land", args, effectiveTo, nil
	}

	args = []string{"promote"}
	effectiveTo = to
	switch to {
	case "parent":
		// Bare promote means one hop to gk-parent, falling back to base.
	case "base":
		base := finishBaseBranch(ctx, runner, cfg)
		if base == "" {
			return "", nil, "", fmt.Errorf("worktree finish: cannot determine base branch")
		}
		effectiveTo = base
		args = append(args, base)
	default:
		args = append(args, to)
	}
	if autostash {
		args = append(args, "--autostash")
	}
	return "promote", args, effectiveTo, nil
}

func finishBaseBranch(ctx context.Context, runner git.Runner, cfg *config.Config) string {
	if cfg != nil && cfg.BaseBranch != "" {
		return cfg.BaseBranch
	}
	remote := "origin"
	if cfg != nil && cfg.Remote != "" {
		remote = cfg.Remote
	}
	if base, err := git.NewClient(runner).DefaultBranch(ctx, remote); err == nil && base != "" {
		return base
	}
	if r, ok := runner.(*git.ExecRunner); ok {
		return resolveDefaultBranchForWorktree(ctx, r)
	}
	return ""
}

func cleanupFinishedWorktree(ctx context.Context, runner *git.ExecRunner, path, branch string, deleteBranch bool) (removed, branchDeleted bool, err error) {
	mainPath, mainErr := mainWorktreePath(ctx, runner)
	if mainErr != nil {
		return false, false, fmt.Errorf("worktree finish cleanup: locate main worktree: %w", mainErr)
	}
	if sameDir(mainPath, path) {
		return false, false, fmt.Errorf("worktree finish cleanup: refusing to remove the main worktree")
	}
	mainRunner := &git.ExecRunner{Dir: mainPath}
	if _, stderr, rerr := mainRunner.Run(ctx, "worktree", "remove", path); rerr != nil {
		return false, false, fmt.Errorf("worktree finish cleanup: remove %s: %s: %w", path, strings.TrimSpace(string(stderr)), rerr)
	}
	removed = true
	if deleteBranch {
		if _, stderr, derr := mainRunner.Run(ctx, "branch", "-d", branch); derr != nil {
			return removed, false, fmt.Errorf("worktree finish cleanup: delete branch %s: %s: %w", branch, strings.TrimSpace(string(stderr)), derr)
		}
		branchDeleted = true
	}
	return removed, branchDeleted, nil
}

func newWorktreeCleanupCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove safe, finished worktrees in bulk",
		Long: `Find gk worktrees that are safe to reclaim.

By default cleanup is a dry-run report. Pass -y to remove candidates. The
safe default skips the current worktree, dirty trees, live locks, protected
branches, detached/bare worktrees, and branches that are not merged into their
gk-parent or base.`,
		Args: cobra.NoArgs,
		RunE: runWorktreeCleanup,
	}
	c.Flags().Bool("merged", true, "only remove worktrees whose branch is merged into its parent/base")
	c.Flags().String("stale", "", "only remove worktrees whose branch tip is older than this age (e.g. 7d, 12h)")
	c.Flags().Bool("delete-branches", false, "delete the local branch after removing its worktree")
	c.Flags().BoolP("yes", "y", false, "perform removals; without this, cleanup only reports candidates")
	c.Flags().Bool("force-stale-locks", false, "unlock and remove worktrees whose lock holder is no longer running")
	c.Flags().Bool("discard-dirty", false, "allow removal of dirty worktrees with git worktree remove --force (destructive)")
	return c
}

func runWorktreeCleanup(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	runner := &git.ExecRunner{Dir: RepoFlag()}
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}
	cfg, _ := config.Load(cmd.Flags())
	staleRaw, _ := cmd.Flags().GetString("stale")
	stale, err := parseWorktreeStale(staleRaw)
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun := DryRun() || !yes

	report, err := collectWorktreeCleanup(ctx, cmd, runner, cfg, stale)
	if err != nil {
		return err
	}
	report.DryRun = dryRun
	if !dryRun {
		report.Removed, report.Failed = applyWorktreeCleanup(ctx, cmd, runner, report.Candidates)
	}
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), report)
	}
	renderWorktreeCleanup(cmd.OutOrStdout(), report)
	return nil
}

func collectWorktreeCleanup(ctx context.Context, cmd *cobra.Command, runner *git.ExecRunner, cfg *config.Config, stale time.Duration) (worktreeCleanupJSON, error) {
	out, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return worktreeCleanupJSON{}, fmt.Errorf("worktree cleanup: list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	entries := parseWorktreePorcelain(string(out))
	current := currentWorktreePath(ctx, runner)
	meta := loadWorktreeBranchMeta(ctx, runner)
	mergedOnly, _ := cmd.Flags().GetBool("merged")
	deleteBranches, _ := cmd.Flags().GetBool("delete-branches")
	forceStaleLocks, _ := cmd.Flags().GetBool("force-stale-locks")
	discardDirty, _ := cmd.Flags().GetBool("discard-dirty")

	protected := map[string]bool{}
	if cfg != nil {
		for _, p := range cfg.Branch.Protected {
			protected[p] = true
		}
		if cfg.BaseBranch != "" {
			protected[cfg.BaseBranch] = true
		}
	}

	// finishBaseBranch is loop-invariant (it depends only on cfg/runner), so
	// resolve it once here rather than per worktree inside cleanupMergeTarget.
	var mergeBase string
	if mergedOnly {
		mergeBase = finishBaseBranch(ctx, runner, cfg)
	}

	report := worktreeCleanupJSON{
		Candidates: []worktreeCleanupEntry{},
		Skipped:    []worktreeCleanupEntry{},
	}
	for _, e := range entries {
		row := worktreeCleanupEntry{Path: e.Path, Branch: e.Branch}
		skip := func(reason string) {
			row.Reasons = append(row.Reasons, reason)
			report.Skipped = append(report.Skipped, row)
		}
		switch {
		case current != "" && sameDir(current, e.Path):
			skip("current")
			continue
		case e.Bare:
			skip("bare")
			continue
		case e.Detached:
			skip("detached")
			continue
		case e.Branch == "":
			skip("no-branch")
			continue
		case protected[e.Branch] || isProtectedBranchName(e.Branch, nil):
			skip("protected")
			continue
		}

		lock := worktreeLockInfo(ctx, runner, e.Path)
		row.Locked = lock.Locked
		if lock.Locked {
			switch {
			case lock.Alive:
				skip("locked-live")
				continue
			case !forceStaleLocks:
				skip("locked-stale")
				continue
			}
			row.Reasons = append(row.Reasons, "locked-stale")
		}

		dirty := worktreeDirtyAt(ctx, e.Path)
		row.Dirty = dirty
		if dirty != nil && !discardDirty {
			skip("dirty")
			continue
		}
		if dirty != nil {
			row.Reasons = append(row.Reasons, "dirty-discard")
		} else {
			row.Reasons = append(row.Reasons, "clean")
		}

		if bm, ok := meta[e.Branch]; ok && !bm.LastCommit.IsZero() {
			age := time.Since(bm.LastCommit)
			row.Age = shortAge(bm.LastCommit)
			if stale > 0 {
				if age < stale {
					skip("fresh")
					continue
				}
				row.Reasons = append(row.Reasons, "stale")
			}
		} else if stale > 0 {
			skip("age-unknown")
			continue
		}

		if mergedOnly {
			target := cleanupMergeTarget(ctx, runner, mergeBase, e.Branch)
			row.Target = target
			if target == "" {
				skip("no-merge-target")
				continue
			}
			if target == e.Branch {
				skip("target-is-self")
				continue
			}
			if !isAncestor(ctx, runner, e.Branch, target) {
				skip("unmerged")
				continue
			}
			row.Reasons = append(row.Reasons, "merged")
		}

		row.Action = "remove-worktree"
		if deleteBranches {
			row.Action = "remove-worktree-delete-branch"
		}
		report.Candidates = append(report.Candidates, row)
	}
	return report, nil
}

func cleanupMergeTarget(ctx context.Context, runner git.Runner, base, branch string) string {
	return branchparent.NewResolver(git.NewClient(runner)).ResolveBase(ctx, branch, base)
}

func applyWorktreeCleanup(ctx context.Context, cmd *cobra.Command, runner *git.ExecRunner, candidates []worktreeCleanupEntry) (removed, failed []worktreeCleanupEntry) {
	deleteBranches, _ := cmd.Flags().GetBool("delete-branches")
	forceStaleLocks, _ := cmd.Flags().GetBool("force-stale-locks")
	discardDirty, _ := cmd.Flags().GetBool("discard-dirty")
	progress := cmd.OutOrStdout()
	if JSONOut() {
		progress = io.Discard
	}
	for _, c := range candidates {
		err := removeCleanupWorktree(ctx, runner, progress, c.Path, c.Locked && forceStaleLocks, c.Dirty != nil && discardDirty)
		if err != nil {
			c.Error = err.Error()
			failed = append(failed, c)
			continue
		}
		if deleteBranches && c.Branch != "" {
			// The candidate already passed isAncestor(branch, target), but git's
			// `branch -d` checks merged-ness against this runner's HEAD (or
			// upstream), not target — so a branch merged into a non-HEAD target
			// would be wrongly refused. Re-confirm the merge into target and
			// force-delete only then; otherwise fall back to -d's own backstop.
			delFlag := "-d"
			if c.Target != "" && c.Target != c.Branch && isAncestor(ctx, runner, c.Branch, c.Target) {
				delFlag = "-D"
			}
			if _, stderr, derr := runner.Run(ctx, "branch", delFlag, c.Branch); derr != nil {
				c.Error = fmt.Sprintf("delete branch %s: %s: %v", c.Branch, strings.TrimSpace(string(stderr)), derr)
				failed = append(failed, c)
				continue
			}
			c.BranchDeleted = true
		}
		removed = append(removed, c)
	}
	return removed, failed
}

func removeCleanupWorktree(ctx context.Context, runner git.Runner, w io.Writer, path string, forceStaleLock, discardDirty bool) error {
	if forceStaleLock {
		return forceRemoveWorktree(ctx, runner, w, path)
	}
	args := []string{"worktree", "remove"}
	if discardDirty {
		args = append(args, "--force")
	}
	args = append(args, path)
	if _, stderr, err := runner.Run(ctx, args...); err != nil {
		return fmt.Errorf("remove %s: %s: %w", path, strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintf(w, "removed worktree %s\n", path)
	return nil
}

func parseWorktreeStale(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	var d time.Duration
	if n, ok := strings.CutSuffix(raw, "d"); ok {
		hours, err := time.ParseDuration(n + "h")
		if err != nil {
			return 0, fmt.Errorf("invalid --stale %q", raw)
		}
		d = hours * 24
	} else {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid --stale %q", raw)
		}
		d = parsed
	}
	// A non-positive age would make the `stale > 0` gate silently drop the
	// filter — the opposite of what --stale intends. Reject it explicitly.
	if d <= 0 {
		return 0, fmt.Errorf("invalid --stale %q: must be positive", raw)
	}
	return d, nil
}

func renderWorktreeCleanup(w io.Writer, report worktreeCleanupJSON) {
	if report.DryRun {
		fmt.Fprintf(w, "worktree cleanup: %d candidate(s), %d skipped (dry-run)\n", len(report.Candidates), len(report.Skipped))
	} else {
		fmt.Fprintf(w, "worktree cleanup: removed %d, failed %d, skipped %d\n", len(report.Removed), len(report.Failed), len(report.Skipped))
	}
	for _, c := range report.Candidates {
		fmt.Fprintf(w, "  remove %s (%s", c.Path, c.Branch)
		if c.Target != "" {
			fmt.Fprintf(w, " -> %s", c.Target)
		}
		if len(c.Reasons) > 0 {
			fmt.Fprintf(w, "; %s", strings.Join(c.Reasons, ", "))
		}
		fmt.Fprintln(w, ")")
	}
	for _, c := range report.Failed {
		fmt.Fprintf(w, "  failed %s: %s\n", c.Path, c.Error)
	}
}
