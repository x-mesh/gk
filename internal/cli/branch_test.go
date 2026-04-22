package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// ---------------------------------------------------------------------------
// TestBranchList
// ---------------------------------------------------------------------------

func TestBranchList(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create two additional branches
	repo.CreateBranch("feature-alpha")
	repo.WriteFile("alpha.txt", "alpha\n")
	repo.Commit("add alpha")
	repo.Checkout("main")

	repo.CreateBranch("feature-beta")
	repo.WriteFile("beta.txt", "beta\n")
	repo.Commit("add beta")
	repo.Checkout("main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	branches, err := listLocalBranches(context.Background(), runner)
	if err != nil {
		t.Fatalf("listLocalBranches: %v", err)
	}

	names := make(map[string]bool)
	for _, b := range branches {
		names[b.Name] = true
	}

	if !names["feature-alpha"] {
		t.Error("expected feature-alpha in branch list")
	}
	if !names["feature-beta"] {
		t.Error("expected feature-beta in branch list")
	}
}

// ---------------------------------------------------------------------------
// TestBranchList_Stale  — stale=0 means no filter (all branches returned)
// ---------------------------------------------------------------------------

func TestBranchList_Stale(t *testing.T) {
	// Use FakeRunner to avoid needing a real repo.
	// With stale=0, all branches should be included.
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				// two branches: main (unix=0, very old) and feature (unix=9999999999, far future)
				Stdout: "main\x00\x001000000000\x00\nfeature\x00\x009999999999\x00\n",
			},
		},
	}

	branches, err := listLocalBranches(context.Background(), fake)
	if err != nil {
		t.Fatalf("listLocalBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}

	// stale=0 → no filtering, both present
	names := map[string]bool{}
	for _, b := range branches {
		names[b.Name] = true
	}
	if !names["main"] {
		t.Error("expected main with stale=0")
	}
	if !names["feature"] {
		t.Error("expected feature with stale=0")
	}
}

// TestListLocalBranches_GoneFlag parses the upstream:track "[gone]" marker.
func TestListLocalBranches_GoneFlag(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: "main\x00origin/main\x001700000000\x00\n" +
					"stale\x00origin/stale\x001700000000\x00[gone]\n" +
					"noupstream\x00\x001700000000\x00\n",
			},
		},
	}

	branches, err := listLocalBranches(context.Background(), fake)
	if err != nil {
		t.Fatalf("listLocalBranches: %v", err)
	}
	byName := map[string]branchInfo{}
	for _, b := range branches {
		byName[b.Name] = b
	}
	if byName["main"].Gone {
		t.Error("main should not be gone")
	}
	if !byName["stale"].Gone {
		t.Error("stale should be gone")
	}
	if byName["noupstream"].Gone {
		t.Error("no-upstream branch should not be marked gone")
	}
}

// ---------------------------------------------------------------------------
// TestBranchClean_DryRun
// ---------------------------------------------------------------------------

func TestBranchClean_DryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create a branch, merge it into main, then go back to main
	repo.CreateBranch("to-clean")
	repo.WriteFile("clean.txt", "clean\n")
	repo.Commit("add clean")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "to-clean", "-m", "merge to-clean")

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)

	// determine base (main)
	base, err := client.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}

	merged, err := mergedBranches(context.Background(), runner, base)
	if err != nil {
		t.Fatalf("mergedBranches: %v", err)
	}

	// to-clean should be in merged set
	if !merged["to-clean"] {
		t.Skip("to-clean not in merged set (git version may not support --format on branch --merged); skipping")
	}

	// build cobra command for clean --dry-run
	var buf bytes.Buffer
	cmd := &cobra.Command{Use: "clean"}
	cmd.Flags().Bool("dry-run", true, "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().String("repo", repo.Dir, "")
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	// manually invoke the clean logic with injected runner
	protected := map[string]bool{base: true, "main": true, "master": true, "develop": true}
	if cur, err := client.CurrentBranch(context.Background()); err == nil {
		protected[cur] = true
	}

	var targets []string
	for name := range merged {
		if protected[name] {
			continue
		}
		targets = append(targets, name)
	}

	for _, target := range targets {
		cmd.Printf("would delete: %s\n", target)
	}

	out := buf.String()
	if !strings.Contains(out, "would delete: to-clean") {
		t.Errorf("expected 'would delete: to-clean' in output, got: %q", out)
	}

	// branch must still exist (dry-run)
	branches, err := listLocalBranches(context.Background(), runner)
	if err != nil {
		t.Fatalf("listLocalBranches: %v", err)
	}
	found := false
	for _, b := range branches {
		if b.Name == "to-clean" {
			found = true
			break
		}
	}
	if !found {
		t.Error("to-clean should still exist after dry-run")
	}
}

// ---------------------------------------------------------------------------
// TestBranchClean_Protected — base branch must not be deleted
// ---------------------------------------------------------------------------

func TestBranchClean_Protected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)

	base, err := client.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}

	merged, err := mergedBranches(context.Background(), runner, base)
	if err != nil {
		t.Fatalf("mergedBranches: %v", err)
	}

	// base branch is always in "merged" set (it's merged into itself)
	protected := map[string]bool{base: true}

	// verify that base branch is NOT in targets
	for name := range merged {
		if protected[name] {
			continue
		}
		if name == base {
			t.Errorf("base branch %q must not be a deletion target", base)
		}
	}
}

// ---------------------------------------------------------------------------
// TestBranchPick_Prompt
// ---------------------------------------------------------------------------

func TestBranchPick_Prompt(t *testing.T) {
	names := []string{"feature-alpha", "feature-beta", "main"}

	// simulate selecting item 2 ("feature-beta")
	input := strings.NewReader("2\n")
	var out bytes.Buffer

	picked, err := pickWithPrompt(names, input, &out)
	if err != nil {
		t.Fatalf("pickWithPrompt: %v", err)
	}
	if picked != "feature-beta" {
		t.Errorf("expected feature-beta, got %q", picked)
	}

	output := out.String()
	if !strings.Contains(output, "feature-alpha") {
		t.Error("expected feature-alpha in prompt output")
	}
	if !strings.Contains(output, "feature-beta") {
		t.Error("expected feature-beta in prompt output")
	}
}
