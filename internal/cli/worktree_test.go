package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	add.Flags().Bool("init", false, "")
	add.Flags().Bool("no-init", false, "")
	rm := &cobra.Command{Use: "remove", Args: cobra.ExactArgs(1), RunE: runWorktreeRemove}
	rm.Flags().BoolP("force", "f", false, "")
	rm.Flags().Bool("force-locked", false, "")
	prune := &cobra.Command{Use: "prune", RunE: runWorktreePrune}
	initc := &cobra.Command{Use: "init", Args: cobra.RangeArgs(0, 1), RunE: runWorktreeInit}
	initc.Flags().Bool("save", false, "")

	wt.AddCommand(list, add, rm, prune, initc,
		newWorktreeAcquireCmd(),
		newWorktreeRunCmd(),
		newWorktreeFinishCmd(),
		newWorktreeCleanupCmd(),
	)
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
	// The PATH column compactPath-ellipsises long temp dirs but always
	// preserves the basename, so assert on that.
	if !strings.Contains(buf2.String(), filepath.Base(wtPath)) {
		t.Errorf("list missing worktree basename %q\n%s", filepath.Base(wtPath), buf2.String())
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

// TestWorktreeAdd_DryRunNoSideEffect guards the fixed bug: --dry-run must
// describe the plan without creating a worktree or branch.
func TestWorktreeAdd_DryRunNoSideEffect(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "dry-wt")

	root, buf := buildWorktreeCmd(repo.Dir, "add", "--dry-run", "-b", wtPath, "feat/dry")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree add --dry-run failed: %v\nout: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "would add worktree") {
		t.Errorf("dry-run output missing plan line:\n%s", buf.String())
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the worktree at %s (stat err=%v)", wtPath, err)
	}
	if branchExists(context.Background(), &git.ExecRunner{Dir: repo.Dir}, "feat/dry") {
		t.Errorf("dry-run created branch feat/dry")
	}
}

