package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/resolve"
)

// FormatHunkDiff는 충돌 영역을 색상 구분된 diff 형식으로 포맷한다.
// Ours 라인은 초록색 (- prefix), Theirs 라인은 빨간색 (+ prefix).
func FormatHunkDiff(hunk resolve.ConflictHunk) string {
	var b strings.Builder
	green := color.New(color.FgGreen)
	red := color.New(color.FgRed)
	for _, line := range hunk.Ours {
		green.Fprintf(&b, "- %s\n", line)
	}
	for _, line := range hunk.Theirs {
		red.Fprintf(&b, "+ %s\n", line)
	}
	return b.String()
}

// RunResolveTUI는 huh 기반 form을 표시하여 각 충돌 영역의 해결 방법을 선택한다.
// aiResolutions가 nil이면 ours/theirs만 표시한다.
// 반환값은 파일별 FileResolution 목록이다.
func RunResolveTUI(
	files []resolve.ConflictFile,
	aiResolutions map[string][]resolve.HunkResolution,
) ([]resolve.FileResolution, error) {
	var results []resolve.FileResolution

	for _, cf := range files {
		var fileRes resolve.FileResolution
		fileRes.Path = cf.Path

		aiRes := aiResolutions[cf.Path] // may be nil
		hunkIdx := 0

		for _, seg := range cf.Segments {
			if seg.Hunk == nil {
				continue
			}

			diff := FormatHunkDiff(*seg.Hunk)
			title := fmt.Sprintf("%s — conflict %d", cf.Path, hunkIdx+1)

			var choice string
			var opts []huh.Option[string]

			if aiRes != nil && hunkIdx < len(aiRes) {
				ai := aiRes[hunkIdx]
				opts = append(opts,
					huh.NewOption(fmt.Sprintf("ours — keep local (%s)", ai.Rationale), "ours"),
					huh.NewOption(fmt.Sprintf("theirs — accept remote (%s)", ai.Rationale), "theirs"),
					huh.NewOption(fmt.Sprintf("merged — AI combined (%s)", ai.Rationale), "merged"),
				)
				choice = string(ai.Strategy) // AI 추천을 기본 선택으로
			} else {
				opts = append(opts,
					huh.NewOption("ours — keep local changes", "ours"),
					huh.NewOption("theirs — accept remote changes", "theirs"),
				)
			}

			sel := huh.NewSelect[string]().
				Title(title).
				Description(diff).
				Options(opts...).
				Value(&choice)

			form := huh.NewForm(huh.NewGroup(sel))
			if err := form.Run(); err != nil {
				return nil, err
			}

			hr := resolve.HunkResolution{Strategy: resolve.Strategy(choice)}
			switch choice {
			case "ours":
				hr.ResolvedLines = seg.Hunk.Ours
			case "theirs":
				hr.ResolvedLines = seg.Hunk.Theirs
			case "merged":
				if aiRes != nil && hunkIdx < len(aiRes) {
					hr.ResolvedLines = aiRes[hunkIdx].ResolvedLines
					hr.Rationale = aiRes[hunkIdx].Rationale
				}
			}

			fileRes.Resolutions = append(fileRes.Resolutions, hr)
			hunkIdx++
		}

		results = append(results, fileRes)
	}

	return results, nil
}
