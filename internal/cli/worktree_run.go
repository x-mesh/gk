package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// worktreeRunJSON is the result contract for `gk worktree run`: which
// worktree the command ran in, whether this call created it, the command and
// its exit code, and whether --cleanup reclaimed the worktree afterward.
// Fields are append-only.
type worktreeRunJSON struct {
	Path     string   `json:"path"`
	Branch   string   `json:"branch,omitempty"`
	Created  bool     `json:"created"`
	Init     string   `json:"init,omitempty"`
	Command  []string `json:"command"`
	ExitCode int      `json:"exit_code"`
	Removed  bool     `json:"removed"`
}

// newWorktreeRunCmd builds the `worktree run` subcommand — the CLI form of an
// isolated, parallel task: stand up a worktree, run a command in it, and
// optionally tear it down. It is the single-shot sibling of the Workflow
// worktree-isolation pattern.
func newWorktreeRunCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "run <branch> -- <command> [args...]",
		Short: "Create (or reuse) a worktree, run a command in it, optionally reclaim it",
		Long: `Runs a command inside a worktree for <branch> — the CLI form of an
isolated, parallel task.

If a worktree is already checked out on <branch>, it is reused; otherwise gk
creates one (managed-base layout, gk-parent recorded) before running. Pass
--init to run worktree.init before the command, including reused worktrees.
The command runs with the worktree as its working directory, and gk exits with
the command's exit code.

With --cleanup, a command that succeeds (exit 0) has its worktree removed —
and, when this call created the branch, the branch deleted too. A failing
command always leaves the worktree in place for inspection.

Everything after '--' is the command, run directly (not through a shell), so
chain with an explicit shell when you need operators:

Examples:
  gk worktree run feat/api -- go test ./...
  gk worktree run hotfix -- make build                  # new branch+worktree off HEAD
  gk worktree run feat/api --cleanup -- sh -c 'npm ci && npm test'
`,
		Args: cobra.MinimumNArgs(1),
		RunE: runWorktreeRun,
	}
	c.Flags().String("from", "", "base ref when creating a new branch (default: HEAD)")
	c.Flags().Bool("cleanup", false, "remove the worktree when the command succeeds (and delete the branch if this call created it)")
	c.Flags().Bool("init", false, "run worktree init before the command, including reused worktrees")
	c.Flags().Bool("no-init", false, "skip worktree init")
	return c
}

func runWorktreeRun(cmd *cobra.Command, args []string) error {
	// cobra records the position of '--' so we can split the worktree spec
	// from the command without it being ambiguous with the command's own
	// flags.
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		return WithHint(
			fmt.Errorf("worktree run: missing '--' before the command"),
			"usage: gk worktree run <branch> -- <command> [args...]",
		)
	}
	spec := args[:dash]
	command := args[dash:]
	if len(spec) != 1 {
		return fmt.Errorf("worktree run: exactly one branch (or worktree name) is required before '--'")
	}
	if len(command) == 0 {
		return fmt.Errorf("worktree run: a command is required after '--'")
	}
	ref := spec[0]

	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	cfg, _ := config.Load(cmd.Flags())
	w := cmd.OutOrStdout()
	jsonMode := JSONOut()

	wtPath, created, createdBranch, initStatus, err := ensureRunWorktree(ctx, cmd, runner, cfg, ref, jsonMode, w)
	if err != nil {
		return err
	}

	exitCode, runErr := runInWorktree(ctx, wtPath, command, jsonMode)
	if runErr != nil {
		// The command could not be started at all (e.g. not on PATH). There
		// is nothing to clean up by policy — surface it as a plain error.
		return fmt.Errorf("worktree run: %w", runErr)
	}

	removed := false
	if cleanup, _ := cmd.Flags().GetBool("cleanup"); cleanup && exitCode == 0 {
		if rerr := reclaimRunWorktree(ctx, runner, wtPath, ref, createdBranch); rerr == nil {
			removed = true
		} else if !jsonMode {
			fmt.Fprintf(cmd.ErrOrStderr(), "worktree run: cleanup skipped: %v\n", rerr)
		}
	}

	res := worktreeRunJSON{
		Path: wtPath, Branch: ref, Created: created,
		Init: initStatus, Command: command, ExitCode: exitCode, Removed: removed,
	}
	if jsonMode {
		_ = emitAgentResult(w, res)
	} else {
		summary := fmt.Sprintf("worktree run: exit %d in %s", exitCode, wtPath)
		if removed {
			summary += " (worktree reclaimed)"
		}
		fmt.Fprintln(w, summary)
	}

	// Mirror the command's exit code as gk's own — the way precheck/pull
	// propagate a child's code — so a non-zero command fails the gk call.
	// runErr already handled the could-not-start case, so exitCode >= 0 here.
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

