package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// gk apply — patch application with an automatic remediation ladder.
//
// Session-audit evidence shows the dominant agent pattern around raw
// `git apply` is retry churn: a --check probe, a failure, then flag
// permutations (--recount, --unidiff-zero, --3way) until one sticks.
// One gk call collapses that loop: each patch walks a fixed ladder of
// strategies and the result records which rung succeeded.

// Ladder strategy names, in attempt order. Recorded per patch in the
// result so callers can see which rung actually applied it.
const (
	applyStrategyPlain       = "plain"
	applyStrategyRecount     = "recount"
	applyStrategyUnidiffZero = "recount+unidiff-zero"
	applyStrategyThreeWay    = "3way"
)

// git apply learned to combine --cached with --3way in 2.35; on older
// git the 3-way rung is skipped in --staged mode instead of surfacing
// a flag-compatibility error as a bogus ladder failure.
const (
	threeWayCachedGitMajor = 2
	threeWayCachedGitMinor = 35
)

func init() {
	rootCmd.AddCommand(newApplyCmd())
}

func newApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply <patch-file>...",
		Short: "패치 적용 — 실패하면 recount/unidiff-zero/3way 순서로 자동 재시도",
		Long: `패치 파일을 적용하되, 실패하면 흔한 원인 순서대로 전략을 바꿔 재시도한다:
plain → --recount(줄 수가 어긋난 헤더) → --unidiff-zero(컨텍스트 0줄 패치)
→ --3way(컨텍스트가 어긋난 패치의 3-way 병합 폴백). 어느 전략으로 적용됐는지
결과에 기록되므로 플래그 조합을 손으로 순회할 필요가 없다.

패치를 여러 개 주면 전부-또는-전무로 적용한다 — 하나라도 모든 전략이
실패하면 이미 적용된 패치까지 되돌린 뒤 실패를 보고한다.

--check(및 전역 --dry-run)는 각 패치를 현재 트리에 대해 독립적으로 검사한다:
실제 실행은 패치를 순차 적용하므로, 앞 패치 위에서 생성된 시리즈는 --check가
실패해도 실제 적용은 성공할 수 있다.

  gk apply fix.patch              # 워킹트리에 적용 (plain git apply와 동일 범위)
  gk apply --staged fix.patch     # 인덱스에만 적용 (git apply --cached)
  gk apply --check fix.patch      # 적용하지 않고 가능한 전략만 확인
  gk apply --reverse fix.patch    # 반대로 적용 (패치 되돌리기)`,
		Args: cobra.MinimumNArgs(1),
		RunE: runApply,
	}
	cmd.Flags().Bool("staged", false, "워킹트리는 두고 인덱스에만 적용 (git apply --cached)")
	cmd.Flags().Bool("cached", false, "--staged의 별칭")
	cmd.Flags().Bool("check", false, "적용하지 않고 적용 가능 여부(성공할 전략)만 검사")
	cmd.Flags().Bool("reverse", false, "패치를 반대로 적용 (이미 적용된 패치 되돌리기)")
	return cmd
}

// applyRung is one strategy attempt in the remediation ladder.
type applyRung struct {
	strategy string
	flags    []string
}

// applyRungs builds the ladder. The --unidiff-zero rung's spec gate —
// "the patch shows zero-context hunks OR --recount alone failed" — is
// always satisfied by its ladder position (it only runs after the
// recount rung failed), so it needs no separate patch inspection.
func applyRungs(ctx context.Context, runner git.Runner, staged bool) []applyRung {
	rungs := []applyRung{
		{applyStrategyPlain, nil},
		{applyStrategyRecount, []string{"--recount"}},
		{applyStrategyUnidiffZero, []string{"--recount", "--unidiff-zero"}},
	}
	if !staged || threeWayCachedSupported(ctx, runner) {
		rungs = append(rungs, applyRung{applyStrategyThreeWay, []string{"--3way"}})
	}
	return rungs
}

// threeWayCachedSupported reports whether the installed git accepts
// --cached together with --3way. An unparsable version reads as "no"
// so the ladder degrades to a skipped rung instead of a flag error.
func threeWayCachedSupported(ctx context.Context, runner git.Runner) bool {
	out, _, err := runner.Run(ctx, "--version")
	if err != nil {
		return false
	}
	major, minor := parseGitVersion(string(out))
	return major > threeWayCachedGitMajor ||
		(major == threeWayCachedGitMajor && minor >= threeWayCachedGitMinor)
}

