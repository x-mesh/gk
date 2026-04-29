package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
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

func TestResolveWorktreePath_AbsoluteWins(t *testing.T) {
	cfg := &config.Config{Worktree: config.WorktreeConfig{Base: "/tmp/ignored", Project: "p"}}
	got, err := resolveWorktreePath(context.Background(), &git.FakeRunner{}, cfg, "/explicit/abs")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/explicit/abs" {
		t.Errorf("absolute path should passthrough, got %q", got)
	}
}

func TestResolveWorktreePath_RelativeUsesManagedBase(t *testing.T) {
	home, _ := os.UserHomeDir()
	cfg := &config.Config{Worktree: config.WorktreeConfig{Base: "~/.gk/worktree", Project: "myproj"}}
	got, err := resolveWorktreePath(context.Background(), &git.FakeRunner{}, cfg, "ai-commit")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".gk/worktree", "myproj", "ai-commit")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveWorktreePath_SubdirPreserved(t *testing.T) {
	cfg := &config.Config{Worktree: config.WorktreeConfig{Base: "/tmp/base", Project: "p"}}
	got, err := resolveWorktreePath(context.Background(), &git.FakeRunner{}, cfg, "feat/api")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/base", "p", "feat/api")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveWorktreePath_AutoProjectSlug(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --show-toplevel": {Stdout: "/Users/me/work/agentic/gk\n"},
		},
	}
	cfg := &config.Config{Worktree: config.WorktreeConfig{Base: "/tmp/base", Project: ""}}
	got, err := resolveWorktreePath(context.Background(), fake, cfg, "feat-x")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/base", "gk", "feat-x")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveWorktreePath_EmptyBaseFallsBack(t *testing.T) {
	cfg := &config.Config{Worktree: config.WorktreeConfig{Base: "", Project: "p"}}
	got, err := resolveWorktreePath(context.Background(), &git.FakeRunner{}, cfg, "name")
	if err != nil {
		t.Fatal(err)
	}
	if got != "name" {
		t.Errorf("empty base should return raw input, got %q", got)
	}
}

func TestResolveWorktreePath_NoToplevelFallsBack(t *testing.T) {
	// rev-parse returns empty (or errors) — we must not crash; instead
	// fall through to the cwd-relative behavior.
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --show-toplevel": {Stdout: "", ExitCode: 128},
		},
	}
	cfg := &config.Config{Worktree: config.WorktreeConfig{Base: "/tmp/base", Project: ""}}
	got, err := resolveWorktreePath(context.Background(), fake, cfg, "name")
	if err != nil {
		t.Fatal(err)
	}
	if got != "name" {
		t.Errorf("fallback expected, got %q", got)
	}
}

func TestBranchExists(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"show-ref --verify --quiet refs/heads/main":    {}, // exit 0 → exists
			"show-ref --verify --quiet refs/heads/missing": {ExitCode: 1},
		},
	}
	if !branchExists(context.Background(), fake, "main") {
		t.Error("branchExists(main) = false, want true")
	}
	if branchExists(context.Background(), fake, "missing") {
		t.Error("branchExists(missing) = true, want false")
	}
}

func TestBranchInUse(t *testing.T) {
	porcelain := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /tmp/other\nHEAD def\nbranch refs/heads/feat\n"
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"worktree list --porcelain": {Stdout: porcelain},
		},
	}
	if !branchInUse(context.Background(), fake, "feat") {
		t.Error("branchInUse(feat) = false, want true")
	}
	if branchInUse(context.Background(), fake, "unused") {
		t.Error("branchInUse(unused) = true, want false")
	}
}

func TestNonEmptyDirExists(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent")
	if got, err := nonEmptyDirExists(absent); err != nil || got {
		t.Errorf("absent: got=%v err=%v, want (false, nil)", got, err)
	}

	emptyDir := t.TempDir()
	if got, err := nonEmptyDirExists(emptyDir); err != nil || got {
		t.Errorf("empty dir: got=%v err=%v, want (false, nil)", got, err)
	}

	nonEmpty := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonEmpty, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := nonEmptyDirExists(nonEmpty); err != nil || !got {
		t.Errorf("non-empty dir: got=%v err=%v, want (true, nil)", got, err)
	}

	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := nonEmptyDirExists(file); err != nil || !got {
		t.Errorf("file path: got=%v err=%v, want (true, nil)", got, err)
	}
}

func TestFindWorktreeEntry(t *testing.T) {
	entries := []WorktreeEntry{
		{Path: "/a", Branch: "main"},
		{Path: "/b", Branch: "feat"},
	}
	if got := findWorktreeEntry(entries, "/b"); got == nil || got.Branch != "feat" {
		t.Errorf("hit: got %+v", got)
	}
	if got := findWorktreeEntry(entries, "/missing"); got != nil {
		t.Errorf("miss: got %+v, want nil", got)
	}
}

