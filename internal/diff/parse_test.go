package diff

import (
	"strings"
	"testing"
)

func TestParseUnifiedDiff_BasicModification(t *testing.T) {
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package main
 
-var x = 1
+var x = 2
+var y = 3
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.OldPath != "foo.go" {
		t.Errorf("OldPath = %q, want %q", f.OldPath, "foo.go")
	}
	if f.NewPath != "foo.go" {
		t.Errorf("NewPath = %q, want %q", f.NewPath, "foo.go")
	}
	if f.Status != StatusModified {
		t.Errorf("Status = %v, want StatusModified", f.Status)
	}
	if f.AddedLines != 2 {
		t.Errorf("AddedLines = %d, want 2", f.AddedLines)
	}
	if f.DeletedLines != 1 {
		t.Errorf("DeletedLines = %d, want 1", f.DeletedLines)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(f.Hunks))
	}

	h := f.Hunks[0]
	if h.OldStart != 1 || h.OldCount != 3 {
		t.Errorf("hunk old range = %d,%d, want 1,3", h.OldStart, h.OldCount)
	}
	if h.NewStart != 1 || h.NewCount != 4 {
		t.Errorf("hunk new range = %d,%d, want 1,4", h.NewStart, h.NewCount)
	}
	if len(h.Lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(h.Lines))
	}

	// 라인 종류 및 번호 검증
	assertLine(t, h.Lines[0], LineContext, "package main", 1, 1)
	assertLine(t, h.Lines[1], LineContext, "", 2, 2)
	assertLine(t, h.Lines[2], LineDeleted, "var x = 1", 3, 0)
	assertLine(t, h.Lines[3], LineAdded, "var x = 2", 0, 3)
	assertLine(t, h.Lines[4], LineAdded, "var y = 3", 0, 4)
}

func TestParseUnifiedDiff_MultipleFiles(t *testing.T) {
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,3 @@
 package main
+var x = 1
diff --git a/bar.go b/bar.go
index 3333333..4444444 100644
--- a/bar.go
+++ b/bar.go
@@ -1,3 +1,2 @@
 package main
-var y = 2
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result.Files))
	}

	if result.Files[0].NewPath != "foo.go" {
		t.Errorf("first file NewPath = %q, want %q", result.Files[0].NewPath, "foo.go")
	}
	if result.Files[0].AddedLines != 1 {
		t.Errorf("first file AddedLines = %d, want 1", result.Files[0].AddedLines)
	}
	if result.Files[1].NewPath != "bar.go" {
		t.Errorf("second file NewPath = %q, want %q", result.Files[1].NewPath, "bar.go")
	}
	if result.Files[1].DeletedLines != 1 {
		t.Errorf("second file DeletedLines = %d, want 1", result.Files[1].DeletedLines)
	}
}

func TestParseUnifiedDiff_BinaryFile(t *testing.T) {
	input := `diff --git a/image.png b/image.png
index 1111111..2222222 100644
Binary files a/image.png and b/image.png differ
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}
	if !result.Files[0].IsBinary {
		t.Error("expected IsBinary = true")
	}
	if len(result.Files[0].Hunks) != 0 {
		t.Errorf("expected 0 hunks for binary file, got %d", len(result.Files[0].Hunks))
	}
}

func TestParseUnifiedDiff_Rename(t *testing.T) {
	input := `diff --git a/old.go b/new.go
similarity index 95%
rename from old.go
rename to new.go
index 1111111..2222222 100644
--- a/old.go
+++ b/new.go
@@ -1,3 +1,3 @@
 package main
 
-var name = "old"
+var name = "new"
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Status != StatusRenamed {
		t.Errorf("Status = %v, want StatusRenamed", f.Status)
	}
	if f.OldPath != "old.go" {
		t.Errorf("OldPath = %q, want %q", f.OldPath, "old.go")
	}
	if f.NewPath != "new.go" {
		t.Errorf("NewPath = %q, want %q", f.NewPath, "new.go")
	}
}

