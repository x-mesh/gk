package cli

import (
	"context"
	"errors"
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

// RunResolveTUI walks each conflict hunk and asks the user to pick a
// resolution. Long hunks scroll inside ui.ScrollSelectTUI's viewport,
// so the diff stays visible while the user decides.
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

			var options []ui.ScrollSelectOption
			if aiRes != nil && hunkIdx < len(aiRes) {
				ai := aiRes[hunkIdx]
				options = []ui.ScrollSelectOption{
					{Key: "o", Value: "ours", Display: fmt.Sprintf("ours — keep local (%s)", ai.Rationale)},
					{Key: "t", Value: "theirs", Display: fmt.Sprintf("theirs — accept remote (%s)", ai.Rationale)},
					{Key: "m", Value: "merged", Display: fmt.Sprintf("merged — AI combined (%s)", ai.Rationale)},
				}
				// Mark whichever option matches the AI recommendation
				// as the enter-default so the happy path is one keystroke.
				rec := strings.ToLower(string(ai.Strategy))
				for i := range options {
					if options[i].Value == rec {
						options[i].IsDefault = true
						break
					}
				}
			} else {
				options = []ui.ScrollSelectOption{
					{Key: "o", Value: "ours", Display: "ours — keep local changes"},
					{Key: "t", Value: "theirs", Display: "theirs — accept remote changes"},
				}
			}

			choice, err := ui.ScrollSelectTUI(context.Background(), title, diff, options)
			if err != nil {
				if errors.Is(err, ui.ErrPickerAborted) {
					return nil, err
				}
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