// ensureRunWorktree returns the worktree path for ref, creating one when none
// is checked out. It reports whether this call created the worktree and
// whether it also created the branch, so --cleanup knows whether to delete
// the branch. Creation mirrors `gk worktree add`: managed-base layout,
// gk-parent metadata, and worktree.init bootstrap.
func ensureRunWorktree(ctx context.Context, cmd *cobra.Command, runner *git.ExecRunner, cfg *config.Config, ref string, jsonMode bool, w io.Writer) (path string, created, createdBranch bool, initStatus string, err error) {
	doInit, _ := cmd.Flags().GetBool("init")
	noInit, _ := cmd.Flags().GetBool("no-init")

	if entry, ferr := findWorktreeForBranch(ctx, runner, ref); ferr == nil && entry != nil {
		initStatus, err := bootstrapWorktreeForAgent(ctx, w, runner, cfg, entry.Path, jsonMode, doInit, noInit)
		return entry.Path, false, false, initStatus, err
	}

	resolved, rerr := resolveWorktreePath(ctx, runner, cfg, ref)
	if rerr != nil {
		return "", false, false, "", rerr
	}
	if resolved != ref {
		if mkerr := os.MkdirAll(filepath.Dir(resolved), 0o755); mkerr != nil {
			return "", false, false, "", fmt.Errorf("ensure worktree base: %w", mkerr)
		}
	}

	newBranch := !branchExists(ctx, runner, ref)
	from, _ := cmd.Flags().GetString("from")
	gitArgs := []string{"worktree", "add"}
	if newBranch {
		gitArgs = append(gitArgs, "-b", ref, resolved)
		if from != "" {
			gitArgs = append(gitArgs, from)
		}
	} else {
		gitArgs = append(gitArgs, resolved, ref)
	}
	if _, stderr, aerr := runner.Run(ctx, gitArgs...); aerr != nil {
		return "", false, false, "", fmt.Errorf("worktree run: create: %s: %w", strings.TrimSpace(string(stderr)), aerr)
	}
	if newBranch {
		recordWorktreeParent(ctx, runner, ref, from)
	}

	initStatus, err = bootstrapWorktreeForAgent(ctx, w, runner, cfg, resolved, jsonMode, doInit, noInit)
	if err != nil {
		return "", false, false, "", err
	}
	return resolved, true, newBranch, initStatus, nil
}

func bootstrapWorktreeForAgent(ctx context.Context, w io.Writer, runner *git.ExecRunner, cfg *config.Config, path string, jsonMode, doInit, noInit bool) (string, error) {
	if noInit {
		return "skipped", nil
	}
	if !doInit {
		return "skipped", nil
	}
	initW := w
	if jsonMode {
		initW = io.Discard
	}
	if err := bootstrapWorktree(ctx, initW, runner, cfg, path, worktreeInitOpts{
		explicitInit: true,
		prompt:       false,
		fromAdd:      true,
	}); err != nil {
		return "", err
	}
	return "done", nil
}

// runInWorktree executes command with the worktree as its working directory,
// inheriting stdin/stderr. In JSON mode the command's stdout is redirected to
// stderr so gk's stdout carries only the result envelope. Returns the exit
// code; a non-ExitError (command not found) is returned as err so the caller
// can distinguish "ran and failed" from "never started".
func runInWorktree(ctx context.Context, dir string, command []string, jsonMode bool) (int, error) {
	c := exec.CommandContext(ctx, command[0], command[1:]...)
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stderr = os.Stderr
	if jsonMode {
		c.Stdout = os.Stderr
	} else {
		c.Stdout = os.Stdout
	}
	c.Env = os.Environ()
	if rerr := c.Run(); rerr != nil {
		var ee *exec.ExitError
		if errors.As(rerr, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, rerr
	}
	return 0, nil
}

// reclaimRunWorktree removes the worktree and, when this call created the
// branch, deletes it too. Used by --cleanup after a successful command. A
// failed remove is returned; a lingering branch after a successful remove is
// swallowed (best-effort) since the worktree — the expensive artifact — is
// already gone.
func reclaimRunWorktree(ctx context.Context, runner *git.ExecRunner, path, branch string, createdBranch bool) error {
	if _, stderr, err := runner.Run(ctx, "worktree", "remove", "--force", path); err != nil {
		return errors.New(strings.TrimSpace(string(stderr)))
	}
	if createdBranch {
		// SelfCreated: createdBranch means this same run made the branch,
		// so no protected-name coincidence can make it worth keeping.
		_ = deleteBranchGuarded(ctx, runner, nil, branch,
			branchDeleteOpts{Force: true, SelfCreated: true})
	}
	return nil
}
