package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fatih/color"
	"pgregory.net/rapid"
)

// Feature: gk-diff, Property 1: 파싱 라운드트립 — For any 유효한 DiffResult,
// FormatUnifiedDiff → ParseUnifiedDiff 라운드트립이 원본을 보존해야 한다.
// **Validates: Requirements 6.1, 6.5**

// genSafeContent는 diff 접두사(+, -, @, \, diff, Binary 등)로 시작하지 않는
// 안전한 라인 콘텐츠를 생성한다.
func genSafeContent(t *rapid.T) string {
	// 알파벳으로 시작하는 안전한 콘텐츠 생성
	prefix := rapid.StringMatching(`[a-zA-Z]`).Draw(t, "prefix")
	body := rapid.StringMatching(`[a-zA-Z0-9_ ]{0,40}`).Draw(t, "body")
	return prefix + body
}

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

// genDiffLine은 주어진 종류의 DiffLine을 생성한다.
func genDiffLine(t *rapid.T, kind LineKind) DiffLine {
	content := genSafeContent(t)
	return DiffLine{
		Kind:    kind,
		Content: content,
	}
}

// genHunk는 유효한 Hunk를 생성한다. oldStart와 newStart를 인자로 받아
// 라인 번호를 일관되게 유지한다.
func genHunk(t *rapid.T, oldStart, newStart int) Hunk {
	numLines := rapid.IntRange(1, 10).Draw(t, "numLines")

	lines := make([]DiffLine, 0, numLines)
	oldNum := oldStart
	newNum := newStart

	for i := 0; i < numLines; i++ {
		kind := rapid.SampledFrom([]LineKind{LineContext, LineAdded, LineDeleted}).Draw(t, "lineKind")
		dl := genDiffLine(t, kind)

		switch kind {
		case LineContext:
			dl.OldNum = oldNum
			dl.NewNum = newNum
			oldNum++
			newNum++
		case LineAdded:
			dl.OldNum = 0
			dl.NewNum = newNum
			newNum++
		case LineDeleted:
			dl.OldNum = oldNum
			dl.NewNum = 0
			oldNum++
		}

		lines = append(lines, dl)
	}

	oldCount := oldNum - oldStart
	newCount := newNum - newStart

	return Hunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
		Lines:    lines,
	}
}

// genDiffFile은 비바이너리 DiffFile을 생성한다 (hunks가 있는 파일만).
func genDiffFile(t *rapid.T) DiffFile {
	path := genSafePath(t)

	// Modified 상태만 생성 (라운드트립에서 가장 안정적)
	status := rapid.SampledFrom([]FileStatus{StatusModified}).Draw(t, "status")

	numHunks := rapid.IntRange(1, 3).Draw(t, "numHunks")
	hunks := make([]Hunk, 0, numHunks)

	oldStart := 1
	newStart := 1

	addedTotal := 0
	deletedTotal := 0

	for i := 0; i < numHunks; i++ {
		// hunk 사이에 간격을 두어 현실적인 diff를 생성
		if i > 0 {
			gap := rapid.IntRange(5, 20).Draw(t, "gap")
			oldStart += gap
			newStart += gap
		}

		h := genHunk(t, oldStart, newStart)
		hunks = append(hunks, h)

		// 다음 hunk의 시작 위치 계산
		oldStart += h.OldCount
		newStart += h.NewCount

		// 추가/삭제 라인 수 집계
		for _, l := range h.Lines {
			switch l.Kind {
			case LineAdded:
				addedTotal++
			case LineDeleted:
				deletedTotal++
			}
		}
	}

	return DiffFile{
		OldPath:      path,
		NewPath:      path,
		Status:       status,
		IsBinary:     false,
		Hunks:        hunks,
		AddedLines:   addedTotal,
		DeletedLines: deletedTotal,
	}
}

