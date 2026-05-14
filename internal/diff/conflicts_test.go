package diff

import "testing"

func TestIsConflictMarkerLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"start marker bare", "<<<<<<<", true},
		{"start marker with branch", "<<<<<<< HEAD", true},
		{"split marker", "=======", true},
		{"end marker", ">>>>>>> feature/x", true},
		{"diff3 ancestor", "||||||| merged-common", true},
		{"too many chars", "<<<<<<<<", false},
		{"too few chars", "<<<<<<", false},
		{"prefix only matches", "<<<<<<<a", false},
		{"empty", "", false},
		{"context line", "if foo {", false},
		{"equals comparison", "x == 7", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsConflictMarkerLine(tc.in); got != tc.want {
				t.Fatalf("IsConflictMarkerLine(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestFilterConflictHunks(t *testing.T) {
	mkHunk := func(contents ...string) Hunk {
		lines := make([]DiffLine, len(contents))
		for i, c := range contents {
			lines[i] = DiffLine{Kind: LineContext, Content: c}
		}
		return Hunk{Lines: lines}
	}

	result := &DiffResult{Files: []DiffFile{
		{
			NewPath: "clean.go",
			Hunks:   []Hunk{mkHunk("a", "b")},
		},
		{
			NewPath: "conflict.go",
			Hunks: []Hunk{
				mkHunk("a", "b"),
				mkHunk("<<<<<<< HEAD", "x", "=======", "y", ">>>>>>> feat"),
			},
		},
		{
			NewPath: "all-conflict.go",
			Hunks: []Hunk{
				mkHunk("<<<<<<< HEAD", "z", "=======", "w", ">>>>>>> feat"),
			},
		},
	}}

	filtered := FilterConflictHunks(result)

	if len(filtered.Files) != 2 {
		t.Fatalf("expected 2 files (conflict.go, all-conflict.go), got %d", len(filtered.Files))
	}
	if filtered.Files[0].NewPath != "conflict.go" {
		t.Fatalf("expected conflict.go first, got %s", filtered.Files[0].NewPath)
	}
	if len(filtered.Files[0].Hunks) != 1 {
		t.Fatalf("expected 1 hunk in conflict.go (only the conflict one), got %d", len(filtered.Files[0].Hunks))
	}

	// 원본 불변성 확인
	if len(result.Files) != 3 || len(result.Files[1].Hunks) != 2 {
		t.Fatalf("FilterConflictHunks mutated original result")
	}
}

func TestBuildConflictHunks(t *testing.T) {
	raw := []string{
		"line 1",
		"line 2",
		"line 3",
		"<<<<<<< HEAD",
		"ours-a",
		"ours-b",
		"=======",
		"theirs-a",
		">>>>>>> feat",
		"line 4",
		"line 5",
	}

	hunks := BuildConflictHunks(raw, 2)
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	h := hunks[0]

	// 라인 분류 검증.
	var ours, theirs, ctx int
	for _, l := range h.Lines {
		switch l.Kind {
		case LineDeleted:
			ours++
		case LineAdded:
			theirs++
		case LineContext:
			ctx++
		}
	}
	if ours != 2 {
		t.Errorf("expected 2 ours lines (LineDeleted), got %d", ours)
	}
	if theirs != 1 {
		t.Errorf("expected 1 theirs line (LineAdded), got %d", theirs)
	}
	// context = 2 위 + 2 아래 + 3 marker = 7
	if ctx != 7 {
		t.Errorf("expected 7 context lines (incl. markers), got %d", ctx)
	}

	if h.OldStart != 2 {
		t.Errorf("expected OldStart=2 (block at index 3, context=2), got %d", h.OldStart)
	}
}

func TestBuildConflictHunks_AdjacentBlocksMerge(t *testing.T) {
	// 두 conflict block이 컨텍스트가 겹칠 만큼 가까우면 한 hunk로 병합.
	raw := []string{
		"line 1",
		"<<<<<<< HEAD",
		"a-ours",
		"=======",
		"a-theirs",
		">>>>>>> feat",
		"middle",
		"<<<<<<< HEAD",
		"b-ours",
		"=======",
		"b-theirs",
		">>>>>>> feat",
		"line end",
	}
	hunks := BuildConflictHunks(raw, 3)
	if len(hunks) != 1 {
		t.Fatalf("expected 1 merged hunk, got %d", len(hunks))
	}
}

func TestBuildConflictHunks_NoMarkers(t *testing.T) {
	hunks := BuildConflictHunks([]string{"a", "b", "c"}, 3)
	if len(hunks) != 0 {
		t.Fatalf("expected no hunks for marker-free input, got %d", len(hunks))
	}
}

func TestConflictFileIndices(t *testing.T) {
	result := &DiffResult{Files: []DiffFile{
		{NewPath: "a.go", Hunks: []Hunk{{Lines: []DiffLine{{Content: "ok"}}}}},
		{NewPath: "b.go", Hunks: []Hunk{{Lines: []DiffLine{{Content: "<<<<<<< HEAD"}}}}},
		{NewPath: "c.go", Hunks: []Hunk{{Lines: []DiffLine{{Content: "ok"}}}}},
		{NewPath: "d.go", Hunks: []Hunk{{Lines: []DiffLine{{Content: ">>>>>>> feat"}}}}},
	}}

	got := ConflictFileIndices(result)
	want := []int{1, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v; want %v", got, want)
		}
	}
}