func TestParseUnifiedDiff_RenameWithoutContent(t *testing.T) {
	input := `diff --git a/old.go b/new.go
similarity index 100%
rename from old.go
rename to new.go
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Status != StatusRenamed {
		t.Errorf("Status = %v, want StatusRenamed", f.Status)
	}
	if f.OldPath != "old.go" {
		t.Errorf("OldPath = %q, want %q", f.OldPath, "old.go")
	}
	if f.NewPath != "new.go" {
		t.Errorf("NewPath = %q, want %q", f.NewPath, "new.go")
	}
	if len(f.Hunks) != 0 {
		t.Errorf("expected 0 hunks, got %d", len(f.Hunks))
	}
}

func TestParseUnifiedDiff_ModeChange(t *testing.T) {
	input := `diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Status != StatusModeChanged {
		t.Errorf("Status = %v, want StatusModeChanged", f.Status)
	}
	if f.OldMode != "100644" {
		t.Errorf("OldMode = %q, want %q", f.OldMode, "100644")
	}
	if f.NewMode != "100755" {
		t.Errorf("NewMode = %q, want %q", f.NewMode, "100755")
	}
}

func TestParseUnifiedDiff_NewFile(t *testing.T) {
	input := `diff --git a/new.go b/new.go
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/new.go
@@ -0,0 +1,3 @@
+package main
+
+var x = 1
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Status != StatusAdded {
		t.Errorf("Status = %v, want StatusAdded", f.Status)
	}
	if f.NewPath != "new.go" {
		t.Errorf("NewPath = %q, want %q", f.NewPath, "new.go")
	}
	if f.AddedLines != 3 {
		t.Errorf("AddedLines = %d, want 3", f.AddedLines)
	}
}

func TestParseUnifiedDiff_DeletedFile(t *testing.T) {
	input := `diff --git a/old.go b/old.go
deleted file mode 100644
index 1111111..0000000
--- a/old.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package main
-
-var x = 1
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Status != StatusDeleted {
		t.Errorf("Status = %v, want StatusDeleted", f.Status)
	}
	if f.DeletedLines != 3 {
		t.Errorf("DeletedLines = %d, want 3", f.DeletedLines)
	}
}

func TestParseUnifiedDiff_MultipleHunks(t *testing.T) {
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package main
 
+import "fmt"
 func main() {
@@ -10,3 +11,4 @@
 	x := 1
 	y := 2
+	fmt.Println(x, y)
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if len(f.Hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(f.Hunks))
	}
	if f.Hunks[0].OldStart != 1 {
		t.Errorf("first hunk OldStart = %d, want 1", f.Hunks[0].OldStart)
	}
	if f.Hunks[1].OldStart != 10 {
		t.Errorf("second hunk OldStart = %d, want 10", f.Hunks[1].OldStart)
	}
	if f.AddedLines != 2 {
		t.Errorf("AddedLines = %d, want 2", f.AddedLines)
	}
}

