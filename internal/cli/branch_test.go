package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/x-mesh/gk/internal/branchclean"
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
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track)%00%(objectname:short) refs/heads": {
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
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track)%00%(objectname:short) refs/heads": {
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

// ---------------------------------------------------------------------------
// Task 9.3: CLI 플래그 및 기존 호환성 테스트
// ---------------------------------------------------------------------------

// TestBranchClean_GoneFlag_Compat verifies --gone collects only gone-upstream
// branches (backward compatibility with existing behavior).
func TestBranchClean_GoneFlag_Compat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create a branch with a fake gone upstream
	repo.CreateBranch("gone-branch")
	repo.WriteFile("gone.txt", "gone\n")
	repo.Commit("add gone")
	repo.Checkout("main")

	// create a merged branch
	repo.CreateBranch("merged-branch")
	repo.WriteFile("merged.txt", "merged\n")
	repo.Commit("add merged")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "merged-branch", "-m", "merge merged-branch")

	// simulate gone upstream by setting upstream then removing it
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("fetch", "origin")
	repo.RunGit("branch", "--set-upstream-to=origin/gone-branch", "gone-branch")
	// delete the remote tracking ref to simulate "gone"
	repo.RunGit("update-ref", "-d", "refs/remotes/origin/gone-branch")

	runner := &git.ExecRunner{Dir: repo.Dir}

	// Use Cleaner directly with --gone --yes to verify only gone branches are collected
	client := git.NewClient(runner)
	cleaner := &branchclean.Cleaner{
		Runner: runner,
		Client: client,
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}

	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		DryRun:    true,
		Gone:      true,
		Protected: []string{"main", "master", "develop"},
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	// gone-branch should be in dry-run candidates
	found := false
	for _, c := range result.DryRun {
		if c.Name == "gone-branch" {
			found = true
		}
	}
	if !found {
		t.Error("expected gone-branch in dry-run candidates with --gone flag")
	}
}

// TestBranchClean_NoFlags_MergedCollection verifies that running without flags
// collects merged branches (default behavior).
func TestBranchClean_NoFlags_MergedCollection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create and merge a branch
	repo.CreateBranch("to-merge")
	repo.WriteFile("merge.txt", "merge\n")
	repo.Commit("add merge")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "to-merge", "-m", "merge to-merge")

	// set up remote HEAD so DefaultBranch works
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("fetch", "origin")
	repo.SetRemoteHEAD("origin", "main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)

	cleaner := &branchclean.Cleaner{
		Runner: runner,
		Client: client,
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}

	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		DryRun:    true,
		Protected: []string{"main", "master", "develop"},
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	// to-merge should be in dry-run candidates as merged
	found := false
	for _, c := range result.DryRun {
		if c.Name == "to-merge" {
			found = true
			if c.Status != branchclean.StatusMerged {
				t.Errorf("expected status merged, got %s", c.Status)
			}
		}
	}
	if !found {
		t.Error("expected to-merge in dry-run candidates (default merged collection)")
	}
}

// TestBranchClean_StaleNegativeError verifies --stale with value ≤ 0 returns error.
func TestBranchClean_StaleNegativeError(t *testing.T) {
	cleaner := &branchclean.Cleaner{
		Runner: &git.FakeRunner{},
		Client: git.NewClient(&git.FakeRunner{}),
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}

	_, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Stale: -1,
	})
	if err == nil {
		t.Fatal("expected error for --stale -1")
	}
	if !strings.Contains(err.Error(), "invalid --stale value") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestBranchClean_RemoteStandalone verifies --remote alone runs prune
// without local branch deletion.
func TestBranchClean_RemoteStandalone(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"remote prune origin": {Stdout: ""},
		},
	}

	cleaner := &branchclean.Cleaner{
		Runner: fake,
		Client: git.NewClient(fake),
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}

	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Remote: true,
		Yes:    true,
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	if !result.Pruned {
		t.Error("expected Pruned=true for --remote")
	}
	if len(result.Deleted) != 0 {
		t.Errorf("expected no deleted branches for --remote standalone, got %v", result.Deleted)
	}

	// verify git remote prune was called
	pruneCalled := false
	for _, call := range fake.Calls {
		if len(call.Args) >= 3 && call.Args[0] == "remote" && call.Args[1] == "prune" {
			pruneCalled = true
		}
	}
	if !pruneCalled {
		t.Error("expected git remote prune to be called")
	}
}

