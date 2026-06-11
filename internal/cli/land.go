package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/branchclean"
	"github.com/x-mesh/gk/internal/branchparent"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// gk land is the session-closing compound verb: everything an agent (or
// human) runs at the end of a work session — commit what's dirty, sync with
// upstream and base, push — as one transaction with per-step ✓ output. It
// stops at the first failure and names the failed step plus the exact way
// back, so "wrap up my session" is one turn instead of four.
//
// Steps run as child gk processes (the proven `pull --ai` pattern) rather
// than in-process calls: each child gets real flag parsing and the same
// terminal, and land itself stays a thin orchestrator. GK_AGENT is stripped
// from child environments — land composes its own result contract.

// landPromoteUseBase is the sentinel NoOptDefVal for a bare `--promote`
// (no value): resolve the target to the configured base branch.
const landPromoteUseBase = "\x00base"

type landStepRun struct {
	Name string `json:"name"`
	// Result: ok | failed | skipped
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// landResultJSON is the machine-readable outcome of `gk land --json` /
// agent mode. Fields are append-only.
type landResultJSON struct {
	Schema     int           `json:"schema"`
	Result     string        `json:"result"` // landed | failed | dry-run
	Steps      []landStepRun `json:"steps"`
	FailedStep string        `json:"failed_step,omitempty"`
	Resume     string        `json:"resume,omitempty"`
}

func init() {
	cmd := &cobra.Command{
		Use:   "land",
		Short: "Wrap up the session: commit, pull --with-base, push — one command",
		Long: `Runs the session-closing sequence as one transaction:

  1. commit   gk commit -f          (skipped when the tree is clean)
  2. pull     gk pull --with-base   (sync upstream and fast-forward the base)
  3. push     gk push               (secret scan included)
  4. promote  merge --into <base> + push --from <base>  (only with --promote)
  5. cleanup  merged-branch + worktree reclaim          (only with --cleanup)

--promote forward-merges the current branch into its base and pushes it
(the manual gk merge --into <base> + gk push --from <base> pair) as a final
step. Bare --promote climbs ONE hop: the branch's parent when one is set
(branch.<name>.gk-parent — the same resolution gk status uses for its
"ready to merge into" line), else the configured base. --promote=<branch>
overrides the target explicitly. A merge conflict pauses with gk's normal
resolve/continue contract and land reports the promote step as failed with
the resume path.

Each step prints a ✓ on success; the first failure stops the run and names
the failed step with the exact resume path. Re-running gk land after fixing
the failure is safe — completed steps degrade to no-ops (clean tree skips
commit, an up-to-date branch pulls and pushes nothing).

With the global --json flag (or GK_AGENT=1) the result is a machine
contract: {steps:[{name,result}], failed_step?, resume?}; step progress
moves to stderr so stdout stays parseable.`,
		Args: cobra.NoArgs,
		RunE: runLand,
	}
	cmd.Flags().Bool("with-base", true, "fast-forward the local base branch during the pull step (--with-base=false to skip)")
	cmd.Flags().Bool("cleanup", false, "after pushing, delete fully-merged branches and reclaim their worktrees")
	cmd.Flags().String("promote", "", "after pushing, merge the current branch into its parent/base branch and push it (bare --promote = gk-parent, else base; --promote=<branch> overrides)")
	// A bare `--promote` (no value) resolves to the configured base branch.
	cmd.Flags().Lookup("promote").NoOptDefVal = landPromoteUseBase
	rootCmd.AddCommand(cmd)
}

func runLand(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}
	cfg, _ := config.Load(cmd.Flags())

	withBase, _ := cmd.Flags().GetBool("with-base")
	cleanup, _ := cmd.Flags().GetBool("cleanup")
	promote, _ := cmd.Flags().GetString("promote")
	jsonMode := JSONOut()

	dirty, err := landTreeDirty(ctx, runner)
	if err != nil {
		return err
	}

	// Resolve the promote target: a bare --promote (sentinel) means the
	// branch's parent/base; --promote=<branch> overrides; empty means the
	// step is off. The current branch is resolved first because the bare
	// path needs it for parent lookup (and the skip check below uses it).
	promoteTarget := ""
	currentBranch := ""
	if promote != "" {
		client := git.NewClient(runner)
		cb, err := client.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("land: resolve current branch for promote: %w", err)
		}
		currentBranch = strings.TrimSpace(cb)
		if promote == landPromoteUseBase {
			// Parent-aware, one hop: a bare --promote lands on the branch's
			// direct parent when gk-parent metadata resolves — the same
			// resolution gk status uses for its "ready to merge into <base>"
			// line, so both surfaces always name the same target. Without
			// parent metadata this degrades to the trunk resolver (explicit
			// config → origin/HEAD → local fallback), the pre-parent
			// behavior. In a main→develop→feat stack, landing feat promotes
			// to develop, not main — climbing further is a deliberate second
			// land (chain promotion is the planned Phase 2).
			cfgBase := resolveBaseForStatus(ctx, runner, client, cfg).Resolved
			base, _, issues := branchparent.NewResolver(client).ResolveBaseWithIssues(ctx, currentBranch, cfgBase)
			for _, iss := range issues {
				fmt.Fprintln(cmd.ErrOrStderr(), iss.Message)
			}
			promoteTarget = base
		} else {
			promoteTarget = promote
		}
		if promoteTarget == "" {
			return fmt.Errorf("land: --promote could not resolve a base branch — pass --promote=<branch> or set base_branch")
		}
	}

	type landStep struct {
		name   string
		skip   string // non-empty → skipped with this reason
		args   []string
		run    func(context.Context) error
		plan   string // dry-run description (defaults to "gk <args>")
		resume string // shown when the step fails
	}

	// Pass the flag in BOTH polarities: the child pull reads pull.with_base
	// from config, so omitting the flag would let config re-enable what
	// `gk land --with-base=false` promised to skip.
	pullArgs := []string{"pull", fmt.Sprintf("--with-base=%t", withBase)}
	steps := []landStep{
		{
			name: "commit", args: []string{"commit", "-f"},
			skip:   landSkipWhen(!dirty, "clean tree"),
			resume: "fix the commit (gk commit), then rerun: gk land",
		},
		{
			name: "pull", args: pullArgs,
			resume: "on conflict: gk resolve --ai && gk continue, then rerun: gk land",
		},
		{
			name: "push", args: []string{"push"},
			resume: "fix the push (gk push), then rerun: gk land",
		},
	}
	if promoteTarget != "" {
		steps = append(steps, landStep{
			name: "promote",
			skip: landSkipWhen(currentBranch == promoteTarget, "already on "+promoteTarget),
			run: func(c context.Context) error {
				gkPath, err := os.Executable()
				if err != nil {
					return fmt.Errorf("locate gk binary: %w", err)
				}
				return landPromote(c, gkPath, repo, jsonMode, promoteTarget)
			},
			plan:   fmt.Sprintf("merge --into %s + push --from %s", promoteTarget, promoteTarget),
			resume: "resolve the promote conflict (gk resolve --ai && gk continue), then rerun: gk land --promote",
		})
	}
	if cleanup {
		steps = append(steps, landStep{
			name:   "cleanup",
			run:    func(c context.Context) error { return runLandCleanup(c, cmd, runner, cfg) },
			plan:   "branch clean --worktrees (merged only)",
			resume: "retry the reclaim: gk branch clean --worktrees",
		})
	}

	// Progress goes to stderr in JSON mode so stdout carries only the
	// result document; in human mode everything shares stdout like ship.
	progress := cmd.OutOrStdout()
	if jsonMode {
		progress = cmd.ErrOrStderr()
	}

	if DryRun() {
		res := landResultJSON{Schema: 1, Result: "dry-run"}
		fmt.Fprintln(progress, landHeader("─── Land plan ────────────────────────────────"))
		for _, s := range steps {
			detail := s.plan
			if detail == "" {
				detail = strings.Join(append([]string{"gk"}, s.args...), " ")
			}
			state := "run"
			if s.skip != "" {
				state, detail = "skip", s.skip
			}
			fmt.Fprintf(progress, "  %-8s %-5s %s\n", s.name, state, detail)
			res.Steps = append(res.Steps, landStepRun{Name: s.name, Result: "dry-run", Detail: detail})
		}
		if jsonMode {
			return emitAgentResult(cmd.OutOrStdout(), res)
		}
		return nil
	}

	gkPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("land: locate gk binary: %w", err)
	}

	good := color.New(color.FgGreen, color.Bold).SprintFunc()
	res := landResultJSON{Schema: 1, Result: "landed"}
	for _, s := range steps {
		if s.skip != "" {
			fmt.Fprintf(progress, "  %s %-8s %s\n", cellFaint("·"), s.name, cellFaint("skipped — "+s.skip))
			res.Steps = append(res.Steps, landStepRun{Name: s.name, Result: "skipped", Detail: s.skip})
			continue
		}
		fmt.Fprintln(progress, landHeader("─── land: "+s.name+" ─────────────────────────────"))
		var stepErr error
		if s.run != nil {
			stepErr = s.run(ctx)
		} else {
			stepErr = landRunChild(ctx, gkPath, repo, jsonMode, s.args...)
		}
		if stepErr != nil {
			res.Result = "failed"
			res.FailedStep = s.name
			res.Resume = selfRewrite(s.resume)
			res.Steps = append(res.Steps, landStepRun{Name: s.name, Result: "failed", Detail: stepErr.Error()})
			if jsonMode {
				_ = emitAgentResult(cmd.OutOrStdout(), res)
			}
			return WithRemedy(
				fmt.Errorf("land: step %q failed: %w", s.name, stepErr),
				s.resume,
				errRemedy{Command: "gk land", Safety: "safe"},
			)
		}
		fmt.Fprintf(progress, "  %s %-8s\n", good("✓"), s.name)
		res.Steps = append(res.Steps, landStepRun{Name: s.name, Result: "ok"})
	}

	fmt.Fprintln(progress, landHeader("─── Land complete ────────────────────────────"))
	fmt.Fprintf(progress, "  %s session landed\n", good("✓"))
	if jsonMode {
		return emitAgentResult(cmd.OutOrStdout(), res)
	}
	return nil
}

