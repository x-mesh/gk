//go:build e2e

// Package e2e drives the compiled gk binary against real git repositories.
// It is gated behind the `e2e` build tag (it builds the binary, so it is too
// slow for the default unit run): `go test -tags e2e ./internal/e2e/` or
// `make test-e2e`.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/testutil"
)

var gkBin string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		// No git → nothing to drive; skip the whole suite cleanly.
		os.Exit(0)
	}
	tmp, err := os.MkdirTemp("", "gk-e2e-bin")
	if err != nil {
		panic(err)
	}
	gkBin = filepath.Join(tmp, "gk")
	build := exec.Command("go", "build", "-o", gkBin, "./cmd/gk")
	build.Dir = moduleRoot()
	if out, berr := build.CombinedOutput(); berr != nil {
		os.RemoveAll(tmp)
		panic("e2e: build gk failed: " + berr.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// moduleRoot returns the repo root (this file lives at internal/e2e/).
func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// runGk runs the built binary in dir with git isolation and returns the
// combined output and the exit error (nil on success).
func runGk(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(gkBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=gk-e2e", "GIT_AUTHOR_EMAIL=e2e@example.com",
		"GIT_COMMITTER_NAME=gk-e2e", "GIT_COMMITTER_EMAIL=e2e@example.com",
		"GK_AI_DISABLE=1", // keep e2e deterministic and offline
		"NO_COLOR=1",
		"LC_ALL=C", "LANG=C",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestE2E_Refresh exercises the session's flagship: `gk refresh` fast-forwards
// tracked branches to their remotes without leaving the current branch.
func TestE2E_Refresh(t *testing.T) {
	up := testutil.NewRepo(t)
	up.WriteFile("a.txt", "1\n")
	up.Commit("m0")
	up.RunGit("branch", "develop")

	down := testutil.NewRepo(t)
	down.AddRemote("origin", up.Dir)
	down.RunGit("fetch", "origin")
	down.SetRemoteHEAD("origin", "main")
	down.RunGit("checkout", "-B", "main", "origin/main")
	down.RunGit("checkout", "-B", "develop", "origin/develop")
	down.RunGit("checkout", "-b", "feat/x", "main")

	// Advance both upstream branches.
	up.WriteFile("a.txt", "2\n")
	up.Commit("m1")
	up.Checkout("develop")
	up.WriteFile("d.txt", "d\n")
	up.Commit("d1")
	up.Checkout("main")

	out, err := runGk(t, down.Dir, "refresh")
	if err != nil {
		t.Fatalf("gk refresh failed: %v\n%s", err, out)
	}
	for _, b := range []string{"main", "develop"} {
		local := down.RunGit("rev-parse", "refs/heads/"+b)
		remote := down.RunGit("rev-parse", "refs/remotes/origin/"+b)
		if local != remote {
			t.Errorf("%s not fast-forwarded: local %s != origin %s", b, local, remote)
		}
	}
	if cur := down.RunGit("rev-parse", "--abbrev-ref", "HEAD"); cur != "feat/x" {
		t.Errorf("current branch changed to %q, want feat/x", cur)
	}
}

// TestE2E_NextLocalFallback verifies `gk next` produces a deterministic local
// plan when AI is disabled.
func TestE2E_NextLocalFallback(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "1\n")
	r.Commit("init")
	r.WriteFile("a.txt", "1\n2\n") // unstaged change

	out, err := runGk(t, r.Dir, "next")
	if err != nil {
		t.Fatalf("gk next failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "gk ") {
		t.Errorf("next output has no recommended gk command:\n%s", out)
	}
}

// TestE2E_NextRunRefusesNonTTY verifies the advise→act loop refuses to run
// without a terminal and points the user to the command instead.
func TestE2E_NextRunRefusesNonTTY(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "1\n")
	r.Commit("init")
	r.WriteFile("a.txt", "1\n2\n")

	out, err := runGk(t, r.Dir, "next", "--run")
	if err == nil {
		t.Fatalf("expected non-zero exit for --run without TTY, got success:\n%s", out)
	}
	if !strings.Contains(out, "terminal") {
		t.Errorf("expected a terminal-required hint, got:\n%s", out)
	}
}

// TestE2E_ReviewHelpHasBase verifies the new --base flag is wired.
func TestE2E_ReviewHelpHasBase(t *testing.T) {
	r := testutil.NewRepo(t)
	out, err := runGk(t, r.Dir, "review", "--help")
	if err != nil {
		t.Fatalf("gk review --help failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "--base") {
		t.Errorf("review --help missing --base flag:\n%s", out)
	}
}

// TestE2E_AIDisabledGate verifies an AI command refuses cleanly when AI is
// disabled (the env we run e2e under).
func TestE2E_AIDisabledGate(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "1\n")
	r.Commit("init")

	out, err := runGk(t, r.Dir, "do", "rename this branch")
	if err == nil {
		t.Fatalf("expected gk do to fail with AI disabled, got success:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "disabled") {
		t.Errorf("expected an 'AI disabled' message, got:\n%s", out)
	}
}
