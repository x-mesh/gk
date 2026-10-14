package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/x-mesh/gk/internal/branchclean"
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

const cleanupActionPruneRecord = "prune-record"

// worktreeCleanupOpts carries cleanup's classification flags so callers
// without those flags (gk worktree list) classify with the same rules.
type worktreeCleanupOpts struct {
	merged          bool
	deleteBranches  bool
	forceStaleLocks bool
	discardDirty    bool
	stale           time.Duration
	// scanned holds the per-path status gk worktree list already ran, so a
	// list classifies without a second git status per worktree.
	scanned map[string]worktreeListStatus
}

type worktreeCleanupEntry struct {
	Path          string            `json:"path"`
	Project       string            `json:"project,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	Target        string            `json:"target,omitempty"`
	Action        string            `json:"action,omitempty"`
	Reasons       []string          `json:"reasons,omitempty"`
	Age           string            `json:"age,omitempty"`
	Dirty         *contextDirtyJSON `json:"dirty,omitempty"`
	Untracked     []string          `json:"untracked,omitempty"`
	Disposable    []string          `json:"disposable,omitempty"`
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
			if cerr := finishCleanup(ctx, runner, cfg, &res, path, branch, deleteBranch); cerr != nil {
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
		if cerr := finishCleanup(ctx, runner, cfg, &res, path, branch, deleteBranch); cerr != nil {
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

func cleanupFinishedWorktree(ctx context.Context, runner *git.ExecRunner, cfg *config.Config, path, branch string, deleteBranch bool) (removed, branchDeleted bool, err error) {
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
		// -d, not -D: an unmerged branch must survive a finish. The
		// protected check is redundant with the caller's own skip list,
		// and deliberately so — this is the last gate before the delete.
		if derr := deleteBranchGuarded(ctx, mainRunner, cfg, branch, branchDeleteOpts{}); derr != nil {
			return removed, false, fmt.Errorf("worktree finish cleanup: %w", derr)
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
gk-parent or base. The report names the reason for every kept worktree.

A branch that never moved since it was created is kept as unstarted, even
though it counts as merged: its worktree is often a workspace that an agent
opened a moment ago. With --stale, cleanup removes an unstarted worktree
whose branch was created longer ago than the given age.

Pass -i to choose in a checkbox list instead of -y. The list shows every
worktree except the current and main ones. Each row shows the branch, the
size, the age of the last commit, and a state: the verdict of cleanup with
its numbers (unmerged ↑3), uncommitted changes, and whether the branch is
pushed. The box under the list describes the row under the cursor: its path,
the last commit message, when the branch was created, and the distance to its
parent or base. s changes the order: safe first, oldest commit, largest, and
name. The candidates are already checked. You can also check a
worktree that cleanup keeps. If it holds uncommitted changes, or a detached
HEAD that no branch or tag contains, cleanup shows what is lost and asks
again. The branch of a worktree that you check yourself is never deleted.
In gk worktree without a subcommand, x opens the same list.

A tree whose only change is untracked, regenerable files (package-lock.json,
yarn.lock, pnpm-lock.yaml, bun.lock, bun.lockb, .DS_Store) is not dirty:
cleanup deletes those files and removes the worktree. Any other untracked file,
or any tracked change, keeps the worktree.

If the directory of a worktree is already gone ([prunable] in gk worktree
list), only its registration in .git/worktrees is left. Cleanup removes that
registration even if the branch is not merged or not old enough, because no
file and no commit is lost. It never deletes the branch of such a worktree,
even with --delete-branches. The current worktree, detached worktrees, and
protected branches stay excluded.`,
		Args: cobra.NoArgs,
		RunE: runWorktreeCleanup,
	}
	c.Flags().Bool("merged", true, "only remove worktrees whose branch is merged into its parent/base (not applied to [prunable] registrations)")
	c.Flags().String("stale", "", "only remove worktrees whose branch tip is older than this age, e.g. 7d, 12h (not applied to [prunable] registrations)")
	c.Flags().Bool("delete-branches", false, "delete the local branch after removing its worktree (never for [prunable] registrations)")
	c.Flags().BoolP("yes", "y", false, "perform removals; without this, cleanup only reports candidates")
	c.Flags().BoolP("interactive", "i", false, "choose the worktrees to remove in a checkbox list that shows each one's size (needs a terminal)")
	c.Flags().Bool("force-stale-locks", false, "unlock and remove worktrees whose lock holder is no longer running")
	c.Flags().Bool("discard-dirty", false, "allow removal of dirty worktrees with git worktree remove --force (destructive)")
	c.Flags().BoolP("global", "g", false, "clean gk-managed worktrees of every project under worktree.base; runs outside a repository")
	return c
}

