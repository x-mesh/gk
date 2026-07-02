package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

func newUnstageTestCmd() *cobra.Command {
	return &cobra.Command{Use: "unstage", Args: cobra.ArbitraryArgs, RunE: runUnstage}
}

// setRepoFlagForTest pins the global --repo flag to dir for one test —
// package-level flag state otherwise leaks between tests in this package.
func setRepoFlagForTest(t *testing.T, dir string) {
	t.Helper()
	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })
}

// gk unstage drops the given paths from the index, leaves other staged
// files alone, and never touches working-tree contents.
func TestUnstage_DropsIndexKeepsContents(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "one")
	repo.WriteFile("b.txt", "two")
	repo.Commit("init")
	repo.WriteFile("a.txt", "one-changed")
	repo.WriteFile("b.txt", "two-changed")
	repo.RunGit("add", ".")
	setRepoFlagForTest(t, repo.Dir)

	cmd := newUnstageTestCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"a.txt"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	staged := repo.RunGit("diff", "--cached", "--name-only")
	if strings.Contains(staged, "a.txt") {
		t.Errorf("a.txt should be unstaged, still in index: %q", staged)
	}
	if !strings.Contains(staged, "b.txt") {
		t.Errorf("b.txt must stay staged: %q", staged)
	}
	data, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "one-changed" {
		t.Errorf("working-tree content changed: %q", data)
	}
	if !strings.Contains(out.String(), "unstaged 1 file(s)") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

// Before the first commit there is no HEAD to reset against — unstage
// falls back to `git rm --cached` and the working file survives.
func TestUnstage_NoCommitsYet(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "one")
	repo.RunGit("add", "a.txt")
	setRepoFlagForTest(t, repo.Dir)

	cmd := newUnstageTestCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if staged := repo.RunGit("diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Errorf("index should be empty: %q", staged)
	}
	data, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt"))
	if err != nil || string(data) != "one" {
		t.Errorf("working file must survive: %q err=%v", data, err)
	}
}

// F2: staged, then edited again before the first commit — `git rm --cached`
// without -f refuses here; unstage must still succeed and keep the newest
// working-tree content.
func TestUnstage_NoCommitsStagedThenModified(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "one")
	repo.RunGit("add", "a.txt")
	repo.WriteFile("a.txt", "one-edited-after-staging")
	setRepoFlagForTest(t, repo.Dir)

	cmd := newUnstageTestCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if staged := repo.RunGit("diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Errorf("index should be empty: %q", staged)
	}
	data, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt"))
	if err != nil || string(data) != "one-edited-after-staging" {
		t.Errorf("newest working content must survive: %q err=%v", data, err)
	}
}

// F3: no-HEAD, no-path unstage from a subdirectory must drop the FULL staged
// set (`:/` pathspec), not just the cwd subtree.
func TestUnstage_NoCommitsFromSubdirUnstagesWholeRepo(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("root.txt", "r")
	repo.WriteFile("sub/inner.txt", "i")
	repo.RunGit("add", ".")
	setRepoFlagForTest(t, "") // no --repo: the runner resolves against cwd
	t.Chdir(filepath.Join(repo.Dir, "sub"))

	cmd := newUnstageTestCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if staged := repo.RunGit("diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Errorf("whole staged set should be dropped, left: %q", staged)
	}
}

// F5: pathspecs that match nothing must not claim the whole index is clean.
func TestUnstage_PathMatchesNothingMessage(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "one")
	repo.Commit("init")
	repo.WriteFile("a.txt", "two")
	repo.RunGit("add", "a.txt")
	setRepoFlagForTest(t, repo.Dir)

	cmd := newUnstageTestCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"missing.txt"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "matching the given path(s)") {
		t.Errorf("message should scope the no-op to the given paths: %q", out.String())
	}
	if staged := repo.RunGit("diff", "--cached", "--name-only"); !strings.Contains(staged, "a.txt") {
		t.Errorf("a.txt must remain staged: %q", staged)
	}
}

// A clean index is a reported no-op, not an error.
func TestUnstage_NothingStaged(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "one")
	repo.Commit("init")
	setRepoFlagForTest(t, repo.Dir)

	cmd := newUnstageTestCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing staged") {
		t.Errorf("unexpected output: %q", out.String())
	}
}
