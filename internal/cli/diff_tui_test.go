package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/diff"
	"pgregory.net/rapid"
)

func TestDiffFileToPickerItem_Modified(t *testing.T) {
	f := diff.DiffFile{
		OldPath:      "main.go",
		NewPath:      "main.go",
		Status:       diff.StatusModified,
		AddedLines:   10,
		DeletedLines: 3,
	}
	item := DiffFileToPickerItem(f, 0)

	if item.Key != "0" {
		t.Errorf("Key = %q, want %q", item.Key, "0")
	}
	if item.Display != "main.go" {
		t.Errorf("Display = %q, want %q", item.Display, "main.go")
	}
	if len(item.Cells) != 4 {
		t.Fatalf("len(Cells) = %d, want 4", len(item.Cells))
	}
	if item.Cells[0] != "main.go" {
		t.Errorf("Cells[0] = %q, want %q", item.Cells[0], "main.go")
	}
	if item.Cells[1] != "modified" {
		t.Errorf("Cells[1] = %q, want %q", item.Cells[1], "modified")
	}
	if item.Cells[2] != "+10" {
		t.Errorf("Cells[2] = %q, want %q", item.Cells[2], "+10")
	}
	if item.Cells[3] != "-3" {
		t.Errorf("Cells[3] = %q, want %q", item.Cells[3], "-3")
	}
}

func TestDiffFileToPickerItem_Deleted(t *testing.T) {
	f := diff.DiffFile{
		OldPath:      "old.go",
		NewPath:      "",
		Status:       diff.StatusDeleted,
		AddedLines:   0,
		DeletedLines: 50,
	}
	item := DiffFileToPickerItem(f, 2)

	if item.Key != "2" {
		t.Errorf("Key = %q, want %q", item.Key, "2")
	}
	// Deleted files should use OldPath for display.
	if item.Display != "old.go" {
		t.Errorf("Display = %q, want %q", item.Display, "old.go")
	}
	if item.Cells[0] != "old.go" {
		t.Errorf("Cells[0] = %q, want %q", item.Cells[0], "old.go")
	}
	if item.Cells[1] != "deleted" {
		t.Errorf("Cells[1] = %q, want %q", item.Cells[1], "deleted")
	}
}

func TestDiffFileToPickerItem_AllStatuses(t *testing.T) {
	tests := []struct {
		status diff.FileStatus
		want   string
	}{
		{diff.StatusAdded, "added"},
		{diff.StatusDeleted, "deleted"},
		{diff.StatusRenamed, "renamed"},
		{diff.StatusCopied, "copied"},
		{diff.StatusModeChanged, "mode"},
		{diff.StatusModified, "modified"},
	}
	for _, tt := range tests {
		f := diff.DiffFile{Status: tt.status, NewPath: "x.go", OldPath: "x.go"}
		item := DiffFileToPickerItem(f, 0)
		if item.Cells[1] != tt.want {
			t.Errorf("status %d: Cells[1] = %q, want %q", tt.status, item.Cells[1], tt.want)
		}
	}
}

func TestFoldSummary(t *testing.T) {
	h := diff.Hunk{
		Lines: make([]diff.DiffLine, 7),
	}
	got := FoldSummary(h)
	want := "[+7 lines]"
	if got != want {
		t.Errorf("FoldSummary = %q, want %q", got, want)
	}
}

func TestFoldSummary_Empty(t *testing.T) {
	h := diff.Hunk{}
	got := FoldSummary(h)
	want := "[+0 lines]"
	if got != want {
		t.Errorf("FoldSummary = %q, want %q", got, want)
	}
}

func TestRenderFileDiff_Unfolded(t *testing.T) {
	f := &diff.DiffFile{
		Hunks: []diff.Hunk{
			{
				Header: "@@ -1,3 +1,4 @@",
				Lines: []diff.DiffLine{
					{Kind: diff.LineContext, Content: "line1", OldNum: 1, NewNum: 1},
					{Kind: diff.LineAdded, Content: "new", NewNum: 2},
				},
			},
		},
	}
	opts := diff.RenderOptions{NoColor: true}
	foldState := map[int]bool{}

	result := renderFileDiff(f, opts, foldState)
	if !strings.Contains(result, "@@ -1,3 +1,4 @@") {
		t.Error("expected hunk header in output")
	}
	if !strings.Contains(result, "line1") {
		t.Error("expected context line in output")
	}
	if !strings.Contains(result, "new") {
		t.Error("expected added line in output")
	}
}

