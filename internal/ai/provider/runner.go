package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ExecHook, when non-nil, is called after every ExecRunner.Run with the
// command, duration, and final error. It is intended for debug logging
// from higher layers (cli/root.go wires it to Dbg) without forcing the
// provider package to depend on the cli package. stdin is not passed —
// it can contain the user's diff and must never leak through debug
// logs by default.
var ExecHook func(name string, args []string, dur time.Duration, err error)

// CommandRunner executes an external program and returns its captured
// stdout / stderr. Adapters use this instead of exec.CommandContext so
// tests can inject canned responses without a real binary.
type CommandRunner interface {
	// Run executes `name <args...>` with optional stdin payload, the
	// supplied env (merged onto os.Environ when len>0), and a bounded
	// context. Implementations must return ExitError on non-zero exits
	// so callers can branch on exit-code specifics.
	Run(ctx context.Context, name string, args []string, stdin []byte, env []string) (stdout, stderr []byte, err error)
}

// ExecRunner is the default CommandRunner — invokes real processes.
type ExecRunner struct{}

// Run implements CommandRunner.
func (ExecRunner) Run(ctx context.Context, name string, args []string, stdin []byte, env []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)
	stdout := outBuf.Bytes()
	stderr := errBuf.Bytes()

	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			wrapped := &ExecError{
				Code:   exit.ExitCode(),
				Name:   name,
				Args:   args,
				Stderr: string(stderr),
			}
			if ExecHook != nil {
				ExecHook(name, args, dur, wrapped)
			}
			return stdout, stderr, wrapped
		}
		// ctx cancelled or binary missing → return raw err
		if ExecHook != nil {
			ExecHook(name, args, dur, err)
		}
		return stdout, stderr, err
	}
	if ExecHook != nil {
		ExecHook(name, args, dur, nil)
	}
	return stdout, stderr, nil
}

// ExecError wraps a non-zero exit of an external CLI.
type ExecError struct {
	Code   int
	Name   string
	Args   []string
	Stderr string
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("%s: exit %d: %s", e.Name, e.Code, e.Stderr)
}

// FakeCommandRunner is a test double. Calls are recorded in order; the
// next Response in the list is returned per invocation. When Responses
// is exhausted the runner falls back to DefaultResponse.
type FakeCommandRunner struct {
	Responses       []FakeCommandResponse
	DefaultResponse FakeCommandResponse

	Calls []FakeCommandCall
	idx   int
}

// FakeCommandResponse is one canned stdout/stderr/error tuple.
type FakeCommandResponse struct {
	Stdout []byte
	Stderr []byte
	Err    error
}

// FakeCommandCall records one invocation's arguments and stdin payload.
type FakeCommandCall struct {
	Name  string
	Args  []string
	Stdin []byte
	Env   []string
}

// Run implements CommandRunner.
func (f *FakeCommandRunner) Run(_ context.Context, name string, args []string, stdin []byte, env []string) ([]byte, []byte, error) {
	call := FakeCommandCall{
		Name:  name,
		Args:  append([]string(nil), args...),
		Stdin: append([]byte(nil), stdin...),
		Env:   append([]string(nil), env...),
	}
	f.Calls = append(f.Calls, call)

	resp := f.DefaultResponse
	if f.idx < len(f.Responses) {
		resp = f.Responses[f.idx]
		f.idx++
	}
	return resp.Stdout, resp.Stderr, resp.Err
}

var (
	_ CommandRunner = ExecRunner{}
	_ CommandRunner = (*FakeCommandRunner)(nil)
)