// genDiffResult는 1~5개 파일을 포함하는 임의의 DiffResult를 생성한다.
func genDiffResult(t *rapid.T) *DiffResult {
	numFiles := rapid.IntRange(1, 5).Draw(t, "numFiles")
	files := make([]DiffFile, numFiles)
	for i := range files {
		files[i] = genDiffFile(t)
	}
	return &DiffResult{Files: files}
}

// genRandomDiffLine은 임의의 종류(Added/Deleted/Context)를 가진 DiffLine을 생성한다.
// 라인 번호는 종류에 따라 적절히 설정된다.
func genRandomDiffLine(t *rapid.T) DiffLine {
	kind := rapid.SampledFrom([]LineKind{LineContext, LineAdded, LineDeleted}).Draw(t, "kind")
	content := genSafeContent(t)
	lineNum := rapid.IntRange(1, 9999).Draw(t, "lineNum")

	dl := DiffLine{
		Kind:    kind,
		Content: content,
	}

	switch kind {
	case LineContext:
		dl.OldNum = lineNum
		dl.NewNum = lineNum
	case LineAdded:
		dl.OldNum = 0
		dl.NewNum = lineNum
	case LineDeleted:
		dl.OldNum = lineNum
		dl.NewNum = 0
	}

	return dl
}

// Feature: gk-diff, Property 2: 라인 렌더링 불변 속성 — For any DiffLine에 대해,
// 렌더링 결과는 종류에 따른 올바른 색상, 마커, 사이드 바, 라인 번호 포맷을 포함해야 한다.
// **Validates: Requirements 2.1, 2.2, 2.3, 2.7**
func TestProperty_LineRenderInvariant(t *testing.T) {
	forceColor(t)

	rapid.Check(t, func(rt *rapid.T) {
		line := genRandomDiffLine(rt)
		rendered := RenderLine(line, false)

		switch line.Kind {
		case LineAdded:
			// 초록색 ANSI 코드 포함
			if !strings.Contains(rendered, "\x1b[") {
				rt.Fatalf("LineAdded 렌더링에 ANSI 이스케이프 코드가 없음: %q", rendered)
			}
			// ▶ 마커 포함
			if !strings.Contains(rendered, "▶") {
				rt.Fatalf("LineAdded 렌더링에 ▶ 마커가 없음: %q", rendered)
			}
			// ▌ 사이드 바 포함
			if !strings.Contains(rendered, "▌") {
				rt.Fatalf("LineAdded 렌더링에 ▌ 사이드 바가 없음: %q", rendered)
			}

		case LineDeleted:
			// 빨간색 ANSI 코드 포함
			if !strings.Contains(rendered, "\x1b[") {
				rt.Fatalf("LineDeleted 렌더링에 ANSI 이스케이프 코드가 없음: %q", rendered)
			}
			// ◀ 마커 포함
			if !strings.Contains(rendered, "◀") {
				rt.Fatalf("LineDeleted 렌더링에 ◀ 마커가 없음: %q", rendered)
			}
			// ▌ 사이드 바 포함
			if !strings.Contains(rendered, "▌") {
				rt.Fatalf("LineDeleted 렌더링에 ▌ 사이드 바가 없음: %q", rendered)
			}

		case LineContext:
			// faint ANSI 코드 포함
			if !strings.Contains(rendered, "\x1b[") {
				rt.Fatalf("LineContext 렌더링에 ANSI 이스케이프 코드가 없음: %q", rendered)
			}
			// · 마커 포함
			if !strings.Contains(rendered, "·") {
				rt.Fatalf("LineContext 렌더링에 · 마커가 없음: %q", rendered)
			}
		}

		// 라인 번호 4자리 고정폭 검증
		var expectedNum int
		switch line.Kind {
		case LineAdded:
			expectedNum = line.NewNum
		case LineDeleted, LineContext:
			expectedNum = line.OldNum
		}
		if expectedNum > 0 {
			formatted := fmt.Sprintf("%4d", expectedNum)
			if !strings.Contains(rendered, formatted) {
				rt.Fatalf("렌더링에 4자리 고정폭 라인 번호 %q가 없음: %q", formatted, rendered)
			}
		}

		// 콘텐츠 포함 검증
		if !strings.Contains(rendered, line.Content) {
			rt.Fatalf("렌더링에 원본 콘텐츠 %q가 없음: %q", line.Content, rendered)
		}
	})
}

