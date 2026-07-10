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

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// applyFixture is a repo with one committed file plus a directory
// (outside the repo) to hold patch files, so repo status assertions
// never see the patches themselves.
type applyFixture struct {
	repo     *testutil.Repo
	patchDir string
}

const (
	applyBaseContent = "one\ntwo\nthree\nfour\nfive\n"
	applyEditContent = "one\nTWO\nthree\nfour\nfive\n"
)

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", applyBaseContent)
	repo.Commit("base")
	setRepoFlagForTest(t, repo.Dir)
	return &applyFixture{repo: repo, patchDir: t.TempDir()}
}

// capturePatch snapshots `git diff [args...]` into a patch file and
// restores the working tree, returning the patch path. RunGit trims
// the trailing newline, which git apply needs back.
func (f *applyFixture) capturePatch(t *testing.T, name string, diffArgs ...string) string {
	t.Helper()
	diff := f.repo.RunGit(append([]string{"diff"}, diffArgs...)...)
	f.repo.RunGit("checkout", "--", ".")
	return f.writePatch(t, name, diff+"\n")
}

func (f *applyFixture) writePatch(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(f.patchDir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}
	return p
}

func (f *applyFixture) readWorktree(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.repo.Dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// impossiblePatch targets a file that has never existed with bogus
// blob ids, so every rung — plain, recount, unidiff-zero, and the
// 3-way fallback (no such blobs in the object db) — must fail.
func (f *applyFixture) impossiblePatch(t *testing.T) string {
	t.Helper()
	return f.writePatch(t, "impossible.patch", `diff --git a/ghost.txt b/ghost.txt
index 1111111..2222222 100644
--- a/ghost.txt
+++ b/ghost.txt
@@ -1,3 +1,3 @@
 never existed
-old line
+new line
 more context
`)
}

func runApplyCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newApplyCmd()
	// The real tree inherits these from rootCmd; the standalone harness
	// must silence them itself or usage text pollutes the output buffer.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func setJSONFlagForTest(t *testing.T) {
	t.Helper()
	prev := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prev })
}

func decodeApplyResult(t *testing.T, out string) applyResultJSON {
	t.Helper()
	var res applyResultJSON
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode apply result %q: %v", out, err)
	}
	return res
}

// A clean patch applies to the working tree only — the index stays
// untouched (default mode matches plain git apply, not --index).
func TestApply_WorktreeDefault(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "clean.patch")

	out, err := runApplyCmd(t, patch)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.readWorktree(t, "a.txt"); got != applyEditContent {
		t.Errorf("worktree content = %q, want patched", got)
	}
	if staged := f.repo.RunGit("diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Errorf("index must stay untouched, staged: %q", staged)
	}
	if !strings.Contains(out, "applied") || !strings.Contains(out, applyStrategyPlain) {
		t.Errorf("output should name the strategy: %q", out)
	}
}

// --staged applies to the index only; the working-tree file keeps its
// pre-apply content.
func TestApply_StagedIndexOnly(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "staged.patch")

	if _, err := runApplyCmd(t, "--staged", patch); err != nil {
		t.Fatal(err)
	}
	if staged := f.repo.RunGit("diff", "--cached", "--name-only"); !strings.Contains(staged, "a.txt") {
		t.Errorf("a.txt should be staged: %q", staged)
	}
	if got := f.readWorktree(t, "a.txt"); got != applyBaseContent {
		t.Errorf("--staged must not touch the working tree, got %q", got)
	}
}

// --cached is an alias for --staged.
func TestApply_CachedAlias(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "cached.patch")

	if _, err := runApplyCmd(t, "--cached", patch); err != nil {
		t.Fatal(err)
	}
	if staged := f.repo.RunGit("diff", "--cached", "--name-only"); !strings.Contains(staged, "a.txt") {
		t.Errorf("a.txt should be staged via --cached: %q", staged)
	}
}