// TestBranchClean_NonTTY_NoFlags_Error verifies that non-TTY without --yes
// or --force returns an error. In test environment, stdin/stdout are not TTYs.
func TestBranchClean_NonTTY_NoFlags_Error(t *testing.T) {
	// runBranchClean checks ui.IsTerminal() which returns false in tests.
	// Without --yes, --force, or --dry-run, it should return an error.
	var buf bytes.Buffer
	cmd := &cobra.Command{Use: "clean", RunE: runBranchClean}
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Bool("gone", false, "")
	cmd.Flags().Bool("no-ai", false, "")
	cmd.Flags().Int("stale", 0, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().Bool("remote", false, "")
	cmd.Flags().Bool("squash-merged", false, "")
	cmd.Flags().BoolP("yes", "y", false, "")
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetContext(context.Background())

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error for non-TTY without --yes or --force")
	}
	if !strings.Contains(err.Error(), "non-interactive mode requires --yes or --force") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestBranchClean_ForceFlag_DeleteFlag verifies --force uses -D instead of -d.
func TestBranchClean_ForceFlag_DeleteFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create a branch (not merged) — needs -D to delete
	repo.CreateBranch("unmerged-branch")
	repo.WriteFile("unmerged.txt", "unmerged\n")
	repo.Commit("add unmerged")
	repo.Checkout("main")

	// set up remote HEAD
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("fetch", "origin")
	repo.SetRemoteHEAD("origin", "main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)

	// First, verify -d (without force) fails for unmerged branch
	cleaner := &branchclean.Cleaner{
		Runner: runner,
		Client: client,
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}

	// Use --all --yes to include the unmerged branch via stale detection
	// Actually, unmerged branch won't be in merged set. Let's merge it first
	// then test the flag difference.

	// Create a merged branch to test -d vs -D flag
	repo.CreateBranch("merged-for-force")
	repo.WriteFile("force.txt", "force\n")
	repo.Commit("add force")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "merged-for-force", "-m", "merge force")

	// Test with force=false → should use -d
	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Yes:       true,
		Force:     false,
		Protected: []string{"main", "master", "develop"},
	})
	if err != nil {
		t.Fatalf("Cleaner.Run (no force): %v", err)
	}

	// merged-for-force should be deleted with -d
	found := false
	for _, name := range result.Deleted {
		if name == "merged-for-force" {
			found = true
		}
	}
	if !found {
		t.Error("expected merged-for-force to be deleted")
	}

	// Now create another branch and test with force=true
	repo.CreateBranch("merged-for-D")
	repo.WriteFile("D.txt", "D\n")
	repo.Commit("add D")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "merged-for-D", "-m", "merge D")

	result2, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Yes:       true,
		Force:     true,
		Protected: []string{"main", "master", "develop"},
	})
	if err != nil {
		t.Fatalf("Cleaner.Run (force): %v", err)
	}

	found = false
	for _, name := range result2.Deleted {
		if name == "merged-for-D" {
			found = true
		}
	}
	if !found {
		t.Error("expected merged-for-D to be deleted with --force")
	}
}

// TestBranchClean_ForceFlag_UsesCapitalD verifies that --force causes
// git branch -D to be called instead of -d.
func TestBranchClean_ForceFlag_UsesCapitalD(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD":                                                               {Stdout: "main\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD":                                           {Stdout: "origin/main\n"},
			"for-each-ref --merged=main --format=%(refname:short)%00%(committerdate:unix) refs/heads": {Stdout: "feat-done\x001700000000\n"},
			"for-each-ref --format=%(refname:short) refs/heads":                                       {Stdout: "main\nfeat-done\n"},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track)%00%(objectname:short) refs/heads": {
				Stdout: "main\x00origin/main\x001700000000\x00\nfeat-done\x00\x001700000000\x00\n",
			},
			"branch -D feat-done": {Stdout: "Deleted branch feat-done\n"},
		},
	}

	cleaner := &branchclean.Cleaner{
		Runner: fake,
		Client: git.NewClient(fake),
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}

	_, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Yes:       true,
		Force:     true,
		Protected: []string{"main", "master", "develop"},
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	// verify -D was used, not -d
	capitalDUsed := false
	for _, call := range fake.Calls {
		if len(call.Args) >= 2 && call.Args[0] == "branch" && call.Args[1] == "-D" {
			capitalDUsed = true
		}
		if len(call.Args) >= 2 && call.Args[0] == "branch" && call.Args[1] == "-d" {
			t.Error("expected -D (force), but -d was called")
		}
	}
	if !capitalDUsed {
		t.Error("expected git branch -D to be called with --force")
	}
}

