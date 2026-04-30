package diff

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
)

// RenderOptions는 렌더링 동작을 제어하는 옵션이다.
type RenderOptions struct {
	NoColor    bool // ANSI 이스케이프 코드 비활성화
	NoWordDiff bool // 단어 단위 하이라이트 비활성화
	Context    int  // 컨텍스트 라인 수 (기본 3, 현재 파서에서 제어)
}

// ── 색상/스타일 팩토리 ──────────────────────────────────────────

// colorSet은 렌더링에 사용되는 색상 함수 집합이다.
type colorSet struct {
	green     func(string, ...interface{}) string
	red       func(string, ...interface{}) string
	faint     func(a ...interface{}) string
	bold      func(a ...interface{}) string
	faintFmt  func(string, ...interface{}) string
	greenBold func(a ...interface{}) string
	redBold   func(a ...interface{}) string
	cyan      func(a ...interface{}) string
}

func newColorSet(noColor bool) colorSet {
	if noColor {
		return colorSet{
			green:     fmt.Sprintf,
			red:       fmt.Sprintf,
			faint:     fmt.Sprint,
			bold:      fmt.Sprint,
			faintFmt:  fmt.Sprintf,
			greenBold: fmt.Sprint,
			redBold:   fmt.Sprint,
			cyan:      fmt.Sprint,
		}
	}
	return colorSet{
		green:     color.GreenString,
		red:       color.RedString,
		faint:     color.New(color.Faint).SprintFunc(),
		bold:      color.New(color.Bold).SprintFunc(),
		faintFmt:  color.New(color.Faint).Sprintf,
		greenBold: color.New(color.FgGreen, color.Bold).SprintFunc(),
		redBold:   color.New(color.FgRed, color.Bold).SprintFunc(),
		cyan:      color.New(color.FgCyan).SprintFunc(),
	}
}

// ── 파일 헤더 렌더링 ────────────────────────────────────────────

// renderFileHeader는 파일 헤더를 렌더링한다.
// 볼드체 파일명 + 상태 + 구분선(─)
func renderFileHeader(w io.Writer, f *DiffFile, cs colorSet) {
	// 파일 경로 결정
	path := f.NewPath
	if f.Status == StatusDeleted {
		path = f.OldPath
	}

	// 상태 표시
	var statusStr string
	switch f.Status {
	case StatusAdded:
		statusStr = cs.green(" [added]")
	case StatusDeleted:
		statusStr = cs.red(" [deleted]")
	case StatusRenamed:
		statusStr = cs.cyan(fmt.Sprintf(" [renamed: %s → %s]", f.OldPath, f.NewPath))
	case StatusCopied:
		statusStr = cs.cyan(fmt.Sprintf(" [copied: %s → %s]", f.OldPath, f.NewPath))
	case StatusModeChanged:
		statusStr = cs.faintFmt(" [mode: %s → %s]", f.OldMode, f.NewMode)
	default:
		statusStr = ""
	}

	fmt.Fprintf(w, "%s%s\n", cs.bold(path), statusStr)
	fmt.Fprintln(w, cs.faint(strings.Repeat("─", 60)))
}

// ── Hunk 헤더 렌더링 ────────────────────────────────────────────

// renderHunkHeader는 @@ 헤더를 렌더링한다.
func renderHunkHeader(w io.Writer, h *Hunk, cs colorSet) {
	fmt.Fprintln(w, cs.cyan(h.Header))
}

// ── 라인 렌더링 ─────────────────────────────────────────────────

// RenderLine은 단일 DiffLine을 렌더링하여 문자열로 반환한다.
// 속성 테스트에서 개별 라인 렌더링 동작을 검증할 수 있도록 export한다.
func RenderLine(line DiffLine, noColor bool) string {
	cs := newColorSet(noColor)
	return renderLine(line, cs)
}

// renderLine은 단일 DiffLine을 렌더링한다.
func renderLine(line DiffLine, cs colorSet) string {
	switch line.Kind {
	case LineAdded:
		lineNum := formatLineNum(line.NewNum)
		return cs.green("%s  %s %s %s",
			lineNum, "▶", "▌", line.Content)
	case LineDeleted:
		lineNum := formatLineNum(line.OldNum)
		return cs.red("%s  %s %s %s",
			lineNum, "◀", "▌", line.Content)
	case LineContext:
		lineNum := formatLineNum(line.OldNum)
		return cs.faintFmt("%s  · %s", lineNum, line.Content)
	default:
		return line.Content
	}
}

// renderLineWithWordDiff는 워드 diff span을 적용하여 라인을 렌더링한다.
// 변경된 구간은 반전(reverse) 스타일로 강조 표시된다.
func renderLineWithWordDiff(line DiffLine, spans []DiffSpan, cs colorSet, noColor bool) string {
	content := highlightSpans(line.Content, spans, line.Kind, noColor)

	switch line.Kind {
	case LineAdded:
		lineNum := formatLineNum(line.NewNum)
		return cs.green("%s  %s %s ", lineNum, "▶", "▌") + content
	case LineDeleted:
		lineNum := formatLineNum(line.OldNum)
		return cs.red("%s  %s %s ", lineNum, "◀", "▌") + content
	default:
		return renderLine(line, cs)
	}
}

