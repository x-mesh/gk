package git

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// ExecHook, when non-nil, is invoked after every ExecRunner.Run with
// the git args, wall-clock duration, and final error. Intended for
// debug logging from higher layers (cli/root.go wires it to Dbg) so
// the git package does not import cli. Captured args start at the
// first non-boilerplate token — the internal `-c core.quotepath=false`
// guard is elided so the log reads like the git command the user
// actually asked for.
var ExecHook func(args []string, dur time.Duration, err error)

// ExecRunner implements Runner by invoking the system git binary.
type ExecRunner struct {
	// Dir is the working directory for git commands.
	// Empty string means the current working directory.
	Dir string

	// ExtraEnv are additional environment variables appended after the forced
	// guard variables. These can override the guards if needed.
	ExtraEnv []string
}

// guardEnv are injected on every call to ensure deterministic, non-interactive output.
var guardEnv = []string{
	"LC_ALL=C",
	"LANG=C",
	"GIT_OPTIONAL_LOCKS=0",
	"GIT_TERMINAL_PROMPT=0",
}

// buildCmd constructs the exec.Cmd for the given args without starting it.
// Exported for testing environment variable injection.
func (r *ExecRunner) buildCmd(ctx context.Context, args ...string) *exec.Cmd {
	full := append([]string{"-c", "core.quotepath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = r.Dir
	cmd.Env = append(guardEnv, r.ExtraEnv...)
	return cmd
}

// Run executes `git <args...>` and returns captured stdout and stderr.
// Non-zero exit codes produce an *ExitError; stdout/stderr are still returned.
func (r *ExecRunner) Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	cmd := r.buildCmd(ctx, args...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start)
	stdout = outBuf.Bytes()
	stderr = errBuf.Bytes()

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			wrapped := &ExitError{
				Code:   exitErr.ExitCode(),
				Args:   args,
				Stderr: string(stderr),
			}
			if ExecHook != nil {
				ExecHook(args, dur, wrapped)
			}
			return stdout, stderr, wrapped
		}
		if ExecHook != nil {
			ExecHook(args, dur, runErr)
		}
		return stdout, stderr, runErr
	}

	if ExecHook != nil {
		ExecHook(args, dur, nil)
	}
	return stdout, stderr, nil
}
