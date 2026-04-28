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

// buildSwitchCmd wires a minimal root with the switch subcommand for tests.
// It sets --no-color and forces --repo so tests don't depend on package globals.
func buildSwitchCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	sw := &cobra.Command{
		Use:  "switch [branch]",
		Args: cobra.MaximumNArgs(1),
		RunE: runSwitch,
	}
	sw.Flags().BoolP("create", "c", false, "")
	sw.Flags().BoolP("force", "f", false, "")
	sw.Flags().Bool("detach", false, "")
	sw.Flags().BoolP("main", "m", false, "")
	sw.Flags().Bool("develop", false, "")

	testRoot.AddCommand(sw)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	allArgs := append([]string{"--repo", repoDir, "switch"}, extraArgs...)
	testRoot.SetArgs(allArgs)
	return testRoot, buf
}

// TestListRemoteOnlyBranches verifies the picker ingredient:
//   - HEAD aliases (refs/remotes/origin/HEAD) are filtered
//   - entries whose short name matches an existing local branch are hidden
//   - all other refs/remotes/* entries surface with a proper trackRef
func TestListRemoteOnlyBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	// Simulate remote refs without needing a real remote: write packed
	// refs under refs/remotes/origin/* pointing at the seed commit.
	head := strings.TrimSpace(repo.RunGit("rev-parse", "HEAD"))

	repo.RunGit("update-ref", "refs/remotes/origin/HEAD", head)
	repo.RunGit("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	repo.RunGit("update-ref", "refs/remotes/origin/main", head)
	repo.RunGit("update-ref", "refs/remotes/origin/feature/new", head)
	repo.RunGit("update-ref", "refs/remotes/origin/hotfix", head)
	// Locally create one that should dedupe with origin/hotfix.
	repo.CreateBranch("hotfix")
	repo.Checkout("main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	locals, err := listLocalBranches(context.Background(), runner)
	if err != nil {
		t.Fatalf("listLocalBranches: %v", err)
	}
	remotes, err := listRemoteOnlyBranches(context.Background(), runner, locals)
	if err != nil {
		t.Fatalf("listRemoteOnlyBranches: %v", err)
	}

	got := map[string]string{}
	for _, r := range remotes {
		got[r.Name] = r.TrackRef
	}
	// main exists locally → excluded. hotfix exists locally → excluded.
	// HEAD alias → excluded. Only feature/new remains.
	if len(got) != 1 || got["feature/new"] != "origin/feature/new" {
		t.Errorf("unexpected remote-only set: %+v", got)
	}
}

// TestSwitch_DirectByName changes branch when a name is given.
func TestSwitch_DirectByName(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feature-x")
	repo.WriteFile("x.txt", "x")
	repo.Commit("x")
	repo.Checkout("main")

	root, buf := buildSwitchCmd(repo.Dir, "feature-x")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, err := client.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if cur != "feature-x" {
		t.Errorf("current = %q, want feature-x", cur)
	}
	if !strings.Contains(buf.String(), "switched to feature-x") {
		t.Errorf("missing confirmation in output: %s", buf.String())
	}
}

// TestSwitch_CreateNew with -c creates a new branch and switches to it.
func TestSwitch_CreateNew(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	root, buf := buildSwitchCmd(repo.Dir, "-c", "feat/fresh")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch -c failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, err := client.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if cur != "feat/fresh" {
		t.Errorf("current = %q, want feat/fresh", cur)
	}
}

// TestSwitch_CreateWithoutName errors cleanly when -c has no arg.
func TestSwitch_CreateWithoutName(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	root, _ := buildSwitchCmd(repo.Dir, "-c")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires a branch name") {
		t.Errorf("expected 'requires a branch name' error, got %v", err)
	}
}

// TestSwitch_UnknownBranch returns an error.
func TestSwitch_UnknownBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	root, _ := buildSwitchCmd(repo.Dir, "does-not-exist")
	if err := root.Execute(); err == nil {
		t.Fatal("expected error on unknown branch")
	}
}

// TestSwitch_MainFallsBackToLocal resolves --main via the local 'main' branch
// when no remote is configured.
func TestSwitch_MainFallsBackToLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feature-x")

	root, buf := buildSwitchCmd(repo.Dir, "--main")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch --main failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, _ := client.CurrentBranch(context.Background())
	if cur != "main" {
		t.Errorf("current = %q, want main", cur)
	}
}

// TestSwitch_DevelopFindsDevBranch picks 'dev' when 'develop' is absent.
func TestSwitch_DevelopFindsDevBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("dev")
	repo.Checkout("main")

	root, buf := buildSwitchCmd(repo.Dir, "--develop")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch --develop failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, _ := client.CurrentBranch(context.Background())
	if cur != "dev" {
		t.Errorf("current = %q, want dev", cur)
	}
}

// TestSwitch_MainDevelopMutex rejects both flags together.
func TestSwitch_MainDevelopMutex(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	root, _ := buildSwitchCmd(repo.Dir, "--main", "--develop")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually-exclusive error, got %v", err)
	}
}

// TestSwitch_DevelopMissingErrors reports missing develop/dev branch.
func TestSwitch_DevelopMissingErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	root, _ := buildSwitchCmd(repo.Dir, "--develop")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "develop/dev") {
		t.Errorf("expected 'no develop/dev' error, got %v", err)
	}
}
