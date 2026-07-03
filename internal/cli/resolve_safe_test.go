package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/testutil"
)

// conflictRepo builds a real merge conflict: for each file, [0] lands on the
// default branch (ours) and [1] on a side branch (theirs), both on top of the
// given base contents. Returns with the merge paused in conflict.
func conflictRepo(t *testing.T, base map[string]string, sides map[string][2]string) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	for p, c := range base {
		repo.WriteFile(p, c)
	}
	repo.Commit("base")
	def := strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD"))
	repo.RunGit("checkout", "-b", "side")
	for p, v := range sides {
		repo.WriteFile(p, v[1])
	}
	repo.Commit("side change")
	repo.RunGit("checkout", def)
	for p, v := range sides {
		repo.WriteFile(p, v[0])
	}
	repo.Commit("ours change")
	if _, err := repo.TryGit("merge", "side"); err == nil {
		t.Fatal("expected the merge to conflict")
	}
	setRepoFlagForTest(t, repo.Dir)
	t.Chdir(repo.Dir)
	return repo
}

func runResolveFlags(t *testing.T, flags map[string]string, args []string) error {
	t.Helper()
	cmd, _, _ := rootCmd.Find([]string{"resolve"})
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for k := range flags {
			def := "false"
			if k == "strategy" {
				def = ""
			}
			_ = cmd.Flags().Set(k, def)
		}
	})
	return runResolveWithContext(t, cmd, args)
}

// --safe resolves a trailing-whitespace-only conflict deterministically and
// finishes the merge — no AI provider, no human. (Internal spacing and
// indentation are meaningful and deliberately NOT in this tier.)
func TestResolveSafe_WhitespaceConflictEndToEnd(t *testing.T) {
	repo := conflictRepo(t,
		map[string]string{"a.txt": "one\ntwo\nthree\n"},
		map[string][2]string{"a.txt": {
			"one\ntwo changed\nthree\n",   // ours
			"one\ntwo changed  \nthree\n", // theirs — trailing whitespace only
		}},
	)

	if err := runResolveFlags(t, map[string]string{"safe": "true"}, nil); err != nil {
		t.Fatalf("safe resolve should finish the merge: %v", err)
	}
	if unmerged := repo.RunGit("ls-files", "-u"); strings.TrimSpace(unmerged) != "" {
		t.Errorf("index should have no unmerged entries:\n%s", unmerged)
	}
	if _, err := repo.TryGit("rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		t.Error("merge should be concluded (MERGE_HEAD gone)")
	}
}

// --safe never guesses: a semantic conflict stays marked and unmerged, and
// the run reports paused instead of done.
func TestResolveSafe_LeavesSemanticConflict(t *testing.T) {
	repo := conflictRepo(t,
		map[string]string{"b.txt": "x = 0\n"},
		map[string][2]string{"b.txt": {"x = 1\n", "x = 2\n"}},
	)

	err := runResolveFlags(t, map[string]string{"safe": "true"}, nil)
	if err == nil {
		t.Fatal("semantic conflict must leave the operation paused (non-nil)")
	}
	if unmerged := repo.RunGit("ls-files", "-u"); !strings.Contains(unmerged, "b.txt") {
		t.Errorf("b.txt must stay unmerged:\n%s", unmerged)
	}
	data := repo.RunGit("show", ":1:b.txt") // base stage still present
	if strings.TrimSpace(data) != "x = 0" {
		t.Errorf("stages must be untouched, base = %q", data)
	}
}

// A failing resolve.verify command (from the GLOBAL config — the only place
// it is honored) rolls the resolution back to the exact conflicted state —
// markers restored, stages intact, operation paused.
func TestResolveVerifyGate_RollsBackOnFailure(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "gk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "gk", "config.yaml"),
		[]byte("resolve:\n  verify: [\"false\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := conflictRepo(t,
		map[string]string{"c.txt": "x = 0\n"},
		map[string][2]string{"c.txt": {"x = 1\n", "x = 2\n"}},
	)

	err := runResolveFlags(t, map[string]string{"strategy": "theirs"}, nil)
	if err == nil {
		t.Fatal("failed verification must leave the operation paused")
	}
	content := repo.RunGit("show", ":2:c.txt") // ours stage survives the rollback
	if strings.TrimSpace(content) != "x = 1" {
		t.Errorf("ours stage must survive rollback, got %q", content)
	}
	if unmerged := repo.RunGit("ls-files", "-u"); !strings.Contains(unmerged, "c.txt") {
		t.Errorf("c.txt must be unmerged again after rollback:\n%s", unmerged)
	}
}

// Repo-local .gk.yaml must NOT be able to run verify commands — an untrusted
// checkout would gain arbitrary shell execution (cross-vendor review S1,
// same trust boundary as init.ai_gitignore). The repo-local "false" here is
// ignored, so the resolution completes.
func TestResolveVerify_RepoLocalConfigIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // clean global config
	repo := conflictRepo(t,
		map[string]string{"d.txt": "x = 0\n"},
		map[string][2]string{"d.txt": {"x = 1\n", "x = 2\n"}},
	)
	repo.WriteFile(".gk.yaml", "resolve:\n  verify: [\"false\"]\n")

	if err := runResolveFlags(t, map[string]string{"strategy": "theirs"}, nil); err != nil {
		t.Fatalf("repo-local verify must be ignored (merge should finish): %v", err)
	}
	if unmerged := repo.RunGit("ls-files", "-u"); strings.TrimSpace(unmerged) != "" {
		t.Errorf("no unmerged entries expected:\n%s", unmerged)
	}
}