func landHeader(s string) string {
	return color.New(color.FgCyan, color.Bold).Sprint(s)
}

func landSkipWhen(cond bool, reason string) string {
	if cond {
		return reason
	}
	return ""
}

// landTreeDirty reports whether anything (staged, unstaged, or untracked)
// would feed a commit.
func landTreeDirty(ctx context.Context, runner git.Runner) (bool, error) {
	out, stderr, err := runner.Run(ctx, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("land: git status: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// landRunChild is swapped by tests to fake child executions without
// spawning the test binary as gk.
var landRunChild = runLandChild

// runLandChild executes one step as a child gk process. The child inherits
// the terminal (prompts and color keep working); in JSON mode its stdout is
// rerouted to stderr so land's stdout carries only the result document.
// GK_AGENT is stripped so children print human progress, not envelopes —
// land owns the machine contract.
func runLandChild(ctx context.Context, gkPath, repo string, jsonMode bool, args ...string) error {
	c := exec.CommandContext(ctx, gkPath, args...)
	if repo != "" {
		c.Dir = repo
	}
	c.Stdin = os.Stdin
	c.Stderr = os.Stderr
	if jsonMode {
		c.Stdout = os.Stderr
	} else {
		c.Stdout = os.Stdout
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GK_AGENT=") {
			continue
		}
		env = append(env, kv)
	}
	c.Env = env
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("exit %d", ee.ExitCode())
		}
		return err
	}
	return nil
}

