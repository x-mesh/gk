package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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
	// Run captures stdout/stderr into buffers with no tty attached, so any
	// editor git spawns (rebase/merge/cherry-pick --continue confirm the
	// commit message through one) hangs invisibly. `true` makes git accept
	// the prepared message as-is. ExtraEnv still overrides for callers
	// that need a different editor (e.g. the rebase todo driver).
	"GIT_EDITOR=true",
}

// buildCmd constructs the exec.Cmd for the given args without starting it.
// Exported for testing environment variable injection.
//
// Env layering, in append order so later entries win for duplicate keys:
//  1. os.Environ()  — preserves HOME, USER, PATH, SSH_AUTH_SOCK, etc. so git
//     can locate ~/.gitconfig, credential helpers, and the user identity.
//  2. guardEnv      — forces deterministic locale/lock/prompt behaviour even
//     when the parent inherits something else.
//  3. r.ExtraEnv    — caller-provided overrides win over both.
func (r *ExecRunner) buildCmd(ctx context.Context, args ...string) *exec.Cmd {
	full := append([]string{"-c", "core.quotepath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = r.Dir
	env := append(os.Environ(), guardEnv...)
	cmd.Env = append(env, r.ExtraEnv...)
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
	err = wrapExit(args, cmd.Run(), errBuf.Bytes())
	if ExecHook != nil {
		ExecHook(args, time.Since(start), err)
	}
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// Stream runs `git <args...>` with stdin and writes stdout to w as git
// produces it. Errors are those of Run.
func (r *ExecRunner) Stream(ctx context.Context, stdin io.Reader, w io.Writer, args ...string) error {
	cmd := r.buildCmd(ctx, args...)
	var errBuf bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, w, &errBuf

	start := time.Now()
	err := wrapExit(args, cmd.Run(), errBuf.Bytes())
	if ExecHook != nil {
		ExecHook(args, time.Since(start), err)
	}
	return err
}

// wrapExit turns a non-zero git exit into an *ExitError.
func wrapExit(args []string, runErr error, stderr []byte) error {
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return &ExitError{Code: exitErr.ExitCode(), Args: args, Stderr: string(stderr)}
	}
	return runErr
}
