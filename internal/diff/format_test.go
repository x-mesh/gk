package diff

import (
	"strings"
	"testing"
)

func TestFormatUnifiedDiff_NilResult(t *testing.T) {
	got := FormatUnifiedDiff(nil)
	if got != "" {
		t.Errorf("expected empty string for nil result, got %q", got)
	}
}

func TestFormatUnifiedDiff_EmptyFiles(t *testing.T) {
	got := FormatUnifiedDiff(&DiffResult{})
	if got != "" {
		t.Errorf("expected empty string for empty files, got %q", got)
	}
}

func TestFormatUnifiedDiff_SimpleModified(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "hello.go",
				NewPath: "hello.go",
				Status:  StatusModified,
				Hunks: []Hunk{
					{
						OldStart: 1,
						OldCount: 3,
						NewStart: 1,
						NewCount: 4,
						Lines: []DiffLine{
							{Kind: LineContext, Content: "package main", OldNum: 1, NewNum: 1},
							{Kind: LineDeleted, Content: "old line", OldNum: 2},
							{Kind: LineAdded, Content: "new line", NewNum: 2},
							{Kind: LineAdded, Content: "extra line", NewNum: 3},
							{Kind: LineContext, Content: "end", OldNum: 3, NewNum: 4},
						},
					},
				},
			},
		},
	}

	got := FormatUnifiedDiff(result)

	// 기본 구조 확인
	if !strings.Contains(got, "diff --git a/hello.go b/hello.go") {
		t.Error("missing diff --git header")
	}
	if !strings.Contains(got, "--- a/hello.go") {
		t.Error("missing --- header")
	}
	if !strings.Contains(got, "+++ b/hello.go") {
		t.Error("missing +++ header")
	}
	if !strings.Contains(got, "@@ -1,3 +1,4 @@") {
		t.Error("missing @@ hunk header")
	}
	if !strings.Contains(got, " package main") {
		t.Error("missing context line")
	}
	if !strings.Contains(got, "-old line") {
		t.Error("missing deleted line")
	}
	if !strings.Contains(got, "+new line") {
		t.Error("missing added line")
	}
}

func TestFormatUnifiedDiff_AddedFile(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "new.go",
				NewPath: "new.go",
				Status:  StatusAdded,
				NewMode: "100644",
				Hunks: []Hunk{
					{
						OldStart: 0,
						OldCount: 0,
						NewStart: 1,
						NewCount: 2,
						Lines: []DiffLine{
							{Kind: LineAdded, Content: "package new", NewNum: 1},
							{Kind: LineAdded, Content: "func New() {}", NewNum: 2},
						},
					},
				},
			},
		},
	}

	got := FormatUnifiedDiff(result)

	if !strings.Contains(got, "new file mode 100644") {
		t.Error("missing new file mode header")
	}
	if !strings.Contains(got, "--- /dev/null") {
		t.Error("missing --- /dev/null for added file")
	}
	if !strings.Contains(got, "+++ b/new.go") {
		t.Error("missing +++ header for added file")
	}
}

func TestFormatUnifiedDiff_DeletedFile(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "old.go",
				NewPath: "old.go",
				Status:  StatusDeleted,
				OldMode: "100644",
				Hunks: []Hunk{
					{
						OldStart: 1,
						OldCount: 1,
						NewStart: 0,
						NewCount: 0,
						Lines: []DiffLine{
							{Kind: LineDeleted, Content: "package old", OldNum: 1},
						},
					},
				},
			},
		},
	}

	got := FormatUnifiedDiff(result)

	if !strings.Contains(got, "deleted file mode 100644") {
		t.Error("missing deleted file mode header")
	}
	if !strings.Contains(got, "--- a/old.go") {
		t.Error("missing --- header for deleted file")
	}
	if !strings.Contains(got, "+++ /dev/null") {
		t.Error("missing +++ /dev/null for deleted file")
	}
}

func TestFormatUnifiedDiff_RenamedFile(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "old_name.go",
				NewPath: "new_name.go",
				Status:  StatusRenamed,
				Hunks: []Hunk{
					{
						OldStart: 1,
						OldCount: 1,
						NewStart: 1,
						NewCount: 1,
						Lines: []DiffLine{
							{Kind: LineContext, Content: "package main", OldNum: 1, NewNum: 1},
						},
					},
				},
			},
		},
	}

	got := FormatUnifiedDiff(result)

	if !strings.Contains(got, "rename from old_name.go") {
		t.Error("missing rename from header")
	}
	if !strings.Contains(got, "rename to new_name.go") {
		t.Error("missing rename to header")
	}
}

