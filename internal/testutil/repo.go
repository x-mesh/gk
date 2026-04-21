package testutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a temporary isolated git repository for integration tests.
type Repo struct {
	Dir    string // working tree
	GitDir string // .git
	t      testing.TB
}

// isolationEnv returns a fresh env slice with git isolation variables injected.
func isolationEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=gk-test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=gk-test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"LC_ALL=C",
		"LANG=C",
		"GIT_OPTIONAL_LOCKS=0",
	)
}

// NewRepo creates a fresh git repo in t.TempDir() with full isolation.
// The repo is initialised on branch "main" with one empty commit.
func NewRepo(t testing.TB) *Repo {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found in PATH")
	}

	dir := t.TempDir()
	r := &Repo{
		Dir:    dir,
		GitDir: filepath.Join(dir, ".git"),
		t:      t,
	}

	// init
	r.RunGit("-c", "init.defaultBranch=main", "init")

	// local config (these go into .git/config, not global)
	r.RunGit("config", "user.name", "gk-test")
	r.RunGit("config", "user.email", "test@example.com")
	r.RunGit("config", "core.autocrlf", "false")
	r.RunGit("config", "core.quotepath", "false")

	// initial commit so HEAD is valid
	keepDir := filepath.Join(dir, ".gkkeep")
	if err := os.MkdirAll(keepDir, 0o755); err != nil {
		t.Fatalf("testutil.NewRepo: mkdir .gkkeep: %v", err)
	}
	r.WriteFile(filepath.Join(".gkkeep", "README"), "init\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "initial")

	return r
}

// buildCmd constructs an exec.Cmd for `git <args...>` in the repo dir.
func (r *Repo) buildCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = isolationEnv()
	return cmd
}

// RunGit executes `git <args...>` in the repo dir with isolation env.
// Returns combined stdout (trimmed). t.Fatal on non-zero exit.
func (r *Repo) RunGit(args ...string) string {
	r.t.Helper()
	out, err := r.TryGit(args...)
	if err != nil {
		r.t.Fatalf("git %s: %v\noutput: %s", strings.Join(args, " "), err, out)
	}
	return out
}

// TryGit is like RunGit but returns stdout + exit error without failing the test.
func (r *Repo) TryGit(args ...string) (string, error) {
	r.t.Helper()
	cmd := r.buildCmd(args...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	return strings.TrimRight(buf.String(), "\n"), err
}

// WriteFile creates or overwrites a file relative to Dir.
// Intermediate directories are created as needed.
// t.Fatal on error.
func (r *Repo) WriteFile(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.Dir, filepath.Clean(filepath.FromSlash(path)))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatalf("testutil.WriteFile: mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatalf("testutil.WriteFile: write %s: %v", path, err)
	}
}

// Commit stages all changes and creates a commit with msg.
// Returns the 40-character commit SHA.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", msg)
	sha := r.RunGit("rev-parse", "HEAD")
	return sha
}

// CreateBranch creates and checks out a new branch.
func (r *Repo) CreateBranch(name string) {
	r.t.Helper()
	r.RunGit("checkout", "-b", name)
}

// Checkout checks out an existing ref.
func (r *Repo) Checkout(ref string) {
	r.t.Helper()
	r.RunGit("checkout", ref)
}

// AddRemote adds a remote with the given name pointing to url (or another repo's Dir).
func (r *Repo) AddRemote(name, url string) {
	r.t.Helper()
	r.RunGit("remote", "add", name, url)
}

// SetRemoteHEAD sets refs/remotes/<remote>/HEAD to a symbolic-ref for branch.
func (r *Repo) SetRemoteHEAD(remote, branch string) {
	r.t.Helper()
	symref := fmt.Sprintf("refs/remotes/%s/%s", remote, branch)
	r.RunGit("symbolic-ref", fmt.Sprintf("refs/remotes/%s/HEAD", remote), symref)
}
