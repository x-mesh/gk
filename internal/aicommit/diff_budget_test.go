package aicommit

import (
	"fmt"
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
		t.Error("second file's header should still appear (digest form ok)")
	}
	if !strings.Contains(got, "large diff digested") {
		t.Error("expected digest marker for bar.go body that overflowed budget")
	}
	if len(got) > cap+200 { // small slop for the trailing marker
		t.Errorf("truncated length %d exceeds cap %d", len(got), cap)
	}
}

// TestTruncatePerFileDigest checks that a single file exceeding the
// per-file cap is digested even when the whole group fits the byte cap —
// the savings lever, where the old code would have sent the full patch.
func TestTruncatePerFileDigest(t *testing.T) {
	big := buildLargeFileDiff()
	// Group cap is huge (whole diff fits), per-file cap small → digest.
	got := truncateDiff(big, 1<<20, 2048)
	if strings.Count(got, "@@ ") != 0 {
		t.Errorf("oversized file should be digested (no raw hunks), got:\n%s", got)
	}
	if !strings.Contains(got, "large diff digested") {
		t.Error("expected digest marker")
	}
	if !strings.Contains(got, "LoadConfig") {
		t.Error("digest must still name the changed symbols")
	}
	if !strings.HasPrefix(got, "diff --git a/big.go b/big.go") {
		t.Error("file header must be preserved")
	}
}

// TestDigestForTruncatedSavings measures the byte/token reduction of
// digesting an oversized file vs sending its full patch. Run with -v to
// see the numbers; this is the "절감폭" the per-file digest cap buys.
func TestDigestForTruncatedSavings(t *testing.T) {
	full := buildLargeFileDiff()
	files := splitDiffByFile(full)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	digest := digestFileBlock(files[0])

	fullTok := (len(full) + charsPerToken - 1) / charsPerToken
	digTok := (len(digest) + charsPerToken - 1) / charsPerToken
	saved := 100.0 * float64(len(full)-len(digest)) / float64(len(full))

	t.Logf("full patch : %5d bytes (~%d tok)", len(full), fullTok)
	t.Logf("digest     : %5d bytes (~%d tok)", len(digest), digTok)
	t.Logf("reduction  : %.1f%%", saved)
	t.Logf("digest line: %s", strings.TrimSpace(strings.TrimPrefix(digest, files[0].header)))

	if len(digest) >= len(full) {
		t.Fatalf("digest (%d) should be smaller than full patch (%d)", len(digest), len(full))
	}
	if !strings.Contains(digest, "LoadConfig") {
		t.Error("digest should name the changed symbols")
	}
}

// buildLargeFileDiff synthesises a ~20KB single-file Go diff: five hunks,
// each with a function context (so the digest can name symbols) padded
// with added lines.
func buildLargeFileDiff() string {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n")
	b.WriteString("index aaaaaaa..bbbbbbb 100644\n")
	b.WriteString("--- a/big.go\n")
	b.WriteString("+++ b/big.go\n")
	funcs := []string{"LoadConfig", "ParseRequest", "RenderResponse", "ValidateInput", "BuildIndex"}
	for i, fn := range funcs {
		fmt.Fprintf(&b, "@@ -%d,4 +%d,44 @@ func %s() error {\n", i*100+1, i*100+1, fn)
		b.WriteString(" // surrounding context\n")
		for j := 0; j < 40; j++ {
			fmt.Fprintf(&b, "+\t_ = doWork(%d, %q) // padding to grow the patch body\n", j, fn)
		}
	}
	return b.String()
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