type applyResultJSON struct {
	Schema int `json:"schema"`
	// Result names what the run did — "applied" for a real apply, "check"
	// for a --check probe, "dry-run" when the global --dry-run forced the
	// probe — following the land/rebase convention, so agents can tell a
	// probe's success payload from a real apply's.
	Result     string             `json:"result"`
	Applied    []applyAppliedJSON `json:"applied"`
	Failed     *applyFailedJSON   `json:"failed"` // null when every patch applied
	RolledBack bool               `json:"rolled_back"`
}

type applyAppliedJSON struct {
	Patch    string `json:"patch"`
	Strategy string `json:"strategy"`
}

type applyFailedJSON struct {
	Patch string `json:"patch"`
	Error string `json:"error"`
}

func (r applyResultJSON) agentState() string {
	if r.Failed != nil {
		return envStateError
	}
	return ""
}

func runApply(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	w := cmd.OutOrStdout()

	staged, _ := cmd.Flags().GetBool("staged")
	cached, _ := cmd.Flags().GetBool("cached")
	staged = staged || cached
	check, _ := cmd.Flags().GetBool("check")
	reverse, _ := cmd.Flags().GetBool("reverse")
	// The global --dry-run contract maps onto --check: walk the ladder,
	// mutate nothing. Both probe each patch against the CURRENT tree
	// independently, while a real multi-patch run applies sequentially —
	// a stacked series can fail the probe yet apply for real (see Long).
	if DryRun() {
		check = true
	}

	// Resolve each patch to an absolute path once: the existence precheck
	// runs from gk's own CWD while git apply runs inside --repo, so a
	// relative path would otherwise name two different files.
	patches := make([]string, 0, len(args))
	for _, p := range args {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("apply: resolve patch path %s: %w", p, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return WithHint(fmt.Errorf("apply: patch file not found: %s", p),
				"pass a readable patch file path")
		}
		patches = append(patches, abs)
	}

	var modeFlags []string
	if staged {
		modeFlags = append(modeFlags, "--cached")
	}
	if reverse {
		modeFlags = append(modeFlags, "--reverse")
	}
	rungs := applyRungs(ctx, runner, staged)

	// Rollback anchors, captured before anything mutates. write-tree
	// snapshots the index; in worktree mode the tree is captured with
	// the same throwaway-index snapshot `gk snapshot` uses, so files a
	// patch creates or edits — tracked or not — can all be restored.
	var idxTree, wtTree string
	if !check {
		out, stderr, err := runner.Run(ctx, "write-tree")
		if err != nil {
			return WithBlocked(
				fmt.Errorf("apply: cannot snapshot the index for rollback: %s",
					firstNonEmptyLine(string(stderr), err.Error())),
				"index-not-snapshottable",
				"resolve unmerged paths (or commit intent-to-add entries) first, then retry",
			)
		}
		idxTree = strings.TrimSpace(string(out))
		if !staged {
			wtTree, err = snapshotTree(ctx, runner)
			if err != nil {
				return fmt.Errorf("apply: snapshot working tree for rollback: %w", err)
			}
		}
	}

	res := applyResultJSON{Schema: 1, Result: applyRunMode(check), Applied: []applyAppliedJSON{}}
	for _, patch := range patches {
		strategy, applyErr := applyPatchLadder(ctx, runner, modeFlags, rungs, patch, check)
		if applyErr == nil {
			// git apply --3way implies --index: a successful 3-way rung
			// stages the result too. Default (worktree) mode promises
			// plain-git-apply scope, so pin the index back to its pre-run
			// snapshot and leave the patch on the working tree only.
			if !check && !staged && strategy == applyStrategyThreeWay {
				if _, stderr, rerr := runner.Run(ctx, "read-tree", idxTree); rerr != nil {
					return fmt.Errorf("apply: restore index after 3-way apply of %s: %s: %w",
						patch, strings.TrimSpace(string(stderr)), rerr)
				}
			}
			res.Applied = append(res.Applied, applyAppliedJSON{Patch: patch, Strategy: strategy})
			if !JSONOut() {
				verb := "applied"
				if check {
					verb = "check ok"
				}
				fmt.Fprintln(w, successLinef(verb, "%s (%s)", patch, strategy))
			}
			continue
		}

		res.Failed = &applyFailedJSON{Patch: patch, Error: applyErr.Error()}
		var rbErr error
		if !check {
			rbErr = rollbackApply(ctx, runner, idxTree, wtTree, staged)
			res.RolledBack = rbErr == nil
		}
		if JSONOut() {
			_ = emitAgentResult(w, res)
		} else {
			bad := color.New(color.FgRed, color.Bold).SprintFunc()
			fmt.Fprintf(w, "%s %s — %s\n", bad("✗"), patch, applyErr.Error())
			switch {
			case check:
				// probe only — nothing to roll back
			case rbErr == nil && staged:
				fmt.Fprintln(w, cellFaint("  rolled back — index restored to its pre-apply state"))
			case rbErr == nil:
				fmt.Fprintln(w, cellFaint("  rolled back — index and working tree restored to their pre-apply state"))
			}
		}
		return applyFailureError(ctx, runner, staged, reverse, check, rungs, patches, patch, applyErr, rbErr)
	}

	if JSONOut() {
		return emitAgentResult(w, res)
	}
	return nil
}