// Feature: gk-diff, Property 3: NoColor 불변 속성 — For any DiffResult에 대해,
// RenderOptions{NoColor: true}로 렌더링한 결과에는 ANSI 이스케이프 시퀀스(\x1b[)가
// 포함되지 않아야 한다.
// **Validates: Requirements 2.6**
func TestProperty_NoColorInvariant(t *testing.T) {
	// fatih/color 라이브러리의 글로벌 NoColor 플래그도 설정하여
	// 라이브러리 내부에서도 ANSI 코드를 생성하지 않도록 한다.
	prevNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prevNoColor }()

	rapid.Check(t, func(rt *rapid.T) {
		result := genDiffResult(rt)

		var buf bytes.Buffer
		err := Render(&buf, result, RenderOptions{NoColor: true})
		if err != nil {
			rt.Fatalf("Render 실패: %v", err)
		}

		output := buf.String()
		if strings.Contains(output, "\x1b[") {
			rt.Fatalf("NoColor 모드에서 ANSI 이스케이프 시퀀스가 발견됨: %q", output)
		}
	})
}

// Feature: gk-diff, Property 6: 워드 Diff Span 커버리지 (Word Diff Span Coverage) —
// For any 두 문자열 쌍 (oldLine, newLine)에 대해, ComputeWordDiff가 반환하는
// oldSpans는 oldLine 전체를, newSpans는 newLine 전체를 빈틈 없이 커버해야 한다.
// span들이 겹치지 않고 연속적이며 전체 문자열을 포함해야 한다.
// **Validates: Requirements 10.1**
func TestProperty_WordDiffSpanCoverage(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		oldLine := rapid.String().Draw(rt, "oldLine")
		newLine := rapid.String().Draw(rt, "newLine")

		oldSpans, newSpans := ComputeWordDiff(oldLine, newLine)

		// oldLine 커버리지 검증
		assertSpansCoverProperty(rt, oldSpans, oldLine, "old")
		// newLine 커버리지 검증
		assertSpansCoverProperty(rt, newSpans, newLine, "new")
	})
}

// assertSpansCoverProperty는 속성 테스트용 span 커버리지 검증 헬퍼이다.
// rapid.T를 사용하여 실패 시 반례를 축소(shrink)할 수 있다.
func assertSpansCoverProperty(t *rapid.T, spans []DiffSpan, line string, label string) {
	if len(line) == 0 {
		// 빈 문자열이면 span이 없거나 nil이어야 함
		if len(spans) != 0 {
			t.Fatalf("%s: 빈 문자열에 span이 %d개 존재", label, len(spans))
		}
		return
	}

	if len(spans) == 0 {
		t.Fatalf("%s: 비어있지 않은 문자열(%q)에 span이 없음", label, line)
	}

	// 1. 첫 span은 0에서 시작
	if spans[0].Start != 0 {
		t.Fatalf("%s: 첫 span 시작이 0이 아님: %d (line=%q)", label, spans[0].Start, line)
	}

	// 2. 마지막 span은 len(line)에서 끝남
	if spans[len(spans)-1].End != len(line) {
		t.Fatalf("%s: 마지막 span 끝(%d)이 문자열 길이(%d)와 다름 (line=%q)",
			label, spans[len(spans)-1].End, len(line), line)
	}

	// 3. 연속성 검증: 각 span[i].Start == span[i-1].End (겹침 없음, 빈틈 없음)
	for i := 1; i < len(spans); i++ {
		if spans[i].Start != spans[i-1].End {
			t.Fatalf("%s: span[%d].End(%d) != span[%d].Start(%d) — 빈틈 또는 겹침 (line=%q)",
				label, i-1, spans[i-1].End, i, spans[i].Start, line)
		}
	}

	// 4. 각 span의 Start < End (빈 span 없음)
	for i, s := range spans {
		if s.Start >= s.End {
			t.Fatalf("%s: span[%d]이 비어있음: Start=%d, End=%d (line=%q)",
				label, i, s.Start, s.End, line)
		}
	}
}