func runWorktreeCleanup(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	runner := &git.ExecRunner{Dir: RepoFlag()}
	global, _ := cmd.Flags().GetBool("global")
	if !global {
		if err := ensureGitRepo(ctx, runner); err != nil {
			return err
		}
	}
	cfg, _ := config.Load(cmd.Flags())
	staleRaw, _ := cmd.Flags().GetString("stale")
	stale, err := parseWorktreeStale(staleRaw)
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun := DryRun() || !yes
	opts := worktreeCleanupOpts{stale: stale}
	opts.merged, _ = cmd.Flags().GetBool("merged")
	opts.deleteBranches, _ = cmd.Flags().GetBool("delete-branches")
	opts.forceStaleLocks, _ = cmd.Flags().GetBool("force-stale-locks")
	opts.discardDirty, _ = cmd.Flags().GetBool("discard-dirty")

	if interactive, _ := cmd.Flags().GetBool("interactive"); interactive {
		switch {
		case global:
			return fmt.Errorf("worktree cleanup: -i works on one repository; run it without --global inside that repository")
		case yes:
			return fmt.Errorf("worktree cleanup: -i and -y cannot be combined; -i asks which worktrees to remove, -y removes every candidate")
		case !promptAllowed():
			return fmt.Errorf("worktree cleanup: -i needs an interactive terminal; run gk worktree cleanup --json for a machine-readable report")
		}
		return runWorktreeCleanupSelect(ctx, cmd.OutOrStdout(), runner, cfg, opts, currentWorktreePath(ctx, runner))
	}

	var report worktreeCleanupJSON
	if global {
		report, err = runGlobalWorktreeCleanup(ctx, cmd, cfg, currentWorktreePath(ctx, runner), opts, dryRun)
		if err != nil {
			return err
		}
	} else {
		report, err = collectWorktreeCleanup(ctx, runner, cfg, opts, "", currentWorktreePath(ctx, runner))
		if err != nil {
			return err
		}
		if !dryRun {
			report.Removed, report.Failed = applyWorktreeCleanup(ctx, cleanupProgress(cmd), runner, report.Candidates, opts)
		}
	}
	report.DryRun = dryRun
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), report)
	}
	renderWorktreeCleanup(cmd.OutOrStdout(), report)
	return nil
}

