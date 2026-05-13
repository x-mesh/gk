package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// runGit runs git in dir; t.Fatal on error to keep callers terse.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	r := &git.ExecRunner{Dir: dir}
	if _, _, err := r.Run(context.Background(), args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func TestCurrentWorktreeInfoPrimary(t *testing.T) {
	tmp := t.TempDir()
	runGit(t, tmp, "init", "-q", "-b", "main")
	runGit(t, tmp, "config", "user.email", "t@example.com")
	runGit(t, tmp, "config", "user.name", "t")
	runGit(t, tmp, "commit", "--allow-empty", "-q", "-m", "init")

	info, err := currentWorktreeInfo(context.Background(), &git.ExecRunner{Dir: tmp})
	if err != nil {
		t.Fatalf("currentWorktreeInfo: %v", err)
	}
	if !info.IsPrimary {
		t.Error("primary worktree should be detected as IsPrimary=true")
	}
	// Resolve symlinks for comparison — macOS /tmp is a symlink to /private/tmp.
	want, _ := filepath.EvalSymlinks(tmp)
	got, _ := filepath.EvalSymlinks(info.Path)
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestCurrentWorktreeInfoLinked(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	wtPath := filepath.Join(t.TempDir(), "feature-x")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feature/x", wtPath)
	defer func() {
		// Cleanup: best-effort, the temp dirs vanish anyway but we want
		// to keep the worktree DB clean if tests run in sequence.
		_ = os.RemoveAll(wtPath)
	}()

	info, err := currentWorktreeInfo(context.Background(), &git.ExecRunner{Dir: wtPath})
	if err != nil {
		t.Fatalf("currentWorktreeInfo: %v", err)
	}
	if info.IsPrimary {
		t.Error("linked worktree must not be IsPrimary")
	}
	if info.Name != "feature-x" {
		t.Errorf("Name = %q, want %q", info.Name, "feature-x")
	}
}

func TestRenderBranchSectionWithoutWorktreeAnnotation(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	st := &git.Status{Branch: "main", Upstream: "origin/main", Ahead: 0, Behind: 0}
	out := renderBranchSection(cmd, &git.ExecRunner{Dir: repo}, st, ui.SectionLayoutBar, "main", "origin/main", "main")

	if !strings.Contains(out, "main") {
		t.Errorf("BRANCH section missing branch name\n%s", out)
	}
	if !strings.Contains(out, "origin/main") {
		t.Errorf("BRANCH section missing upstream\n%s", out)
	}
	if strings.Contains(out, "wt:") {
		t.Errorf("primary worktree must not surface 'wt:' line\n%s", out)
	}
	if strings.Contains(out, " @ ") {
		t.Errorf("primary worktree must not surface '@' annotation\n%s", out)
	}
}

func TestRenderBranchSectionWorktreeAnnotation(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	wtPath := filepath.Join(t.TempDir(), "tmux")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feature/tmux", wtPath)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	st := &git.Status{Branch: "feature/tmux", Upstream: "", Ahead: 0, Behind: 0}
	out := renderBranchSection(cmd, &git.ExecRunner{Dir: wtPath}, st, ui.SectionLayoutBar, "feature/tmux", "", "main")

	if !strings.Contains(out, "feature/tmux") {
		t.Errorf("BRANCH missing branch name\n%s", out)
	}
	if !strings.Contains(out, "@") {
		t.Errorf("worktree annotation missing '@'\n%s", out)
	}
	if !strings.Contains(out, "tmux") {
		t.Errorf("worktree name missing\n%s", out)
	}
	if !strings.Contains(out, "wt:") {
		t.Errorf("worktree path line missing\n%s", out)
	}
}

func TestRenderBranchSectionShowsForkParent(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")
	runGit(t, repo, "checkout", "-q", "-b", "feature/x")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	st := &git.Status{Branch: "feature/x"}
	out := renderBranchSection(cmd, &git.ExecRunner{Dir: repo}, st, ui.SectionLayoutBar, "feature/x", "", "main")

	if !strings.Contains(out, "←") {
		t.Errorf("BRANCH section should show fork-parent arrow ←\n%s", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("BRANCH section should name fork-parent 'main'\n%s", out)
	}
}

func TestRenderBranchSectionSuppressesParentOnTrunk(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	st := &git.Status{Branch: "main"}
	out := renderBranchSection(cmd, &git.ExecRunner{Dir: repo}, st, ui.SectionLayoutBar, "main", "", "main")

	if strings.Contains(out, "←") {
		t.Errorf("trunk branch must not show fork-parent arrow\n%s", out)
	}
}

// TestRenderBranchSectionRepoPrefix locks the repo-name prefix on the
// BRANCH header — anchors captures/logs to a project and tells the user
// "you're in gk" even when the branch name is generic ("main").
func TestRenderBranchSectionRepoPrefix(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "gk")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	st := &git.Status{Branch: "main"}
	out := renderBranchSection(cmd, &git.ExecRunner{Dir: repo}, st, ui.SectionLayoutBar, "main", "", "main")

	if !strings.Contains(out, "gk ·") {
		t.Errorf("BRANCH section should prefix repo name 'gk ·'\n%s", out)
	}
}

// TestRenderBranchSectionHidesRedundantWorktreeName verifies that a linked
// worktree whose directory name equals the current branch name doesn't
// emit a redundant "@ <name>" annotation — the branch name already carries
// that token, and gk's default layout (~/.gk/worktree/<repo>/<branch>)
// makes the collision the common case.
func TestRenderBranchSectionHidesRedundantWorktreeName(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	// Worktree directory name == branch name ("fix-bug"). Worth setting
	// up explicitly because the existing annotation test uses divergent
	// names ("tmux" vs "feature/tmux") and so wouldn't catch a regression
	// of the dedup logic.
	wtPath := filepath.Join(t.TempDir(), "fix-bug")
	runGit(t, repo, "worktree", "add", "-q", "-b", "fix-bug", wtPath)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	st := &git.Status{Branch: "fix-bug"}
	out := renderBranchSection(cmd, &git.ExecRunner{Dir: wtPath}, st, ui.SectionLayoutBar, "fix-bug", "", "main")

	if strings.Contains(out, " @ ") {
		t.Errorf("BRANCH should suppress '@ <name>' when worktree name matches branch\n%s", out)
	}
	// The wt: line still surfaces — annotation dedup must not hide the
	// load-bearing path info.
	if !strings.Contains(out, "wt:") {
		t.Errorf("worktree path line must still render\n%s", out)
	}
}

// TestRenderBranchSectionCondensesHomePathInWtLine verifies the wt: line
// abbreviates $HOME so the worktree path stays scannable instead of
// burning a third of the line on "/Users/<name>/".
func TestRenderBranchSectionCondensesHomePathInWtLine(t *testing.T) {
	home := t.TempDir()
	// macOS: /var/folders/... is a symlink to /private/var/folders/...;
	// git resolves toplevel through the symlink, so HOME must match the
	// canonical form for the prefix check inside condenseHomePath to fire.
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	t.Setenv("HOME", home)

	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	wtPath := filepath.Join(home, "wt", "feature-x")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feature-x", wtPath)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	st := &git.Status{Branch: "feature-x"}
	out := renderBranchSection(cmd, &git.ExecRunner{Dir: wtPath}, st, ui.SectionLayoutBar, "feature-x", "", "main")

	if !strings.Contains(out, "~/wt/feature-x") {
		t.Errorf("wt: line should condense $HOME to '~'\n%s", out)
	}
}

func TestRenderBranchSectionDetached(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	st := &git.Status{Branch: "(detached)", Upstream: ""}
	out := renderBranchSection(cmd, &git.ExecRunner{Dir: repo}, st, ui.SectionLayoutBar, "(detached)", "", "")

	if !strings.Contains(out, "detached") {
		t.Errorf("detached HEAD must surface 'detached'\n%s", out)
	}
}
