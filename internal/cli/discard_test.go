package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// buildDiscardCmd wires a minimal cobra root with discard for tests.
func buildDiscardCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	dc := &cobra.Command{Use: "discard <path>...", Args: cobra.MinimumNArgs(1), RunE: runDiscard}
	dc.Flags().Bool("dry-run", false, "")
	testRoot.AddCommand(dc)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs(append([]string{"--repo", repoDir, "discard"}, extraArgs...))
	return testRoot, buf
}

func readRepoFile(t *testing.T, repo *testutil.Repo, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo.Dir, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// The core contract: the worktree change is gone, and the pre-discard content
// survives in the refs/wip snapshot.
func TestDiscard_RestoresIndexVersionAndSnapshotsFirst(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")
	repo.WriteFile("a.txt", "dirty")

	root, buf := buildDiscardCmd(repo.Dir, "a.txt")
	if err := root.Execute(); err != nil {
		t.Fatalf("discard failed: %v\nout: %s", err, buf.String())
	}

	if got := readRepoFile(t, repo, "a.txt"); got != "v1" {
		t.Errorf("a.txt = %q, want restored index version %q", got, "v1")
	}
	// The discarded content must be recoverable from the snapshot.
	if got := repo.RunGit("show", "refs/wip/main:a.txt"); got != "dirty" {
		t.Errorf("snapshot content = %q, want the pre-discard %q", got, "dirty")
	}
	if out := buf.String(); !strings.Contains(out, "refs/wip/main") || !strings.Contains(out, "a.txt") {
		t.Errorf("output must name the snapshot ref and the file, got:\n%s", out)
	}
}

// Staged changes stay staged: discard restores the worktree from the INDEX,
// exactly like `git checkout -- <path>`.
func TestDiscard_KeepsStagedChanges(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")
	repo.WriteFile("a.txt", "staged")
	repo.RunGit("add", "a.txt")
	repo.WriteFile("a.txt", "worktree-only")

	root, buf := buildDiscardCmd(repo.Dir, "a.txt")
	if err := root.Execute(); err != nil {
		t.Fatalf("discard failed: %v\nout: %s", err, buf.String())
	}

	if got := readRepoFile(t, repo, "a.txt"); got != "staged" {
		t.Errorf("a.txt = %q, want the staged (index) version %q", got, "staged")
	}
	if diff := repo.RunGit("diff", "--cached", "--name-only"); !strings.Contains(diff, "a.txt") {
		t.Errorf("staged change must survive the discard, got cached diff %q", diff)
	}
}

// Only the named pathspecs are touched; untracked files are never removed.
func TestDiscard_ScopesToPathspecAndKeepsUntracked(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.WriteFile("b.txt", "v1")
	repo.Commit("v1")
	repo.WriteFile("a.txt", "dirty-a")
	repo.WriteFile("b.txt", "dirty-b")
	repo.WriteFile("new.txt", "untracked")

	root, buf := buildDiscardCmd(repo.Dir, "a.txt")
	if err := root.Execute(); err != nil {
		t.Fatalf("discard failed: %v\nout: %s", err, buf.String())
	}

	if got := readRepoFile(t, repo, "a.txt"); got != "v1" {
		t.Errorf("a.txt = %q, want %q", got, "v1")
	}
	if got := readRepoFile(t, repo, "b.txt"); got != "dirty-b" {
		t.Errorf("b.txt = %q, want untouched %q (outside the pathspec)", got, "dirty-b")
	}
	if got := readRepoFile(t, repo, "new.txt"); got != "untracked" {
		t.Errorf("new.txt = %q, want untracked file left in place", got)
	}
}

// A clean scope is a no-op — and crucially, no snapshot is written for it.
func TestDiscard_NothingToDiscardWritesNoSnapshot(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")

	root, buf := buildDiscardCmd(repo.Dir, "a.txt")
	if err := root.Execute(); err != nil {
		t.Fatalf("discard on clean tree failed: %v\nout: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "nothing to discard") {
		t.Errorf("expected a nothing-to-discard notice, got:\n%s", buf.String())
	}
	if _, err := repo.TryGit("rev-parse", "--verify", "--quiet", "refs/wip/main"); err == nil {
		t.Error("a no-op discard must not create a snapshot ref")
	}
}

// --dry-run lists the targets and changes nothing — no restore, no snapshot.
func TestDiscard_DryRunTouchesNothing(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")
	repo.WriteFile("a.txt", "dirty")

	root, buf := buildDiscardCmd(repo.Dir, "--dry-run", "a.txt")
	if err := root.Execute(); err != nil {
		t.Fatalf("discard --dry-run failed: %v\nout: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "would discard") || !strings.Contains(buf.String(), "a.txt") {
		t.Errorf("dry-run must list the targets, got:\n%s", buf.String())
	}
	if got := readRepoFile(t, repo, "a.txt"); got != "dirty" {
		t.Errorf("a.txt = %q, dry-run must not touch the worktree", got)
	}
	if _, err := repo.TryGit("rev-parse", "--verify", "--quiet", "refs/wip/main"); err == nil {
		t.Error("dry-run must not create a snapshot ref")
	}
}

// The JSON contract carries the discarded paths and the snapshot anchor.
func TestDiscard_JSONContract(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")
	repo.WriteFile("a.txt", "dirty")
	repo.WriteFile("new.txt", "untracked")

	root, buf := buildDiscardCmd(repo.Dir, "--json", ".")
	if err := root.Execute(); err != nil {
		t.Fatalf("discard --json failed: %v\nout: %s", err, buf.String())
	}
	t.Cleanup(func() { flagJSON = false })

	var res discardResultJSON
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("output is not the bare JSON payload: %v\nout: %s", err, buf.String())
	}
	if res.Result != "discarded" || len(res.Discarded) != 1 || res.Discarded[0] != "a.txt" {
		t.Errorf("result = %+v, want discarded [a.txt]", res)
	}
	if res.SnapshotRef != "refs/wip/main" || res.SnapshotSHA == "" {
		t.Errorf("snapshot anchor missing: %+v", res)
	}
	if len(res.UntrackedKept) != 1 || res.UntrackedKept[0] != "new.txt" {
		t.Errorf("untracked_kept = %v, want [new.txt]", res.UntrackedKept)
	}
}

// Conflicted paths are a blocked precondition: discarding a side mid-conflict
// belongs to conflict resolution, not to discard.
func TestDiscard_UnmergedPathsBlock(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "base")
	repo.Commit("base")
	repo.CreateBranch("side")
	repo.WriteFile("a.txt", "side-change")
	repo.Commit("side change")
	repo.Checkout("main")
	repo.WriteFile("a.txt", "main-change")
	repo.Commit("main change")
	if _, err := repo.TryGit("merge", "side"); err == nil {
		t.Fatal("expected the merge to conflict")
	}

	root, buf := buildDiscardCmd(repo.Dir, "a.txt")
	err := root.Execute()
	if err == nil {
		t.Fatalf("discard on an unmerged path must be refused, out:\n%s", buf.String())
	}
	if codeFrom(err) != "unmerged-paths" || stateFrom(err) != envStateBlocked {
		t.Errorf("state=%q code=%q, want blocked/unmerged-paths (err: %v)", stateFrom(err), codeFrom(err), err)
	}
	if got := readRepoFile(t, repo, "a.txt"); !strings.Contains(got, "<<<<<<<") {
		t.Errorf("the conflicted file must be left untouched, got %q", got)
	}
}

// Review F1: the abort-on-snapshot-failure path is the promptless verb's
// whole justification — a failed snapshot must abort BEFORE anything is
// destroyed, and the worktree must come out untouched.
func TestDiscard_SnapshotFailureAbortsDiscard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based failure injection needs POSIX modes")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")
	repo.WriteFile("a.txt", "dirty-snapfail")

	// Deny object writes: snapshotTree's `add -A` must hash the dirty file
	// into .git/objects, and read-only dirs make exactly that fail while
	// leaving the read-side (status) working.
	restore := makeTreeReadOnly(t, filepath.Join(repo.Dir, ".git", "objects"))
	t.Cleanup(restore)

	root, buf := buildDiscardCmd(repo.Dir, "a.txt")
	err := root.Execute()
	restore() // let the asserts below use the repo normally
	if err == nil {
		t.Fatalf("discard must abort when the snapshot cannot be written, out:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "snapshot before discarding") {
		t.Errorf("error must attribute the abort to the snapshot: %v", err)
	}
	if got := readRepoFile(t, repo, "a.txt"); got != "dirty-snapfail" {
		t.Errorf("a.txt = %q — the worktree must be untouched when the snapshot fails", got)
	}
}

