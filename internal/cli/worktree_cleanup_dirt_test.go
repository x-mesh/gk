package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/testutil"
)

func TestParseUntrackedPorcelainZ(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		wantUntracked []string
		wantOnly      bool
	}{
		{"clean", "", nil, true},
		{"untracked only", "?? package-lock.json\x00?? web/yarn.lock\x00", []string{"package-lock.json", "web/yarn.lock"}, true},
		{"modified tracked", " M src/a.go\x00?? package-lock.json\x00", []string{"package-lock.json"}, false},
		{"rename source is not an entry", "R  new.go\x00old.go\x00?? notes.md\x00", []string{"notes.md"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, only := parseUntrackedPorcelainZ(tc.raw)
			if !reflect.DeepEqual(got, tc.wantUntracked) || only != tc.wantOnly {
				t.Fatalf("got (%v, %v), want (%v, %v)", got, only, tc.wantUntracked, tc.wantOnly)
			}
		})
	}
}

func TestDisposableUntrackedOnly(t *testing.T) {
	cases := []struct {
		name      string
		untracked []string
		only      bool
		want      bool
	}{
		{"lockfile", []string{"package-lock.json"}, true, true},
		{"nested lockfile and DS_Store", []string{"web/pnpm-lock.yaml", "docs/.DS_Store"}, true, true},
		{"lockfile plus authored note", []string{"package-lock.json", "DESIGN_REVIEW.md"}, true, false},
		{"directory entry is unknown", []string{"scratch/"}, true, false},
		{"tracked changes present", []string{"package-lock.json"}, false, false},
		{"nothing untracked", nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := disposableUntrackedOnly(tc.untracked, tc.only) != nil
			if got != tc.want {
				t.Fatalf("disposableUntrackedOnly(%v, %v) = %v, want %v", tc.untracked, tc.only, got, tc.want)
			}
		})
	}
}

func TestRemoveDisposableFiles_RefusesEscape(t *testing.T) {
	root := t.TempDir()
	if err := removeDisposableFiles(root, []string{"../outside.lock"}); err == nil {
		t.Fatal("expected refusal for a path escaping the worktree")
	}
}

func addCleanupWorktree(t *testing.T, repoDir, name string) string {
	t.Helper()
	wtPath := filepath.Join(t.TempDir(), name)
	root, buf := buildWorktreeCmd(repoDir, "add", "--no-init", "-b", wtPath, name)
	if err := root.Execute(); err != nil {
		t.Fatalf("add worktree %s: %v\nout: %s", name, err, buf.String())
	}
	return wtPath
}

func runCleanupJSON(t *testing.T, repoDir string, args ...string) worktreeCleanupJSON {
	t.Helper()
	root, buf := buildWorktreeCmd(repoDir, "cleanup", append([]string{"--json"}, args...)...)
	if err := root.Execute(); err != nil {
		t.Fatalf("cleanup: %v\nout: %s", err, buf.String())
	}
	var rep worktreeCleanupJSON
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal cleanup: %v\nraw: %s", err, buf.String())
	}
	return rep
}

func TestWorktreeCleanup_DisposableUntrackedIsReclaimed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	lockOnly := addCleanupWorktree(t, repo.Dir, "lock-only")
	if err := os.WriteFile(filepath.Join(lockOnly, "package-lock.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withNote := addCleanupWorktree(t, repo.Dir, "lock-and-note")
	for name, body := range map[string]string{"package-lock.json": "{}\n", "REVIEW.md": "keep me\n"} {
		if err := os.WriteFile(filepath.Join(withNote, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	withEdit := addCleanupWorktree(t, repo.Dir, "lock-and-edit")
	if err := os.WriteFile(filepath.Join(withEdit, "package-lock.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withEdit, ".gkkeep", "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dry := runCleanupJSON(t, repo.Dir)
	if len(dry.Candidates) != 1 || dry.Candidates[0].Branch != "lock-only" {
		t.Fatalf("want only lock-only as candidate, got %+v", dry.Candidates)
	}
	if !reflect.DeepEqual(dry.Candidates[0].Disposable, []string{"package-lock.json"}) {
		t.Errorf("disposable = %v", dry.Candidates[0].Disposable)
	}
	if !cleanupSkippedFor(dry, "lock-and-note", "dirty") || !cleanupSkippedFor(dry, "lock-and-edit", "dirty") {
		t.Errorf("worktrees with real work must stay: %+v", dry.Skipped)
	}
	for _, s := range dry.Skipped {
		if s.Branch == "lock-and-note" && !reflect.DeepEqual(s.Untracked, []string{"REVIEW.md", "package-lock.json"}) {
			t.Errorf("skipped entry should name its untracked files, got %v", s.Untracked)
		}
	}
	if _, err := os.Stat(filepath.Join(lockOnly, "package-lock.json")); err != nil {
		t.Fatalf("dry-run touched the lockfile: %v", err)
	}

	applied := runCleanupJSON(t, repo.Dir, "-y")
	if len(applied.Removed) != 1 || len(applied.Failed) != 0 {
		t.Fatalf("apply: removed=%+v failed=%+v", applied.Removed, applied.Failed)
	}
	if _, err := os.Stat(lockOnly); !os.IsNotExist(err) {
		t.Fatalf("lock-only worktree survived (stat err=%v)", err)
	}
	for _, kept := range []string{withNote, withEdit} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("worktree with real work was removed: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(withNote, "REVIEW.md")); err != nil {
		t.Fatalf("authored file lost: %v", err)
	}
}

func TestRenderWorktreeCleanup_ExplainsDryRunAndKept(t *testing.T) {
	report := worktreeCleanupJSON{
		DryRun: true,
		Candidates: []worktreeCleanupEntry{
			{Path: "/wt/humanize", Branch: "humanize", Target: "main", Reasons: []string{"clean", "merged"}},
			{Path: "/wt/lock", Branch: "lock", Target: "develop", Reasons: []string{"disposable-untracked", "merged"}, Disposable: []string{"package-lock.json"}},
		},
		Skipped: []worktreeCleanupEntry{
			{Path: "/wt/dirty", Branch: "dirty", Reasons: []string{"dirty"}, Dirty: &contextDirtyJSON{Unstaged: 4, Untracked: 5},
				Untracked: []string{"a.md", "b.md", "c.md", "d.md", "e.md"}},
			{Path: "/repo", Branch: "develop", Reasons: []string{"current"}},
		},
	}
	var buf bytes.Buffer
	renderWorktreeCleanup(&buf, report)
	out := buf.String()
	for _, want := range []string{
		"(dry-run): 2 would be removed, 2 kept",
		"would remove /wt/humanize (humanize -> main; clean, merged)",
		"+ delete regenerable untracked: package-lock.json",
		"kept (1 dirty, 1 current):",
		"dirty: 4 modified, 5 untracked [a.md, b.md, c.md, +2 more]",
		"current (the worktree you are in)",
		"nothing was removed",
		"--discard-dirty",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}