// TestWorktreeAdd_DryRunJSONEnvelope checks the agent envelope shape and the
// no-side-effect contract together.
func TestWorktreeAdd_DryRunJSONEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "dry-json-wt")

	flagAgent = true
	t.Cleanup(func() { flagAgent = false })

	root, buf := buildWorktreeCmd(repo.Dir, "add", "--json", "--dry-run", "-b", wtPath, "feat/dj")
	if err := root.Execute(); err != nil {
		t.Fatalf("add --json --dry-run failed: %v\nout: %s", err, buf.String())
	}

	var env struct {
		OK     bool            `json:"ok"`
		Result worktreeAddJSON `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("envelope unmarshal: %v\nraw: %s", err, buf.String())
	}
	if !env.OK {
		t.Errorf("envelope ok=false:\n%s", buf.String())
	}
	if !env.Result.DryRun || !env.Result.Created || env.Result.Branch != "feat/dj" {
		t.Errorf("unexpected dry-run result: %+v", env.Result)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run --json created the worktree at %s", wtPath)
	}
}

// TestWorktreeAdd_JSON verifies the bare result payload for a real add,
// including the init outcome field.
func TestWorktreeAdd_JSON(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "json-wt")

	root, buf := buildWorktreeCmd(repo.Dir, "add", "--json", "--no-init", "-b", wtPath, "feat/js")
	if err := root.Execute(); err != nil {
		t.Fatalf("add --json failed: %v\nout: %s", err, buf.String())
	}
	var res worktreeAddJSON
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("result unmarshal: %v\nraw: %s", err, buf.String())
	}
	if filepath.Base(res.Path) != "json-wt" || res.Branch != "feat/js" || !res.Created || res.Init != "skipped" {
		t.Errorf("unexpected result: %+v", res)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("add did not create worktree at %s: %v", wtPath, err)
	}
	// Remove it so the temp repo tears down cleanly.
	rm, rbuf := buildWorktreeCmd(repo.Dir, "remove", wtPath)
	if err := rm.Execute(); err != nil {
		t.Fatalf("remove failed: %v\nout: %s", err, rbuf.String())
	}
}

// TestDirtyPtrIfAny covers the clean→nil / dirty→non-nil contract that drives
// the omitempty dirty field on context and list entries.
func TestDirtyPtrIfAny(t *testing.T) {
	if dirtyPtrIfAny(contextDirtyJSON{}) != nil {
		t.Error("clean tree should yield nil")
	}
	if got := dirtyPtrIfAny(contextDirtyJSON{Untracked: 1}); got == nil || got.Untracked != 1 {
		t.Errorf("dirty tree should yield non-nil, got %+v", got)
	}
}

// TestWorktreeList_JSONEnriched verifies the list --json payload carries the
// current mark and a dirty block for a worktree with uncommitted work.
func TestWorktreeList_JSONEnriched(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	if err := os.WriteFile(filepath.Join(repo.Dir, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, buf := buildWorktreeCmd(repo.Dir, "list", "--json")
	if err := root.Execute(); err != nil {
		t.Fatalf("list --json: %v\nout: %s", err, buf.String())
	}
	var entries []worktreeListEntryJSON
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	var cur *worktreeListEntryJSON
	for i := range entries {
		if entries[i].Current {
			cur = &entries[i]
		}
	}
	if cur == nil {
		t.Fatalf("no current worktree marked in %d entries", len(entries))
	}
	if cur.Dirty == nil || cur.Dirty.Untracked < 1 {
		t.Errorf("expected untracked dirty on current worktree, got %+v", cur.Dirty)
	}
}

// TestRunInWorktree_ExitCode covers exit-code reporting and the
// could-not-start error path.
func TestRunInWorktree_ExitCode(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if code, err := runInWorktree(ctx, dir, []string{"true"}, true); err != nil || code != 0 {
		t.Errorf("true: code=%d err=%v", code, err)
	}
	if code, err := runInWorktree(ctx, dir, []string{"false"}, true); err != nil || code != 1 {
		t.Errorf("false: code=%d err=%v", code, err)
	}
	if _, err := runInWorktree(ctx, dir, []string{"gk-no-such-binary-zzz"}, true); err == nil {
		t.Error("expected error for a command that cannot start")
	}
}

// TestWorktreeRun_CreatesRunsCleansUp exercises the full isolated-task
// transaction: create a worktree, run a command in it, reclaim on success.
func TestWorktreeRun_CreatesRunsCleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	// Keep the managed worktree base inside the test sandbox.
	t.Setenv("GK_WORKTREE_BASE", filepath.Join(t.TempDir(), "wtbase"))

	root, buf := buildWorktreeCmd(repo.Dir, "run", "--json", "iso-task", "--cleanup", "--no-init", "--", "true")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree run: %v\nout: %s", err, buf.String())
	}
	var res worktreeRunJSON
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if !res.Created || res.ExitCode != 0 || !res.Removed || res.Branch != "iso-task" || res.Init != "skipped" {
		t.Errorf("unexpected result: %+v", res)
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Errorf("cleanup left the worktree at %s (stat err=%v)", res.Path, err)
	}
	if branchExists(context.Background(), &git.ExecRunner{Dir: repo.Dir}, "iso-task") {
		t.Error("cleanup left the created branch iso-task behind")
	}
}

func TestWorktreeAcquire_CreatesInitializesAndReuses(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	t.Setenv("GK_WORKTREE_BASE", filepath.Join(t.TempDir(), "wtbase"))
	repo.WriteFile(".gk.yaml", "worktree:\n  init:\n    run:\n      - touch INIT_RAN\n")

	root, buf := buildWorktreeCmd(repo.Dir, "acquire", "--json", "agent-task")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree acquire: %v\nout: %s", err, buf.String())
	}
	var first worktreeAcquireJSON
	if err := json.Unmarshal(buf.Bytes(), &first); err != nil {
		t.Fatalf("unmarshal first acquire: %v\nraw: %s", err, buf.String())
	}
	if !first.Created || first.Reused || first.Branch != "agent-task" || first.Init != "done" {
		t.Fatalf("unexpected first acquire result: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(first.Path, "INIT_RAN")); err != nil {
		t.Fatalf("acquire did not run worktree.init in %s: %v", first.Path, err)
	}

	root2, buf2 := buildWorktreeCmd(repo.Dir, "acquire", "--json", "agent-task")
	if err := root2.Execute(); err != nil {
		t.Fatalf("worktree acquire reuse: %v\nout: %s", err, buf2.String())
	}
	var second worktreeAcquireJSON
	if err := json.Unmarshal(buf2.Bytes(), &second); err != nil {
		t.Fatalf("unmarshal second acquire: %v\nraw: %s", err, buf2.String())
	}
	if second.Created || !second.Reused || !sameDir(second.Path, first.Path) || second.Init != "done" {
		t.Fatalf("unexpected second acquire result: %+v", second)
	}
}

func TestWorktreeCleanup_DryRunAndApply(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "done-wt")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", wtPath, "done")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree add: %v\nout: %s", err, buf.String())
	}

	root2, buf2 := buildWorktreeCmd(repo.Dir, "cleanup", "--json", "--delete-branches")
	if err := root2.Execute(); err != nil {
		t.Fatalf("worktree cleanup dry-run: %v\nout: %s", err, buf2.String())
	}
	var dry worktreeCleanupJSON
	if err := json.Unmarshal(buf2.Bytes(), &dry); err != nil {
		t.Fatalf("unmarshal cleanup dry-run: %v\nraw: %s", err, buf2.String())
	}
	if !dry.DryRun || len(dry.Candidates) != 1 || dry.Candidates[0].Branch != "done" {
		t.Fatalf("unexpected cleanup dry-run: %+v", dry)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("dry-run removed worktree: %v", err)
	}

	root3, buf3 := buildWorktreeCmd(repo.Dir, "cleanup", "--json", "--delete-branches", "-y")
	if err := root3.Execute(); err != nil {
		t.Fatalf("worktree cleanup apply: %v\nout: %s", err, buf3.String())
	}
	var applied worktreeCleanupJSON
	if err := json.Unmarshal(buf3.Bytes(), &applied); err != nil {
		t.Fatalf("unmarshal cleanup apply: %v\nraw: %s", err, buf3.String())
	}
	if applied.DryRun || len(applied.Removed) != 1 || !applied.Removed[0].BranchDeleted {
		t.Fatalf("unexpected cleanup apply: %+v", applied)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup left worktree at %s (stat err=%v)", wtPath, err)
	}
	if branchExists(context.Background(), &git.ExecRunner{Dir: repo.Dir}, "done") {
		t.Fatal("cleanup left deleted branch behind")
	}
}

func TestWorktreeFinishChildArgs(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
	}}
	cfg := &config.Config{Remote: "origin"}

	mode, args, to, err := finishChildArgs(context.Background(), r, cfg, "parent", false, true)
	if err != nil {
		t.Fatalf("finishChildArgs parent: %v", err)
	}
	if mode != "promote" || strings.Join(args, " ") != "promote --autostash" || to != "parent" {
		t.Fatalf("unexpected parent args: mode=%s args=%v to=%s", mode, args, to)
	}

	mode, args, to, err = finishChildArgs(context.Background(), r, cfg, "base", false, false)
	if err != nil {
		t.Fatalf("finishChildArgs base: %v", err)
	}
	if mode != "promote" || strings.Join(args, " ") != "promote main" || to != "main" {
		t.Fatalf("unexpected base args: mode=%s args=%v to=%s", mode, args, to)
	}

	mode, args, to, err = finishChildArgs(context.Background(), r, cfg, "parent", true, false)
	if err != nil {
		t.Fatalf("finishChildArgs push: %v", err)
	}
	if mode != "land" || strings.Join(args, " ") != "land --to parent" || to != "parent" {
		t.Fatalf("unexpected push args: mode=%s args=%v to=%s", mode, args, to)
	}
}

func TestParseWorktreeStale(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"12h", 12 * time.Hour, false},
		{"", 0, false}, // not provided → filter disabled, no error
		{"bad", 0, true},
		{"7x", 0, true},
		{"0d", 0, true},  // non-positive must error, not silently disable
		{"-3d", 0, true}, // negative must error
	}
	for _, c := range cases {
		got, err := parseWorktreeStale(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseWorktreeStale(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseWorktreeStale(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseWorktreeStale(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// cleanupSkippedFor reports whether the report skipped <branch> for <reason>.
func cleanupSkippedFor(rep worktreeCleanupJSON, branch, reason string) bool {
	for _, s := range rep.Skipped {
		if s.Branch != branch {
			continue
		}
		for _, r := range s.Reasons {
			if r == reason {
				return true
			}
		}
	}
	return false
}

// TestCleanupFinishedWorktree_RemovesLinkedAndRefusesMain covers the
// destructive finish-cleanup helper directly: it must refuse the main worktree
// and remove a linked worktree (and its branch with --delete-branch).
func TestCleanupFinishedWorktree_RemovesLinkedAndRefusesMain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	ctx := context.Background()
	runner := &git.ExecRunner{Dir: repo.Dir}

	mainPath, err := mainWorktreePath(ctx, runner)
	if err != nil {
		t.Fatalf("main worktree path: %v", err)
	}
	if _, _, err := cleanupFinishedWorktree(ctx, runner, mainPath, "main", false); err == nil {
		t.Fatal("expected refusal to remove the main worktree")
	}
	if _, statErr := os.Stat(mainPath); statErr != nil {
		t.Fatalf("main worktree disappeared after refusal: %v", statErr)
	}

	wtPath := filepath.Join(t.TempDir(), "finish-wt")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", wtPath, "feat-finish")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree add: %v\nout: %s", err, buf.String())
	}
	removed, branchDeleted, err := cleanupFinishedWorktree(ctx, runner, wtPath, "feat-finish", true)
	if err != nil {
		t.Fatalf("cleanupFinishedWorktree: %v", err)
	}
	if !removed || !branchDeleted {
		t.Fatalf("removed=%v branchDeleted=%v, want both true", removed, branchDeleted)
	}
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Fatalf("linked worktree not removed (stat err=%v)", statErr)
	}
	if branchExists(ctx, runner, "feat-finish") {
		t.Fatal("branch feat-finish not deleted")
	}
}

// TestWorktreeCleanup_SkipsDirtyAndUnmerged proves the two safety guards that
// keep cleanup from reclaiming uncommitted or un-integrated work.
func TestWorktreeCleanup_SkipsDirtyAndUnmerged(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	// Dirty: a modified tracked file must keep the worktree out of candidates.
	dirtyPath := filepath.Join(t.TempDir(), "dirty-wt")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", dirtyPath, "dirty-wt")
	if err := root.Execute(); err != nil {
		t.Fatalf("add dirty worktree: %v\nout: %s", err, buf.String())
	}
	if err := os.WriteFile(filepath.Join(dirtyPath, ".gkkeep", "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("dirty the worktree: %v", err)
	}

	// Unmerged: a branch whose tip is ahead of base must be skipped under --merged.
	repo.CreateBranch("ahead")
	repo.WriteFile("ahead.txt", "x\n")
	repo.Commit("ahead commit")
	repo.Checkout("main")
	aheadPath := filepath.Join(t.TempDir(), "ahead-wt")
	rootA, bufA := buildWorktreeCmd(repo.Dir, "add", "--no-init", aheadPath, "ahead")
	if err := rootA.Execute(); err != nil {
		t.Fatalf("add ahead worktree: %v\nout: %s", err, bufA.String())
	}

	rootC, bufC := buildWorktreeCmd(repo.Dir, "cleanup", "--json")
	if err := rootC.Execute(); err != nil {
		t.Fatalf("cleanup: %v\nout: %s", err, bufC.String())
	}
	var rep worktreeCleanupJSON
	if err := json.Unmarshal(bufC.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal cleanup: %v\nraw: %s", err, bufC.String())
	}
	if len(rep.Candidates) != 0 {
		t.Fatalf("expected no candidates, got %+v", rep.Candidates)
	}
	if !cleanupSkippedFor(rep, "dirty-wt", "dirty") {
		t.Errorf("dirty worktree not skipped for dirty: %+v", rep.Skipped)
	}
	if !cleanupSkippedFor(rep, "ahead", "unmerged") {
		t.Errorf("ahead worktree not skipped for unmerged: %+v", rep.Skipped)
	}
}

// TestWorktreeCleanup_SkipsProtected proves a config-protected branch is never
// a removal candidate.
func TestWorktreeCleanup_SkipsProtected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile(".gk.yaml", "branch:\n  protected:\n    - release\n")

	relPath := filepath.Join(t.TempDir(), "release-wt")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", relPath, "release")
	if err := root.Execute(); err != nil {
		t.Fatalf("add release worktree: %v\nout: %s", err, buf.String())
	}
	rootC, bufC := buildWorktreeCmd(repo.Dir, "cleanup", "--json")
	if err := rootC.Execute(); err != nil {
		t.Fatalf("cleanup: %v\nout: %s", err, bufC.String())
	}
	var rep worktreeCleanupJSON
	if err := json.Unmarshal(bufC.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, bufC.String())
	}
	if !cleanupSkippedFor(rep, "release", "protected") {
		t.Fatalf("protected branch not skipped: candidates=%+v skipped=%+v", rep.Candidates, rep.Skipped)
	}
}

// TestWorktreeFinish_FullFlowWithFakeChild exercises the whole finish path —
// child promote + linked-worktree removal + branch deletion — by faking the
// promote/land child process (no real gk binary needed).
func TestWorktreeFinish_FullFlowWithFakeChild(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	ctx := context.Background()
	runner := &git.ExecRunner{Dir: repo.Dir}

	wtPath := filepath.Join(t.TempDir(), "feat-wt")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", wtPath, "feat-x")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree add: %v\nout: %s", err, buf.String())
	}

	var childArgs [][]string
	prev := landRunChild
	landRunChild = func(_ context.Context, _, _ string, _ bool, args ...string) error {
		childArgs = append(childArgs, args)
		return nil
	}
	t.Cleanup(func() { landRunChild = prev })

	// finish operates on the CURRENT worktree, so point --repo at the linked tree.
	fin, finBuf := buildWorktreeCmd(wtPath, "finish", "--json", "--cleanup", "--delete-branch")
	if err := fin.Execute(); err != nil {
		t.Fatalf("worktree finish: %v\nout: %s", err, finBuf.String())
	}
	var res worktreeFinishJSON
	if err := json.Unmarshal(finBuf.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal finish: %v\nraw: %s", err, finBuf.String())
	}
	if res.Mode != "promote" || res.To != "parent" || res.Branch != "feat-x" {
		t.Fatalf("unexpected finish result: %+v", res)
	}
	if !res.Removed || !res.BranchDeleted {
		t.Fatalf("finish did not clean up: %+v", res)
	}
	if len(childArgs) != 1 || len(childArgs[0]) == 0 || childArgs[0][0] != "promote" {
		t.Fatalf("unexpected child invocations: %v", childArgs)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("finish left the worktree at %s (stat err=%v)", wtPath, err)
	}
	if branchExists(ctx, runner, "feat-x") {
		t.Fatal("finish left branch feat-x behind")
	}
}

// TestWorktreeCleanup_SkipsLockedLiveAndStale proves the lock guards: a
// live-locked worktree and a stale-locked one (without --force-stale-locks)
// are both kept out of the candidate set.
func TestWorktreeCleanup_SkipsLockedLiveAndStale(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	livePath := filepath.Join(t.TempDir(), "live-wt")
	repo.RunGit("worktree", "add", livePath, "-b", "live")
	repo.RunGit("worktree", "lock", "--reason", fmt.Sprintf("claude agent (pid %d)", os.Getpid()), livePath)

	stalePath := filepath.Join(t.TempDir(), "stale-wt")
	repo.RunGit("worktree", "add", stalePath, "-b", "stale")
	repo.RunGit("worktree", "lock", "--reason", "claude agent (pid 999999)", stalePath)

	root, buf := buildWorktreeCmd(repo.Dir, "cleanup", "--json")
	if err := root.Execute(); err != nil {
		t.Fatalf("cleanup: %v\nout: %s", err, buf.String())
	}
	var rep worktreeCleanupJSON
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if !cleanupSkippedFor(rep, "live", "locked-live") {
		t.Errorf("live-locked worktree not skipped: %+v", rep.Skipped)
	}
	if !cleanupSkippedFor(rep, "stale", "locked-stale") {
		t.Errorf("stale-locked worktree not skipped: %+v", rep.Skipped)
	}
}

// TestWorktreeFinish_ValidationAndDryRun covers the command-level guards that
// run before any promote/land child process.
func TestWorktreeFinish_ValidationAndDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	root, buf := buildWorktreeCmd(repo.Dir, "finish", "--delete-branch")
	if err := root.Execute(); err == nil {
		t.Fatalf("expected error for --delete-branch without --cleanup; out: %s", buf.String())
	}

	root2, buf2 := buildWorktreeCmd(repo.Dir, "finish", "--json", "--dry-run")
	if err := root2.Execute(); err != nil {
		t.Fatalf("finish dry-run: %v\nout: %s", err, buf2.String())
	}
	var res worktreeFinishJSON
	if err := json.Unmarshal(buf2.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal finish dry-run: %v\nraw: %s", err, buf2.String())
	}
	if !res.DryRun || res.Mode != "promote" || res.To != "parent" || res.Branch != "main" {
		t.Fatalf("unexpected dry-run result: %+v", res)
	}
}

// TestWorktreeRun_InitDefaultAndExplicit pins the changed `run --init`
// semantics: no init by default, init on create with --init, and re-init on a
// reused worktree.
func TestWorktreeRun_InitDefaultAndExplicit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	t.Setenv("GK_WORKTREE_BASE", filepath.Join(t.TempDir(), "wtbase"))
	repo.WriteFile(".gk.yaml", "worktree:\n  init:\n    run:\n      - touch INIT_RAN\n")

	root, buf := buildWorktreeCmd(repo.Dir, "run", "--json", "no-init-task", "--", "true")
	if err := root.Execute(); err != nil {
		t.Fatalf("run no-init: %v\nout: %s", err, buf.String())
	}
	var a worktreeRunJSON
	if err := json.Unmarshal(buf.Bytes(), &a); err != nil {
		t.Fatalf("unmarshal no-init: %v\nraw: %s", err, buf.String())
	}
	if a.Init != "skipped" {
		t.Fatalf("run without --init: Init=%q, want skipped", a.Init)
	}
	if _, err := os.Stat(filepath.Join(a.Path, "INIT_RAN")); !os.IsNotExist(err) {
		t.Fatalf("run without --init still bootstrapped (stat err=%v)", err)
	}

	root2, buf2 := buildWorktreeCmd(repo.Dir, "run", "--json", "--init", "init-task", "--", "true")
	if err := root2.Execute(); err != nil {
		t.Fatalf("run --init: %v\nout: %s", err, buf2.String())
	}
	var b worktreeRunJSON
	if err := json.Unmarshal(buf2.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal --init: %v\nraw: %s", err, buf2.String())
	}
	if b.Init != "done" {
		t.Fatalf("run --init: Init=%q, want done", b.Init)
	}
	initFile := filepath.Join(b.Path, "INIT_RAN")
	if _, err := os.Stat(initFile); err != nil {
		t.Fatalf("run --init did not bootstrap: %v", err)
	}

	// Reuse: --init re-applies bootstrap to an existing worktree.
	if err := os.Remove(initFile); err != nil {
		t.Fatalf("remove init marker: %v", err)
	}
	root3, buf3 := buildWorktreeCmd(repo.Dir, "run", "--json", "--init", "init-task", "--", "true")
	if err := root3.Execute(); err != nil {
		t.Fatalf("run --init reuse: %v\nout: %s", err, buf3.String())
	}
	var c worktreeRunJSON
	if err := json.Unmarshal(buf3.Bytes(), &c); err != nil {
		t.Fatalf("unmarshal reuse: %v\nraw: %s", err, buf3.String())
	}
	if c.Created || c.Init != "done" {
		t.Fatalf("run --init reuse: %+v, want created=false init=done", c)
	}
	if _, err := os.Stat(initFile); err != nil {
		t.Fatalf("run --init did not re-bootstrap on reuse: %v", err)
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
	buf := &bytes.Buffer{}
	err := worktreeTUIRemove(context.Background(), runner, buf, WorktreeEntry{Path: "/tmp/fake", Bare: true}, nil)
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

// --- worktree list helpers (sw-style columns) ---

func TestWorktreeSourceLabel(t *testing.T) {
	cases := []struct {
		name string
		meta worktreeBranchMeta
		want string
	}{
		{"upstream wins", worktreeBranchMeta{Upstream: "origin/main", ForkBranch: "main", ForkPoint: "abc1234"}, "⇄ origin/main"},
		{"fork fallback", worktreeBranchMeta{ForkBranch: "main", ForkPoint: "abc1234"}, "from main@abc1234"},
		{"local only", worktreeBranchMeta{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := worktreeSourceLabel(tc.meta); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Creating a branch via worktree add must record its fork parent so
// SOURCE / gk status / gk land --promote resolve against the real base.
func TestWorktreeAdd_RecordsParent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("develop")
	repo.WriteFile("d.txt", "d")
	repo.Commit("develop commit")
	// develop stays checked out → blank base records the current branch.

	// -b without --from → parent = current branch (develop).
	wiz := filepath.Join(t.TempDir(), "wiz")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "-b", wiz, "feat/wiz")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree add failed: %v\nout: %s", err, buf.String())
	}
	if got := strings.TrimSpace(repo.RunGit("config", "branch.feat/wiz.gk-parent")); got != "develop" {
		t.Errorf("gk-parent = %q, want develop", got)
	}

	// --from main → parent = main.
	fromMain := filepath.Join(t.TempDir(), "from-main")
	root2, buf2 := buildWorktreeCmd(repo.Dir, "add", "-b", "--from", "main", fromMain, "feat/from-main")
	if err := root2.Execute(); err != nil {
		t.Fatalf("worktree add --from failed: %v\nout: %s", err, buf2.String())
	}
	if got := strings.TrimSpace(repo.RunGit("config", "branch.feat/from-main.gk-parent")); got != "main" {
		t.Errorf("gk-parent = %q, want main", got)
	}

	// Auto-created branch (no -b, basename) → parent = current branch.
	auto := filepath.Join(t.TempDir(), "auto-br")
	root3, buf3 := buildWorktreeCmd(repo.Dir, "add", auto)
	if err := root3.Execute(); err != nil {
		t.Fatalf("worktree add auto failed: %v\nout: %s", err, buf3.String())
	}
	if got := strings.TrimSpace(repo.RunGit("config", "branch.auto-br.gk-parent")); got != "develop" {
		t.Errorf("gk-parent = %q, want develop", got)
	}

	// Checking out an existing branch creates nothing → records nothing.
	existing := filepath.Join(t.TempDir(), "existing")
	root4, buf4 := buildWorktreeCmd(repo.Dir, "add", existing, "main")
	if err := root4.Execute(); err != nil {
		t.Fatalf("worktree add existing failed: %v\nout: %s", err, buf4.String())
	}
	if out, err := repo.TryGit("config", "branch.main.gk-parent"); err == nil {
		t.Errorf("existing-branch checkout must not record a parent, got %q", out)
	}
}

// recordWorktreeParent only records local-branch bases; everything else
// (remote refs, SHAs, detached HEAD) must silently record nothing.
func TestRecordWorktreeParent_SkipsNonLocalBase(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/x")
	repo.Checkout("main")
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	noRecord := func(label, from string) {
		t.Helper()
		recordWorktreeParent(ctx, runner, "feat/x", from)
		if out, err := repo.TryGit("config", "branch.feat/x.gk-parent"); err == nil {
			t.Fatalf("%s must not record, got %q", label, out)
		}
	}
	noRecord("remote-like base", "origin/main")
	noRecord("raw SHA base", strings.TrimSpace(repo.RunGit("rev-parse", "HEAD")))

	repo.RunGit("checkout", "--detach")
	noRecord("detached HEAD with blank base", "")
	repo.Checkout("main")

	// Local branch base records; refs/heads/ prefix is normalized.
	recordWorktreeParent(ctx, runner, "feat/x", "refs/heads/main")
	if got := strings.TrimSpace(repo.RunGit("config", "branch.feat/x.gk-parent")); got != "main" {
		t.Errorf("gk-parent = %q, want main", got)
	}
}

// worktreeAddDetail decorates the success line with branch + base; every
// git failure must degrade to "" rather than block the message.
func TestWorktreeAddDetail(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	head := strings.TrimSpace(repo.RunGit("rev-parse", "--short", "HEAD"))
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	cases := []struct {
		name      string
		newBranch bool
		branch    string
		from      string
		detach    bool
		want      string
	}{
		{"new branch from HEAD", true, "feat/x", "", false,
			" (new branch feat/x from main@" + head + ")"},
		{"new branch from explicit ref", true, "feat/y", "main", false,
			" (new branch feat/y from main@" + head + ")"},
		{"existing branch", false, "main", "", false,
			" (main@" + head + ")"},
		{"detached", false, "", "", true,
			" (detached @" + head + ")"},
		{"unresolvable ref degrades to empty", false, "nope", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := worktreeAddDetail(ctx, runner, tc.newBranch, tc.branch, tc.from, tc.detach)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCompactPathPreservesBasename(t *testing.T) {
	long := "/Users/jinwoo/very/deeply/nested/path/that/exceeds/the/cap/feature-branch-with-long-name"
	got := compactPath(long, 60)
	if !strings.HasSuffix(got, "feature-branch-with-long-name") {
		t.Errorf("compactPath should preserve basename, got %q", got)
	}
	if runeLen(got) > 60 {
		t.Errorf("compactPath should respect cap, got len=%d (%q)", runeLen(got), got)
	}
}

func TestCompactPathShortStaysIntact(t *testing.T) {
	short := "/Users/jinwoo/work/gk"
	if got := compactPath(short, 60); got != short {
		t.Errorf("short path mutated: %q", got)
	}
}

func TestRenderWorktreeRowsHeaderAndCurrentMarker(t *testing.T) {
	rows := []worktreeRow{
		{Current: true, Branch: "main", Source: "⇄ origin/main", Path: "/repo"},
		{Branch: "feat/x", Source: "from main@abc1234", Diff: "↑3", Age: "2h", Path: "/repo/wt"},
	}
	out := renderWorktreeRows(rows)
	if len(out) < 3 {
		t.Fatalf("expected header + 2 rows, got %d lines", len(out))
	}
	header := stripANSIForWidth(out[0])
	for _, want := range []string{"BRANCH", "SOURCE", "DIFF", "AGE", "PATH"} {
		if !strings.Contains(header, want) {
			t.Errorf("header missing %q\n%s", want, header)
		}
	}
	if !strings.Contains(out[1], "★") {
		t.Errorf("current row should carry ★ marker\n%s", out[1])
	}
	if strings.Contains(out[2], "★") {
		t.Errorf("non-current row should not carry ★\n%s", out[2])
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

// TestWorktreeColumnPriority pins the responsive-drop intent for `gk wt`:
// BRANCH is the identity column (highest weight), AGE outranks HASH so a
// narrowing terminal sheds the bare SHA before the glanceable age, and HASH
// is the first to go.
func TestWorktreeColumnPriority(t *testing.T) {
	p := worktreeColumnPriority()
	if p["BRANCH"] <= p["AGE"] || p["AGE"] <= p["HASH"] {
		t.Fatalf("want BRANCH > AGE > HASH, got BRANCH=%d AGE=%d HASH=%d",
			p["BRANCH"], p["AGE"], p["HASH"])
	}
	// HASH must be the lowest of the data columns so it drops first.
	for _, k := range []string{"BRANCH", "PROJECT", "AGE", "PATH", "SOURCE", "FLAGS"} {
		if p[k] <= p["HASH"] {
			t.Errorf("%s (%d) should outrank HASH (%d)", k, p[k], p["HASH"])
		}
	}
	// BRANCH is the survivor of last resort.
	for k, v := range p {
		if k != "BRANCH" && v >= p["BRANCH"] {
			t.Errorf("%s (%d) must rank below BRANCH (%d)", k, v, p["BRANCH"])
		}
	}
}