// A patch with miscounted hunk headers fails the plain rung and
// succeeds on the recount rung; the result records the strategy.
func TestApply_RecountRung(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "good.patch")
	data, err := os.ReadFile(patch)
	if err != nil {
		t.Fatal(err)
	}
	mangled := strings.Replace(string(data), "@@ -1,5 +1,5 @@", "@@ -1,3 +1,4 @@", 1)
	if mangled == string(data) {
		t.Fatalf("hunk header not found in %q", data)
	}
	patch = f.writePatch(t, "mangled.patch", mangled)
	setJSONFlagForTest(t)

	out, err := runApplyCmd(t, patch)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeApplyResult(t, out)
	if len(res.Applied) != 1 || res.Applied[0].Strategy != applyStrategyRecount {
		t.Errorf("want recount strategy, got %+v", res.Applied)
	}
	if got := f.readWorktree(t, "a.txt"); got != applyEditContent {
		t.Errorf("patch content not applied: %q", got)
	}
}

// A zero-context (-U0) patch needs --unidiff-zero; the ladder reaches
// that rung and records it.
func TestApply_ZeroContextRung(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "u0.patch", "-U0")
	setJSONFlagForTest(t)

	out, err := runApplyCmd(t, patch)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeApplyResult(t, out)
	if len(res.Applied) != 1 || res.Applied[0].Strategy != applyStrategyUnidiffZero {
		t.Errorf("want %s strategy, got %+v", applyStrategyUnidiffZero, res.Applied)
	}
}

// --check probes the ladder without mutating anything.
func TestApply_CheckMutatesNothing(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "probe.patch")
	treeBefore := f.repo.RunGit("write-tree")

	out, err := runApplyCmd(t, "--check", patch)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.readWorktree(t, "a.txt"); got != applyBaseContent {
		t.Errorf("--check must not touch the working tree, got %q", got)
	}
	if treeAfter := f.repo.RunGit("write-tree"); treeAfter != treeBefore {
		t.Errorf("--check must not touch the index: %s != %s", treeAfter, treeBefore)
	}
	if !strings.Contains(out, "check ok") {
		t.Errorf("unexpected output: %q", out)
	}
}

// git reports a would-conflict 3-way CHECK as exit 0 ("with conflicts"
// on stderr); gk normalizes that to failure so --check predicts what a
// real apply would do — and mutates nothing while probing.
func TestApply_CheckPredictsThreeWayConflict(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "drifted.patch")
	// Drift the committed line the patch targets so plain/recount fail
	// on context and 3-way falls back to a conflicted merge.
	f.repo.WriteFile("a.txt", "one\nDRIFTED\nthree\nfour\nfive\n")
	f.repo.Commit("drift")
	treeBefore := f.repo.RunGit("write-tree")

	_, err := runApplyCmd(t, "--check", patch)
	if err == nil {
		t.Fatal("check should fail when a real apply would leave conflicts")
	}
	if treeAfter := f.repo.RunGit("write-tree"); treeAfter != treeBefore {
		t.Errorf("--check must not touch the index: %s != %s", treeAfter, treeBefore)
	}
	if got := f.readWorktree(t, "a.txt"); !strings.Contains(got, "DRIFTED") {
		t.Errorf("--check must not touch the working tree, got %q", got)
	}
}

// --reverse undoes an applied patch.
func TestApply_Reverse(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "rev.patch")

	if _, err := runApplyCmd(t, patch); err != nil {
		t.Fatal(err)
	}
	if _, err := runApplyCmd(t, "--reverse", patch); err != nil {
		t.Fatal(err)
	}
	if got := f.readWorktree(t, "a.txt"); got != applyBaseContent {
		t.Errorf("--reverse should restore the original content, got %q", got)
	}
}

