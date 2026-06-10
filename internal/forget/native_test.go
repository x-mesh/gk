package forget

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// historyPaths returns every path ever touched in the given rev range,
// across all branches and tags.
func historyPaths(t *testing.T, r *testutil.Repo) string {
	t.Helper()
	return r.RunGit("log", "--branches", "--tags", "--name-only", "--format=")
}

func subjects(t *testing.T, r *testutil.Repo, ref string) []string {
	t.Helper()
	out := r.RunGit("log", "--reverse", "--format=%s", ref)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func TestRunNativeLinearPrune(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "one\n")
	r.Commit("keep: a")
	r.WriteFile(".xm/op/state.json", "{}\n")
	r.Commit("xm only — must vanish")
	r.WriteFile("a.txt", "one\ntwo\n")
	r.WriteFile(".xm/plan.md", "p\n")
	r.Commit("mixed — keep code part")
	r.RunGit("commit", "--allow-empty", "-m", "deliberately empty")

	res, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".xm/"})
	if err != nil {
		t.Fatalf("RunNative: %v", err)
	}

	if got := historyPaths(t, r); strings.Contains(got, ".xm/") {
		t.Errorf(".xm still in history:\n%s", got)
	}
	want := []string{"initial", "keep: a", "mixed — keep code part", "deliberately empty"}
	if got := subjects(t, r, "main"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("subjects = %v, want %v", got, want)
	}
	if res.CommitsPruned != 1 {
		t.Errorf("CommitsPruned = %d, want 1", res.CommitsPruned)
	}
	// mixed commit kept its non-target content
	if got := r.RunGit("show", "main:a.txt"); got != "one\ntwo" {
		t.Errorf("a.txt = %q", got)
	}
	// untouched prefix keeps its SHAs: "keep: a" commit must be identical
	// before/after — verified via its still-resolvable pre-rewrite SHA in
	// the commit map (old == new).
	for old, new_ := range res.CommitMap {
		if old == new_ {
			return // at least one byte-stable commit
		}
	}
	t.Error("no commit kept its SHA — untouched prefix should be byte-stable")
}

func TestRunNativeMergeCollapse(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "base\n")
	r.Commit("base")
	// side branch touching ONLY the target path
	r.RunGit("checkout", "-qb", "side")
	r.WriteFile(".xm/op/x.json", "1\n")
	r.Commit("xm: side work")
	r.WriteFile(".xm/op/y.json", "2\n")
	r.Commit("xm: more side work")
	r.RunGit("checkout", "-q", "main")
	r.WriteFile("a.txt", "base\nmain\n")
	r.Commit("main: code")
	r.RunGit("merge", "-q", "--no-ff", "side", "-m", "merge xm side")

	res, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".xm"})
	if err != nil {
		t.Fatalf("RunNative: %v", err)
	}
	if got := historyPaths(t, r); strings.Contains(got, ".xm") {
		t.Errorf(".xm still in history:\n%s", got)
	}
	// the whole side branch AND the merge must be gone from main
	want := []string{"initial", "base", "main: code"}
	if got := subjects(t, r, "main"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("subjects = %v, want %v", got, want)
	}
	if merges := r.RunGit("log", "--merges", "--format=%s", "main"); merges != "" {
		t.Errorf("merge commit survived: %q", merges)
	}
	// side's tip pruned to nothing relevant — its branch ref must point
	// at its surviving ancestor (base), not vanish (it shares history).
	if got := r.RunGit("log", "--format=%s", "-1", "side"); got != "base" {
		t.Errorf("side tip = %q, want %q", got, "base")
	}
	if res.CommitsPruned != 3 { // two side commits + merge
		t.Errorf("CommitsPruned = %d, want 3", res.CommitsPruned)
	}
}

func TestRunNativeEvilMergeKept(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "base\n")
	r.Commit("base")
	r.RunGit("checkout", "-qb", "side")
	r.WriteFile(".xm/s.json", "s\n")
	r.Commit("xm only")
	r.RunGit("checkout", "-q", "main")
	r.WriteFile("b.txt", "m\n")
	r.Commit("main work")
	// merge with extra non-target content recorded in the merge itself
	r.RunGit("merge", "-q", "--no-ff", "--no-commit", "side")
	r.WriteFile("evil.txt", "added during merge\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-qm", "evil merge")

	if _, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".xm"}); err != nil {
		t.Fatalf("RunNative: %v", err)
	}
	// merge content must survive even though its side parent vanished
	if got := r.RunGit("show", "main:evil.txt"); got != "added during merge" {
		t.Errorf("evil.txt = %q", got)
	}
	if got := historyPaths(t, r); strings.Contains(got, ".xm") {
		t.Errorf(".xm still in history:\n%s", got)
	}
}

func TestRunNativeRootPrune(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	// testutil's initial commit touches only .gkkeep — make THAT the target
	// so the root itself is pruned and a later commit becomes the new root.
	r.WriteFile("code.txt", "x\n")
	r.Commit("real work")

	res, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".gkkeep"})
	if err != nil {
		t.Fatalf("RunNative: %v", err)
	}
	got := subjects(t, r, "main")
	if strings.Join(got, "|") != "real work" {
		t.Errorf("subjects = %v, want [real work]", got)
	}
	if parents := r.RunGit("log", "--format=%P", "-1", "main"); strings.TrimSpace(parents) != "" {
		t.Errorf("new root still has parents: %q", parents)
	}
	if res.CommitsPruned != 1 {
		t.Errorf("CommitsPruned = %d, want 1", res.CommitsPruned)
	}
}