// highlightSpans는 DiffSpan 배열을 사용하여 변경된 구간을 강조 표시한다.
func highlightSpans(content string, spans []DiffSpan, kind LineKind, noColor bool) string {
	if len(spans) == 0 || noColor {
		return content
	}

	var baseColor, highlightColor *color.Color
	switch kind {
	case LineAdded:
		baseColor = color.New(color.FgGreen)
		highlightColor = color.New(color.FgGreen, color.ReverseVideo)
	case LineDeleted:
		baseColor = color.New(color.FgRed)
		highlightColor = color.New(color.FgRed, color.ReverseVideo)
	default:
		return content
	}

	var sb strings.Builder
	for _, span := range spans {
		if span.Start >= len(content) {
			break
		}
		end := span.End
		if end > len(content) {
			end = len(content)
		}
		segment := content[span.Start:end]
		if span.Changed {
			sb.WriteString(highlightColor.Sprint(segment))
		} else {
			sb.WriteString(baseColor.Sprint(segment))
		}
	}
	return sb.String()
}

// renderWhitespaceOnlyChange는 공백 전용 변경을 별도로 표시한다.
func renderWhitespaceOnlyChange(line DiffLine, cs colorSet) string {
	switch line.Kind {
	case LineAdded:
		lineNum := formatLineNum(line.NewNum)
		return cs.green("%s  %s %s %s", lineNum, "▶", "▌", line.Content) +
			cs.faintFmt(" [whitespace]")
	case LineDeleted:
		lineNum := formatLineNum(line.OldNum)
		return cs.red("%s  %s %s %s", lineNum, "◀", "▌", line.Content) +
			cs.faintFmt(" [whitespace]")
	default:
		return renderLine(line, cs)
	}
}

// formatLineNum은 라인 번호를 4자리 고정폭으로 포맷한다.
func formatLineNum(num int) string {
	if num == 0 {
		return "    "
	}
	return fmt.Sprintf("%4d", num)
}

// ── 메인 렌더 함수 ──────────────────────────────────────────────

// Render는 DiffResult를 터미널 출력으로 변환하여 w에 쓴다.
func Render(w io.Writer, result *DiffResult, opts RenderOptions) error {
	if result == nil || len(result.Files) == 0 {
		return nil
	}

	cs := newColorSet(opts.NoColor)

	for i, f := range result.Files {
		if i > 0 {
			fmt.Fprintln(w) // 파일 간 빈 줄
		}

		renderFileHeader(w, &f, cs)

		if f.IsBinary {
			fmt.Fprintln(w, cs.faint("Binary file differs"))
			continue
		}

		for j, h := range f.Hunks {
			if j > 0 {
				fmt.Fprintln(w) // Hunk 간 빈 줄
			}
			renderHunkHeader(w, &h, cs)

			if opts.NoWordDiff {
				for _, line := range h.Lines {
					fmt.Fprintln(w, renderLine(line, cs))
				}
			} else {
				renderHunkLinesWithWordDiff(w, h.Lines, cs, opts.NoColor)
			}
		}
	}

	return nil
}

// ── 워드 diff 통합 ──────────────────────────────────────────────

// renderHunkLinesWithWordDiff는 Hunk의 라인들을 렌더링하면서
// 인접한 삭제+추가 라인 쌍에 대해 워드 diff를 적용한다.
func renderHunkLinesWithWordDiff(w io.Writer, lines []DiffLine, cs colorSet, noColor bool) {
	i := 0
	for i < len(lines) {
		// 연속된 삭제 라인 블록 찾기
		delStart := i
		for i < len(lines) && lines[i].Kind == LineDeleted {
			i++
		}
		delEnd := i

		// 연속된 추가 라인 블록 찾기
		addStart := i
		for i < len(lines) && lines[i].Kind == LineAdded {
			i++
		}
		addEnd := i

		delCount := delEnd - delStart
		addCount := addEnd - addStart

		if delCount > 0 && addCount > 0 {
			// 삭제+추가 쌍이 있으면 워드 diff 적용
			// 쌍을 이루는 라인 수만큼 워드 diff 적용
			paired := delCount
			if addCount < paired {
				paired = addCount
			}

			for k := 0; k < paired; k++ {
				delLine := lines[delStart+k]
				addLine := lines[addStart+k]

				if IsWhitespaceOnlyChange(delLine.Content, addLine.Content) {
					// 공백 전용 변경
					fmt.Fprintln(w, renderWhitespaceOnlyChange(delLine, cs))
					fmt.Fprintln(w, renderWhitespaceOnlyChange(addLine, cs))
				} else {
					oldSpans, newSpans := ComputeWordDiff(delLine.Content, addLine.Content)
					fmt.Fprintln(w, renderLineWithWordDiff(delLine, oldSpans, cs, noColor))
					fmt.Fprintln(w, renderLineWithWordDiff(addLine, newSpans, cs, noColor))
				}
			}

			// 남은 삭제 라인 (쌍이 안 맞는 경우)
			for k := paired; k < delCount; k++ {
				fmt.Fprintln(w, renderLine(lines[delStart+k], cs))
			}
			// 남은 추가 라인 (쌍이 안 맞는 경우)
			for k := paired; k < addCount; k++ {
				fmt.Fprintln(w, renderLine(lines[addStart+k], cs))
			}
		} else {
			// 삭제만 있거나 추가만 있는 경우 일반 렌더링
			for k := delStart; k < delEnd; k++ {
				fmt.Fprintln(w, renderLine(lines[k], cs))
			}
			for k := addStart; k < addEnd; k++ {
				fmt.Fprintln(w, renderLine(lines[k], cs))
			}
		}

		// 컨텍스트 라인 또는 기타 라인 처리
		if i < len(lines) && lines[i].Kind != LineDeleted && lines[i].Kind != LineAdded {
			fmt.Fprintln(w, renderLine(lines[i], cs))
			i++
		}
	}
}