func TestRenderFileDiff_Folded(t *testing.T) {
	f := &diff.DiffFile{
		Hunks: []diff.Hunk{
			{
				Header: "@@ -1,3 +1,4 @@",
				Lines: []diff.DiffLine{
					{Kind: diff.LineContext, Content: "line1", OldNum: 1, NewNum: 1},
					{Kind: diff.LineAdded, Content: "new", NewNum: 2},
					{Kind: diff.LineDeleted, Content: "old", OldNum: 2},
				},
			},
		},
	}
	opts := diff.RenderOptions{NoColor: true}
	foldState := map[int]bool{0: true}

	result := renderFileDiff(f, opts, foldState)
	if !strings.Contains(result, "@@ -1,3 +1,4 @@") {
		t.Error("expected hunk header in output")
	}
	if !strings.Contains(result, "[+3 lines]") {
		t.Errorf("expected fold summary in output, got: %s", result)
	}
	// Lines should NOT appear when folded.
	if strings.Contains(result, "line1") {
		t.Error("context line should not appear when folded")
	}
}

func TestToggleAllHunks(t *testing.T) {
	f := &diff.DiffFile{
		Hunks: []diff.Hunk{
			{Lines: make([]diff.DiffLine, 3)},
			{Lines: make([]diff.DiffLine, 5)},
		},
	}

	foldState := map[int]bool{}

	// All unfolded → should fold all.
	toggleAllHunks(f, foldState)
	if !foldState[0] || !foldState[1] {
		t.Error("expected all hunks to be folded")
	}

	// All folded → should unfold all.
	toggleAllHunks(f, foldState)
	if foldState[0] || foldState[1] {
		t.Error("expected all hunks to be unfolded")
	}
}

// ── Property 8 생성기 및 테스트 ──────────────────────────────────

// genSafePath는 유효한 파일 경로를 생성한다.
func genSafePath(t *rapid.T) string {
	segments := rapid.IntRange(1, 3).Draw(t, "pathSegments")
	parts := make([]string, segments)
	for i := range parts {
		parts[i] = rapid.StringMatching(`[a-z][a-z0-9_]{1,10}`).Draw(t, "pathPart")
	}
	ext := rapid.SampledFrom([]string{".go", ".txt", ".md", ".py", ".js"}).Draw(t, "ext")
	return strings.Join(parts, "/") + ext
}

// genDiffFile은 임의의 DiffFile을 생성한다.
// 모든 6가지 FileStatus, 랜덤 AddedLines/DeletedLines(0-1000), IsBinary 플래그를 포함한다.
func genDiffFile(t *rapid.T) diff.DiffFile {
	oldPath := genSafePath(t)
	newPath := genSafePath(t)
	status := rapid.SampledFrom([]diff.FileStatus{
		diff.StatusModified,
		diff.StatusAdded,
		diff.StatusDeleted,
		diff.StatusRenamed,
		diff.StatusCopied,
		diff.StatusModeChanged,
	}).Draw(t, "status")
	addedLines := rapid.IntRange(0, 1000).Draw(t, "addedLines")
	deletedLines := rapid.IntRange(0, 1000).Draw(t, "deletedLines")
	isBinary := rapid.Bool().Draw(t, "isBinary")

	return diff.DiffFile{
		OldPath:      oldPath,
		NewPath:      newPath,
		Status:       status,
		IsBinary:     isBinary,
		AddedLines:   addedLines,
		DeletedLines: deletedLines,
	}
}

// Feature: gk-diff, Property 8: PickerItem 변환 정확성 (PickerItem Conversion) —
// For any DiffFile에 대해, TablePicker용 PickerItem으로 변환할 때 파일 상태,
// 추가 라인 수(AddedLines), 삭제 라인 수(DeletedLines)가 원본 DiffFile의 값과
// 일치해야 한다.
// **Validates: Requirements 4.2**
func TestProperty_PickerItemConversion(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		f := genDiffFile(rt)
		index := rapid.IntRange(0, 100).Draw(rt, "index")

		item := DiffFileToPickerItem(f, index)

		// 1. Cells 길이 검증: 항상 4개 (path, status, added, deleted)
		if len(item.Cells) != 4 {
			rt.Fatalf("Cells 길이가 4가 아님: %d", len(item.Cells))
		}

		// 2. 상태 셀이 올바른 상태 라벨과 일치하는지 검증
		expectedStatus := expectedStatusLabel(f.Status)
		if item.Cells[1] != expectedStatus {
			rt.Fatalf("상태 불일치: DiffFile.Status=%d, Cells[1]=%q, 기대값=%q",
				f.Status, item.Cells[1], expectedStatus)
		}

		// 3. 추가 라인 수 셀이 "+N" 형식과 일치하는지 검증
		expectedAdded := fmt.Sprintf("+%d", f.AddedLines)
		if item.Cells[2] != expectedAdded {
			rt.Fatalf("추가 라인 수 불일치: DiffFile.AddedLines=%d, Cells[2]=%q, 기대값=%q",
				f.AddedLines, item.Cells[2], expectedAdded)
		}

		// 4. 삭제 라인 수 셀이 "-N" 형식과 일치하는지 검증
		expectedDeleted := fmt.Sprintf("-%d", f.DeletedLines)
		if item.Cells[3] != expectedDeleted {
			rt.Fatalf("삭제 라인 수 불일치: DiffFile.DeletedLines=%d, Cells[3]=%q, 기대값=%q",
				f.DeletedLines, item.Cells[3], expectedDeleted)
		}

		// 5. 경로 셀 검증: Deleted 상태면 OldPath, 그 외에는 NewPath
		expectedPath := f.NewPath
		if f.Status == diff.StatusDeleted {
			expectedPath = f.OldPath
		}
		if item.Cells[0] != expectedPath {
			rt.Fatalf("경로 불일치: Status=%d, Cells[0]=%q, 기대값=%q",
				f.Status, item.Cells[0], expectedPath)
		}

		// 6. Display 필드도 경로와 일치하는지 검증
		if item.Display != expectedPath {
			rt.Fatalf("Display 불일치: Display=%q, 기대값=%q",
				item.Display, expectedPath)
		}
	})
}

