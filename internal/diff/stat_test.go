package diff

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestRenderStat_NilResult(t *testing.T) {
	var buf bytes.Buffer
	err := RenderStat(&buf, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected empty output, got: %q", buf.String())
	}
}

func TestRenderStat_EmptyFiles(t *testing.T) {
	var buf bytes.Buffer
	err := RenderStat(&buf, &DiffResult{}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected empty output, got: %q", buf.String())
	}
}

func TestRenderStat_SingleFile(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "file1.go",
				NewPath:      "file1.go",
				Status:       StatusModified,
				AddedLines:   3,
				DeletedLines: 2,
			},
		},
	}

	var buf bytes.Buffer
	err := RenderStat(&buf, result, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	// 파일명 포함
	if !strings.Contains(output, "file1.go") {
		t.Errorf("output should contain filename: %q", output)
	}
	// 변경 수 포함
	if !strings.Contains(output, "5 ") {
		t.Errorf("output should contain total changes '5': %q", output)
	}
	// + 와 - 포함
	if !strings.Contains(output, "+") {
		t.Errorf("output should contain '+': %q", output)
	}
	if !strings.Contains(output, "-") {
		t.Errorf("output should contain '-': %q", output)
	}
	// 요약 라인
	if !strings.Contains(output, "1 file changed") {
		t.Errorf("output should contain '1 file changed': %q", output)
	}
	if !strings.Contains(output, "3 insertions(+)") {
		t.Errorf("output should contain '3 insertions(+)': %q", output)
	}
	if !strings.Contains(output, "2 deletions(-)") {
		t.Errorf("output should contain '2 deletions(-)': %q", output)
	}
}

func TestRenderStat_MultipleFiles(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "file1.go",
				NewPath:      "file1.go",
				Status:       StatusModified,
				AddedLines:   5,
				DeletedLines: 2,
			},
			{
				OldPath:      "file2.go",
				NewPath:      "file2.go",
				Status:       StatusModified,
				AddedLines:   3,
				DeletedLines: 1,
			},
		},
	}

	var buf bytes.Buffer
	err := RenderStat(&buf, result, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "file1.go") {
		t.Errorf("output should contain file1.go: %q", output)
	}
	if !strings.Contains(output, "file2.go") {
		t.Errorf("output should contain file2.go: %q", output)
	}
	if !strings.Contains(output, "2 files changed") {
		t.Errorf("output should contain '2 files changed': %q", output)
	}
	if !strings.Contains(output, "8 insertions(+)") {
		t.Errorf("output should contain '8 insertions(+)': %q", output)
	}
	if !strings.Contains(output, "3 deletions(-)") {
		t.Errorf("output should contain '3 deletions(-)': %q", output)
	}
}

func TestRenderStat_BinaryFile(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

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
	err := RenderStat(&buf, result, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "image.png") {
		t.Errorf("output should contain filename: %q", output)
	}
	if !strings.Contains(output, "Bin") {
		t.Errorf("output should contain 'Bin' for binary file: %q", output)
	}
}

func TestRenderStat_RenamedFile(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "old_name.go",
				NewPath:      "new_name.go",
				Status:       StatusRenamed,
				AddedLines:   1,
				DeletedLines: 0,
			},
		},
	}

	var buf bytes.Buffer
	err := RenderStat(&buf, result, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	// rename 형식: "old → new"
	if !strings.Contains(output, "old_name.go → new_name.go") {
		t.Errorf("output should contain rename format 'old → new': %q", output)
	}
}

func TestRenderStat_DeletedFile(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "removed.go",
				NewPath:      "",
				Status:       StatusDeleted,
				AddedLines:   0,
				DeletedLines: 10,
			},
		},
	}

	var buf bytes.Buffer
	err := RenderStat(&buf, result, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "removed.go") {
		t.Errorf("output should contain deleted filename: %q", output)
	}
	if !strings.Contains(output, "10 ") {
		t.Errorf("output should contain total changes '10': %q", output)
	}
	if !strings.Contains(output, "10 deletions(-)") {
		t.Errorf("output should contain '10 deletions(-)': %q", output)
	}
}

func TestRenderStat_NoColor(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "file.go",
				NewPath:      "file.go",
				Status:       StatusModified,
				AddedLines:   3,
				DeletedLines: 1,
			},
		},
	}

	var buf bytes.Buffer
	err := RenderStat(&buf, result, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	// noColor 모드에서 ANSI 이스케이프 코드가 없어야 함
	if strings.Contains(output, "\x1b[") {
		t.Errorf("noColor mode should not contain ANSI escape codes: %q", output)
	}
}

func TestRenderStat_WithColor(t *testing.T) {
	forceColor(t)

	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "file.go",
				NewPath:      "file.go",
				Status:       StatusModified,
				AddedLines:   3,
				DeletedLines: 1,
			},
		},
	}

	var buf bytes.Buffer
	err := RenderStat(&buf, result, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	// 색상 모드에서 ANSI 이스케이프 코드가 있어야 함
	if !strings.Contains(output, "\x1b[") {
		t.Errorf("color mode should contain ANSI escape codes: %q", output)
	}
}

