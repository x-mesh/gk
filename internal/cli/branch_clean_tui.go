package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/x-mesh/gk/internal/branchclean"
)

// FormatCandidateLabel은 TUI에 표시할 브랜치 라벨을 생성한다.
// 형식: "branch-name  (2d ago)  [squash-merged]  completed: 로그인 기능 구현"
// AI 미사용 시: "branch-name  (2d ago)  [merged]"
func FormatCandidateLabel(c branchclean.CleanCandidate) string {
	var b strings.Builder
	b.WriteString(c.Name)
	b.WriteString("  (")
	b.WriteString(relativeTime(time.Since(c.LastCommitDate)))
	b.WriteString(")  [")
	b.WriteString(string(c.Status))
	b.WriteString("]")

	if c.AICategory != "" {
		fmt.Fprintf(&b, "  %s: %s", c.AICategory, c.AISummary)
	}
	return b.String()
}

// relativeTime은 duration을 사람이 읽기 쉬운 상대 시간으로 변환한다.
func relativeTime(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days < 1:
		return "today"
	case days < 7:
		return fmt.Sprintf("%dd ago", days)
	case days < 30:
		return fmt.Sprintf("%dw ago", days/7)
	case days < 365:
		return fmt.Sprintf("%dm ago", days/30)
	default:
		return fmt.Sprintf("%dy ago", days/365)
	}
}

// RunCleanTUI는 huh MultiSelect form을 표시하여
// 삭제 대상 브랜치를 선택할 수 있게 한다.
// 반환값은 사용자가 선택한 브랜치 이름 목록이다.
func RunCleanTUI(candidates []branchclean.CleanCandidate) ([]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	opts := make([]huh.Option[string], 0, len(candidates))
	for _, c := range candidates {
		label := FormatCandidateLabel(c)
		opt := huh.NewOption(label, c.Name)
		if c.Selected {
			opt = opt.Selected(true)
		}
		opts = append(opts, opt)
	}

	var selected []string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("select branches to delete").
				Options(opts...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}
	return selected, nil
}
