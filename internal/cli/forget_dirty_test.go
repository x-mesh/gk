package cli

import (
	"context"
	"slices"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestDirtyOutsideTargetsAcceptsTargetEntries locks in the bug fix
// motivated by the PostgreSQL data-dir scenario: when a user adds a
// directory to .gitignore and runs `gk forget`, the live files inside
// that directory routinely show up as M/D in `git status`. Those
// entries must not block the run, because filter-repo will erase them
// from history anyway.
func TestDirtyOutsideTargetsAcceptsTargetEntries(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("db/data.bin", "v1\n")
	r.WriteFile("README.md", "tracked\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "init")

	// Mutate a file under the forget target — analogous to the live DB
	// rewriting itself between gk forget and the actual filter-repo run.
	r.WriteFile("db/data.bin", "v2 — different bytes\n")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := dirtyOutsideTargets(context.Background(), runner, []string{"db/"})
	if err != nil {
		t.Fatalf("dirtyOutsideTargets: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("entries inside target should not surface: got %v", got)
	}
}

func TestDirtyOutsideTargetsRejectsExternalChanges(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("db/data.bin", "v1\n")
	r.WriteFile("README.md", "v1\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "init")

	// External dirty file — the user would lose this if filter-repo ran.
	r.WriteFile("README.md", "v2\n")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := dirtyOutsideTargets(context.Background(), runner, []string{"db/"})
	if err != nil {
		t.Fatalf("dirtyOutsideTargets: %v", err)
	}
	if !slices.Equal(got, []string{"README.md"}) {
		t.Errorf("got %v, want [README.md]", got)
	}
}

func TestDirtyOutsideTargetsHandlesDeletes(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("db/data.bin", "v1\n")
	r.WriteFile("outside.txt", "stay\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "init")

	r.RunGit("rm", "db/data.bin")
	r.RunGit("rm", "outside.txt")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := dirtyOutsideTargets(context.Background(), runner, []string{"db/"})
	if err != nil {
		t.Fatalf("dirtyOutsideTargets: %v", err)
	}
	if !slices.Equal(got, []string{"outside.txt"}) {
		t.Errorf("got %v, want [outside.txt] (db/* deletes covered by target)", got)
	}
}

func TestPathUnderAny(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		targets []string
		want    bool
	}{
		{"exact file", "secrets.json", []string{"secrets.json"}, true},
		{"under dir", "db/data/foo", []string{"db"}, true},
		{"under dir with trailing slash", "db/data/foo", []string{"db/"}, true},
		{"sibling false-positive", "db-other/foo", []string{"db"}, false},
		{"different tree", "README.md", []string{"db", "secrets"}, false},
		{"nested target", "a/b/c/d", []string{"a/b"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathUnderAny(tc.path, tc.targets); got != tc.want {
				t.Errorf("pathUnderAny(%q, %v) = %v, want %v", tc.path, tc.targets, got, tc.want)
			}
		})
	}
}

func TestDirtyOutsideTargetsCleanRepo(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := dirtyOutsideTargets(context.Background(), runner, []string{"db/"})
	if err != nil {
		t.Fatalf("dirtyOutsideTargets: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("clean repo returned %v, want empty", got)
	}
}