// runGlobalWorktreeCleanup runs the per-repository cleanup for every
// repository that owns a directory under worktree.base. Repositories are keyed
// by their main worktree because two clones with the same basename share one
// project directory. Only worktrees under base are considered.
func runGlobalWorktreeCleanup(ctx context.Context, cmd *cobra.Command, cfg *config.Config, current string, opts worktreeCleanupOpts, dryRun bool) (worktreeCleanupJSON, error) {
	report := worktreeCleanupJSON{
		Candidates: []worktreeCleanupEntry{},
		Skipped:    []worktreeCleanupEntry{},
	}
	base := ""
	if cfg != nil {
		base = expandHome(cfg.Worktree.Base)
	}
	if base == "" {
		base = expandHome("~/.gk/worktree")
	}
	roots, err := managedWorktreeRoots(base)
	if err != nil {
		return report, fmt.Errorf("worktree cleanup: scan worktree base %q: %w", base, err)
	}
	base = resolvePath(base)
	tag := func(rows []worktreeCleanupEntry) []worktreeCleanupEntry {
		for i := range rows {
			rows[i].Project = managedProjectOf(base, rows[i].Path)
		}
		return rows
	}

	seen := map[string]bool{}
	for _, root := range roots {
		main, err := mainWorktreePath(ctx, &git.ExecRunner{Dir: root.Path})
		if err != nil {
			report.Skipped = append(report.Skipped, worktreeCleanupEntry{
				Path: root.Path, Project: root.Project, Reasons: []string{"repo-unreadable"}, Error: err.Error(),
			})
			continue
		}
		main = resolvePath(main)
		if seen[main] {
			continue
		}
		seen[main] = true

		runner := &git.ExecRunner{Dir: main}
		// Load the repository's own .gk.yaml so its protected branches and
		// base branch decide what is safe, not the caller's cwd config.
		repoFlags := pflag.NewFlagSet("worktree-cleanup-global", pflag.ContinueOnError)
		repoFlags.String("repo", main, "")
		repoCfg, _ := config.Load(repoFlags)
		sub, err := collectWorktreeCleanup(ctx, runner, repoCfg, opts, base, current)
		if err != nil {
			report.Skipped = append(report.Skipped, worktreeCleanupEntry{
				Path: main, Project: root.Project, Reasons: []string{"repo-unreadable"}, Error: err.Error(),
			})
			continue
		}
		report.Candidates = append(report.Candidates, tag(sub.Candidates)...)
		report.Skipped = append(report.Skipped, tag(sub.Skipped)...)
		if !dryRun {
			removed, failed := applyWorktreeCleanup(ctx, cleanupProgress(cmd), runner, sub.Candidates, opts)
			report.Removed = append(report.Removed, removed...)
			report.Failed = append(report.Failed, failed...)
		}
	}
	return report, nil
}

func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// collectWorktreeCleanup classifies the runner repository's worktrees. A
// non-empty within limits the scan to worktrees under that directory; current
// is the worktree the caller runs in, which is never removed.
func collectWorktreeCleanup(ctx context.Context, runner *git.ExecRunner, cfg *config.Config, opts worktreeCleanupOpts, within, current string) (worktreeCleanupJSON, error) {
	out, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return worktreeCleanupJSON{}, fmt.Errorf("worktree cleanup: list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	entries := parseWorktreePorcelain(string(out))
	meta := loadWorktreeBranchMeta(ctx, runner, nil)

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
	if opts.merged {
		mergeBase = finishBaseBranch(ctx, runner, cfg)
	}

	// Each verdict costs several sequential git calls (parent config, merge
	// base, squash check, git status), so classify statusScanWorkers
	// worktrees at a time like the list's status scan; the report keeps the
	// porcelain order.
	var scope []WorktreeEntry
	for _, e := range entries {
		if within != "" && !strings.HasPrefix(resolvePath(e.Path), within+string(filepath.Separator)) {
			continue
		}
		scope = append(scope, e)
	}
	rows := make([]worktreeCleanupEntry, len(scope))
	isCandidate := make([]bool, len(scope))
	sem := make(chan struct{}, min(runtime.NumCPU(), statusScanWorkers))
	var wg sync.WaitGroup
	for i, e := range scope {
		wg.Add(1)
		go func(i int, e WorktreeEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i], isCandidate[i] = classifyCleanupEntry(ctx, runner, e, opts, protected, meta, mergeBase, current)
		}(i, e)
	}
	wg.Wait()

	report := worktreeCleanupJSON{
		Candidates: []worktreeCleanupEntry{},
		Skipped:    []worktreeCleanupEntry{},
	}
	for i, row := range rows {
		if isCandidate[i] {
			report.Candidates = append(report.Candidates, row)
		} else {
			report.Skipped = append(report.Skipped, row)
		}
	}
	return report, nil
}

