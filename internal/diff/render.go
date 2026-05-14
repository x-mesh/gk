package diff

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/ui"
)

// RenderOptions는 렌더링 동작을 제어하는 옵션이다.
type RenderOptions struct {
	NoColor    bool // ANSI 이스케이프 코드 비활성화
	NoWordDiff bool // 단어 단위 하이라이트 비활성화
	Context    int  // 컨텍스트 라인 수 (기본 3, 현재 파서에서 제어)

	// ShowRefs는 파일 헤더 바로 아래에 ◀ LeftRef / ▶ RightRef 한 줄을
	// 표시할지 결정한다. LeftRef/RightRef 둘 다 비어있으면 무시된다.
	ShowRefs bool
	LeftRef  string // ◀ 쪽 비교 대상 라벨 (예: "develop")
	RightRef string // ▶ 쪽 비교 대상 라벨 (예: "origin/main")
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

// renderFileHeader는 파일 헤더를 렌더링한다. ui.RenderSection의 rule
// 레이아웃을 사용해 status / doctor / pull / merge / sync와 동일한
// 시각 언어를 공유한다 — 길이 64로 고정된 가로줄이 기존 60-char `─`
// 패턴을 대체하며, 파일 상태가 chrome 색상을 결정한다:
//
//	added       → SectionHealth   (olive)
//	deleted     → SectionAction   (orange)
//	renamed/copied → SectionDiverged (violet)
//	mode-only   → SectionMuted    (faint cyan)
//	modified    → SectionInfo     (steel blue, default)
//
// 파일 경로는 KeepCase=true로 원본 case를 유지한다 — 일반 섹션 라벨이
// 짧은 태그라 ToUpper가 자연스러운 것과 달리 path는 content 자체이기
// 때문. 섹션의 trailing blank line을 trim해 hunk가 빈 줄 없이 바로
// 이어지도록 한다 (file 간 빈 줄은 Render가 별도로 emit).
func renderFileHeader(w io.Writer, f *DiffFile, cs colorSet) {
	_ = cs // colorSet은 더 이상 헤더 렌더링에 쓰이지 않지만 시그니처 유지
	path := f.NewPath
	if f.Status == StatusDeleted {
		path = f.OldPath
	}

	var statusStr string
	chrome := ui.SectionInfo
	switch f.Status {
	case StatusAdded:
		statusStr = "[added]"
		chrome = ui.SectionHealth
	case StatusDeleted:
		statusStr = "[deleted]"
		chrome = ui.SectionAction
	case StatusRenamed:
		statusStr = fmt.Sprintf("[renamed: %s → %s]", f.OldPath, f.NewPath)
		chrome = ui.SectionDiverged
	case StatusCopied:
		statusStr = fmt.Sprintf("[copied: %s → %s]", f.OldPath, f.NewPath)
		chrome = ui.SectionDiverged
	case StatusModeChanged:
		statusStr = fmt.Sprintf("[mode: %s → %s]", f.OldMode, f.NewMode)
		chrome = ui.SectionMuted
	}

	block := ui.RenderSection(path, statusStr, nil, ui.SectionOpts{
		Layout:   ui.SectionLayoutRule,
		Color:    chrome,
		KeepCase: true,
	})
	fmt.Fprint(w, strings.TrimRight(block, "\n")+"\n")
}

// renderRefLabels는 파일 헤더 바로 아래에 한 줄짜리 ref 라벨을 출력한다.
// ◀ 화살표(붉은색·deletion 쪽)는 LeftRef를, ▶ 화살표(녹색·addition 쪽)는
// RightRef를 가리킨다. 화살표는 본문 라인의 ◀/▶ 마커와 동일한 컬러 키를
// 공유하므로 "왼쪽이 어느 ref인지" 매 화면마다 다시 떠올릴 필요가 없다.
//
// LeftRef/RightRef가 둘 다 비어있거나 ShowRefs가 false면 아무것도 출력하지
// 않는다 — 기존 호출자(예: 테스트)는 옵션을 채우지 않아 그대로 동작한다.
func renderRefLabels(w io.Writer, opts RenderOptions, cs colorSet) {
	if !opts.ShowRefs {
		return
	}
	if opts.LeftRef == "" && opts.RightRef == "" {
		return
	}
	left := opts.LeftRef
	if left == "" {
		left = "(unknown)"
	}
	right := opts.RightRef
	if right == "" {
		right = "(unknown)"
	}
	fmt.Fprintf(w, "      %s %s   %s %s\n",
		cs.red("◀"), cs.faint(left),
		cs.green("▶"), cs.faint(right))
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
		renderRefLabels(w, opts, cs)

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
