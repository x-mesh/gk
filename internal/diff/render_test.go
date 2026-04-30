package diff

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fatih/color"
)

// forceColor는 테스트에서 ANSI 색상을 강제 활성화한다.
func forceColor(t *testing.T) {
	t.Helper()
	prev := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = prev })
}

// noColor는 테스트에서 ANSI 색상을 비활성화한다.
func noColor(t *testing.T) {
	t.Helper()
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })
}

// ── RenderLine 단위 테스트 ──────────────────────────────────────

func TestRenderLine_Added(t *testing.T) {
	forceColor(t)
	line := DiffLine{Kind: LineAdded, Content: "hello world", NewNum: 42}
	got := RenderLine(line, false)

	// 초록색 ANSI 코드 포함 확인
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("added line should contain ANSI escape codes when color enabled, got: %q", got)
	}
	if !strings.Contains(got, "▶") {
		t.Error("added line should contain ▶ marker")
	}
	if !strings.Contains(got, "▌") {
		t.Error("added line should contain ▌ side bar")
	}
	if !strings.Contains(got, "hello world") {
		t.Error("added line should contain content")
	}
	if !strings.Contains(got, "42") {
		t.Error("added line should contain line number 42")
	}
}

func TestRenderLine_Deleted(t *testing.T) {
	forceColor(t)
	line := DiffLine{Kind: LineDeleted, Content: "removed line", OldNum: 7}
	got := RenderLine(line, false)

	if !strings.Contains(got, "\x1b[") {
		t.Errorf("deleted line should contain ANSI escape codes when color enabled, got: %q", got)
	}
	if !strings.Contains(got, "◀") {
		t.Error("deleted line should contain ◀ marker")
	}
	if !strings.Contains(got, "▌") {
		t.Error("deleted line should contain ▌ side bar")
	}
	if !strings.Contains(got, "removed line") {
		t.Error("deleted line should contain content")
	}
	if !strings.Contains(got, "7") {
		t.Error("deleted line should contain line number 7")
	}
}

func TestRenderLine_Context(t *testing.T) {
	noColor(t)
	line := DiffLine{Kind: LineContext, Content: "context line", OldNum: 10, NewNum: 10}
	got := RenderLine(line, true)

	if !strings.Contains(got, "·") {
		t.Error("context line should contain · marker")
	}
	if !strings.Contains(got, "context line") {
		t.Error("context line should contain content")
	}
	if !strings.Contains(got, "  10") {
		t.Error("context line should contain 4-digit line number")
	}
}

func TestRenderLine_NoColor(t *testing.T) {
	noColor(t)
	line := DiffLine{Kind: LineAdded, Content: "no color", NewNum: 1}
	got := RenderLine(line, true)

	if strings.Contains(got, "\x1b[") {
		t.Error("NoColor mode should not contain ANSI escape codes")
	}
	if !strings.Contains(got, "▶") {
		t.Error("NoColor mode should still contain ▶ marker")
	}
	if !strings.Contains(got, "▌") {
		t.Error("NoColor mode should still contain ▌ side bar")
	}
	if !strings.Contains(got, "no color") {
		t.Error("NoColor mode should still contain content")
	}
}

func TestRenderLine_LineNumber_FourDigitFixed(t *testing.T) {
	tests := []struct {
		num  int
		want string
	}{
		{1, "   1"},
		{10, "  10"},
		{100, " 100"},
		{1000, "1000"},
		{0, "    "}, // zero → blank
	}
	for _, tt := range tests {
		got := formatLineNum(tt.num)
		if got != tt.want {
			t.Errorf("formatLineNum(%d) = %q, want %q", tt.num, got, tt.want)
		}
	}
}

// ── Render 전체 출력 테스트 ─────────────────────────────────────