func TestParseUnifiedDiff_EmptyInput(t *testing.T) {
	result, err := ParseUnifiedDiff(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 0 {
		t.Errorf("expected 0 files, got %d", len(result.Files))
	}
}

func TestParseUnifiedDiff_NoNewlineAtEnd(t *testing.T) {
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,2 @@
 package main
-var x = 1
\ No newline at end of file
+var x = 2
\ No newline at end of file
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.AddedLines != 1 {
		t.Errorf("AddedLines = %d, want 1", f.AddedLines)
	}
	if f.DeletedLines != 1 {
		t.Errorf("DeletedLines = %d, want 1", f.DeletedLines)
	}
}

func TestParseUnifiedDiff_HunkSingleLineCount(t *testing.T) {
	// count가 생략된 경우 (단일 라인 변경)
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1 +1 @@
-old line
+new line
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	h := result.Files[0].Hunks[0]
	if h.OldCount != 1 {
		t.Errorf("OldCount = %d, want 1", h.OldCount)
	}
	if h.NewCount != 1 {
		t.Errorf("NewCount = %d, want 1", h.NewCount)
	}
}

func TestParseUnifiedDiff_LineNumbers(t *testing.T) {
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -5,4 +5,5 @@
 line5
-line6
+line6a
+line6b
 line7
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := result.Files[0].Hunks[0].Lines
	// context: line5 → old=5, new=5
	assertLine(t, lines[0], LineContext, "line5", 5, 5)
	// deleted: line6 → old=6, new=0
	assertLine(t, lines[1], LineDeleted, "line6", 6, 0)
	// added: line6a → old=0, new=6
	assertLine(t, lines[2], LineAdded, "line6a", 0, 6)
	// added: line6b → old=0, new=7
	assertLine(t, lines[3], LineAdded, "line6b", 0, 7)
	// context: line7 → old=7, new=8
	assertLine(t, lines[4], LineContext, "line7", 7, 8)
}

func assertLine(t *testing.T, dl DiffLine, kind LineKind, content string, oldNum, newNum int) {
	t.Helper()
	if dl.Kind != kind {
		t.Errorf("line %q: Kind = %v, want %v", content, dl.Kind, kind)
	}
	if dl.Content != content {
		t.Errorf("line content = %q, want %q", dl.Content, content)
	}
	if dl.OldNum != oldNum {
		t.Errorf("line %q: OldNum = %d, want %d", content, dl.OldNum, oldNum)
	}
	if dl.NewNum != newNum {
		t.Errorf("line %q: NewNum = %d, want %d", content, dl.NewNum, newNum)
	}
}

// ── 추가 엣지 케이스 테스트 ──────────────────────────────────────

func TestParseUnifiedDiff_CopyDetection(t *testing.T) {
	input := `diff --git a/original.go b/copied.go
copy from original.go
copy to copied.go
index 1111111..2222222 100644
--- a/original.go
+++ b/copied.go
@@ -1,3 +1,3 @@
 package main
 
-var x = 1
+var x = 2
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Status != StatusCopied {
		t.Errorf("Status = %v, want StatusCopied", f.Status)
	}
	if f.OldPath != "original.go" {
		t.Errorf("OldPath = %q, want %q", f.OldPath, "original.go")
	}
	if f.NewPath != "copied.go" {
		t.Errorf("NewPath = %q, want %q", f.NewPath, "copied.go")
	}
	if f.AddedLines != 1 {
		t.Errorf("AddedLines = %d, want 1", f.AddedLines)
	}
	if f.DeletedLines != 1 {
		t.Errorf("DeletedLines = %d, want 1", f.DeletedLines)
	}
}

func TestParseUnifiedDiff_FilesWithSpacesInPaths(t *testing.T) {
	input := `diff --git a/my file.go b/my file.go
index 1111111..2222222 100644
--- a/my file.go
+++ b/my file.go
@@ -1,2 +1,2 @@
 package main
-var x = 1
+var x = 2
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.OldPath != "my file.go" {
		t.Errorf("OldPath = %q, want %q", f.OldPath, "my file.go")
	}
	if f.NewPath != "my file.go" {
		t.Errorf("NewPath = %q, want %q", f.NewPath, "my file.go")
	}
}

func TestParseUnifiedDiff_VeryLongLines(t *testing.T) {
	longContent := strings.Repeat("x", 100_000)
	input := `diff --git a/big.txt b/big.txt
index 1111111..2222222 100644
--- a/big.txt
+++ b/big.txt
@@ -1 +1 @@
-` + longContent + `
+` + longContent + "y" + `
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.AddedLines != 1 {
		t.Errorf("AddedLines = %d, want 1", f.AddedLines)
	}
	if f.DeletedLines != 1 {
		t.Errorf("DeletedLines = %d, want 1", f.DeletedLines)
	}
	// 삭제된 라인 내용이 원본 긴 문자열과 일치하는지 확인
	if got := f.Hunks[0].Lines[0].Content; got != longContent {
		t.Errorf("deleted line length = %d, want %d", len(got), len(longContent))
	}
	// 추가된 라인 내용이 수정된 긴 문자열과 일치하는지 확인
	if got := f.Hunks[0].Lines[1].Content; got != longContent+"y" {
		t.Errorf("added line length = %d, want %d", len(got), len(longContent)+1)
	}
}

func TestParseUnifiedDiff_MixedBinaryAndTextFiles(t *testing.T) {
	input := `diff --git a/readme.md b/readme.md
index 1111111..2222222 100644
--- a/readme.md
+++ b/readme.md
@@ -1,2 +1,3 @@
 # Title
+New line
diff --git a/logo.png b/logo.png
index 3333333..4444444 100644
Binary files a/logo.png and b/logo.png differ
diff --git a/main.go b/main.go
index 5555555..6666666 100644
--- a/main.go
+++ b/main.go
@@ -1,2 +1,2 @@
 package main
-var v = 1
+var v = 2
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(result.Files))
	}

	// 첫 번째: 텍스트 파일
	if result.Files[0].IsBinary {
		t.Error("first file: expected IsBinary = false")
	}
	if result.Files[0].AddedLines != 1 {
		t.Errorf("first file AddedLines = %d, want 1", result.Files[0].AddedLines)
	}

	// 두 번째: 바이너리 파일
	if !result.Files[1].IsBinary {
		t.Error("second file: expected IsBinary = true")
	}
	if len(result.Files[1].Hunks) != 0 {
		t.Errorf("second file: expected 0 hunks, got %d", len(result.Files[1].Hunks))
	}

	// 세 번째: 텍스트 파일
	if result.Files[2].IsBinary {
		t.Error("third file: expected IsBinary = false")
	}
	if result.Files[2].AddedLines != 1 || result.Files[2].DeletedLines != 1 {
		t.Errorf("third file: AddedLines=%d DeletedLines=%d, want 1,1",
			result.Files[2].AddedLines, result.Files[2].DeletedLines)
	}
}