// landPromote forward-merges the current branch into the base branch and
// publishes it — the manual `gk merge --into <base>` + `gk push --from <base>`
// pair run as one land step. Both execute as child gk processes so a merge
// conflict pauses with gk's normal resolve/continue contract; land then
// reports the promote step as failed with the resume path. --no-ai keeps the
// merge non-interactive (no plan summary) to match land's transaction flow.
func landPromote(ctx context.Context, gkPath, repo string, jsonMode bool, base string) error {
	if err := landRunChild(ctx, gkPath, repo, jsonMode, "merge", "--into", base, "--no-ai"); err != nil {
		return fmt.Errorf("merge --into %s: %w", base, err)
	}
	if err := landRunChild(ctx, gkPath, repo, jsonMode, "push", "--from", base); err != nil {
		return fmt.Errorf("push --from %s: %w", base, err)
	}
	return nil
}

// runLandCleanup reclaims fully-merged branches (and the worktrees holding
// them) after the push — the safe subset of `gk branch clean`: merged-only,
// no AI, protected branches excluded.
func runLandCleanup(ctx context.Context, cmd *cobra.Command, runner *git.ExecRunner, cfg *config.Config) error {
	cleaner := &branchclean.Cleaner{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: cmd.ErrOrStderr(),
		Stdout: cmd.ErrOrStderr(),
	}
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	result, err := cleaner.Run(ctx, branchclean.CleanOptions{
		Yes:        true,
		NoAI:       true,
		Worktrees:  true,
		RemoteName: remote,
		BaseBranch: cfg.BaseBranch,
		Protected:  cfg.Branch.Protected,
	})
	if err != nil {
		return err
	}
	for _, name := range result.Deleted {
		fmt.Fprintln(cmd.ErrOrStderr(), successLine("reclaimed", name))
	}
	if len(result.Failed) > 0 {
		names := make([]string, 0, len(result.Failed))
		for n := range result.Failed {
			names = append(names, n)
		}
		return fmt.Errorf("cleanup could not delete: %s", strings.Join(names, ", "))
	}
	return nil
}
