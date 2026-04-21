package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// newStatusCmd builds a fresh cobra command backed by runStatus for testing.
func newStatusCmd(t *testing.T, repoDir string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	cmd := &cobra.Command{
		Use:  "status",
		RunE: runStatus,
	}
	cmd.SetOut(buf)
	// override flagRepo so ExecRunner points at the temp repo
	flagRepo = repoDir
	return cmd, buf
}

func TestRunStatus_Clean(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "working tree clean") {
		t.Errorf("expected 'working tree clean', got:\n%s", out)
	}
}

func TestRunStatus_Untracked(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	r.WriteFile("newfile.txt", "hello\n")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "untracked:") {
		t.Errorf("expected 'untracked:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "newfile.txt") {
		t.Errorf("expected 'newfile.txt' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "??") {
		t.Errorf("expected '??' marker, got:\n%s", out)
	}
}

func TestRunStatus_Modified(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	// create and commit a file first
	r.WriteFile("tracked.txt", "original\n")
	r.RunGit("add", "tracked.txt")
	r.RunGit("commit", "-m", "add tracked.txt")
	// now modify it without staging
	r.WriteFile("tracked.txt", "modified\n")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "modified:") {
		t.Errorf("expected 'modified:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "tracked.txt") {
		t.Errorf("expected 'tracked.txt' in output, got:\n%s", out)
	}
}

func TestRunStatus_Staged(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	// create and commit a file first
	r.WriteFile("staged.txt", "original\n")
	r.RunGit("add", "staged.txt")
	r.RunGit("commit", "-m", "add staged.txt")
	// modify and stage
	r.WriteFile("staged.txt", "staged content\n")
	r.RunGit("add", "staged.txt")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "staged:") {
		t.Errorf("expected 'staged:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "staged.txt") {
		t.Errorf("expected 'staged.txt' in output, got:\n%s", out)
	}
}

func TestRunStatus_Conflict(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)

	// create conflict: two branches modify same file
	r.WriteFile("conflict.txt", "base\n")
	r.RunGit("add", "conflict.txt")
	r.RunGit("commit", "-m", "add conflict.txt")

	r.RunGit("checkout", "-b", "branch-a")
	r.WriteFile("conflict.txt", "branch-a content\n")
	r.RunGit("add", "conflict.txt")
	r.RunGit("commit", "-m", "branch-a change")

	r.RunGit("checkout", "main")
	r.WriteFile("conflict.txt", "main content\n")
	r.RunGit("add", "conflict.txt")
	r.RunGit("commit", "-m", "main change")

	// attempt merge to create conflict
	_, mergeErr := r.TryGit("merge", "--no-ff", "branch-a")
	if mergeErr == nil {
		// no conflict occurred — skip
		t.Skip("no merge conflict produced; skipping conflict test")
	}

	// verify conflict state
	runner := &git.ExecRunner{Dir: r.Dir}
	client := git.NewClient(runner)
	st, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("client.Status: %v", err)
	}

	hasConflict := false
	for _, e := range st.Entries {
		if e.Kind == git.KindUnmerged {
			hasConflict = true
			break
		}
	}
	if !hasConflict {
		t.Skip("no unmerged entries found; skipping conflict test")
	}

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "conflicts:") {
		t.Errorf("expected 'conflicts:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "conflict.txt") {
		t.Errorf("expected 'conflict.txt' in output, got:\n%s", out)
	}
}
