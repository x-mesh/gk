package cli

import (
	"strings"
	"testing"

	"github.com/fatih/color"
	"pgregory.net/rapid"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/resolve"
)

func init() {
	color.NoColor = true // for test predictability
}

// ---------------------------------------------------------------------------
// Generators
// ---------------------------------------------------------------------------

// genNonEmptyLine generates a non-empty line without newlines.
func genNonEmptyLine(t *rapid.T, label string) string {
	return rapid.StringMatching(`[a-zA-Z0-9_ \t\.\,\;\:\(\)\{\}\[\]\-\+\*\/\#]{1,60}`).Draw(t, label)
}

// genNonEmptyLines generates 1-5 non-empty lines.
func genNonEmptyLines(t *rapid.T, label string) []string {
	n := rapid.IntRange(1, 5).Draw(t, label+"_count")
	lines := make([]string, n)
	for i := range n {
		lines[i] = genNonEmptyLine(t, label)
	}
	return lines
}

// genConflictHunkForDiff generates a ConflictHunk with non-empty Ours and Theirs.
func genConflictHunkForDiff(t *rapid.T) resolve.ConflictHunk {
	return resolve.ConflictHunk{
		Ours:   genNonEmptyLines(t, "ours"),
		Theirs: genNonEmptyLines(t, "theirs"),
	}
}

// ---------------------------------------------------------------------------
// Feature: ai-resolve, Property 8: FormatHunkDiff는 hunk 내용을 포함한다
// ---------------------------------------------------------------------------

