package testutil_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/testutil"
)

func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found in PATH")
	}
}

// TestNewRepo: 생성 후 status 클린, 기본 브랜치 main
func TestNewRepo(t *testing.T) {
	t.Parallel()
	skipIfNoGit(t)

	r := testutil.NewRepo(t)

	status := r.RunGit("status", "--porcelain")
	if status != "" {
		t.Errorf("expected clean working tree, got: %q", status)
	}

	branch := r.RunGit("rev-parse", "--abbrev-ref", "HEAD")
	if branch != "main" {
		t.Errorf("expected branch main, got %q", branch)
	}
}

// TestCommit: WriteFile → Commit → SHA 40자, log에 메시지 보임
func TestCommit(t *testing.T) {
	t.Parallel()
	skipIfNoGit(t)

	r := testutil.NewRepo(t)
	r.WriteFile("hello.txt", "world\n")
	sha := r.Commit("add hello")

	if len(sha) != 40 {
		t.Errorf("expected 40-char SHA, got %d chars: %q", len(sha), sha)
	}

	log := r.RunGit("log", "--oneline", "-1")
	if !strings.Contains(log, "add hello") {
		t.Errorf("expected commit message in log, got: %q", log)
	}
}

// TestCreateBranch_Checkout: feat/x 생성 후 HEAD가 feat/x
func TestCreateBranch_Checkout(t *testing.T) {
	t.Parallel()
	skipIfNoGit(t)

	r := testutil.NewRepo(t)
	r.CreateBranch("feat/x")

	head := r.RunGit("rev-parse", "--abbrev-ref", "HEAD")
	if head != "feat/x" {
		t.Errorf("expected HEAD=feat/x, got %q", head)
	}

	// main으로 돌아가기
	r.Checkout("main")
	head = r.RunGit("rev-parse", "--abbrev-ref", "HEAD")
	if head != "main" {
		t.Errorf("expected HEAD=main after checkout, got %q", head)
	}
}

// TestAddRemote_SetRemoteHEAD: 두 Repo 로컬 연결 후 remote 확인
func TestAddRemote_SetRemoteHEAD(t *testing.T) {
	t.Parallel()
	skipIfNoGit(t)

	origin := testutil.NewRepo(t)
	local := testutil.NewRepo(t)

	local.AddRemote("origin", origin.Dir)
	local.SetRemoteHEAD("origin", "main")

	remotes := local.RunGit("remote", "-v")
	if !strings.Contains(remotes, "origin") {
		t.Errorf("expected origin remote, got: %q", remotes)
	}
	if !strings.Contains(remotes, origin.Dir) {
		t.Errorf("expected origin URL %q in remotes, got: %q", origin.Dir, remotes)
	}

	symref := local.RunGit("symbolic-ref", "refs/remotes/origin/HEAD")
	if !strings.Contains(symref, "main") {
		t.Errorf("expected symbolic-ref to contain main, got %q", symref)
	}
}

// TestIsolation: GIT_CONFIG_GLOBAL=/dev/null 로 user.name=gk-test 만 유효
func TestIsolation(t *testing.T) {
	t.Parallel()
	skipIfNoGit(t)

	r := testutil.NewRepo(t)

	name := r.RunGit("config", "user.name")
	if name != "gk-test" {
		t.Errorf("expected user.name=gk-test, got %q", name)
	}

	email := r.RunGit("config", "user.email")
	if email != "test@example.com" {
		t.Errorf("expected user.email=test@example.com, got %q", email)
	}
}

// TestTryGit: 잘못된 명령은 에러 반환, t.Fatal 없이
func TestTryGit(t *testing.T) {
	t.Parallel()
	skipIfNoGit(t)

	r := testutil.NewRepo(t)

	_, err := r.TryGit("rev-parse", "refs/heads/nonexistent-branch-xyz")
	if err == nil {
		t.Error("expected error for nonexistent ref, got nil")
	}
}