func TestProperty_ParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		original := genDiffResult(t)

		// FormatUnifiedDiff → 텍스트로 변환
		formatted := FormatUnifiedDiff(original)

		// ParseUnifiedDiff → 다시 파싱
		reparsed, err := ParseUnifiedDiff(strings.NewReader(formatted))
		if err != nil {
			t.Fatalf("파싱 실패: %v", err)
		}

		// 파일 수 비교
		if len(reparsed.Files) != len(original.Files) {
			t.Fatalf("파일 수 불일치: 원본 %d, 파싱 결과 %d",
				len(original.Files), len(reparsed.Files))
		}

		for i, origFile := range original.Files {
			parsedFile := reparsed.Files[i]

			// 경로 비교
			if origFile.OldPath != parsedFile.OldPath {
				t.Errorf("파일 %d: OldPath 불일치: %q vs %q",
					i, origFile.OldPath, parsedFile.OldPath)
			}
			if origFile.NewPath != parsedFile.NewPath {
				t.Errorf("파일 %d: NewPath 불일치: %q vs %q",
					i, origFile.NewPath, parsedFile.NewPath)
			}

			// Hunk 수 비교
			if len(origFile.Hunks) != len(parsedFile.Hunks) {
				t.Fatalf("파일 %d: Hunk 수 불일치: 원본 %d, 파싱 결과 %d",
					i, len(origFile.Hunks), len(parsedFile.Hunks))
			}

			for j, origHunk := range origFile.Hunks {
				parsedHunk := parsedFile.Hunks[j]

				// Hunk 라인 범위 비교
				if origHunk.OldStart != parsedHunk.OldStart {
					t.Errorf("파일 %d Hunk %d: OldStart 불일치: %d vs %d",
						i, j, origHunk.OldStart, parsedHunk.OldStart)
				}
				if origHunk.OldCount != parsedHunk.OldCount {
					t.Errorf("파일 %d Hunk %d: OldCount 불일치: %d vs %d",
						i, j, origHunk.OldCount, parsedHunk.OldCount)
				}
				if origHunk.NewStart != parsedHunk.NewStart {
					t.Errorf("파일 %d Hunk %d: NewStart 불일치: %d vs %d",
						i, j, origHunk.NewStart, parsedHunk.NewStart)
				}
				if origHunk.NewCount != parsedHunk.NewCount {
					t.Errorf("파일 %d Hunk %d: NewCount 불일치: %d vs %d",
						i, j, origHunk.NewCount, parsedHunk.NewCount)
				}

				// 라인 수 비교
				if len(origHunk.Lines) != len(parsedHunk.Lines) {
					t.Fatalf("파일 %d Hunk %d: 라인 수 불일치: 원본 %d, 파싱 결과 %d",
						i, j, len(origHunk.Lines), len(parsedHunk.Lines))
				}

				for k, origLine := range origHunk.Lines {
					parsedLine := parsedHunk.Lines[k]

					// 라인 종류 비교
					if origLine.Kind != parsedLine.Kind {
						t.Errorf("파일 %d Hunk %d 라인 %d: Kind 불일치: %v vs %v",
							i, j, k, origLine.Kind, parsedLine.Kind)
					}

					// 라인 내용 비교
					if origLine.Content != parsedLine.Content {
						t.Errorf("파일 %d Hunk %d 라인 %d: Content 불일치: %q vs %q",
							i, j, k, origLine.Content, parsedLine.Content)
					}
				}
			}
		}
	})
}

