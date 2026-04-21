package git

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestExecRunner_GitVersion calls `git --version` and expects success.
func TestExecRunner_GitVersion(t *testing.T) {
	r := &ExecRunner{}
	stdout, _, err := r.Run(context.Background(), "--version")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(string(stdout), "git version") {
		t.Errorf("unexpected stdout: %q", string(stdout))
	}
}

// TestExecRunner_UnknownSubcommand verifies that an unknown git subcommand
// returns an *ExitError with a non-zero Code.
func TestExecRunner_UnknownSubcommand(t *testing.T) {
	r := &ExecRunner{}
	_, _, err := r.Run(context.Background(), "fakeopcmd")
	if err == nil {
		t.Fatal("expected an error for unknown subcommand, got nil")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *ExitError, got %T: %v", err, err)
	}
	if exitErr.Code == 0 {
		t.Errorf("expected non-zero exit code, got 0")
	}
}

// TestExecRunner_BuildCmd_EnvGuards checks that buildCmd injects the required
// guard environment variables into cmd.Env.
func TestExecRunner_BuildCmd_EnvGuards(t *testing.T) {
	r := &ExecRunner{}
	cmd := r.buildCmd(context.Background(), "--version")

	required := []string{
		"LC_ALL=C",
		"LANG=C",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	}
	for _, kv := range required {
		found := false
		for _, e := range cmd.Env {
			if e == kv {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("guard env %q not found in cmd.Env: %v", kv, cmd.Env)
		}
	}
}

// TestExecRunner_BuildCmd_QuotePath checks that core.quotepath=false is prepended.
func TestExecRunner_BuildCmd_QuotePath(t *testing.T) {
	r := &ExecRunner{}
	cmd := r.buildCmd(context.Background(), "status")
	args := cmd.Args // [git, -c, core.quotepath=false, status]
	if len(args) < 3 {
		t.Fatalf("expected at least 3 args, got %v", args)
	}
	if args[1] != "-c" || args[2] != "core.quotepath=false" {
		t.Errorf("expected -c core.quotepath=false prefix, got %v", args[1:])
	}
}

// TestExecRunner_BuildCmd_ExtraEnv verifies ExtraEnv is appended after guards.
func TestExecRunner_BuildCmd_ExtraEnv(t *testing.T) {
	r := &ExecRunner{ExtraEnv: []string{"MY_VAR=hello"}}
	cmd := r.buildCmd(context.Background(), "--version")

	last := cmd.Env[len(cmd.Env)-1]
	if last != "MY_VAR=hello" {
		t.Errorf("expected ExtraEnv appended last, got %v", cmd.Env)
	}
}

// TestExecRunner_ContextCancel verifies that cancelling the context causes Run
// to return an error promptly. We use `git hash-object --stdin` with no input
// which blocks, then cancel immediately.
func TestExecRunner_ContextCancel(t *testing.T) {
	r := &ExecRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	_, _, err := r.Run(ctx, "hash-object", "--stdin")
	if err == nil {
		t.Fatal("expected error after context cancel, got nil")
	}
}

// TestExitError_Error checks the formatted error message.
func TestExitError_Error(t *testing.T) {
	e := &ExitError{Code: 128, Args: []string{"status"}, Stderr: "not a git repo"}
	msg := e.Error()
	if !strings.Contains(msg, "128") {
		t.Errorf("error message missing exit code: %q", msg)
	}
	if !strings.Contains(msg, "status") {
		t.Errorf("error message missing args: %q", msg)
	}
	if !strings.Contains(msg, "not a git repo") {
		t.Errorf("error message missing stderr: %q", msg)
	}
}