// makeTreeReadOnly chmods dir and every directory below it to 0555 and
// returns an idempotent restore func (both t.Cleanup and an early explicit
// call may run it).
func makeTreeReadOnly(t *testing.T, dir string) (restore func()) {
	t.Helper()
	var dirs []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, d := range dirs {
				_ = os.Chmod(d, 0o755)
			}
		})
	}
}

// Review F8/F3: the porcelain -z parser must skip rename origins on BOTH
// columns and classify T entries — synthetic bytes, since worktree-side
// renames are awkward to stage deterministically with real git.
func TestCollectDiscardTargets_ParsesRenamesAndEdgeShapes(t *testing.T) {
	porcelain := "RM b.txt\x00a.txt\x00 M plain.txt\x00 R new.txt\x00old.txt\x00 T typechg.txt\x00?? un.txt\x00UU conflict.txt\x00"
	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain -z -- .": {Stdout: porcelain},
	}}
	got, err := collectDiscardTargets(context.Background(), fake, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	// b.txt: staged rename + worktree edit (y==M) — discardable, origin
	// a.txt skipped. new.txt: worktree-side rename (y==R) — no worktree
	// M/D/T change, origin old.txt skipped.
	if want := []string{"b.txt", "plain.txt", "typechg.txt"}; !reflect.DeepEqual(got.files, want) {
		t.Errorf("files = %v, want %v", got.files, want)
	}
	if want := []string{"un.txt"}; !reflect.DeepEqual(got.untracked, want) {
		t.Errorf("untracked = %v, want %v", got.untracked, want)
	}
	if want := []string{"conflict.txt"}; !reflect.DeepEqual(got.unmerged, want) {
		t.Errorf("unmerged = %v, want %v", got.unmerged, want)
	}
	for _, bucket := range [][]string{got.files, got.untracked, got.unmerged} {
		for _, p := range bucket {
			if p == "a.txt" || p == "old.txt" {
				t.Errorf("rename origin leaked into targets: %q", p)
			}
		}
	}
}