func TestParseUnifiedDiff_HunkWithOnlyContextLines(t *testing.T) {
	// 컨텍스트 라인만 있는 hunk (추가/삭제 없음)
	// 실제 git diff에서는 드물지만 파서가 올바르게 처리해야 함
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,3 @@
 line1
 line2
 line3
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.AddedLines != 0 {
		t.Errorf("AddedLines = %d, want 0", f.AddedLines)
	}
	if f.DeletedLines != 0 {
		t.Errorf("DeletedLines = %d, want 0", f.DeletedLines)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(f.Hunks))
	}
	if len(f.Hunks[0].Lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(f.Hunks[0].Lines))
	}
	for i, l := range f.Hunks[0].Lines {
		if l.Kind != LineContext {
			t.Errorf("line %d: Kind = %v, want LineContext", i, l.Kind)
		}
	}
}

func TestParseUnifiedDiff_HunkWithOnlyAdditions(t *testing.T) {
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,0 +1,3 @@
+line1
+line2
+line3
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f := result.Files[0]
	if f.AddedLines != 3 {
		t.Errorf("AddedLines = %d, want 3", f.AddedLines)
	}
	if f.DeletedLines != 0 {
		t.Errorf("DeletedLines = %d, want 0", f.DeletedLines)
	}
	for i, l := range f.Hunks[0].Lines {
		if l.Kind != LineAdded {
			t.Errorf("line %d: Kind = %v, want LineAdded", i, l.Kind)
		}
	}
}