// Feature: gk-diff, Property 4: Diff Stat 총계 불변 속성 (Stat Totals Invariant) —
// For any DiffResult에 대해, RenderStat 출력의 총 추가 라인 수는 모든 파일의
// AddedLines 합계와 같아야 하고, 총 삭제 라인 수는 모든 파일의 DeletedLines 합계와
// 같아야 하며, 총 변경 파일 수는 len(Files)와 같아야 한다.
// **Validates: Requirements 3.1, 3.2, 3.3**
func TestProperty_StatTotalsInvariant(t *testing.T) {
	// ANSI 코드를 비활성화하여 문자열 매칭을 단순화한다.
	prevNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prevNoColor }()

	rapid.Check(t, func(rt *rapid.T) {
		result := genDiffResult(rt)

		// 1. 데이터 모델에서 직접 총계 계산
		expectedAdded := 0
		expectedDeleted := 0
		expectedFiles := len(result.Files)
		for _, f := range result.Files {
			expectedAdded += f.AddedLines
			expectedDeleted += f.DeletedLines
		}

		// 2. RenderStat 출력 생성
		var buf bytes.Buffer
		err := RenderStat(&buf, result, true)
		if err != nil {
			rt.Fatalf("RenderStat 실패: %v", err)
		}
		output := buf.String()

		// 3. 렌더링된 출력에서 총계 문자열이 포함되는지 검증
		// 요약 라인 형식: " N file(s) changed, M insertions(+), K deletions(-)"
		expectedFileStr := fmt.Sprintf("%d %s changed", expectedFiles, pluralFile(expectedFiles))
		if !strings.Contains(output, expectedFileStr) {
			rt.Fatalf("RenderStat 출력에 파일 수 총계 %q가 없음.\n출력: %s", expectedFileStr, output)
		}

		expectedAddedStr := fmt.Sprintf("%d insertions(+)", expectedAdded)
		if !strings.Contains(output, expectedAddedStr) {
			rt.Fatalf("RenderStat 출력에 추가 라인 총계 %q가 없음.\n출력: %s", expectedAddedStr, output)
		}

		expectedDeletedStr := fmt.Sprintf("%d deletions(-)", expectedDeleted)
		if !strings.Contains(output, expectedDeletedStr) {
			rt.Fatalf("RenderStat 출력에 삭제 라인 총계 %q가 없음.\n출력: %s", expectedDeletedStr, output)
		}

		// 4. 각 파일의 stat 라인이 출력에 존재하는지 검증
		for _, f := range result.Files {
			fileTotal := f.AddedLines + f.DeletedLines
			fileTotalStr := fmt.Sprintf("%d ", fileTotal)
			fileName := statFileName(f)
			if !strings.Contains(output, fileName) {
				rt.Fatalf("RenderStat 출력에 파일명 %q가 없음.\n출력: %s", fileName, output)
			}
			if !strings.Contains(output, fileTotalStr) {
				rt.Fatalf("RenderStat 출력에 파일 변경 수 %q가 없음.\n출력: %s", fileTotalStr, output)
			}
		}
	})
}

