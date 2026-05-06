package forget

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestAutoDetectIgnored(t *testing.T) {
	r := testutil.NewRepo(t)

	// Commit a "db/" directory with a file before any .gitignore exists.
	r.WriteFile("db/data.sqlite", "fake-binary\n")
	r.WriteFile("README.md", "tracked\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "add db plus readme")

	// Now retroactively ignore db/. ls-files -i -c should flag db/data.sqlite.
	r.WriteFile(".gitignore", "db/\n")
	r.RunGit("add", ".gitignore")
	r.RunGit("commit", "-m", "ignore db")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := AutoDetectIgnored(context.Background(), runner)
	if err != nil {
		t.Fatalf("AutoDetectIgnored: %v", err)
	}
	want := []string{filepath.ToSlash("db/data.sqlite")}
	if !slices.Equal(got, want) {
		t.Errorf("AutoDetectIgnored = %v, want %v", got, want)
	}
}

func TestAutoDetectIgnoredEmpty(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := AutoDetectIgnored(context.Background(), runner)
	if err != nil {
		t.Fatalf("AutoDetectIgnored: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("AutoDetectIgnored on clean repo = %v, want empty", got)
	}
}

func TestPathInHistoryFiltersUntouched(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("present.txt", "x\n")
	r.RunGit("add", "present.txt")
	r.RunGit("commit", "-m", "add present")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := PathInHistory(context.Background(), runner, []string{"present.txt", "missing.txt"})
	if err != nil {
		t.Fatalf("PathInHistory: %v", err)
	}
	want := []string{"present.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("PathInHistory = %v, want %v", got, want)
	}
}

func TestCountTouchingCommitsDeduplicates(t *testing.T) {
	r := testutil.NewRepo(t)
	// Two commits, each touching db/data — count should be 2 not 4.
	r.WriteFile("db/data", "v1\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "first")
	r.WriteFile("db/data", "v2\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "second")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := CountTouchingCommits(context.Background(), runner, []string{"db/data"})
	if err != nil {
		t.Fatalf("CountTouchingCommits: %v", err)
	}
	if got != 2 {
		t.Errorf("CountTouchingCommits = %d, want 2", got)
	}
}

func TestCountTouchingCommitsZero(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := CountTouchingCommits(context.Background(), runner, []string{"nope.txt"})
	if err != nil {
		t.Fatalf("CountTouchingCommits: %v", err)
	}
	if got != 0 {
		t.Errorf("CountTouchingCommits(missing) = %d, want 0", got)
	}
}
