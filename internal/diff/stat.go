package diff

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
)

const maxBarWidth = 50

// statColorSet은 stat 렌더링에 사용되는 색상 함수 집합이다.
type statColorSet struct {
	green func(string, ...interface{}) string
	red   func(string, ...interface{}) string
	bold  func(a ...interface{}) string
}

func newStatColorSet(noColor bool) statColorSet {
	if noColor {
		return statColorSet{
			green: fmt.Sprintf,
			red:   fmt.Sprintf,
			bold:  fmt.Sprint,
		}
	}
	return statColorSet{
		green: color.GreenString,
		red:   color.RedString,
		bold:  color.New(color.Bold).SprintFunc(),
	}
}

// RenderStat는 DiffResult에서 파일별 통계 요약을 생성한다.
// git diff --stat과 유사한 형식으로 출력한다.
func RenderStat(w io.Writer, result *DiffResult, noColor bool) error {
	if result == nil || len(result.Files) == 0 {
		return nil
	}

	cs := newStatColorSet(noColor)

	// 파일명과 변경 수 계산
	type fileStat struct {
		name     string
		added    int
		deleted  int
		total    int
		isBinary bool
	}

	stats := make([]fileStat, 0, len(result.Files))
	maxNameWidth := 0
	maxTotal := 0
	totalAdded := 0
	totalDeleted := 0

	for _, f := range result.Files {
		name := statFileName(f)
		if len(name) > maxNameWidth {
			maxNameWidth = len(name)
		}

		fs := fileStat{
			name:     name,
			added:    f.AddedLines,
			deleted:  f.DeletedLines,
			total:    f.AddedLines + f.DeletedLines,
			isBinary: f.IsBinary,
		}
		if fs.total > maxTotal {
			maxTotal = fs.total
		}
		totalAdded += f.AddedLines
		totalDeleted += f.DeletedLines
		stats = append(stats, fs)
	}

	// 파일별 stat 라인 출력
	for _, fs := range stats {
		// 파일명 (우측 정렬 패딩)
		padding := maxNameWidth - len(fs.name)
		fmt.Fprintf(w, " %s%s | ", fs.name, strings.Repeat(" ", padding))

		if fs.isBinary {
			fmt.Fprintln(w, "Bin")
			continue
		}

		// 변경 수
		fmt.Fprintf(w, "%d ", fs.total)

		// 막대 그래프
		bar := buildBar(fs.added, fs.deleted, maxTotal, cs)
		fmt.Fprintln(w, bar)
	}

	// 하단 요약
	fmt.Fprintf(w, " %s, %s, %s\n",
		cs.bold(fmt.Sprintf("%d %s changed", len(result.Files), pluralFile(len(result.Files)))),
		cs.green("%d insertions(+)", totalAdded),
		cs.red("%d deletions(-)", totalDeleted),
	)

	return nil
}

// statFileName은 stat 출력에 사용할 파일명을 반환한다.
// rename된 파일은 "old → new" 형식으로 표시한다.
func statFileName(f DiffFile) string {
	if f.Status == StatusRenamed && f.OldPath != f.NewPath {
		return f.OldPath + " → " + f.NewPath
	}
	if f.Status == StatusDeleted {
		return f.OldPath
	}
	return f.NewPath
}

// buildBar는 추가/삭제 비율에 따른 막대 그래프 문자열을 생성한다.
func buildBar(added, deleted, maxTotal int, cs statColorSet) string {
	total := added + deleted
	if total == 0 {
		return ""
	}

	// 막대 전체 너비 계산 (최대 maxBarWidth)
	barWidth := maxBarWidth
	if maxTotal > 0 && maxTotal < maxBarWidth {
		barWidth = maxTotal
	}

	// 비례 계산
	var scaledTotal int
	if maxTotal > 0 {
		scaledTotal = total * barWidth / maxTotal
	}
	if scaledTotal == 0 && total > 0 {
		scaledTotal = 1
	}

	// 추가/삭제 비율에 따라 +/- 분배
	var plusCount, minusCount int
	if total > 0 {
		plusCount = added * scaledTotal / total
		minusCount = scaledTotal - plusCount

		// 최소 1개 보장 — scaledTotal이 2 이상일 때만 시도. else if 로
		// 묶지 않으면 (added>0 && plusCount==0) 분기에서 plusCount를
		// 1로 올린 뒤, (deleted>0 && minusCount==0) 분기가 다시
		// minusCount를 1로 만들면서 plusCount를 0으로 덮어써, 1+1
		// 같은 양쪽 변경에서 `+`가 사라지는 회귀가 발생한다.
		// scaledTotal==1 인 경우에는 한 글자밖에 못 그리므로 비례
		// 계산이 정한 쪽(보통 `-`)을 그대로 둔다.
		switch {
		case scaledTotal >= 2 && added > 0 && plusCount == 0:
			plusCount = 1
			minusCount = scaledTotal - 1
		case scaledTotal >= 2 && deleted > 0 && minusCount == 0:
			minusCount = 1
			plusCount = scaledTotal - 1
		}
	}

	plus := cs.green("%s", strings.Repeat("+", plusCount))
	minus := cs.red("%s", strings.Repeat("-", minusCount))

	return plus + minus
}

// pluralFile은 파일 수에 따라 "file" 또는 "files"를 반환한다.
func pluralFile(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
}
