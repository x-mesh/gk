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
  4. cleanup  merged-branch + worktree reclaim (only with --cleanup)

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
	jsonMode := JSONOut()

	dirty, err := landTreeDirty(ctx, runner)
	if err != nil {
		return err
	}

	type landStep struct {
		name   string
		skip   string // non-empty → skipped with this reason
		args   []string
		run    func(context.Context) error
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
	if cleanup {
		steps = append(steps, landStep{
			name:   "cleanup",
			run:    func(c context.Context) error { return runLandCleanup(c, cmd, runner, cfg) },
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
			detail := strings.Join(append([]string{"gk"}, s.args...), " ")
			if s.run != nil {
				detail = "branch clean --worktrees (merged only)"
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
			res.Resume = s.resume
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
