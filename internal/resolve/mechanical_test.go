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

func TestMechanicalHunk_WhitespaceOnly(t *testing.T) {
	h := hunk([]string{"value = 1", "  done"}, []string{"value  =  1", "done", ""})
	hr, ok := mechanicalHunkResolution("x.go", h, nil)
	if !ok {
		t.Fatal("whitespace-only difference must resolve")
	}
	if strings.Join(hr.ResolvedLines, "\n") != "value = 1\n  done" {
		t.Errorf("should take ours verbatim: %q", hr.ResolvedLines)
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