func TestRunNativeTags(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "1\n")
	r.Commit("code")
	r.RunGit("tag", "-a", "v1", "-m", "anno on kept")
	r.WriteFile(".xm/x.json", "x\n")
	r.Commit("xm only")
	r.RunGit("tag", "-a", "v2", "-m", "anno on pruned")
	r.RunGit("tag", "light-on-pruned")
	r.WriteFile("b.txt", "2\n")
	r.Commit("more code")

	res, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".xm"})
	if err != nil {
		t.Fatalf("RunNative: %v", err)
	}
	// v1 tagged a kept commit → still an annotated tag on "code"
	if got := r.RunGit("log", "--format=%s", "-1", "v1"); got != "code" {
		t.Errorf("v1 points at %q", got)
	}
	if typ := r.RunGit("cat-file", "-t", "v1"); typ != "tag" {
		t.Errorf("v1 type = %q, want tag", typ)
	}
	// v2 / light-on-pruned tagged the pruned commit → retargeted to its
	// surviving ancestor ("code"), not deleted (history continues there).
	if got := r.RunGit("log", "--format=%s", "-1", "v2"); got != "code" {
		t.Errorf("v2 points at %q", got)
	}
	if got := r.RunGit("log", "--format=%s", "-1", "light-on-pruned"); got != "code" {
		t.Errorf("light-on-pruned points at %q", got)
	}
	if len(res.RefsDeleted) != 0 {
		t.Errorf("unexpected ref deletions: %v", res.RefsDeleted)
	}
}

func TestRunNativeLeavesBackupAndRemoteRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	r.WriteFile(".xm/x.json", "x\n")
	r.Commit("xm only")
	r.WriteFile("a.txt", "1\n")
	r.Commit("code")

	oldTip := r.RunGit("rev-parse", "HEAD")
	r.RunGit("update-ref", "refs/gk/forget-backup/main/123", oldTip)
	r.RunGit("update-ref", "refs/remotes/origin/main", oldTip)

	if _, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".xm"}); err != nil {
		t.Fatalf("RunNative: %v", err)
	}
	// the rewrite must not touch refs outside heads/tags, and the old
	// objects must remain readable through them (no gc, rollback valid)
	if got := r.RunGit("rev-parse", "refs/gk/forget-backup/main/123"); got != oldTip {
		t.Errorf("backup ref moved: %s", got)
	}
	if got := r.RunGit("rev-parse", "refs/remotes/origin/main"); got != oldTip {
		t.Errorf("remote-tracking ref moved: %s", got)
	}
	r.RunGit("cat-file", "-e", oldTip) // old history still reachable
	if got := r.RunGit("rev-parse", "main"); got == oldTip {
		t.Error("main was not rewritten")
	}
}

func TestRunNativeHeadVanishGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	preTip := r.RunGit("rev-parse", "HEAD")
	// every commit on HEAD touches only the target → entire history vanishes
	_, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".gkkeep"})
	if err == nil || !strings.Contains(err.Error(), "entire history") {
		t.Fatalf("want HEAD-vanish refusal, got %v", err)
	}
	// abort path: refs untouched
	if got := r.RunGit("rev-parse", "HEAD"); got != preTip {
		t.Errorf("HEAD moved on aborted rewrite: %s", got)
	}
}

func TestRunNativeGuards(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "1\n")
	sha := r.Commit("code")

	// replace ref present → refuse
	r.RunGit("update-ref", "refs/replace/"+sha, sha)
	if _, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".xm"}); err == nil || !strings.Contains(err.Error(), "replace") {
		t.Errorf("want replace-refs refusal, got %v", err)
	}
	r.RunGit("update-ref", "-d", "refs/replace/"+sha)

	// shallow marker → refuse
	if err := os.WriteFile(filepath.Join(r.GitDir, "shallow"), []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".xm"}); err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Errorf("want shallow refusal, got %v", err)
	}
}

func TestRunNativeCommitMap(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	r.WriteFile(".xm/x.json", "x\n")
	xmSha := r.Commit("xm only")
	r.WriteFile("a.txt", "1\n")
	codeSha := r.Commit("code")

	res, err := RunNative(context.Background(), r.Dir, r.GitDir, []string{".xm"})
	if err != nil {
		t.Fatalf("RunNative: %v", err)
	}
	newTip := r.RunGit("rev-parse", "main")
	if res.CommitMap[codeSha] != newTip {
		t.Errorf("commit map for code commit = %q, want %q", res.CommitMap[codeSha], newTip)
	}
	// pruned commit maps to its surviving ancestor's NEW sha
	runner := &git.ExecRunner{Dir: r.Dir}
	parent, _, _ := runner.Run(context.Background(), "rev-parse", "main~1")
	if res.CommitMap[xmSha] != strings.TrimSpace(string(parent)) {
		t.Errorf("pruned commit map = %q, want %q", res.CommitMap[xmSha], strings.TrimSpace(string(parent)))
	}
}