// classifyCleanupEntry returns the report row for one worktree and whether
// cleanup removes it.
func classifyCleanupEntry(ctx context.Context, runner *git.ExecRunner, e WorktreeEntry, opts worktreeCleanupOpts, protected map[string]bool, meta map[string]worktreeBranchMeta, mergeBase, current string) (worktreeCleanupEntry, bool) {
	row := worktreeCleanupEntry{Path: e.Path, Branch: e.Branch}
	skip := func(reason string) (worktreeCleanupEntry, bool) {
		row.Reasons = append(row.Reasons, reason)
		return row, false
	}
	switch {
	case current != "" && sameDir(current, e.Path):
		return skip("current")
	case e.Bare:
		return skip("bare")
	case e.Detached:
		return skip("detached")
	case e.Branch == "":
		return skip("no-branch")
	case protected[e.Branch] || isProtectedBranchName(e.Branch, nil):
		return skip("protected")
	}

	// The porcelain entry already says whether the worktree is locked; the
	// re-read only finds the lock holder, and running it for every entry
	// cost one git call per worktree on each gk worktree list.
	var lock worktreeLock
	if e.Locked {
		lock = worktreeLockInfo(ctx, runner, e.Path)
	}
	row.Locked = lock.Locked
	if lock.Locked {
		switch {
		case lock.Alive:
			return skip("locked-live")
		case !opts.forceStaleLocks:
			return skip("locked-stale")
		}
		row.Reasons = append(row.Reasons, "locked-stale")
	}

	// git also marks a worktree on an unmounted volume prunable. Only when
	// the parent directory still exists is the worktree directory known to
	// be gone; otherwise the entry goes through the usual gates.
	if e.Prunable && isDirPath(filepath.Dir(e.Path)) {
		// Only the registration is left, so removing it loses no file and
		// no commit: the merge and age gates do not apply. The branch is
		// kept because its merge was never checked.
		row.Reasons = append(row.Reasons, "prunable")
		row.Action = cleanupActionPruneRecord
		return row, true
	}

	dirty, untracked, onlyUntracked := cleanupWorktreeStatus(ctx, e.Path, opts.scanned)
	row.Dirty = dirty
	if dirty != nil {
		disposable := disposableUntrackedOnly(untracked, onlyUntracked)
		switch {
		case opts.discardDirty:
			row.Reasons = append(row.Reasons, "dirty-discard")
		case disposable != nil:
			row.Disposable = disposable
			row.Reasons = append(row.Reasons, "disposable-untracked")
		default:
			row.Untracked = untracked
			return skip("dirty")
		}
	} else {
		row.Reasons = append(row.Reasons, "clean")
	}

	// A branch that never moved since it was created has no work of its
	// own, so the merge gate would call it merged, but its worktree is
	// often a workspace an agent opened moments ago. Only --stale reclaims
	// it, judged by when the branch was created.
	if created, ok := branchUnstartedSince(ctx, runner, e.Branch); ok {
		if opts.stale == 0 || time.Since(created) < opts.stale {
			return skip("unstarted")
		}
		row.Reasons = append(row.Reasons, "unstarted")
	}

	if bm, ok := meta[e.Branch]; ok && !bm.LastCommit.IsZero() {
		age := time.Since(bm.LastCommit)
		row.Age = shortAge(bm.LastCommit)
		if opts.stale > 0 {
			if age < opts.stale {
				return skip("fresh")
			}
			row.Reasons = append(row.Reasons, "stale")
		}
	} else if opts.stale > 0 {
		return skip("age-unknown")
	}

	if opts.merged {
		target := cleanupMergeTarget(ctx, runner, mergeBase, e.Branch)
		row.Target = target
		if target == "" {
			return skip("no-merge-target")
		}
		if target == e.Branch {
			return skip("target-is-self")
		}
		if isAncestor(ctx, runner, e.Branch, target) {
			row.Reasons = append(row.Reasons, "merged")
		} else {
			// A squash merge never becomes an ancestor; the content test
			// (merging into target changes nothing) is the proof that the
			// work landed. Check errors degrade to the old "unmerged" skip
			// — under-claiming keeps the worktree, never removes one.
			ok, _ := branchclean.EffectivelyMerged(ctx, runner, target, e.Branch)
			if !ok {
				return skip("unmerged")
			}
			row.Reasons = append(row.Reasons, "squash-merged")
		}
	}

	row.Action = "remove-worktree"
	if opts.deleteBranches {
		row.Action = "remove-worktree-delete-branch"
	}
	return row, true
}