// Review F8, real-git half: a staged rename with a worktree edit restores the
// NEW path to its index version and never touches or resurrects the origin.
func TestDiscard_StagedRenameWithWorktreeEdit(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")
	repo.RunGit("mv", "a.txt", "b.txt")
	repo.WriteFile("b.txt", "edited")

	root, buf := buildDiscardCmd(repo.Dir, ".")
	if err := root.Execute(); err != nil {
		t.Fatalf("discard failed: %v\nout: %s", err, buf.String())
	}
	if got := readRepoFile(t, repo, "b.txt"); got != "v1" {
		t.Errorf("b.txt = %q, want the staged (renamed) index version %q", got, "v1")
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("origin a.txt must not be resurrected (stat err=%v)", err)
	}
	if cached := repo.RunGit("diff", "--cached", "--name-only"); !strings.Contains(cached, "b.txt") {
		t.Errorf("staged rename lost from the index: %q", cached)
	}
}

// Review F6: the checkout is batched to keep argv bounded — shrink the batch
// size and prove a multi-batch discard still restores every file.
func TestDiscard_ChecksOutInBatches(t *testing.T) {
	old := discardCheckoutBatch
	discardCheckoutBatch = 2
	t.Cleanup(func() { discardCheckoutBatch = old })

	repo := testutil.NewRepo(t)
	names := []string{"f1.txt", "f2.txt", "f3.txt", "f4.txt", "f5.txt"}
	for _, n := range names {
		repo.WriteFile(n, "v1")
	}
	repo.Commit("v1")
	for _, n := range names {
		repo.WriteFile(n, "dirty")
	}

	root, buf := buildDiscardCmd(repo.Dir, ".")
	if err := root.Execute(); err != nil {
		t.Fatalf("batched discard failed: %v\nout: %s", err, buf.String())
	}
	for _, n := range names {
		if got := readRepoFile(t, repo, n); got != "v1" {
			t.Errorf("%s = %q, want %q (a batch was skipped)", n, got, "v1")
		}
	}
}

// A deleted-in-worktree file is a discardable change: discard recreates it.
func TestDiscard_RestoresWorktreeDeletedFile(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")
	if err := os.Remove(filepath.Join(repo.Dir, "a.txt")); err != nil {
		t.Fatal(err)
	}

	root, buf := buildDiscardCmd(repo.Dir, "a.txt")
	if err := root.Execute(); err != nil {
		t.Fatalf("discard failed: %v\nout: %s", err, buf.String())
	}
	if got := readRepoFile(t, repo, "a.txt"); got != "v1" {
		t.Errorf("a.txt = %q, want the deleted file recreated as %q", got, "v1")
	}
}
