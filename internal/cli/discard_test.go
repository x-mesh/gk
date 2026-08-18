package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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