// applyRunMode names what this run does for the JSON result. The global
// --dry-run is surfaced distinctly so a batch-driven `gk --dry-run apply`
// reads like the other dry-run-marked verbs.
func applyRunMode(check bool) string {
	switch {
	case DryRun():
		return "dry-run"
	case check:
		return "check"
	default:
		return "applied"
	}
}

// applyPatchLadder tries each rung in order and returns the strategy
// that succeeded. In check mode every attempt carries --check so
// nothing is mutated. A real run relies on git apply's all-or-nothing
// semantics — only the 3-way rung can leave partial (conflicted)
// state behind on failure, and the caller rolls the whole run back.
// The returned error carries the FIRST rung's diagnostic: the plain
// failure names the mismatching file/line, while later rungs tend to
// fail with less specific fallback errors.
func applyPatchLadder(ctx context.Context, runner git.Runner, modeFlags []string, rungs []applyRung, patch string, check bool) (string, error) {
	var firstErr error
	for _, rung := range rungs {
		gitArgs := []string{"apply"}
		gitArgs = append(gitArgs, modeFlags...)
		gitArgs = append(gitArgs, rung.flags...)
		if check {
			gitArgs = append(gitArgs, "--check")
		}
		gitArgs = append(gitArgs, "--", patch)
		_, stderr, err := runner.Run(ctx, gitArgs...)
		serr := string(stderr)
		// git reports a would-conflict 3-way CHECK as success (exit 0,
		// "with conflicts" on stderr) even though the real apply exits
		// non-zero — normalize to failure so --check predicts the real
		// outcome.
		if err == nil && check && rung.strategy == applyStrategyThreeWay &&
			strings.Contains(serr, "with conflicts") {
			err = fmt.Errorf("3-way merge would leave conflicts")
		}
		if err == nil {
			return rung.strategy, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s", firstNonEmptyLine(serr, err.Error()))
		}
	}
	return "", firstErr
}