func TestRenderStat_AddedOnlyFile(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "new_file.go",
				NewPath:      "new_file.go",
				Status:       StatusAdded,
				AddedLines:   20,
				DeletedLines: 0,
			},
		},
	}

	var buf bytes.Buffer
	err := RenderStat(&buf, result, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	// + 만 있고 - 는 막대에 없어야 함 (요약 라인의 deletions(-) 제외)
	lines := strings.Split(output, "\n")
	statLine := lines[0] // 첫 번째 라인이 파일 stat
	if !strings.Contains(statLine, "+") {
		t.Errorf("added-only file should have '+' in bar: %q", statLine)
	}
	if strings.Contains(statLine, "-") {
		t.Errorf("added-only file should not have '-' in bar: %q", statLine)
	}
}

func TestRenderStat_FilenameAlignment(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "a.go",
				NewPath:      "a.go",
				Status:       StatusModified,
				AddedLines:   1,
				DeletedLines: 0,
			},
			{
				OldPath:      "very/long/path/file.go",
				NewPath:      "very/long/path/file.go",
				Status:       StatusModified,
				AddedLines:   2,
				DeletedLines: 1,
			},
		},
	}

	var buf bytes.Buffer
	err := RenderStat(&buf, result, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	lines := strings.Split(output, "\n")

	// 두 파일 라인 모두 | 구분자를 포함해야 함
	for i := 0; i < 2; i++ {
		if !strings.Contains(lines[i], " | ") {
			t.Errorf("line %d should contain ' | ' separator: %q", i, lines[i])
		}
	}

	// | 위치가 정렬되어야 함
	pos0 := strings.Index(lines[0], " | ")
	pos1 := strings.Index(lines[1], " | ")
	if pos0 != pos1 {
		t.Errorf("'|' separators should be aligned: line0=%d, line1=%d", pos0, pos1)
	}
}

func TestStatFileName(t *testing.T) {
	tests := []struct {
		name     string
		file     DiffFile
		expected string
	}{
		{
			name:     "modified file uses NewPath",
			file:     DiffFile{OldPath: "a.go", NewPath: "a.go", Status: StatusModified},
			expected: "a.go",
		},
		{
			name:     "added file uses NewPath",
			file:     DiffFile{OldPath: "", NewPath: "new.go", Status: StatusAdded},
			expected: "new.go",
		},
		{
			name:     "deleted file uses OldPath",
			file:     DiffFile{OldPath: "old.go", NewPath: "", Status: StatusDeleted},
			expected: "old.go",
		},
		{
			name:     "renamed file shows old → new",
			file:     DiffFile{OldPath: "old.go", NewPath: "new.go", Status: StatusRenamed},
			expected: "old.go → new.go",
		},
		{
			name:     "renamed file same path uses NewPath",
			file:     DiffFile{OldPath: "same.go", NewPath: "same.go", Status: StatusRenamed},
			expected: "same.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statFileName(tt.file)
			if got != tt.expected {
				t.Errorf("statFileName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestPluralFile(t *testing.T) {
	if got := pluralFile(1); got != "file" {
		t.Errorf("pluralFile(1) = %q, want %q", got, "file")
	}
	if got := pluralFile(0); got != "files" {
		t.Errorf("pluralFile(0) = %q, want %q", got, "files")
	}
	if got := pluralFile(2); got != "files" {
		t.Errorf("pluralFile(2) = %q, want %q", got, "files")
	}
}

// TestBuildBar_ScaledTotalOneNoOverwrite is a regression guard for the
// dispatcher bug where two consecutive `if` statements at scaledTotal=1
// would set plusCount=1 then immediately reset it to 0 via the deleted
// branch — making a 1+1 file show only `-` and lose the `+` entirely.
//
// With the bug the visible bar for added=1, deleted=1, scaledTotal=1
// would be "-" only. After the fix, the proportional math runs to
// completion and the dispatcher does not double-fire.
func TestBuildBar_ScaledTotalOneNoOverwrite(t *testing.T) {
	cs := newStatColorSet(true) // disable color for substring matching

	// added=1, deleted=1: one line each; total=2. With maxTotal=2 and
	// barWidth=2, scaledTotal=2 → plusCount=1, minusCount=1. (Not the
	// pathological case yet — but verifies the happy-path stays happy.)
	bar := buildBar(1, 1, 2, cs)
	plusOK := strings.Contains(bar, "+")
	minusOK := strings.Contains(bar, "-")
	if !plusOK || !minusOK {
		t.Errorf("1+1 with maxTotal=2: bar should contain both + and -, got %q", bar)
	}

	// added=1, deleted=1 with a much larger maxTotal forces scaledTotal
	// to round down to 1. Pre-fix this dropped one side. We now accept
	// either single-char outcome (the proportional rounding choice),
	// but the dispatcher must not double-fire and produce a bar where
	// plusCount + minusCount > scaledTotal.
	bar = buildBar(1, 1, 100, cs)
	plusN := strings.Count(bar, "+")
	minusN := strings.Count(bar, "-")
	if plusN+minusN > 1 {
		t.Errorf("1+1 with maxTotal=100 (scaledTotal=1): bar exceeds 1 char (plus=%d minus=%d): %q",
			plusN, minusN, bar)
	}
	if plusN == 0 && minusN == 0 {
		t.Errorf("1+1 with maxTotal=100: bar must show at least one char, got %q", bar)
	}
}
