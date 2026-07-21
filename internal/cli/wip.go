package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/git"
)

// wipMarker is the conventional subject used by gwip / gunwip.
const wipMarker = "--wip-- [skip ci]"

func init() {
	wip := &cobra.Command{
		Use:   "wip",
		Short: "Stage everything and create a throwaway WIP commit",
		Long: `Creates a disposable commit so you can switch contexts without losing work.

Stages all tracked changes (including deletions) and commits with subject
"--wip-- [skip ci]". The commit skips hooks (--no-verify) and signing
(--no-gpg-sign) so it is fast and reversible — use 'gk unwip' later to
undo the commit while keeping the changes staged.`,
		RunE: runWip,
	}
	wip.AddCommand(newWIPRepairCmd())
	rootCmd.AddCommand(wip)

	unwip := &cobra.Command{
		Use:   "unwip",
		Short: "Undo a WIP commit created by 'gk wip'",
		Long: `If HEAD is a commit whose subject starts with '--wip--', resets it with
'git reset HEAD~1' so the changes return to the working tree. Refuses to
act on non-WIP commits.`,
		RunE: runUnwip,
	}
	rootCmd.AddCommand(unwip)
}

type wipRepairPlan struct {
	Commit      string
	Subject     string
	Descendants int
}

func newWIPRepairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair [commit]",
		Short: "Rewrite one buried, unpushed WIP commit as AI-generated commits",
		Long: `Finds a WIP commit on the current branch's first-parent line, rebuilds
only that WIP diff as semantic commits through gk commit's AI path, then replays
its later linear commits. The target and every descendant must be local-only;
merge descendants are refused because replaying them needs rebase-merges.

Without --yes this prints the exact rewrite plan. Always inspect that plan
first. The operation creates a backup ref before moving the branch.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runWIPRepair,
	}
	cmd.Flags().BoolP("yes", "y", false, "perform the history rewrite after showing the plan")
	cmd.Flags().String("provider", "", "AI provider passed to the temporary gk commit run")
	cmd.Flags().String("model", "", "AI model passed to the temporary gk commit run")
	cmd.Flags().String("lang", "", "AI output language passed to the temporary gk commit run")
	return cmd
}

func runWIPRepair(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}
	patterns, err := aicommit.CompileWIPPatterns(nil)
	if err != nil {
		return err
	}
	target := ""
	if len(args) == 1 {
		target = args[0]
	}
	plan, err := planBuriedWIPRepair(ctx, runner, patterns, target)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wip repair plan: %s %s\n", shortSHA(plan.Commit), plan.Subject)
	fmt.Fprintf(cmd.OutOrStdout(), "  AI rebuilds this WIP, then replays %d later local commit(s)\n", plan.Descendants)
	if DryRun() {
		return nil
	}
	if dirty, err := git.NewClient(runner).IsDirty(ctx); err != nil {
		return fmt.Errorf("wip repair: inspect working tree: %w", err)
	} else if dirty {
		return WithHint(fmt.Errorf("wip repair: working tree must be clean"), "commit, stash, or restore current changes before rewriting history")
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes {
		return WithHint(fmt.Errorf("wip repair: history rewrite needs confirmation"), "review with --dry-run, then rerun with `gk wip repair --yes`")
	}
	return applyWIPRepair(ctx, cmd, runner, plan)
}

func planBuriedWIPRepair(ctx context.Context, runner git.Runner, patterns []*regexp.Regexp, requested string) (wipRepairPlan, error) {
	out, stderr, err := runner.Run(ctx, "log", "--first-parent", "--format=%H%x00%s", "HEAD")
	if err != nil {
		return wipRepairPlan{}, fmt.Errorf("wip repair: list first-parent history: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	var candidate wipRepairPlan
	headWIP := ""
	for depth, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		parts := strings.SplitN(line, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		sha, subject := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if requested != "" && !strings.HasPrefix(sha, requested) {
			continue
		}
		if !aicommit.IsWIPSubject(subject, patterns) {
			continue
		}
		if depth == 0 {
			headWIP = sha
			if requested != "" {
				return wipRepairPlan{}, WithHint(
					fmt.Errorf("wip repair: %s is the current HEAD WIP", shortSHA(sha)),
					"use `gk commit -f` to unwrap the HEAD WIP chain",
				)
			}
			// HEAD WIP is already handled by the ordinary commit path. Repair
			// exists for WIPs that normal commits can no longer reach.
			continue
		}
		candidate = wipRepairPlan{Commit: sha, Subject: subject}
		break
	}
	if candidate.Commit == "" {
		if headWIP != "" {
			return wipRepairPlan{}, WithHint(
				fmt.Errorf("wip repair: no buried WIP commit found; %s is a WIP at HEAD", shortSHA(headWIP)),
				"use `gk commit -f` to unwrap the HEAD WIP chain",
			)
		}
		if requested != "" {
			return wipRepairPlan{}, fmt.Errorf("wip repair: %q is not a WIP commit on HEAD's first-parent history", requested)
		}
		return wipRepairPlan{}, fmt.Errorf("wip repair: no buried WIP commit found on the current branch")
	}
	remote, _, err := runner.Run(ctx, "branch", "-r", "--contains", candidate.Commit)
	if err != nil {
		return wipRepairPlan{}, fmt.Errorf("wip repair: inspect remote containment: %w", err)
	}
	if strings.TrimSpace(string(remote)) != "" {
		return wipRepairPlan{}, WithHint(fmt.Errorf("wip repair: %s is already on a remote", shortSHA(candidate.Commit)), "refusing to rewrite pushed history")
	}
	merges, _, err := runner.Run(ctx, "rev-list", "--merges", candidate.Commit+"..HEAD")
	if err != nil {
		return wipRepairPlan{}, fmt.Errorf("wip repair: inspect descendant merges: %w", err)
	}
	if strings.TrimSpace(string(merges)) != "" {
		return wipRepairPlan{}, WithHint(fmt.Errorf("wip repair: later history crosses a merge commit"), "repair this WIP manually; automatic merge replay is intentionally unsupported")
	}
	count, _, err := runner.Run(ctx, "rev-list", "--count", candidate.Commit+"..HEAD")
	if err != nil {
		return wipRepairPlan{}, fmt.Errorf("wip repair: count descendants: %w", err)
	}
	_, _ = fmt.Sscanf(strings.TrimSpace(string(count)), "%d", &candidate.Descendants)
	return candidate, nil
}

func applyWIPRepair(ctx context.Context, cmd *cobra.Command, runner *git.ExecRunner, plan wipRepairPlan) error {
	backup, err := aicommit.EnsureBackupRef(ctx, runner)
	if err != nil {
		return fmt.Errorf("wip repair: create backup ref: %w", err)
	}
	tmp, err := os.MkdirTemp("", "gk-wip-repair-*")
	if err != nil {
		return fmt.Errorf("wip repair: create temporary worktree: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if _, stderr, err := runner.Run(ctx, "worktree", "add", "--detach", tmp, plan.Commit); err != nil {
		return fmt.Errorf("wip repair: create temporary worktree: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	defer func() { _, _, _ = runner.Run(context.Background(), "worktree", "remove", "--force", tmp) }()
	tempRunner := &git.ExecRunner{Dir: tmp}
	if _, stderr, err := tempRunner.Run(ctx, "reset", "--mixed", plan.Commit+"^"); err != nil {
		return fmt.Errorf("wip repair: expose WIP diff: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("wip repair: locate gk binary: %w", err)
	}
	argv := []string{"--repo", tmp, "commit", "-f", "--no-wip-unwrap"}
	for _, name := range []string{"provider", "model", "lang"} {
		if value, _ := cmd.Flags().GetString(name); value != "" {
			argv = append(argv, "--"+name, value)
		}
	}
	child := exec.CommandContext(ctx, self, argv...)
	child.Dir = tmp
	child.Stdin = cmd.InOrStdin()
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	if err := child.Run(); err != nil {
		return fmt.Errorf("wip repair: AI recommit failed; current branch is unchanged: %w", err)
	}
	replacement, _, err := tempRunner.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("wip repair: read replacement tip: %w", err)
	}
	// The child commit writes its own backup in the shared repository. Write
	// the original tip again so `gk commit --abort` still restores this repair.
	if _, err := aicommit.EnsureBackupRef(ctx, runner); err != nil {
		return fmt.Errorf("wip repair: refresh backup ref: %w", err)
	}
	if _, stderr, err := runner.Run(ctx, "rebase", "--onto", strings.TrimSpace(string(replacement)), plan.Commit); err != nil {
		hint := "restore point: " + backup
		if backup == "" {
			hint = "the original branch tip is unchanged in the backup ref created before repair"
		}
		return WithHint(fmt.Errorf("wip repair: replay later commits: %s: %w", strings.TrimSpace(string(stderr)), err), hint)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", successLine("wip repaired", fmt.Sprintf("%s → semantic commit(s), replayed %d later commit(s)", shortSHA(plan.Commit), plan.Descendants)))
	if backup != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  backup: %s\n", backup)
	}
	return nil
}

func runWip(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	return createWipCommit(cmd.Context(), runner, cmd.OutOrStdout())
}

func createWipCommit(ctx context.Context, runner git.Runner, w io.Writer) error {
	if _, stderr, err := runner.Run(ctx, "add", "-A"); err != nil {
		return fmt.Errorf("git add -A: %s: %w", strings.TrimSpace(string(stderr)), err)
	}

	// Nothing to commit? Report cleanly so the WIP commit doesn't fail.
	clean, err := stagingIsEmpty(ctx, runner)
	if err != nil {
		return err
	}
	if clean {
		fmt.Fprintln(w, "nothing to wip — working tree is clean")
		return nil
	}

	_, stderr, err := runner.Run(ctx,
		"commit", "--no-verify", "--no-gpg-sign", "-m", wipMarker)
	if err != nil {
		return fmt.Errorf("git commit: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintln(w, successLine("wip commit created", ""))
	fmt.Fprintln(w, stylizeHintLine("hint: gk unwip   # restore working tree"))
	return nil
}

func runUnwip(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	subj, _, err := runner.Run(ctx, "log", "-1", "--format=%s")
	if err != nil {
		return fmt.Errorf("git log: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(subj)), "--wip--") {
		return WithHint(fmt.Errorf("HEAD is not a wip commit"),
			"inspect with: git log -1")
	}

	if _, stderr, err := runner.Run(ctx, "reset", "HEAD~1"); err != nil {
		return fmt.Errorf("git reset HEAD~1: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintln(w, successLine("wip commit undone", ""))
	fmt.Fprintln(w, cellFaint("  changes returned to working tree"))
	return nil
}

// stagingIsEmpty reports whether `git diff --cached` has no changes.
func stagingIsEmpty(ctx context.Context, r git.Runner) (bool, error) {
	stdout, _, err := r.Run(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return false, fmt.Errorf("git diff --cached: %w", err)
	}
	return strings.TrimSpace(string(stdout)) == "", nil
}