// Multi-patch atomicity: when a later patch exhausts the ladder, the
// earlier patch's changes are rolled back — index AND working tree —
// and the result says so.
func TestApply_MultiPatchAtomicRollback(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	good := f.capturePatch(t, "good.patch")
	bad := f.impossiblePatch(t)
	treeBefore := f.repo.RunGit("write-tree")
	setJSONFlagForTest(t)

	out, err := runApplyCmd(t, good, bad)
	if err == nil {
		t.Fatal("expected failure")
	}
	res := decodeApplyResult(t, out)
	if res.Failed == nil || res.Failed.Patch != bad {
		t.Errorf("failed should name the bad patch: %+v", res.Failed)
	}
	if !res.RolledBack {
		t.Errorf("rolled_back should be true: %+v", res)
	}
	if got := f.readWorktree(t, "a.txt"); got != applyBaseContent {
		t.Errorf("good patch must be rolled back, got %q", got)
	}
	if treeAfter := f.repo.RunGit("write-tree"); treeAfter != treeBefore {
		t.Errorf("index must be restored: %s != %s", treeAfter, treeBefore)
	}
	if remedies := RemediesFrom(err); len(remedies) == 0 ||
		!strings.Contains(remedies[0].Command, "apply --check") {
		t.Errorf("failure must carry an apply --check remedy: %+v", remedies)
	}
}

// A rolled-back run also removes files a patch created.
func TestApply_RollbackRemovesCreatedFiles(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("new.txt", "created by patch\n")
	f.repo.RunGit("add", "-N", "new.txt")
	diff := f.repo.RunGit("diff")
	f.repo.RunGit("reset", "-q")
	if err := os.Remove(filepath.Join(f.repo.Dir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	create := f.writePatch(t, "create.patch", diff+"\n")
	bad := f.impossiblePatch(t)

	if _, err := runApplyCmd(t, create, bad); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(f.repo.Dir, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("patch-created file must be removed on rollback (stat err=%v)", err)
	}
}

// --staged rollback restores the index exactly.
func TestApply_StagedRollbackRestoresIndex(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	good := f.capturePatch(t, "good.patch")
	bad := f.impossiblePatch(t)
	treeBefore := f.repo.RunGit("write-tree")
	setJSONFlagForTest(t)

	out, err := runApplyCmd(t, "--staged", good, bad)
	if err == nil {
		t.Fatal("expected failure")
	}
	res := decodeApplyResult(t, out)
	if !res.RolledBack {
		t.Errorf("rolled_back should be true: %+v", res)
	}
	if treeAfter := f.repo.RunGit("write-tree"); treeAfter != treeBefore {
		t.Errorf("index must be restored: %s != %s", treeAfter, treeBefore)
	}
}

// Re-applying an applied patch fails every rung; the error names the
// likely cause and suggests --reverse.
func TestApply_AlreadyAppliedHint(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "twice.patch")

	if _, err := runApplyCmd(t, patch); err != nil {
		t.Fatal(err)
	}
	_, err := runApplyCmd(t, patch)
	if err == nil {
		t.Fatal("expected failure on the second apply")
	}
	if !strings.Contains(err.Error(), "already applied") {
		t.Errorf("error should say already applied: %v", err)
	}
	if hint := HintFrom(err); !strings.Contains(hint, "--reverse") {
		t.Errorf("hint should mention --reverse: %q", hint)
	}
	if remedies := RemediesFrom(err); len(remedies) == 0 ||
		!strings.Contains(remedies[0].Command, "--reverse") {
		t.Errorf("remedy should probe with --reverse: %+v", remedies)
	}
}

// A missing patch file fails before any snapshot or mutation.
func TestApply_MissingPatchFile(t *testing.T) {
	f := newApplyFixture(t)
	_, err := runApplyCmd(t, filepath.Join(f.patchDir, "nope.patch"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want a patch-not-found error, got %v", err)
	}
}

// The 3-way rung applies a context-drifted patch — and in worktree mode
// leaves the index untouched. git apply --3way implies --index (it stages
// the merged result), so gk restores the pre-run index snapshot to keep the
// rung's scope identical to the other rungs'.
func TestApply_ThreeWayRungWorktreeDoesNotStage(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "threeway.patch")
	// Drift line 5 — inside the hunk's -U3 context, so plain/recount/
	// unidiff-zero fail on context, while the 3-way merge (line 2 vs
	// line 5, two unchanged lines apart) is clean.
	const drifted = "one\ntwo\nthree\nfour\nFIVE-DRIFT\n"
	f.repo.WriteFile("a.txt", drifted)
	f.repo.Commit("drift")
	treeBefore := f.repo.RunGit("write-tree")
	setJSONFlagForTest(t)

	out, err := runApplyCmd(t, patch)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeApplyResult(t, out)
	if len(res.Applied) != 1 || res.Applied[0].Strategy != applyStrategyThreeWay {
		t.Fatalf("want %s strategy, got %+v", applyStrategyThreeWay, res.Applied)
	}
	if got := f.readWorktree(t, "a.txt"); got != "one\nTWO\nthree\nfour\nFIVE-DRIFT\n" {
		t.Errorf("worktree must carry both edits, got %q", got)
	}
	if staged := f.repo.RunGit("diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Errorf("3-way success must not stage changes in worktree mode, staged: %q", staged)
	}
	if treeAfter := f.repo.RunGit("write-tree"); treeAfter != treeBefore {
		t.Errorf("index must be restored after the 3-way rung: %s != %s", treeAfter, treeBefore)
	}
}

// The --staged 3-way rung is version-gated: git < 2.35 rejects
// --cached with --3way, so the rung is dropped there (and on an
// unparsable version) instead of failing the ladder with a flag error.
func TestApplyRungs_StagedVersionGate(t *testing.T) {
	cases := []struct {
		name    string
		version string
		staged  bool
		want    int
	}{
		{"old git staged skips 3way", "git version 2.34.1", true, 3},
		{"old git worktree keeps 3way", "git version 2.34.1", false, 4},
		{"gate boundary 2.35", "git version 2.35.0", true, 4},
		{"vendor suffix", "git version 2.39.5 (Apple Git-154)", true, 4},
		{"unparsable degrades to skip", "not a git version", true, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
				"--version": {Stdout: tc.version},
			}}
			rungs := applyRungs(context.Background(), fake, tc.staged)
			if len(rungs) != tc.want {
				t.Fatalf("rungs = %d, want %d: %+v", len(rungs), tc.want, rungs)
			}
			last := rungs[len(rungs)-1].strategy
			if tc.want == 4 && last != applyStrategyThreeWay {
				t.Errorf("last rung = %q, want %s", last, applyStrategyThreeWay)
			}
			if tc.want == 3 && last == applyStrategyThreeWay {
				t.Errorf("3way rung must be skipped: %+v", rungs)
			}
		})
	}
}