func TestRender_BasicOutput(t *testing.T) {
	noColor(t)
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "a/main.go",
				NewPath: "a/main.go",
				Status:  StatusModified,
				Hunks: []Hunk{
					{
						OldStart: 1, OldCount: 3,
						NewStart: 1, NewCount: 4,
						Header: "@@ -1,3 +1,4 @@",
						Lines: []DiffLine{
							{Kind: LineContext, Content: "package main", OldNum: 1, NewNum: 1},
							{Kind: LineAdded, Content: "import \"fmt\"", NewNum: 2},
							{Kind: LineContext, Content: "", OldNum: 2, NewNum: 3},
							{Kind: LineDeleted, Content: "// old comment", OldNum: 3},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	err := Render(&buf, result, RenderOptions{NoColor: true})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	out := buf.String()

	// 파일 헤더 확인
	if !strings.Contains(out, "a/main.go") {
		t.Error("output should contain file path")
	}
	if !strings.Contains(out, "─") {
		t.Error("output should contain separator line")
	}

	// Hunk 헤더 확인
	if !strings.Contains(out, "@@ -1,3 +1,4 @@") {
		t.Error("output should contain hunk header")
	}

	// 마커 확인
	if !strings.Contains(out, "▶") {
		t.Error("output should contain ▶ for added lines")
	}
	if !strings.Contains(out, "◀") {
		t.Error("output should contain ◀ for deleted lines")
	}
	if !strings.Contains(out, "·") {
		t.Error("output should contain · for context lines")
	}
}

func TestRender_NoColor_NoANSI(t *testing.T) {
	noColor(t)
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "test.go",
				NewPath: "test.go",
				Status:  StatusModified,
				Hunks: []Hunk{
					{
						OldStart: 1, OldCount: 1,
						NewStart: 1, NewCount: 2,
						Header: "@@ -1,1 +1,2 @@",
						Lines: []DiffLine{
							{Kind: LineContext, Content: "existing", OldNum: 1, NewNum: 1},
							{Kind: LineAdded, Content: "new line", NewNum: 2},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	err := Render(&buf, result, RenderOptions{NoColor: true})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if strings.Contains(buf.String(), "\x1b[") {
		t.Error("NoColor mode should not contain any ANSI escape sequences")
	}
}

func TestRender_BinaryFile(t *testing.T) {
	noColor(t)
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

	var buf bytes.Buffer
	err := Render(&buf, result, RenderOptions{NoColor: true})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if !strings.Contains(buf.String(), "Binary file differs") {
		t.Error("binary file should show 'Binary file differs' message")
	}
}

func TestRender_FileStatuses(t *testing.T) {
	noColor(t)
	tests := []struct {
		status FileStatus
		want   string
	}{
		{StatusAdded, "[added]"},
		{StatusDeleted, "[deleted]"},
		{StatusRenamed, "[renamed:"},
		{StatusModeChanged, "[mode:"},
	}

	for _, tt := range tests {
		f := DiffFile{
			OldPath: "old.go",
			NewPath: "new.go",
			Status:  tt.status,
			OldMode: "100644",
			NewMode: "100755",
		}
		result := &DiffResult{Files: []DiffFile{f}}

		var buf bytes.Buffer
		err := Render(&buf, result, RenderOptions{NoColor: true})
		if err != nil {
			t.Fatalf("Render returned error for status %v: %v", tt.status, err)
		}

		if !strings.Contains(buf.String(), tt.want) {
			t.Errorf("status %v: output should contain %q, got:\n%s", tt.status, tt.want, buf.String())
		}
	}
}

func TestRender_NilResult(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, nil, RenderOptions{})
	if err != nil {
		t.Fatalf("Render(nil) returned error: %v", err)
	}
	if buf.Len() != 0 {
		t.Error("Render(nil) should produce no output")
	}
}

func TestRender_EmptyFiles(t *testing.T) {
	result := &DiffResult{Files: []DiffFile{}}
	var buf bytes.Buffer
	err := Render(&buf, result, RenderOptions{})
	if err != nil {
		t.Fatalf("Render(empty) returned error: %v", err)
	}
	if buf.Len() != 0 {
		t.Error("Render(empty files) should produce no output")
	}
}

func TestRender_MultipleFiles(t *testing.T) {
	noColor(t)
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "a.go", NewPath: "a.go", Status: StatusModified,
				Hunks: []Hunk{{
					Header: "@@ -1,1 +1,1 @@",
					Lines:  []DiffLine{{Kind: LineAdded, Content: "a", NewNum: 1}},
				}},
			},
			{
				OldPath: "b.go", NewPath: "b.go", Status: StatusModified,
				Hunks: []Hunk{{
					Header: "@@ -1,1 +1,1 @@",
					Lines:  []DiffLine{{Kind: LineDeleted, Content: "b", OldNum: 1}},
				}},
			},
		},
	}

	var buf bytes.Buffer
	err := Render(&buf, result, RenderOptions{NoColor: true})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "a.go") {
		t.Error("output should contain first file")
	}
	if !strings.Contains(out, "b.go") {
		t.Error("output should contain second file")
	}
}

func TestRender_MultipleHunks(t *testing.T) {
	noColor(t)
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "main.go", NewPath: "main.go", Status: StatusModified,
				Hunks: []Hunk{
					{
						Header: "@@ -1,2 +1,2 @@",
						Lines:  []DiffLine{{Kind: LineAdded, Content: "first", NewNum: 1}},
					},
					{
						Header: "@@ -10,2 +10,2 @@",
						Lines:  []DiffLine{{Kind: LineDeleted, Content: "second", OldNum: 10}},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	err := Render(&buf, result, RenderOptions{NoColor: true})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "@@ -1,2 +1,2 @@") {
		t.Error("output should contain first hunk header")
	}
	if !strings.Contains(out, "@@ -10,2 +10,2 @@") {
		t.Error("output should contain second hunk header")
	}
}
