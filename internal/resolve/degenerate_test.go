package resolve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/testutil"
)

// setupDeleteModify는 delete/modify 충돌로 멈춘 rebase repo를 만든다:
// main이 f.txt를 지웠고, feat의 pick이 같은 파일을 수정했다.
func setupDeleteModify(t *testing.T) (*testutil.Repo, *Resolver, *gitstate.State) {
	t.Helper()
	repo := testutil.NewRepo(t)

	repo.WriteFile("f.txt", "base\n")
	repo.Commit("base")

	repo.CreateBranch("feat")
	repo.WriteFile("f.txt", "feat change\n")
	repo.Commit("feat: modify f")

	repo.Checkout("main")
	repo.RunGit("rm", "-q", "f.txt")
	repo.Commit("main: delete f")

	repo.Checkout("feat")
	if _, err := repo.TryGit("rebase", "main"); err == nil {
		t.Skip("expected delete/modify conflict but rebase succeeded")
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	r := &Resolver{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: os.Stderr,
		Stdout: os.Stdout,
		Root:   repo.Dir,
	}
	state, err := gitstate.Detect(context.Background(), repo.Dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	return repo, r, state
}

func assertNoUnmerged(t *testing.T, repo *testutil.Repo) {
	t.Helper()
	if out := repo.RunGit("diff", "--name-only", "--diff-filter=U"); out != "" {
		t.Fatalf("index still has unmerged paths: %q", out)
	}
}

// AI가 "theirs"(수정 유지)를 고르면 파일이 살아남아 stage돼야 한다.
func TestResolveDegenerateAI_KeepModified(t *testing.T) {
	repo, r, state := setupDeleteModify(t)
	r.Provider = &fakeResolveProvider{
		resolveRes: provider.ConflictResolutionResult{
			Resolutions: []provider.ConflictResolutionOutput{
				{Index: 0, Strategy: "theirs", Rationale: "modification is the newer intent"},
			},
		},
	}

	res, err := r.Run(context.Background(), state, ResolveOptions{Strategy: "ai"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Resolved) != 1 || res.Resolved[0] != "f.txt" {
		t.Fatalf("Resolved = %v, want [f.txt] (skipped=%v failed=%v)", res.Resolved, res.Skipped, res.Failed)
	}
	if !res.AIUsed {
		t.Error("AIUsed should be true")
	}
	assertNoUnmerged(t, repo)
	data, err := os.ReadFile(filepath.Join(repo.Dir, "f.txt"))
	if err != nil {
		t.Fatalf("f.txt should exist: %v", err)
	}
	if string(data) != "feat change\n" {
		t.Errorf("f.txt = %q, want the modified content", data)
	}
}

// AI가 삭제된 쪽("ours")을 고르면 파일을 지우는 결정이다.
func TestResolveDegenerateAI_TakeDeletion(t *testing.T) {
	repo, r, state := setupDeleteModify(t)
	r.Provider = &fakeResolveProvider{
		resolveRes: provider.ConflictResolutionResult{
			Resolutions: []provider.ConflictResolutionOutput{
				{Index: 0, Strategy: "ours", Rationale: "file was retired upstream"},
			},
		},
	}

	res, err := r.Run(context.Background(), state, ResolveOptions{Strategy: "ai"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Resolved) != 1 {
		t.Fatalf("Resolved = %v, want 1 path (skipped=%v failed=%v)", res.Resolved, res.Skipped, res.Failed)
	}
	assertNoUnmerged(t, repo)
	if _, err := os.Stat(filepath.Join(repo.Dir, "f.txt")); !os.IsNotExist(err) {
		t.Errorf("f.txt should be deleted, stat err = %v", err)
	}
}

// AI가 "merged"로 새 내용을 주면 그 내용으로 살린다.
func TestResolveDegenerateAI_MergedContent(t *testing.T) {
	repo, r, state := setupDeleteModify(t)
	r.Provider = &fakeResolveProvider{
		resolveRes: provider.ConflictResolutionResult{
			Resolutions: []provider.ConflictResolutionOutput{
				{Index: 0, Strategy: "merged", Resolved: []string{"merged line"}, Rationale: "kept the gist"},
			},
		},
	}

	res, err := r.Run(context.Background(), state, ResolveOptions{Strategy: "ai"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Resolved) != 1 {
		t.Fatalf("Resolved = %v (skipped=%v failed=%v)", res.Resolved, res.Skipped, res.Failed)
	}
	assertNoUnmerged(t, repo)
	data, err := os.ReadFile(filepath.Join(repo.Dir, "f.txt"))
	if err != nil {
		t.Fatalf("f.txt should exist: %v", err)
	}
	if string(data) != "merged line\n" {
		t.Errorf("f.txt = %q, want the AI-merged content", data)
	}
}