// Rollback restores the pre-run WORKING TREE snapshot, not HEAD: a user's
// uncommitted edit that existed before the run must survive a multi-patch
// rollback intact.
func TestApply_RollbackPreservesPreexistingDirtyEdits(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	// -U1 keeps line 5 out of the hunk context so the patch still applies
	// plainly over the dirty file below.
	good := f.capturePatch(t, "good.patch", "-U1")
	// Pre-existing uncommitted local edit on line 5.
	const dirty = "one\ntwo\nthree\nfour\nFIVE-LOCAL\n"
	f.repo.WriteFile("a.txt", dirty)
	bad := f.impossiblePatch(t)

	if _, err := runApplyCmd(t, good, bad); err == nil {
		t.Fatal("expected failure")
	}
	if got := f.readWorktree(t, "a.txt"); got != dirty {
		t.Errorf("rollback must restore the dirty pre-apply content (local edit intact, patch edit gone), got %q", got)
	}
}

// --check probes each patch against the CURRENT tree independently, so a
// stacked series (p2 generated on top of p1) fails the probe while the real
// sequential run applies it — the failure hint carries that caveat.
func TestApply_CheckSeriesMispredictsButRealRunApplies(t *testing.T) {
	f := newApplyFixture(t)
	// p1: two → TWO.
	f.repo.WriteFile("a.txt", applyEditContent)
	p1 := f.capturePatch(t, "p1.patch")
	// p2: generated on top of p1 — TWO → TWO-MORE on the same line, so no
	// rung (including 3way, which would conflict) can pass it standalone.
	const p2Content = "one\nTWO-MORE\nthree\nfour\nfive\n"
	f.repo.WriteFile("a.txt", applyEditContent)
	f.repo.RunGit("add", "a.txt")
	f.repo.WriteFile("a.txt", p2Content)
	diff := f.repo.RunGit("diff")
	f.repo.RunGit("reset", "-q")
	f.repo.RunGit("checkout", "--", ".")
	p2 := f.writePatch(t, "p2.patch", diff+"\n")

	_, err := runApplyCmd(t, "--check", p1, p2)
	if err == nil {
		t.Fatal("--check probes the pristine tree per patch; a stacked series must fail it")
	}
	if hint := HintFrom(err); !strings.Contains(hint, "independently") {
		t.Errorf("check failure hint should explain the per-patch limitation: %q", hint)
	}
	// The real run applies the series sequentially and succeeds.
	if _, err := runApplyCmd(t, p1, p2); err != nil {
		t.Fatalf("real run must apply the stacked series: %v", err)
	}
	if got := f.readWorktree(t, "a.txt"); got != p2Content {
		t.Errorf("worktree = %q, want the stacked result %q", got, p2Content)
	}
}