type reflogEntry struct {
	at      time.Time
	subject string
}

// branchReflog reads the reflog of branch, newest entry first. ok is false
// when git cannot read it.
func branchReflog(ctx context.Context, runner git.Runner, branch string) ([]reflogEntry, bool) {
	out, _, err := runner.Run(ctx, "reflog", "show", "--date=unix", "--format=%gd%x00%gs", "refs/heads/"+branch)
	if err != nil {
		return nil, false
	}
	var entries []reflogEntry
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		selector, subject, ok := strings.Cut(line, "\x00")
		open := strings.LastIndex(selector, "@{")
		if !ok || open < 0 || !strings.HasSuffix(selector, "}") {
			continue
		}
		secs, err := strconv.ParseInt(selector[open+2:len(selector)-1], 10, 64)
		if err != nil {
			continue
		}
		entries = append(entries, reflogEntry{at: time.Unix(secs, 0), subject: subject})
	}
	return entries, true
}

func isBranchCreation(e reflogEntry) bool {
	return strings.HasPrefix(e.subject, "branch: Created from")
}

// branchUnstartedSince returns when branch was created if its reflog holds
// nothing but the creation entry. A missing or unreadable reflog reports
// false, which keeps the merge gate's verdict.
func branchUnstartedSince(ctx context.Context, runner git.Runner, branch string) (time.Time, bool) {
	entries, ok := branchReflog(ctx, runner, branch)
	if !ok || len(entries) != 1 || !isBranchCreation(entries[0]) {
		return time.Time{}, false
	}
	return entries[0].at, true
}

// branchCreatedAt returns when branch was created, read from the oldest
// reflog entry. ok is false once expiry has dropped the creation entry.
func branchCreatedAt(ctx context.Context, runner git.Runner, branch string) (time.Time, bool) {
	entries, ok := branchReflog(ctx, runner, branch)
	if !ok || len(entries) == 0 || !isBranchCreation(entries[len(entries)-1]) {
		return time.Time{}, false
	}
	return entries[len(entries)-1].at, true
}

func cleanupWorktreeStatus(ctx context.Context, path string, scanned map[string]worktreeListStatus) (dirty *contextDirtyJSON, untracked []string, onlyUntracked bool) {
	if st, ok := scanned[path]; ok {
		return st.dirty, st.untracked, st.onlyUntracked
	}
	dirty = worktreeDirtyAt(ctx, path)
	if dirty == nil {
		return nil, nil, false
	}
	untracked, onlyUntracked = worktreeUntrackedAt(ctx, path)
	return dirty, untracked, onlyUntracked
}

func cleanupMergeTarget(ctx context.Context, runner git.Runner, base, branch string) string {
	// ExplicitOnly: branch deletion is destructive, so the merge target must
	// come from an explicit gk-parent or the trunk fallback — never a heuristic
	// inference. Otherwise a branch forked off a single non-trunk sibling would
	// be judged "merged" into that guess and deleted despite never reaching the
	// real trunk (same guard land.go applies to its hop targets).
	return branchparent.NewResolver(git.NewClient(runner)).ExplicitOnly().ResolveBase(ctx, branch, base)
}