func TestOrphanBranchTip_Formats(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"log -1 --format=%h\x1f%s\x1f%ar refs/heads/feat": {
				Stdout: "abc1234\x1ffix: handle X\x1f2 hours ago\n",
			},
		},
	}
	got := orphanBranchTip(context.Background(), fake, "feat")
	for _, want := range []string{"abc1234", "fix: handle X", "2 hours ago"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestOrphanBranchTip_EmptyOnError(t *testing.T) {
	fake := &git.FakeRunner{
		DefaultResp: git.FakeResponse{ExitCode: 128},
	}
	if got := orphanBranchTip(context.Background(), fake, "none"); got != "" {
		t.Errorf("expected empty on error, got %q", got)
	}
}

func TestPromptOrphanBranchResolution_NonTTYSurfacesError(t *testing.T) {
	// Tests run without a real TTY, so the interactive path short-
	// circuits with a helpful error pointing at `git branch -D`.
	_, err := promptOrphanBranchResolution("ai-commit", "tip: abc  feat: X  · 2h")
	if err == nil {
		t.Fatal("expected non-TTY error, got nil")
	}
	for _, want := range []string{"ai-commit", "git branch -D"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %q", want, err.Error())
		}
	}
}

func TestWorktreeTUIRemove_BareRefuses(t *testing.T) {
	// Bare worktrees must be refused up front — git would anyway,
	// but the message is clearer coming from gk.
	runner := &git.ExecRunner{Dir: t.TempDir()}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetErr(buf)
	err := worktreeTUIRemove(context.Background(), runner, cmd, WorktreeEntry{Path: "/tmp/fake", Bare: true})
	if err == nil || !strings.Contains(err.Error(), "bare") {
		t.Errorf("expected bare-refusal error, got %v", err)
	}
}

func TestWorktreeTUI_NonTTYFallsBackToHelp(t *testing.T) {
	// When stdin/stdout is not a TTY (as in `go test`), bare `gk wt`
	// must not attempt to draw an interactive UI. Instead it prints
	// the usage help and returns nil. We verify by executing the
	// TUI handler directly with a fresh cobra command and checking
	// that its output contains the Long description.
	cmd := &cobra.Command{Use: "worktree"}
	cmd.Long = "Worktree management helpers.\n\nWith no subcommand, gk opens an interactive TUI."
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetContext(context.Background())

	if err := runWorktreeTUI(cmd, nil); err != nil {
		t.Fatalf("runWorktreeTUI non-TTY: unexpected error %v", err)
	}
	if !strings.Contains(buf.String(), "interactive TUI") {
		t.Errorf("expected fallback help in output, got:\n%s", buf.String())
	}
}

func TestResolveWorktreePath_RejectsProjectWithSeparator(t *testing.T) {
	cases := []string{"ev/il", "..", "../../etc", "with\\back"}
	for _, bad := range cases {
		cfg := &config.Config{Worktree: config.WorktreeConfig{Base: "/tmp/base", Project: bad}}
		if _, err := resolveWorktreePath(context.Background(), &git.FakeRunner{}, cfg, "x"); err == nil {
			t.Errorf("project %q should be rejected", bad)
		}
	}
}

// --- worktreeDiffsFromBranches ---

func TestWorktreeDiffsFromBranches(t *testing.T) {
	t.Parallel()
	branches := []branchInfo{
		{Name: "feat/a", Ahead: 3, Behind: 1},
		{Name: "feat/b", Ahead: 0, Behind: 0}, // synced — excluded
		{Name: "main", Ahead: 0, Behind: 0},
	}
	entries := []WorktreeEntry{
		{Path: "/wt/a", Branch: "feat/a"},
		{Path: "/wt/b", Branch: "feat/b"},                        // synced — excluded
		{Path: "/wt/c", Branch: "feat/missing"},                  // not in branches — excluded
		{Path: "/wt/d", Branch: "feat/detached", Detached: true}, // detached — excluded
		{Path: "/wt/e", Bare: true, Branch: "main"},              // bare — excluded
		{Path: "/wt/f", Branch: ""},                              // empty branch — excluded
	}
	got := worktreeDiffsFromBranches(entries, branches)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(got), got)
	}
	if got["feat/a"] != [2]int{3, 1} {
		t.Errorf("feat/a: want [3 1], got %+v", got["feat/a"])
	}
}

func TestWorktreeDiffsFromBranches_EmptyInputs(t *testing.T) {
	t.Parallel()
	if got := worktreeDiffsFromBranches(nil, nil); len(got) != 0 {
		t.Errorf("nil/nil should yield empty map, got %+v", got)
	}
	if got := worktreeDiffsFromBranches(
		[]WorktreeEntry{{Path: "/wt", Branch: "main"}}, nil); len(got) != 0 {
		t.Errorf("empty branches should yield empty map, got %+v", got)
	}
}