// The JSON result marks probe runs — "check" for --check, "dry-run" when the
// global --dry-run forced the probe — so a success payload is never
// byte-identical between a probe and a real apply ("applied").
func TestApply_ResultDistinguishesCheckFromApply(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "mode.patch")
	setJSONFlagForTest(t)
	prevDry := flagDryRun
	t.Cleanup(func() { flagDryRun = prevDry })

	out, err := runApplyCmd(t, "--check", patch)
	if err != nil {
		t.Fatal(err)
	}
	if res := decodeApplyResult(t, out); res.Result != "check" {
		t.Errorf("--check result = %q, want check", res.Result)
	}

	flagDryRun = true
	out, err = runApplyCmd(t, patch)
	flagDryRun = prevDry
	if err != nil {
		t.Fatal(err)
	}
	if res := decodeApplyResult(t, out); res.Result != "dry-run" {
		t.Errorf("--dry-run result = %q, want dry-run", res.Result)
	}
	if got := f.readWorktree(t, "a.txt"); got != applyBaseContent {
		t.Errorf("--dry-run must not mutate the tree, got %q", got)
	}

	out, err = runApplyCmd(t, patch)
	if err != nil {
		t.Fatal(err)
	}
	if res := decodeApplyResult(t, out); res.Result != "applied" {
		t.Errorf("real result = %q, want applied", res.Result)
	}
}

// With --repo pointing elsewhere, a relative patch path resolves against the
// caller's CWD for BOTH the existence check and the git invocation.
func TestApply_RepoFlagResolvesPatchFromCWD(t *testing.T) {
	f := newApplyFixture(t)
	f.repo.WriteFile("a.txt", applyEditContent)
	patch := f.capturePatch(t, "rel.patch")
	t.Chdir(f.patchDir)

	if _, err := runApplyCmd(t, filepath.Base(patch)); err != nil {
		t.Fatalf("relative patch with --repo set: %v", err)
	}
	if got := f.readWorktree(t, "a.txt"); got != applyEditContent {
		t.Errorf("patch not applied via CWD-relative path, got %q", got)
	}
}

// The registered command exposes exactly the specced surface.
func TestApply_CommandSurface(t *testing.T) {
	cmd := newApplyCmd()
	if cmd.Name() != "apply" {
		t.Errorf("command name = %q", cmd.Name())
	}
	for _, flag := range []string{"staged", "cached", "check", "reverse"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing --%s flag", flag)
		}
	}
	if err := cobra.MinimumNArgs(1)(cmd, nil); err == nil {
		t.Error("apply should require at least one patch file")
	}
}
