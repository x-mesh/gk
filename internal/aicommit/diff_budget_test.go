package aicommit

import (
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package foo

-var x = 1
+var x = 2
+var y = 3
diff --git a/bar.go b/bar.go
index 3333333..4444444 100644
--- a/bar.go
+++ b/bar.go
@@ -10,2 +10,3 @@
 line a
+line b
 line c
`

func TestTruncateDiffNoCap(t *testing.T) {
	got := TruncateDiff(sampleDiff, 0)
	if got != sampleDiff {
		t.Error("capBytes=0 must pass through unchanged")
	}
}

func TestTruncateDiffUnderCap(t *testing.T) {
	got := TruncateDiff(sampleDiff, 10000)
	if got != sampleDiff {
		t.Error("under-cap diff must pass through unchanged")
	}
}

func TestTruncateDiffOverCapEmitsHeaders(t *testing.T) {
	// Build a diff where the second file is large enough to exceed the
	// remaining budget. First file is the small sample; second file
	// gets a hunk padded to ensure it can't fit alongside the first.
	bigBody := "+pad line\n"
	bigDiff := sampleDiff + strings.Repeat(bigBody, 200)
	files := splitDiffByFile(bigDiff)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	// Cap = first file fits whole, second file body cannot fit.
	cap := len(files[0].body) + 200
	got := TruncateDiff(bigDiff, cap)

	if !strings.Contains(got, "diff --git a/foo.go b/foo.go") {
		t.Error("first file header should be preserved")
	}
	if !strings.Contains(got, "diff --git a/bar.go b/bar.go") {
		t.Error("second file's header should still appear (stub form ok)")
	}
	if !strings.Contains(got, "byte(s) of diff truncated") {
		t.Error("expected truncation marker for bar.go body")
	}
	if len(got) > cap+200 { // small slop for the trailing marker
		t.Errorf("truncated length %d exceeds cap %d", len(got), cap)
	}
}

func TestTruncateDiffNonGitFallback(t *testing.T) {
	raw := strings.Repeat("xy ", 5000)
	got := TruncateDiff(raw, 100)
	if len(got) > 200 {
		t.Errorf("fallback truncation too long: %d", len(got))
	}
	if !strings.Contains(got, "[gk: truncated to fit budget]") {
		t.Error("expected fallback truncation marker")
	}
}

func TestSplitDiffByFile(t *testing.T) {
	files := splitDiffByFile(sampleDiff)
	if len(files) != 2 {
		t.Fatalf("split: %d files", len(files))
	}
	if !strings.HasPrefix(files[0].body, "diff --git a/foo.go") {
		t.Errorf("first file body: %q", files[0].body[:30])
	}
	if !strings.HasPrefix(files[1].body, "diff --git a/bar.go") {
		t.Errorf("second file body: %q", files[1].body[:30])
	}
	// Header excludes hunks.
	if strings.Contains(files[0].header, "@@") {
		t.Error("header must not contain hunk lines")
	}
}

func TestSplitDiffByFileNotGit(t *testing.T) {
	if got := splitDiffByFile("not a diff at all"); got != nil {
		t.Errorf("expected nil for non-git input, got %v", got)
	}
}
