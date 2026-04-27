package resolve_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/resolve"
	"github.com/x-mesh/gk/internal/testutil"
)

// setupMergeConflict creates a real git repo with a merge conflict.
// Returns the repo, runner, and gitstate.State.
func setupMergeConflict(t *testing.T) (*testutil.Repo, git.Runner, *gitstate.State) {
	t.Helper()
	repo := testutil.NewRepo(t)

	// Create a file on main
	repo.WriteFile("conflict.go", "package main\n\nfunc hello() string {\n\treturn \"hello\"\n}\n")
	repo.Commit("initial")

	// Create feature branch with different change
	repo.CreateBranch("feature")
	repo.WriteFile("conflict.go", "package main\n\nfunc hello() string {\n\treturn \"hello from feature\"\n}\n")
	repo.Commit("feature change")

	// Go back to main and make conflicting change
	repo.Checkout("main")
	repo.WriteFile("conflict.go", "package main\n\nfunc hello() string {\n\treturn \"hello from main\"\n}\n")
	repo.Commit("main change")

	// Attempt merge — should conflict
	_, err := repo.TryGit("merge", "feature")
	if err == nil {
		t.Fatal("expected merge conflict, but merge succeeded")
	}

	runner := &git.ExecRunner{Dir: repo.Dir, ExtraEnv: os.Environ()}
	state, err := gitstate.Detect(context.Background(), repo.Dir)
	if err != nil {
		t.Fatalf("gitstate.Detect: %v", err)
	}
	if state.Kind != gitstate.StateMerge {
		t.Fatalf("expected StateMerge, got %v", state.Kind)
	}

	return repo, runner, state
}

// newRepoResolver creates a Resolver wired to a real repo directory.
func newRepoResolver(repo *testutil.Repo, runner git.Runner) *resolve.Resolver {
	return &resolve.Resolver{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: os.Stderr,
		Stdout: os.Stdout,
		ReadFile: func(path string) ([]byte, error) {
			return os.ReadFile(filepath.Join(repo.Dir, path))
		},
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			return os.WriteFile(filepath.Join(repo.Dir, path), data, perm)
		},
	}
}

func TestIntegration_StrategyOurs(t *testing.T) {
	t.Parallel()
	repo, runner, state := setupMergeConflict(t)

	r := newRepoResolver(repo, runner)

	result, err := r.Run(context.Background(), state, resolve.ResolveOptions{
		Strategy: resolve.StrategyOurs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Resolved) == 0 {
		t.Fatal("expected at least one resolved file")
	}

	// Verify file content: should contain "hello from main" (ours)
	content, err := os.ReadFile(filepath.Join(repo.Dir, "conflict.go"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "hello from main") {
		t.Errorf("expected ours content, got:\n%s", content)
	}
	if strings.Contains(string(content), "<<<<<<<") {
		t.Error("conflict markers should be removed")
	}
}

func TestIntegration_StrategyTheirs(t *testing.T) {
	t.Parallel()
	repo, runner, state := setupMergeConflict(t)

	r := newRepoResolver(repo, runner)

	result, err := r.Run(context.Background(), state, resolve.ResolveOptions{
		Strategy: resolve.StrategyTheirs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Resolved) == 0 {
		t.Fatal("expected at least one resolved file")
	}

	content, err := os.ReadFile(filepath.Join(repo.Dir, "conflict.go"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "hello from feature") {
		t.Errorf("expected theirs content, got:\n%s", content)
	}
	if strings.Contains(string(content), "<<<<<<<") {
		t.Error("conflict markers should be removed")
	}
}

func TestIntegration_DryRun_NoFileChange(t *testing.T) {
	t.Parallel()
	repo, runner, state := setupMergeConflict(t)

	// Read original content before dry-run
	origContent, err := os.ReadFile(filepath.Join(repo.Dir, "conflict.go"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var stdout strings.Builder
	r := newRepoResolver(repo, runner)
	r.Stdout = &stdout

	_, err = r.Run(context.Background(), state, resolve.ResolveOptions{
		DryRun:   true,
		Strategy: resolve.StrategyOurs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// File should be unchanged
	afterContent, err := os.ReadFile(filepath.Join(repo.Dir, "conflict.go"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(origContent) != string(afterContent) {
		t.Error("dry-run should not modify the file")
	}
}

func TestIntegration_Backup_CreatesOrigFile(t *testing.T) {
	t.Parallel()
	repo, runner, state := setupMergeConflict(t)

	r := newRepoResolver(repo, runner)

	_, err := r.Run(context.Background(), state, resolve.ResolveOptions{
		Strategy: resolve.StrategyOurs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	origPath := filepath.Join(repo.Dir, "conflict.go.orig")
	if _, err := os.Stat(origPath); os.IsNotExist(err) {
		t.Error("expected .orig backup file to be created")
	}
}

func TestIntegration_NoBackup_NoOrigFile(t *testing.T) {
	t.Parallel()
	repo, runner, state := setupMergeConflict(t)

	r := newRepoResolver(repo, runner)

	_, err := r.Run(context.Background(), state, resolve.ResolveOptions{
		Strategy: resolve.StrategyOurs,
		NoBackup: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	origPath := filepath.Join(repo.Dir, "conflict.go.orig")
	if _, err := os.Stat(origPath); !os.IsNotExist(err) {
		t.Error("expected no .orig file with --no-backup")
	}
}

func TestIntegration_NoConflict_ReturnsError(t *testing.T) {
	t.Parallel()
	repo := testutil.NewRepo(t)
	repo.WriteFile("clean.go", "package main\n")
	repo.Commit("clean")

	runner := &git.ExecRunner{Dir: repo.Dir, ExtraEnv: os.Environ()}
	state, err := gitstate.Detect(context.Background(), repo.Dir)
	if err != nil {
		t.Fatalf("gitstate.Detect: %v", err)
	}

	r := &resolve.Resolver{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: os.Stderr,
		Stdout: os.Stdout,
	}

	_, err = r.Run(context.Background(), state, resolve.ResolveOptions{
		Strategy: resolve.StrategyOurs,
	})
	if err == nil {
		t.Fatal("expected error when no conflict in progress")
	}
	if !strings.Contains(err.Error(), "no merge/rebase/cherry-pick") {
		t.Errorf("unexpected error: %v", err)
	}
}