func applyWorktreeCleanup(ctx context.Context, progress io.Writer, runner *git.ExecRunner, candidates []worktreeCleanupEntry, opts worktreeCleanupOpts) (removed, failed []worktreeCleanupEntry) {
	for _, c := range candidates {
		if c.Action == cleanupActionPruneRecord {
			if _, stderr, err := runner.Run(ctx, "worktree", "remove", c.Path); err != nil {
				c.Error = fmt.Sprintf("prune record %s: %s: %v", c.Path, strings.TrimSpace(string(stderr)), err)
				failed = append(failed, c)
				continue
			}
			fmt.Fprintf(progress, "pruned record %s (directory was gone; branch %s kept)\n", c.Path, c.Branch)
			removed = append(removed, c)
			continue
		}
		if err := removeDisposableFiles(c.Path, c.Disposable); err != nil {
			c.Error = err.Error()
			failed = append(failed, c)
			continue
		}
		for _, f := range c.Disposable {
			fmt.Fprintf(progress, "deleted %s\n", filepath.Join(c.Path, f))
		}
		err := removeCleanupWorktree(ctx, runner, progress, c.Path, c.Locked && opts.forceStaleLocks, c.Dirty != nil && opts.discardDirty)
		if err != nil {
			c.Error = err.Error()
			failed = append(failed, c)
			continue
		}
		if opts.deleteBranches && c.Branch != "" {
			// The candidate already passed the merged gate, but git's
			// `branch -d` checks merged-ness against this runner's HEAD (or
			// upstream), not target — so a branch merged into a non-HEAD target
			// would be wrongly refused. Re-confirm the merge into target and
			// force-delete only then; otherwise fall back to -d's own backstop.
			// The content re-check covers squash-merged candidates, which are
			// never ancestors and would fail -d despite their work having
			// landed in target.
			delFlag := "-d"
			if c.Target != "" && c.Target != c.Branch {
				if isAncestor(ctx, runner, c.Branch, c.Target) {
					delFlag = "-D"
				} else if ok, _ := branchclean.EffectivelyMerged(ctx, runner, c.Target, c.Branch); ok {
					delFlag = "-D"
				}
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
		// remove --force skips git's own clean check.
		if !discardDirty {
			if err := checkNoHiddenChanges(ctx, runner, path); err != nil {
				return err
			}
		}
		return forceRemoveWorktree(ctx, runner, w, path)
	}
	args := []string{"worktree", "remove"}
	if discardDirty {
		args = append(args, "--force")
	}
	args = append(args, path)
	if _, stderr, err := runner.Run(ctx, args...); err != nil {
		msg := strings.TrimSpace(string(stderr))
		if !isSubmoduleRemoveRefusal(msg) {
			return fmt.Errorf("remove %s: %s: %w", path, msg, err)
		}
		// Git refuses any worktree that ever checked out a submodule, even a
		// clean one, unless remove is forced twice after a deinit, and its
		// own clean check never ran.
		if !discardDirty {
			if err := checkNoHiddenChanges(ctx, runner, path); err != nil {
				return err
			}
		}
		if _, derr, err := runner.Run(ctx, "-C", path, "submodule", "deinit", "--all", "--force"); err != nil {
			return fmt.Errorf("remove %s: deinitialize submodules: %s: %w", path, strings.TrimSpace(string(derr)), err)
		}
		if _, ferr, err := runner.Run(ctx, "worktree", "remove", "--force", "--force", path); err != nil {
			return fmt.Errorf("remove %s: %s: %w", path, strings.TrimSpace(string(ferr)), err)
		}
	}
	fmt.Fprintf(w, "removed worktree %s\n", path)
	return nil
}

// checkNoHiddenChanges guards a forced removal. The caller's status scan
// honours submodule.<name>.ignore and status.showUntrackedFiles, which can
// hide unsaved work that the forced removal would delete.
func checkNoHiddenChanges(ctx context.Context, runner git.Runner, path string) error {
	out, serr, err := runner.Run(ctx, "-C", path, "status", "--porcelain", "--ignore-submodules=none", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("remove %s: check for uncommitted changes: %s: %w", path, strings.TrimSpace(string(serr)), err)
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		return fmt.Errorf("remove %s: the worktree or one of its submodules has uncommitted changes", path)
	}
	return nil
}

func isSubmoduleRemoveRefusal(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "containing submodules cannot be moved or removed")
}

// cleanupProgress is where cleanup prints each removal: the command's
// output, or nowhere when --json carries the result instead.
func cleanupProgress(cmd *cobra.Command) io.Writer {
	if JSONOut() {
		return io.Discard
	}
	return cmd.OutOrStdout()
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