// A: base info is reconstructed in memory from the index stages (git's
// default conflict style has no diff3 block). A CHANGELOG conflict where
// both sides REWROTE an existing entry must be refused by --safe — union
// would be wrong — which is only knowable with the reconstructed base.
func TestResolveSafe_EnrichedBaseRefusesRewriteUnion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := conflictRepo(t,
		map[string]string{"CHANGELOG.md": "# log\n- original entry\n"},
		map[string][2]string{"CHANGELOG.md": {
			"# log\n- rewritten by ours\n",
			"# log\n- rewritten by theirs\n",
		}},
	)

	err := runResolveFlags(t, map[string]string{"safe": "true"}, nil)
	if err == nil {
		t.Fatal("rewrite conflict on a union file must stay paused under --safe")
	}
	if unmerged := repo.RunGit("ls-files", "-u"); !strings.Contains(unmerged, "CHANGELOG.md") {
		t.Errorf("CHANGELOG.md must stay unmerged:\n%s", unmerged)
	}
}

// A(counter): a genuine both-added CHANGELOG conflict still unions and the
// merge completes — the reconstructed base block is empty there.
func TestResolveSafe_EnrichedBaseAllowsAdditiveUnion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := conflictRepo(t,
		map[string]string{"CHANGELOG.md": "# log\n"},
		map[string][2]string{"CHANGELOG.md": {
			"# log\n- ours entry\n",
			"# log\n- theirs entry\n",
		}},
	)

	if err := runResolveFlags(t, map[string]string{"safe": "true"}, nil); err != nil {
		t.Fatalf("both-added union file must resolve and finish the merge: %v", err)
	}
	merged := repo.RunGit("show", "HEAD:CHANGELOG.md")
	if !strings.Contains(merged, "- ours entry") || !strings.Contains(merged, "- theirs entry") {
		t.Errorf("union must keep both entries:\n%s", merged)
	}
	if unmerged := repo.RunGit("ls-files", "-u"); strings.TrimSpace(unmerged) != "" {
		t.Errorf("no unmerged entries expected:\n%s", unmerged)
	}
}

// C: a delete/modify resolution is deferred too — a failing verify gate
// restores the conflicted state instead of leaving the deletion staged.
func TestResolveVerifyGate_RestoresDeleteModify(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "gk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "gk", "config.yaml"),
		[]byte("resolve:\n  verify: [\"false\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("f.txt", "original\n")
	repo.Commit("base")
	def := strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD"))
	repo.RunGit("checkout", "-b", "side")
	repo.WriteFile("f.txt", "modified by side\n")
	repo.Commit("side modifies")
	repo.RunGit("checkout", def)
	repo.RunGit("rm", "-q", "f.txt")
	repo.Commit("ours deletes")
	if _, err := repo.TryGit("merge", "side"); err == nil {
		t.Fatal("expected delete/modify conflict")
	}
	setRepoFlagForTest(t, repo.Dir)
	t.Chdir(repo.Dir)

	// ours = the deleting side → resolution removes the file, gate fails,
	// rollback must bring the conflicted state back.
	err := runResolveFlags(t, map[string]string{"strategy": "ours"}, nil)
	if err == nil {
		t.Fatal("failing verify must leave the operation paused")
	}
	if unmerged := repo.RunGit("ls-files", "-u"); !strings.Contains(unmerged, "f.txt") {
		t.Errorf("f.txt must be unmerged again after rollback:\n%s", unmerged)
	}
}

// L6: while conflicts deliberately remain (safe/confidence holdbacks), the
// resolve.verify commands are skipped — otherwise the held markers would
// fail the build check and roll back the GOOD resolutions with them.
func TestResolveVerifySkippedWhileConflictsRemain(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "gk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "gk", "config.yaml"),
		[]byte("resolve:\n  verify: [\"false\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := conflictRepo(t,
		map[string]string{"a.txt": "keep\n", "b.txt": "x = 0\n"},
		map[string][2]string{
			"a.txt": {"keep changed\n", "keep changed  \n"}, // mechanical (trailing WS)
			"b.txt": {"x = 1\n", "x = 2\n"},                 // semantic — stays
		},
	)

	err := runResolveFlags(t, map[string]string{"safe": "true"}, nil)
	if err == nil {
		t.Fatal("semantic remainder must leave the operation paused")
	}
	unmerged := repo.RunGit("ls-files", "-u")
	if strings.Contains(unmerged, "a.txt") {
		t.Errorf("mechanical file must be staged despite the failing verify command (skipped):\n%s", unmerged)
	}
	if !strings.Contains(unmerged, "b.txt") {
		t.Errorf("semantic file must stay unmerged:\n%s", unmerged)
	}
}
