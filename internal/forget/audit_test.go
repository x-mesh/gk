package forget

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestAuditDepth1GroupsTopLevel(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("src/a/foo.go", "package a\n")
	r.WriteFile("src/b/bar.go", "package b\n")
	r.WriteFile("docs/readme.md", "hi\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "init")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := Audit(context.Background(), runner, r.Dir, 1, 0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	bucket := func(name string) *AuditEntry {
		for i := range got {
			if got[i].Path == name {
				return &got[i]
			}
		}
		return nil
	}

	src := bucket("src")
	if src == nil {
		t.Fatalf("missing src bucket; got %+v", got)
		return
	}
	// src/a/foo.go + src/b/bar.go → 2 distinct blobs.
	if src.UniqueBlobs != 2 {
		t.Errorf("src.UniqueBlobs = %d, want 2", src.UniqueBlobs)
	}
	if !src.InHEAD {
		t.Errorf("src.InHEAD = false, want true")
	}

	docs := bucket("docs")
	if docs == nil {
		t.Fatalf("missing docs bucket")
		return
	}
	if docs.UniqueBlobs != 1 {
		t.Errorf("docs.UniqueBlobs = %d, want 1", docs.UniqueBlobs)
	}
}

func TestAuditMarksHistoryOnly(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("ghost/data.bin", "v1\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "add ghost")

	r.RunGit("rm", "-r", "ghost")
	r.RunGit("commit", "-m", "remove ghost")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := Audit(context.Background(), runner, r.Dir, 1, 0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	for _, e := range got {
		if e.Path != "ghost" {
			continue
		}
		if e.InHEAD {
			t.Errorf("ghost.InHEAD = true after rm; want false (history-only)")
		}
		if e.UniqueBlobs == 0 || e.TotalBytes == 0 {
			t.Errorf("ghost has no blob stats: %+v", e)
		}
		return
	}
	t.Fatal("ghost bucket missing — should still be in audit even after rm")
}

func TestAuditTopCapsResults(t *testing.T) {
	r := testutil.NewRepo(t)
	for _, dir := range []string{"a", "b", "c", "d", "e"} {
		r.WriteFile(dir+"/f", strings.Repeat(dir, 100))
	}
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "init")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := Audit(context.Background(), runner, r.Dir, 1, 3)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d entries, want 3 (top=3)", len(got))
	}
}

func TestAuditDepth0YieldsFiles(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("src/foo.go", "package src\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "init")

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := Audit(context.Background(), runner, r.Dir, 0, 0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	for _, e := range got {
		if e.Path == "src/foo.go" {
			return
		}
	}
	t.Errorf("depth=0 should produce file-grain entry src/foo.go; got %+v", got)
}

func TestBucketKey(t *testing.T) {
	cases := []struct {
		path  string
		depth int
		want  string
	}{
		{"src/foo/bar.go", 0, "src/foo/bar.go"},
		{"src/foo/bar.go", 1, "src"},
		{"src/foo/bar.go", 2, "src/foo"},
		{"src/foo/bar.go", 5, "src/foo/bar.go"},
		{"toplevel.txt", 1, "toplevel.txt"},
	}
	for _, tc := range cases {
		if got := bucketKey(tc.path, tc.depth); got != tc.want {
			t.Errorf("bucketKey(%q, %d) = %q, want %q", tc.path, tc.depth, got, tc.want)
		}
	}
}
