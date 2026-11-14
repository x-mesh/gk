package git

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestExecRunnerBuildCmdPreservesHome guards against a regression where
// ExecRunner overwrote the child env with only the guard variables, dropping
// HOME/USER/PATH. In container environments this caused git to fail with
// "Author identity unknown" because it could no longer locate ~/.gitconfig.
func TestExecRunnerBuildCmdPreservesHome(t *testing.T) {
	t.Setenv("HOME", "/home/test-user")
	t.Setenv("USER", "test-user")

	r := &ExecRunner{}
	cmd := r.buildCmd(context.Background(), "status")

	if !slices.Contains(cmd.Env, "HOME=/home/test-user") {
		t.Errorf("HOME missing from cmd.Env; git would fail to locate ~/.gitconfig.\nGot: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "USER=test-user") {
		t.Errorf("USER missing from cmd.Env.\nGot: %v", cmd.Env)
	}
}

// TestExecRunnerBuildCmdGuardWinsOverParent verifies that guard variables
// override an inherited LC_ALL/LANG/etc., so locale-sensitive git output
// stays deterministic even on hosts that set those vars.
func TestExecRunnerBuildCmdGuardWinsOverParent(t *testing.T) {
	t.Setenv("LC_ALL", "ko_KR.UTF-8")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")

	r := &ExecRunner{}
	cmd := r.buildCmd(context.Background(), "status")

	// Effective value is the last occurrence of a key in cmd.Env.
	got := lastValue(cmd.Env, "LC_ALL")
	if got != "C" {
		t.Errorf("LC_ALL effective value = %q, want %q (guard must override parent)", got, "C")
	}
	got = lastValue(cmd.Env, "GIT_TERMINAL_PROMPT")
	if got != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT effective value = %q, want %q", got, "0")
	}
}

// TestExecRunnerBuildCmdEditorGuard verifies the GIT_EDITOR=true guard
// beats an inherited shell editor. Without it, captured-pipe commands
// that reach an editor (rebase/merge/cherry-pick --continue) spawn an
// invisible vim and hang forever.
func TestExecRunnerBuildCmdEditorGuard(t *testing.T) {
	t.Setenv("GIT_EDITOR", "vim")

	r := &ExecRunner{}
	cmd := r.buildCmd(context.Background(), "rebase", "--continue")

	got := lastValue(cmd.Env, "GIT_EDITOR")
	if got != "true" {
		t.Errorf("GIT_EDITOR effective value = %q, want %q (guard must override parent editor)", got, "true")
	}
}

// TestExecRunnerBuildCmdExtraEnvWinsOverGuard verifies that ExtraEnv has
// the highest precedence, so callers can opt out of guards when needed.
func TestExecRunnerBuildCmdExtraEnvWinsOverGuard(t *testing.T) {
	r := &ExecRunner{ExtraEnv: []string{"LC_ALL=en_US.UTF-8"}}
	cmd := r.buildCmd(context.Background(), "status")

	got := lastValue(cmd.Env, "LC_ALL")
	if got != "en_US.UTF-8" {
		t.Errorf("LC_ALL effective value = %q, want %q (ExtraEnv must win)", got, "en_US.UTF-8")
	}
}

func lastValue(env []string, key string) string {
	prefix := key + "="
	last := ""
	for _, kv := range env {
		if len(kv) > len(prefix) && kv[:len(prefix)] == prefix {
			last = kv[len(prefix):]
		}
	}
	return last
}

// Stream feeds stdin to git and passes its stdout on; a non-zero exit is an
// *ExitError, as from Run.
func TestExecRunnerStream(t *testing.T) {
	dir := t.TempDir()
	r := &ExecRunner{Dir: dir}
	ctx := context.Background()
	if _, _, err := r.Run(ctx, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	var id bytes.Buffer
	if err := r.Stream(ctx, strings.NewReader("hello\n"), &id, "hash-object", "-w", "--stdin"); err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(id.String())
	var out bytes.Buffer
	if err := r.Stream(ctx, strings.NewReader(blob+"\n"), &out, "cat-file", "--batch"); err != nil {
		t.Fatal(err)
	}
	if want := blob + " blob 6\nhello\n\n"; out.String() != want {
		t.Errorf("cat-file --batch = %q, want %q", out.String(), want)
	}
	var exit *ExitError
	if err := r.Stream(ctx, strings.NewReader(""), &out, "cat-file", "--no-such-flag"); !errors.As(err, &exit) || exit.Code == 0 {
		t.Errorf("err = %v, want an *ExitError", err)
	}
}