// rollbackApply restores the index (and, in worktree mode, the touched
// files) to the pre-run snapshots. The worktree restore set is computed
// by re-snapshotting the current tree and diffing against the original:
// paths still present in the original snapshot are checked out from it,
// paths the patches created are deleted. Ordering matters — `git
// checkout <tree> -- <paths>` also writes the index, so the final
// read-tree is what pins the index back to its exact original state
// (including clearing any unmerged entries a failed 3-way left behind).
//
// Known gap: the snapshot honours .gitignore (same as `gk snapshot`),
// so a patch that touched an ignored untracked file is not restored.
func rollbackApply(ctx context.Context, runner *git.ExecRunner, idxTree, wtTree string, staged bool) error {
	if !staged {
		curTree, err := snapshotTree(ctx, runner)
		if err != nil {
			return fmt.Errorf("re-snapshot working tree: %w", err)
		}
		if curTree != wtTree {
			changed, err := treeDiffPaths(ctx, runner, wtTree, curTree)
			if err != nil {
				return err
			}
			keep, err := treePathSet(ctx, runner, wtTree, changed)
			if err != nil {
				return err
			}
			var restore, remove []string
			for _, p := range changed {
				if _, ok := keep[p]; ok {
					restore = append(restore, p)
				} else {
					remove = append(remove, p)
				}
			}
			if len(restore) > 0 {
				checkoutArgs := append([]string{"checkout", wtTree, "--"}, restore...)
				if _, stderr, err := runner.Run(ctx, checkoutArgs...); err != nil {
					return fmt.Errorf("restore files from snapshot: %s: %w",
						strings.TrimSpace(string(stderr)), err)
				}
			}
			if len(remove) > 0 {
				root, err := worktreeRoot(ctx, runner)
				if err != nil {
					return err
				}
				for _, p := range remove {
					if err := os.Remove(filepath.Join(root, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("remove patch-created file %s: %w", p, err)
					}
				}
			}
		}
	}
	if _, stderr, err := runner.Run(ctx, "read-tree", idxTree); err != nil {
		return fmt.Errorf("restore index: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// applyFailureError shapes the ladder-exhaustion failure. A patch
// whose reverse applies cleanly is almost certainly already applied —
// surface that instead of a generic context mismatch. A failed
// rollback is never silent: the error says the repository holds
// partially applied state and how to inspect it.
func applyFailureError(ctx context.Context, runner git.Runner, staged, reverse, check bool, rungs []applyRung, patches []string, patch string, cause, rbErr error) error {
	if rbErr != nil {
		return WithRemedy(
			fmt.Errorf("apply: %s failed (%s) and rollback also failed (%v) — the repository may hold partially applied state", patch, cause, rbErr),
			"inspect what was left behind before retrying; a failed 3-way attempt can leave conflict markers",
			errRemedy{Command: selfCmd("context --include=diff"), Safety: "safe"},
		)
	}

	// Reverse-check probe: if applying the patch in the OPPOSITE
	// direction would succeed, its changes are already present.
	probe := []string{"apply"}
	if staged {
		probe = append(probe, "--cached")
	}
	if !reverse {
		probe = append(probe, "--reverse")
	}
	probe = append(probe, "--check", "--", patch)
	if _, _, err := runner.Run(ctx, probe...); err == nil {
		if reverse {
			return WithRemedy(
				fmt.Errorf("apply: %s does not reverse-apply — the patch does not appear to be applied", patch),
				"the forward patch applies cleanly, so there is nothing to reverse; verify with --check",
				errRemedy{Command: selfCmd("apply --check " + patch), Safety: "safe"},
			)
		}
		return WithRemedy(
			fmt.Errorf("apply: %s appears to be already applied", patch),
			"its changes are already present; nothing to do, or undo them with --reverse",
			errRemedy{Command: selfCmd("apply --check --reverse " + patch), Safety: "safe"},
		)
	}

	tried := make([]string, 0, len(rungs))
	for _, r := range rungs {
		tried = append(tried, r.strategy)
	}
	hint := "probe without mutating to see the failing hunks, or regenerate the patch against the current tree"
	if check && len(patches) > 1 {
		// A stacked series legitimately fails the probe: --check tests each
		// patch against the same unmodified tree, while the real run applies
		// them sequentially.
		hint += "; note --check probes each patch against the current tree independently — a series whose later patches build on earlier ones can fail --check yet apply for real"
	}
	return WithRemedy(
		fmt.Errorf("apply: %s: %s (tried %s)", patch, cause, strings.Join(tried, ", ")),
		hint,
		errRemedy{Command: selfCmd("apply --check " + strings.Join(patches, " ")), Safety: "safe"},
	)
}

// --- small tree helpers ------------------------------------------------------

// treeDiffPaths lists the paths that differ between two trees.
// --no-renames keeps a rename visible as its delete+add pair so both
// sides land in the restore set.
func treeDiffPaths(ctx context.Context, runner git.Runner, a, b string) ([]string, error) {
	out, stderr, err := runner.Run(ctx, "diff-tree", "-r", "-z", "--name-only", "--no-renames", a, b)
	if err != nil {
		return nil, fmt.Errorf("diff snapshots: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// treePathSet returns which of the given paths exist in tree.
func treePathSet(ctx context.Context, runner git.Runner, tree string, paths []string) (map[string]struct{}, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	args := append([]string{"ls-tree", "-r", "-z", "--name-only", tree, "--"}, paths...)
	out, stderr, err := runner.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("list snapshot paths: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	set := make(map[string]struct{}, len(paths))
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			set[p] = struct{}{}
		}
	}
	return set, nil
}

func worktreeRoot(ctx context.Context, runner git.Runner) (string, error) {
	out, stderr, err := runner.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("locate worktree root: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return strings.TrimSpace(string(out)), nil
}