func TestParseUnifiedDiff_HunkWithOnlyDeletions(t *testing.T) {
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,0 @@
-line1
-line2
-line3
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f := result.Files[0]
	if f.AddedLines != 0 {
		t.Errorf("AddedLines = %d, want 0", f.AddedLines)
	}
	if f.DeletedLines != 3 {
		t.Errorf("DeletedLines = %d, want 3", f.DeletedLines)
	}
	for i, l := range f.Hunks[0].Lines {
		if l.Kind != LineDeleted {
			t.Errorf("line %d: Kind = %v, want LineDeleted", i, l.Kind)
		}
	}
}

func TestParseUnifiedDiff_MalformedInput_NoDiffHeader(t *testing.T) {
	// diff --git 헤더 없이 시작하는 입력 → 파일 0개
	input := `--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,2 @@
 line1
-old
+new
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 0 {
		t.Errorf("expected 0 files for input without diff header, got %d", len(result.Files))
	}
}

func TestParseUnifiedDiff_MalformedInput_TruncatedNoHunks(t *testing.T) {
	// diff --git 헤더만 있고 hunk가 없는 경우
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}
	if len(result.Files[0].Hunks) != 0 {
		t.Errorf("expected 0 hunks, got %d", len(result.Files[0].Hunks))
	}
}

func TestParseUnifiedDiff_GarbageInput(t *testing.T) {
	// 완전히 관련 없는 텍스트 → 에러 없이 파일 0개
	input := `This is not a diff at all.
Just some random text.
Nothing to see here.
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 0 {
		t.Errorf("expected 0 files for garbage input, got %d", len(result.Files))
	}
}

func TestParseUnifiedDiff_CopyWithoutContent(t *testing.T) {
	// 내용 변경 없는 복사 (rename without content와 유사)
	input := `diff --git a/src.go b/dst.go
copy from src.go
copy to dst.go
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Status != StatusCopied {
		t.Errorf("Status = %v, want StatusCopied", f.Status)
	}
	if f.OldPath != "src.go" {
		t.Errorf("OldPath = %q, want %q", f.OldPath, "src.go")
	}
	if f.NewPath != "dst.go" {
		t.Errorf("NewPath = %q, want %q", f.NewPath, "dst.go")
	}
	if len(f.Hunks) != 0 {
		t.Errorf("expected 0 hunks, got %d", len(f.Hunks))
	}
}

func TestParseUnifiedDiff_DissimilarityIndex(t *testing.T) {
	// dissimilarity index 헤더가 있는 경우 (완전히 다시 작성된 파일)
	input := `diff --git a/rewrite.go b/rewrite.go
dissimilarity index 95%
index 1111111..2222222 100644
--- a/rewrite.go
+++ b/rewrite.go
@@ -1,2 +1,2 @@
-old content
+new content
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.AddedLines != 1 {
		t.Errorf("AddedLines = %d, want 1", f.AddedLines)
	}
	if f.DeletedLines != 1 {
		t.Errorf("DeletedLines = %d, want 1", f.DeletedLines)
	}
}

func TestParseUnifiedDiff_HunkHeaderWithFunctionContext(t *testing.T) {
	// @@ 헤더 뒤에 함수 컨텍스트가 있는 경우
	input := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -10,3 +10,4 @@ func main() {
 	x := 1
 	y := 2
+	z := 3
`
	result, err := ParseUnifiedDiff(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	h := result.Files[0].Hunks[0]
	if h.OldStart != 10 || h.OldCount != 3 {
		t.Errorf("hunk old range = %d,%d, want 10,3", h.OldStart, h.OldCount)
	}
	if h.NewStart != 10 || h.NewCount != 4 {
		t.Errorf("hunk new range = %d,%d, want 10,4", h.NewStart, h.NewCount)
	}
	// 헤더에 함수 컨텍스트가 포함되어야 함
	if !strings.Contains(h.Header, "func main()") {
		t.Errorf("Header = %q, expected to contain function context", h.Header)
	}
}
