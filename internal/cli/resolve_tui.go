package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/resolve"
	"github.com/x-mesh/gk/internal/ui"
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

			var items []ui.PickerItem
			if aiRes != nil && hunkIdx < len(aiRes) {
				ai := aiRes[hunkIdx]
				items = []ui.PickerItem{
					{Key: "ours", Display: fmt.Sprintf("ours — keep local (%s)", ai.Rationale)},
					{Key: "theirs", Display: fmt.Sprintf("theirs — accept remote (%s)", ai.Rationale)},
					{Key: "merged", Display: fmt.Sprintf("merged — AI combined (%s)", ai.Rationale)},
				}
			} else {
				items = []ui.PickerItem{
					{Key: "ours", Display: "ours — keep local changes"},
					{Key: "theirs", Display: "theirs — accept remote changes"},
				}
			}

			// Print the diff once before the picker so the user sees the
			// hunk while choosing — TablePicker doesn't have a description
			// pane like huh.NewSelect.
			fmt.Println(title)
			fmt.Println(diff)

			picker := &ui.TablePicker{}
			pick, err := picker.Pick(context.Background(), title, items)
			if err != nil {
				return nil, err
			}
			choice := pick.Key

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