// Feature: gk-diff, Property 5: JSON 직렬화 라운드트립 (JSON Round-Trip) —
// For any DiffResult에 대해, JSON으로 직렬화한 후 역직렬화하면 원본과 구조적으로
// 동등한 결과를 얻어야 한다. 또한 JSON 출력에는 files 배열이 존재하고, 각 파일에
// path, status, added_lines, deleted_lines, hunks 필드가 포함되어야 한다.
// **Validates: Requirements 8.1, 8.2**
func TestProperty_JSONRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		original := genDiffResult(rt)

		// 1. DiffResult → DiffJSON 변환
		dj := ToJSON(original)

		// 2. JSON 직렬화
		data, err := json.Marshal(dj)
		if err != nil {
			rt.Fatalf("json.Marshal 실패: %v", err)
		}

		// 3. JSON 역직렬화
		var restored DiffJSON
		if err := json.Unmarshal(data, &restored); err != nil {
			rt.Fatalf("json.Unmarshal 실패: %v", err)
		}

		// 4. 구조적 동등성 검증: 파일 수
		if len(restored.Files) != len(original.Files) {
			rt.Fatalf("파일 수 불일치: 원본 %d, 복원 %d",
				len(original.Files), len(restored.Files))
		}

		// 5. 각 파일의 필수 필드 및 값 검증
		for i, origFile := range original.Files {
			restoredFile := restored.Files[i]

			// path 검증
			if restoredFile.Path != origFile.NewPath {
				rt.Errorf("파일 %d: path 불일치: 원본 %q, 복원 %q",
					i, origFile.NewPath, restoredFile.Path)
			}

			// status 검증
			if restoredFile.Status != origFile.Status.String() {
				rt.Errorf("파일 %d: status 불일치: 원본 %q, 복원 %q",
					i, origFile.Status.String(), restoredFile.Status)
			}

			// added_lines 검증
			if restoredFile.AddedLines != origFile.AddedLines {
				rt.Errorf("파일 %d: added_lines 불일치: 원본 %d, 복원 %d",
					i, origFile.AddedLines, restoredFile.AddedLines)
			}

			// deleted_lines 검증
			if restoredFile.DeletedLines != origFile.DeletedLines {
				rt.Errorf("파일 %d: deleted_lines 불일치: 원본 %d, 복원 %d",
					i, origFile.DeletedLines, restoredFile.DeletedLines)
			}

			// hunks 배열 존재 및 수 검증
			if len(restoredFile.Hunks) != len(origFile.Hunks) {
				rt.Fatalf("파일 %d: hunk 수 불일치: 원본 %d, 복원 %d",
					i, len(origFile.Hunks), len(restoredFile.Hunks))
			}
		}

		// 6. Stat 총계 검증
		expectedAdded := 0
		expectedDeleted := 0
		for _, f := range original.Files {
			expectedAdded += f.AddedLines
			expectedDeleted += f.DeletedLines
		}

		if restored.Stat.TotalFiles != len(original.Files) {
			rt.Errorf("stat.total_files 불일치: 원본 %d, 복원 %d",
				len(original.Files), restored.Stat.TotalFiles)
		}
		if restored.Stat.TotalAdded != expectedAdded {
			rt.Errorf("stat.total_added 불일치: 원본 %d, 복원 %d",
				expectedAdded, restored.Stat.TotalAdded)
		}
		if restored.Stat.TotalDeleted != expectedDeleted {
			rt.Errorf("stat.total_deleted 불일치: 원본 %d, 복원 %d",
				expectedDeleted, restored.Stat.TotalDeleted)
		}

		// 7. raw JSON 구조 검증: files 배열 존재, 각 파일 필수 필드 포함
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			rt.Fatalf("raw JSON 파싱 실패: %v", err)
		}

		// "files" 키 존재 확인
		filesRaw, ok := raw["files"]
		if !ok {
			rt.Fatalf("JSON에 'files' 키가 없음")
		}

		// files가 배열인지 확인
		var filesArr []map[string]json.RawMessage
		if err := json.Unmarshal(filesRaw, &filesArr); err != nil {
			rt.Fatalf("'files'가 배열이 아님: %v", err)
		}

		// 각 파일 객체에 필수 필드 존재 확인
		requiredFields := []string{"path", "status", "added_lines", "deleted_lines", "hunks"}
		for i, fileObj := range filesArr {
			for _, field := range requiredFields {
				if _, exists := fileObj[field]; !exists {
					rt.Fatalf("파일 %d: JSON에 필수 필드 %q가 없음", i, field)
				}
			}
		}
	})
}
