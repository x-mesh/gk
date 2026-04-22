package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// TestParseWorktreePorcelain covers record splitting and field parsing.
func TestParseWorktreePorcelain(t *testing.T) {
	raw := strings.Join([]string{
		"worktree /repo",
		"HEAD 0123456789abcdef0123456789abcdef01234567",
		"branch refs/heads/main",
		"",
		"worktree /tmp/wt-detached",
		"HEAD abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		"detached",
		"locked",
		"",
		"worktree /tmp/wt-bare",
		"bare",
		"",
	}, "\n")

	got := parseWorktreePorcelain(raw)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	if got[0].Path != "/repo" || got[0].Branch != "main" || got[0].Detached {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if !got[1].Detached || !got[1].Locked {
		t.Errorf("entry 1 = %+v", got[1])
	}
	if !got[2].Bare {
		t.Errorf("entry 2 = %+v", got[2])
	}
}

// buildWorktreeCmd wires a minimal cobra root with worktree for tests.
func buildWorktreeCmd(repoDir string, sub string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	wt := &cobra.Command{Use: "worktree"}

	list := &cobra.Command{Use: "list", RunE: runWorktreeList}
	add := &cobra.Command{Use: "add", Args: cobra.RangeArgs(1, 2), RunE: runWorktreeAdd}
	add.Flags().BoolP("new", "b", false, "")
	add.Flags().String("from", "", "")
	add.Flags().Bool("detach", false, "")
	rm := &cobra.Command{Use: "remove", Args: cobra.ExactArgs(1), RunE: runWorktreeRemove}
	rm.Flags().BoolP("force", "f", false, "")
	prune := &cobra.Command{Use: "prune", RunE: runWorktreePrune}

	wt.AddCommand(list, add, rm, prune)
	testRoot.AddCommand(wt)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	args := append([]string{"--repo", repoDir, "worktree", sub}, extraArgs...)
	testRoot.SetArgs(args)
	return testRoot, buf
}

// TestWorktree_AddListRemove exercises the full round-trip against a real repo.
func TestWorktree_AddListRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	// The new worktree must live OUTSIDE the repo dir.
	wtPath := filepath.Join(t.TempDir(), "feature-wt")

	// Add a worktree with a brand-new branch.
	root, buf := buildWorktreeCmd(repo.Dir, "add", "-b", wtPath, "feat/wt")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree add failed: %v\nout: %s", err, buf.String())
	}

	// List should now include both entries.
	root2, buf2 := buildWorktreeCmd(repo.Dir, "list")
	if err := root2.Execute(); err != nil {
		t.Fatalf("worktree list failed: %v", err)
	}
	if !strings.Contains(buf2.String(), wtPath) {
		t.Errorf("list missing %s\n%s", wtPath, buf2.String())
	}
	if !strings.Contains(buf2.String(), "feat/wt") {
		t.Errorf("list missing feat/wt branch\n%s", buf2.String())
	}

	// Remove it.
	root3, buf3 := buildWorktreeCmd(repo.Dir, "remove", wtPath)
	if err := root3.Execute(); err != nil {
		t.Fatalf("worktree remove failed: %v\nout: %s", err, buf3.String())
	}
}

// TestWorktree_ListJSON exercises JSON output parsing.
func TestWorktree_ListJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repo.Dir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	wt := &cobra.Command{Use: "worktree"}
	list := &cobra.Command{Use: "list", RunE: runWorktreeList}
	wt.AddCommand(list)
	testRoot.AddCommand(wt)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs([]string{"--repo", repo.Dir, "--json", "worktree", "list"})

	if err := testRoot.Execute(); err != nil {
		t.Fatalf("list --json failed: %v\nout: %s", err, buf.String())
	}

	var entries []WorktreeEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("json unmarshal: %v\nraw: %s", err, buf.String())
	}
	if len(entries) == 0 {
		t.Fatal("expected at least 1 worktree entry")
	}
}

// TestWorktree_AddNewRequiresBranch catches --new without a name.
func TestWorktree_AddNewRequiresBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt")
	root, _ := buildWorktreeCmd(repo.Dir, "add", "-b", wtPath)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when -b given without branch name")
	}
	if !strings.Contains(err.Error(), "requires a branch name") {
		t.Errorf("unexpected err: %v", err)
	}
}