// TestPropertyFormatHunkDiff verifies that FormatHunkDiff output contains
// all Ours lines and all Theirs lines from the input hunk.
// **Validates: Requirements 6.2**
func TestPropertyFormatHunkDiff(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		hunk := genConflictHunkForDiff(rt)
		result := FormatHunkDiff(hunk)

		for i, line := range hunk.Ours {
			if !strings.Contains(result, line) {
				rt.Fatalf("result does not contain Ours[%d] %q\nresult:\n%s", i, line, result)
			}
		}
		for i, line := range hunk.Theirs {
			if !strings.Contains(result, line) {
				rt.Fatalf("result does not contain Theirs[%d] %q\nresult:\n%s", i, line, result)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

func TestFormatHunkDiff_Basic(t *testing.T) {
	hunk := resolve.ConflictHunk{
		Ours:   []string{"local line 1", "local line 2"},
		Theirs: []string{"remote line 1"},
	}
	result := FormatHunkDiff(hunk)

	if !strings.Contains(result, "- local line 1") {
		t.Errorf("expected ours line with - prefix: %s", result)
	}
	if !strings.Contains(result, "- local line 2") {
		t.Errorf("expected ours line with - prefix: %s", result)
	}
	if !strings.Contains(result, "+ remote line 1") {
		t.Errorf("expected theirs line with + prefix: %s", result)
	}
}

func TestFormatHunkDiff_Empty(t *testing.T) {
	hunk := resolve.ConflictHunk{}
	result := FormatHunkDiff(hunk)
	if result != "" {
		t.Errorf("expected empty string for empty hunk, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// Rich formatter & menu builders
// ---------------------------------------------------------------------------

func TestFormatHunkRich_WithRegionShowsLineNumbers(t *testing.T) {
	hunk := resolve.ConflictHunk{
		OursLabel:   "HEAD",
		TheirsLabel: "cd98609 (drop submodule entries)",
		Ours:        []string{"    if keep {", "        out = append(out, e)"},
		Theirs:      []string{"    if ok {", "        out = append(out, e)"},
	}
	region := &git.ConflictRegion{
		StartMarkerLine: 4,
		MidMarkerLine:   7,
		EndMarkerLine:   10,
		OursLabel:       "HEAD",
		TheirsLabel:     "cd98609 (drop submodule entries)",
		Ours: []git.ConflictLine{
			{LineNum: 5, Text: "    if keep {"},
			{LineNum: 6, Text: "        out = append(out, e)"},
		},
		Theirs: []git.ConflictLine{
			{LineNum: 8, Text: "    if ok {"},
			{LineNum: 9, Text: "        out = append(out, e)"},
		},
		ContextBefore: &git.ConflictLine{LineNum: 3, Text: "    case '1':"},
		ContextAfter:  &git.ConflictLine{LineNum: 11, Text: "    }"},
	}
	got := formatHunkRich(hunk, region)

	for _, want := range []string{
		"HEAD", "cd98609 (drop submodule entries)",
		"   5", "   6", "   8", "   9", "   3", "  11",
		"if keep {", "if ok {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n%s", want, got)
		}
	}
}

func TestFormatHunkRich_WithoutRegionFallsBack(t *testing.T) {
	hunk := resolve.ConflictHunk{
		Ours:   []string{"local"},
		Theirs: []string{"remote"},
	}
	got := formatHunkRich(hunk, nil)
	if !strings.Contains(got, "local") || !strings.Contains(got, "remote") {
		t.Errorf("output missing content lines:\n%s", got)
	}
	// No region → no line-number column populated; the marker columns
	// still appear so the user can read the diff.
	if !strings.Contains(got, "◀") || !strings.Contains(got, "▶") {
		t.Errorf("missing side markers:\n%s", got)
	}
}

func TestFormatResolveTitle(t *testing.T) {
	region := &git.ConflictRegion{
		StartMarkerLine: 188,
		EndMarkerLine:   200,
	}
	got := formatResolveTitle("foo.go", 1, 4, region)
	want := "foo.go — region 1/4 · lines 188–200"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	got = formatResolveTitle("foo.go", 1, 4, nil)
	want = "foo.go — region 1/4"
	if got != want {
		t.Errorf("got %q, want %q (no region)", got, want)
	}
}

func TestBuildResolveOptions_NoAI(t *testing.T) {
	hunk := resolve.ConflictHunk{
		OursLabel:   "HEAD",
		TheirsLabel: "feat-branch",
		Ours:        []string{"a", "b", "c"},
		Theirs:      []string{"x"},
	}
	opts := buildResolveOptions(hunk, nil)
	if len(opts) != 2 {
		t.Fatalf("len(opts) = %d, want 2", len(opts))
	}
	if !strings.Contains(opts[0].Display, "HEAD") || !strings.Contains(opts[0].Display, "3 lines") {
		t.Errorf("ours label missing branch+count: %q", opts[0].Display)
	}
	if !strings.Contains(opts[1].Display, "feat-branch") || !strings.Contains(opts[1].Display, "1 line") {
		t.Errorf("theirs label missing branch+count: %q", opts[1].Display)
	}
}

func TestBuildResolveOptions_WithAI(t *testing.T) {
	hunk := resolve.ConflictHunk{
		OursLabel:   "HEAD",
		TheirsLabel: "feat-branch",
		Ours:        []string{"a"},
		Theirs:      []string{"b"},
	}
	ai := &resolve.HunkResolution{
		Strategy:      resolve.Strategy("theirs"),
		ResolvedLines: []string{"b"},
		Rationale:     "incoming wins",
	}
	opts := buildResolveOptions(hunk, ai)
	if len(opts) != 3 {
		t.Fatalf("len(opts) = %d, want 3 (ours/theirs/merged)", len(opts))
	}
	defaults := 0
	for _, o := range opts {
		if o.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("expected exactly one default option, got %d", defaults)
	}
	if !opts[1].IsDefault {
		t.Errorf("expected theirs to be default (matches AI strategy)")
	}
}

func TestBuildResolveOptions_AIStrategyTrimmed(t *testing.T) {
	hunk := resolve.ConflictHunk{
		OursLabel:   "HEAD",
		TheirsLabel: "feat",
		Ours:        []string{"a"},
		Theirs:      []string{"b"},
	}
	cases := []resolve.Strategy{"theirs", " theirs", "theirs ", "  Theirs ", "THEIRS"}
	for _, s := range cases {
		ai := &resolve.HunkResolution{Strategy: s, ResolvedLines: []string{"b"}, Rationale: "r"}
		opts := buildResolveOptions(hunk, ai)
		if !opts[1].IsDefault {
			t.Errorf("strategy %q: expected theirs to be default, none/wrong set", s)
		}
	}
}

func TestPickLabel(t *testing.T) {
	cases := []struct {
		in, fallback, want string
	}{
		{"HEAD", "fallback", "HEAD"},
		{"", "fallback", "fallback"},
		{"   ", "fb", "fb"},
		{"branch-name", "fb", "branch-name"},
	}
	for _, c := range cases {
		if got := pickLabel(c.in, c.fallback); got != c.want {
			t.Errorf("pickLabel(%q, %q) = %q, want %q", c.in, c.fallback, got, c.want)
		}
	}
}