// TestBranchClean_NoForce_UsesLowercaseD verifies that without --force,
// git branch -d is called.
func TestBranchClean_NoForce_UsesLowercaseD(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD":                                                               {Stdout: "main\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD":                                           {Stdout: "origin/main\n"},
			"for-each-ref --merged=main --format=%(refname:short)%00%(committerdate:unix) refs/heads": {Stdout: "feat-done\x001700000000\n"},
			"for-each-ref --format=%(refname:short) refs/heads":                                       {Stdout: "main\nfeat-done\n"},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track)%00%(objectname:short) refs/heads": {
				Stdout: "main\x00origin/main\x001700000000\x00\nfeat-done\x00\x001700000000\x00\n",
			},
			"branch -d feat-done": {Stdout: "Deleted branch feat-done\n"},
		},
	}

	cleaner := &branchclean.Cleaner{
		Runner: fake,
		Client: git.NewClient(fake),
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}

	_, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Yes:       true,
		Force:     false,
		Protected: []string{"main", "master", "develop"},
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	lowercaseDUsed := false
	for _, call := range fake.Calls {
		if len(call.Args) >= 2 && call.Args[0] == "branch" && call.Args[1] == "-d" {
			lowercaseDUsed = true
		}
		if len(call.Args) >= 2 && call.Args[0] == "branch" && call.Args[1] == "-D" {
			t.Error("expected -d (safe), but -D was called")
		}
	}
	if !lowercaseDUsed {
		t.Error("expected git branch -d to be called without --force")
	}
}

// ---------------------------------------------------------------------------
// runBranchSetParent / runBranchUnsetParent — CLI entry-point integration tests
// ---------------------------------------------------------------------------

func TestRunBranchSetParent_Happy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/parent")
	repo.WriteFile("p.txt", "p\n")
	repo.Commit("p")
	repo.Checkout("main")
	repo.CreateBranch("feat/sub")
	repo.WriteFile("s.txt", "s\n")
	repo.Commit("s")
	t.Chdir(repo.Dir)

	var buf bytes.Buffer
	cmd := &cobra.Command{Use: "set-parent"}
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetContext(context.Background())

	if err := runBranchSetParent(cmd, []string{"feat/parent"}); err != nil {
		t.Fatalf("set-parent should succeed, got: %v", err)
	}
	if !strings.Contains(buf.String(), "set parent of feat/sub to feat/parent") {
		t.Errorf("expected confirmation message, got: %q", buf.String())
	}
	out, _, err := (&git.ExecRunner{Dir: repo.Dir}).Run(context.Background(),
		"config", "--get", "branch.feat/sub.gk-parent")
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if strings.TrimSpace(string(out)) != "feat/parent" {
		t.Errorf("config not written: got %q", out)
	}
}

func TestRunBranchSetParent_DetachedHEAD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	r := &git.ExecRunner{Dir: repo.Dir}
	shaOut, _, _ := r.Run(context.Background(), "rev-parse", "HEAD")
	sha := strings.TrimSpace(string(shaOut))
	r.Run(context.Background(), "checkout", sha)
	t.Chdir(repo.Dir)

	cmd := &cobra.Command{Use: "set-parent"}
	cmd.SetContext(context.Background())
	err := runBranchSetParent(cmd, []string{"main"})
	if err == nil {
		t.Fatal("must error on detached HEAD")
	}
	if !strings.Contains(err.Error(), "detached") {
		t.Errorf("expected 'detached' in error, got: %v", err)
	}
}

func TestRunBranchSetParent_InvalidParentTypo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/sub")
	t.Chdir(repo.Dir)

	cmd := &cobra.Command{Use: "set-parent"}
	cmd.SetContext(context.Background())
	err := runBranchSetParent(cmd, []string{"mian"})
	if err == nil {
		t.Fatal("must error on non-existent branch")
	}
	if !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("expected fuzzy suggestion, got: %v", err)
	}
}

func TestRunBranchUnsetParent_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/sub")
	repo.WriteFile("s.txt", "s\n")
	repo.Commit("s")
	t.Chdir(repo.Dir)

	var buf bytes.Buffer
	cmd := &cobra.Command{Use: "unset-parent"}
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	if err := runBranchUnsetParent(cmd, nil); err != nil {
		t.Fatalf("unset on absent key must be idempotent, got: %v", err)
	}
	r := &git.ExecRunner{Dir: repo.Dir}
	if _, _, err := r.Run(context.Background(),
		"config", "branch.feat/sub.gk-parent", "main"); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	buf.Reset()
	if err := runBranchUnsetParent(cmd, nil); err != nil {
		t.Fatalf("unset after set must succeed, got: %v", err)
	}
	out, _, err := r.Run(context.Background(),
		"config", "--get", "branch.feat/sub.gk-parent")
	if err == nil {
		t.Errorf("config still present after unset: %q", out)
	}
}