// ── Property 7 생성기 및 테스트 ──────────────────────────────────

// genDiffLine은 임의의 DiffLine을 생성한다.
func genDiffLine(t *rapid.T) diff.DiffLine {
	kind := rapid.SampledFrom([]diff.LineKind{
		diff.LineContext,
		diff.LineAdded,
		diff.LineDeleted,
	}).Draw(t, "lineKind")
	content := rapid.StringMatching(`[a-zA-Z0-9 _\-\.\,\;\:]{0,80}`).Draw(t, "content")
	oldNum := rapid.IntRange(0, 9999).Draw(t, "oldNum")
	newNum := rapid.IntRange(0, 9999).Draw(t, "newNum")
	return diff.DiffLine{
		Kind:    kind,
		Content: content,
		OldNum:  oldNum,
		NewNum:  newNum,
	}
}

// genHunk는 임의의 Hunk를 생성한다.
// 0~50개의 랜덤 라인, 랜덤 라인 종류(Context, Added, Deleted), 랜덤 내용을 포함한다.
func genHunk(t *rapid.T) diff.Hunk {
	numLines := rapid.IntRange(0, 50).Draw(t, "numLines")
	lines := make([]diff.DiffLine, numLines)
	for i := range lines {
		lines[i] = genDiffLine(t)
	}
	oldStart := rapid.IntRange(1, 1000).Draw(t, "oldStart")
	oldCount := rapid.IntRange(0, 100).Draw(t, "oldCount")
	newStart := rapid.IntRange(1, 1000).Draw(t, "newStart")
	newCount := rapid.IntRange(0, 100).Draw(t, "newCount")
	header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)

	return diff.Hunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
		Header:   header,
		Lines:    lines,
	}
}

// Feature: gk-diff, Property 7: Hunk 접기 요약 라인 수 (Fold Summary Line Count) —
// For any Hunk에 대해, 접힌 상태의 요약 라인 `[+N lines]`에서 N은
// 해당 Hunk의 len(Lines)와 같아야 한다.
// **Validates: Requirements 11.3**
func TestProperty_FoldSummaryLineCount(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		h := genHunk(rt)

		summary := FoldSummary(h)

		// summary에서 N 값을 파싱한다.
		var n int
		_, err := fmt.Sscanf(summary, "[+%d lines]", &n)
		if err != nil {
			rt.Fatalf("FoldSummary 형식 파싱 실패: %q, 에러: %v", summary, err)
		}

		// N == len(h.Lines) 검증
		if n != len(h.Lines) {
			rt.Fatalf("FoldSummary 라인 수 불일치: 요약의 N=%d, len(Lines)=%d, summary=%q",
				n, len(h.Lines), summary)
		}
	})
}

// expectedStatusLabel은 테스트에서 독립적으로 기대 상태 라벨을 계산한다.
// DiffFileToPickerItem 내부의 statusLabel과 동일한 매핑이지만,
// 테스트 독립성을 위해 별도로 정의한다.
func expectedStatusLabel(s diff.FileStatus) string {
	switch s {
	case diff.StatusAdded:
		return "added"
	case diff.StatusDeleted:
		return "deleted"
	case diff.StatusRenamed:
		return "renamed"
	case diff.StatusCopied:
		return "copied"
	case diff.StatusModeChanged:
		return "mode"
	default:
		return "modified"
	}
}
