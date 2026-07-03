package resolve

import (
	"strings"
	"testing"
)

func hunk(ours, theirs []string) *ConflictHunk {
	return &ConflictHunk{Ours: ours, Theirs: theirs}
}

func TestMechanicalHunk_IdenticalSides(t *testing.T) {
	h := hunk([]string{"a", "b"}, []string{"a", "b"})
	hr, ok := mechanicalHunkResolution("x.go", h, nil)
	if !ok || strings.Join(hr.ResolvedLines, ",") != "a,b" {
		t.Fatalf("identical sides must resolve: ok=%v %+v", ok, hr)
	}
}

func TestMechanicalHunk_TrailingWhitespaceOnly(t *testing.T) {
	h := hunk([]string{"value = 1", "done\t"}, []string{"value = 1  ", "done\r"})
	hr, ok := mechanicalHunkResolution("x.go", h, nil)
	if !ok {
		t.Fatal("trailing-whitespace/CR difference must resolve")
	}
	if strings.Join(hr.ResolvedLines, "\n") != "value = 1\ndone\t" {
		t.Errorf("should take ours verbatim: %q", hr.ResolvedLines)
	}
}

// Internal spacing and indentation carry meaning (string literals, Python,
// Makefile) — the whitespace rule must NOT swallow them (cross-vendor
// review: 2 vendors rated this Critical against the original loose rule).
func TestMechanicalHunk_MeaningfulWhitespaceStays(t *testing.T) {
	cases := [][2][]string{
		{{`s := "a b"`}, {`s := "a  b"`}},  // string literal internal spacing
		{{"    return x"}, {"  return x"}}, // indentation depth
		{{"a", "", "b"}, {"a", "b"}},       // blank-line (paragraph) difference
	}
	for i, c := range cases {
		if _, ok := mechanicalHunkResolution("x.py", hunk(c[0], c[1]), nil); ok {
			t.Errorf("case %d: meaningful whitespace difference must NOT resolve: %q vs %q", i, c[0], c[1])
		}
	}
}

// An explicit empty union list disables union merging (nil keeps defaults).
func TestMechanicalHunk_EmptyUnionListDisables(t *testing.T) {
	h := hunk([]string{"- ours"}, []string{"- theirs"})
	if _, ok := mechanicalHunkResolution("CHANGELOG.md", h, []string{}); ok {
		t.Fatal("explicit empty union_files must disable union merging")
	}
}

// go.sum union refuses when both sides carry different hashes for the same
// module@version — that mismatch is go.sum's tampering signal.
func TestMechanicalHunk_GoSumHashConflictRefused(t *testing.T) {
	h := hunk(
		[]string{"mod-a v1.0.0 h1:AAA"},
		[]string{"mod-a v1.0.0 h1:BBB"},
	)
	if _, ok := mechanicalHunkResolution("go.sum", h, nil); ok {
		t.Fatal("same module@version with different hashes must stay conflicted")
	}
}

// Union merge requires provable additivity when diff3 base info exists: a
// non-empty base block means a rewrite conflict, not two additions.
func TestMechanicalHunk_UnionRequiresAdditiveBase(t *testing.T) {
	h := &ConflictHunk{
		Ours:   []string{"- rewritten ours"},
		Theirs: []string{"- rewritten theirs"},
		Base:   []string{"- original entry"},
	}
	if _, ok := mechanicalHunkResolution("CHANGELOG.md", h, nil); ok {
		t.Fatal("union with non-empty base must stay conflicted")
	}
	h.Base = []string{}
	if _, ok := mechanicalHunkResolution("CHANGELOG.md", h, nil); !ok {
		t.Fatal("union with empty base (both added) must resolve")
	}
}

func TestMechanicalHunk_OneSideUnchangedFromBase(t *testing.T) {
	h := &ConflictHunk{
		Ours:   []string{"original"},
		Theirs: []string{"changed"},
		Base:   []string{"original"},
	}
	hr, ok := mechanicalHunkResolution("x.go", h, nil)
	if !ok || strings.Join(hr.ResolvedLines, ",") != "changed" {
		t.Fatalf("ours==base must take theirs: ok=%v %+v", ok, hr)
	}
	h2 := &ConflictHunk{
		Ours:   []string{"changed"},
		Theirs: []string{"original"},
		Base:   []string{"original"},
	}
	hr2, ok2 := mechanicalHunkResolution("x.go", h2, nil)
	if !ok2 || strings.Join(hr2.ResolvedLines, ",") != "changed" {
		t.Fatalf("theirs==base must take ours: ok=%v %+v", ok2, hr2)
	}
}

func TestMechanicalHunk_GoSumUnion(t *testing.T) {
	h := hunk(
		[]string{"mod-b v1 h1:bbb", "mod-a v1 h1:aaa"},
		[]string{"mod-c v1 h1:ccc", "mod-a v1 h1:aaa"},
	)
	hr, ok := mechanicalHunkResolution("go.sum", h, nil)
	if !ok {
		t.Fatal("go.sum must union")
	}
	want := "mod-a v1 h1:aaa\nmod-b v1 h1:bbb\nmod-c v1 h1:ccc"
	if strings.Join(hr.ResolvedLines, "\n") != want {
		t.Errorf("sorted unique union:\ngot  %q\nwant %q", strings.Join(hr.ResolvedLines, "\n"), want)
	}
}

func TestMechanicalHunk_ChangelogUnionKeepsOrder(t *testing.T) {
	h := hunk([]string{"- ours entry"}, []string{"- theirs entry"})
	hr, ok := mechanicalHunkResolution("docs/CHANGELOG.md", h, nil)
	if !ok {
		t.Fatal("CHANGELOG must union")
	}
	if strings.Join(hr.ResolvedLines, "\n") != "- ours entry\n- theirs entry" {
		t.Errorf("ours-first concat: %q", hr.ResolvedLines)
	}
}

func TestMechanicalHunk_RealConflictStays(t *testing.T) {
	h := hunk([]string{"x = 1"}, []string{"x = 2"})
	if _, ok := mechanicalHunkResolution("x.go", h, nil); ok {
		t.Fatal("semantic conflict must NOT resolve mechanically")
	}
}

// All-or-nothing per file: one judgment hunk keeps the whole file out.
func TestMechanicalFile_AllOrNothing(t *testing.T) {
	cf := ConflictFile{
		Path: "x.go",
		Segments: []Segment{
			{Hunk: hunk([]string{"same"}, []string{"same"})},
			{Context: []string{"ctx"}},
			{Hunk: hunk([]string{"x = 1"}, []string{"x = 2"})},
		},
	}
	if _, ok := mechanicalFileResolutions(cf, nil); ok {
		t.Fatal("file with one semantic hunk must not resolve mechanically")
	}
}