func TestFormatUnifiedDiff_ModeChanged(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "script.sh",
				NewPath: "script.sh",
				Status:  StatusModeChanged,
				OldMode: "100644",
				NewMode: "100755",
			},
		},
	}

	got := FormatUnifiedDiff(result)

	if !strings.Contains(got, "old mode 100644") {
		t.Error("missing old mode header")
	}
	if !strings.Contains(got, "new mode 100755") {
		t.Error("missing new mode header")
	}
}

func TestFormatUnifiedDiff_BinaryFile(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:  "image.png",
				NewPath:  "image.png",
				Status:   StatusModified,
				IsBinary: true,
			},
		},
	}

	got := FormatUnifiedDiff(result)

	if !strings.Contains(got, "Binary files a/image.png and b/image.png differ") {
		t.Error("missing binary files differ message")
	}
}

func TestFormatUnifiedDiff_RoundTrip(t *testing.T) {
	// 원본 unified diff 텍스트
	original := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,5 +1,6 @@
 package main
 
-func old() {}
+func new() {}
+func extra() {}
 
 func keep() {}
`

	// 파싱
	parsed, err := ParseUnifiedDiff(strings.NewReader(original))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// 포맷
	formatted := FormatUnifiedDiff(parsed)

	// 다시 파싱
	reparsed, err := ParseUnifiedDiff(strings.NewReader(formatted))
	if err != nil {
		t.Fatalf("reparse error: %v", err)
	}

	// 구조 비교
	if len(parsed.Files) != len(reparsed.Files) {
		t.Fatalf("file count mismatch: %d vs %d", len(parsed.Files), len(reparsed.Files))
	}

	for i, pf := range parsed.Files {
		rf := reparsed.Files[i]
		if pf.OldPath != rf.OldPath {
			t.Errorf("file %d: OldPath mismatch: %q vs %q", i, pf.OldPath, rf.OldPath)
		}
		if pf.NewPath != rf.NewPath {
			t.Errorf("file %d: NewPath mismatch: %q vs %q", i, pf.NewPath, rf.NewPath)
		}
		if pf.Status != rf.Status {
			t.Errorf("file %d: Status mismatch: %v vs %v", i, pf.Status, rf.Status)
		}
		if len(pf.Hunks) != len(rf.Hunks) {
			t.Fatalf("file %d: hunk count mismatch: %d vs %d", i, len(pf.Hunks), len(rf.Hunks))
		}
		for j, ph := range pf.Hunks {
			rh := rf.Hunks[j]
			if ph.OldStart != rh.OldStart || ph.OldCount != rh.OldCount ||
				ph.NewStart != rh.NewStart || ph.NewCount != rh.NewCount {
				t.Errorf("file %d hunk %d: range mismatch: -%d,%d +%d,%d vs -%d,%d +%d,%d",
					i, j, ph.OldStart, ph.OldCount, ph.NewStart, ph.NewCount,
					rh.OldStart, rh.OldCount, rh.NewStart, rh.NewCount)
			}
			if len(ph.Lines) != len(rh.Lines) {
				t.Fatalf("file %d hunk %d: line count mismatch: %d vs %d", i, j, len(ph.Lines), len(rh.Lines))
			}
			for k, pl := range ph.Lines {
				rl := rh.Lines[k]
				if pl.Kind != rl.Kind {
					t.Errorf("file %d hunk %d line %d: kind mismatch: %v vs %v", i, j, k, pl.Kind, rl.Kind)
				}
				if pl.Content != rl.Content {
					t.Errorf("file %d hunk %d line %d: content mismatch: %q vs %q", i, j, k, pl.Content, rl.Content)
				}
			}
		}
	}
}

func TestFormatUnifiedDiff_MultipleFiles(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "a.go",
				NewPath: "a.go",
				Status:  StatusModified,
				Hunks: []Hunk{
					{
						OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
						Lines: []DiffLine{
							{Kind: LineContext, Content: "package a", OldNum: 1, NewNum: 1},
						},
					},
				},
			},
			{
				OldPath: "b.go",
				NewPath: "b.go",
				Status:  StatusModified,
				Hunks: []Hunk{
					{
						OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 2,
						Lines: []DiffLine{
							{Kind: LineContext, Content: "package b", OldNum: 1, NewNum: 1},
							{Kind: LineAdded, Content: "func B() {}", NewNum: 2},
						},
					},
				},
			},
		},
	}

	got := FormatUnifiedDiff(result)

	// 두 파일 모두 포함되어야 함
	if strings.Count(got, "diff --git") != 2 {
		t.Errorf("expected 2 diff --git headers, got %d", strings.Count(got, "diff --git"))
	}
	if !strings.Contains(got, "a/a.go") {
		t.Error("missing first file")
	}
	if !strings.Contains(got, "a/b.go") {
		t.Error("missing second file")
	}
}
