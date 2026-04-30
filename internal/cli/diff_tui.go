package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/diff"
	"github.com/x-mesh/gk/internal/ui"
)

// DiffFileToPickerItem converts a DiffFile into a ui.PickerItem suitable
// for TablePicker display. The item's Cells contain the file status,
// added-line count, and deleted-line count. Key holds the file index
// as a string so the caller can map back to the original DiffFile.
//
// Exported for property-based testing (Property 8).
func DiffFileToPickerItem(f diff.DiffFile, index int) ui.PickerItem {
	path := f.NewPath
	if f.Status == diff.StatusDeleted {
		path = f.OldPath
	}

	statusStr := statusLabel(f.Status)
	added := fmt.Sprintf("+%d", f.AddedLines)
	deleted := fmt.Sprintf("-%d", f.DeletedLines)

	return ui.PickerItem{
		Key:     fmt.Sprintf("%d", index),
		Display: path,
		Cells:   []string{path, statusStr, added, deleted},
	}
}

// statusLabel returns a short human-readable label for a FileStatus.
func statusLabel(s diff.FileStatus) string {
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

// FoldSummary returns the folded summary line for a Hunk.
// The format is "[+N lines]" where N is len(h.Lines).
//
// Exported for property-based testing (Property 7).
func FoldSummary(h diff.Hunk) string {
	return fmt.Sprintf("[+%d lines]", len(h.Lines))
}

// renderFileDiff renders a single DiffFile's diff content to a string,
// respecting hunk fold states. foldState maps hunk index → folded (true
// means the hunk is collapsed).
func renderFileDiff(f *diff.DiffFile, opts diff.RenderOptions, foldState map[int]bool) string {
	var buf bytes.Buffer

	for i, h := range f.Hunks {
		if i > 0 {
			buf.WriteString("\n")
		}
		// Hunk header is always shown.
		buf.WriteString(h.Header)
		buf.WriteString("\n")

		if foldState[i] {
			// Folded: show summary line.
			buf.WriteString(FoldSummary(h))
			buf.WriteString("\n")
		} else {
			// Unfolded: render all lines.
			for _, line := range h.Lines {
				buf.WriteString(diff.RenderLine(line, opts.NoColor))
				buf.WriteString("\n")
			}
		}
	}

	return buf.String()
}

// runDiffInteractive implements the interactive TUI mode for gk diff.
// It shows a TablePicker for file selection, then a ScrollSelectTUI
// viewport for the selected file's diff. Supports n/p navigation
// between files and Tab to toggle hunk fold/unfold.
func runDiffInteractive(result *diff.DiffResult, opts diff.RenderOptions, noPager bool) error {
	if !ui.IsTerminal() {
		// Non-TTY fallback: render all files to stdout.
		var buf bytes.Buffer
		if err := diff.Render(&buf, result, opts); err != nil {
			return fmt.Errorf("gk diff: 렌더링 실패: %w", err)
		}
		_, err := os.Stdout.Write(buf.Bytes())
		return err
	}

	if len(result.Files) == 0 {
		fmt.Fprintln(os.Stderr, "변경사항 없음")
		return nil
	}

	ctx := context.Background()

	// Build picker items from DiffFiles.
	items := make([]ui.PickerItem, len(result.Files))
	for i, f := range result.Files {
		items[i] = DiffFileToPickerItem(f, i)
	}

	picker := &ui.TablePicker{
		Headers: []string{"FILE", "STATUS", "ADDED", "DELETED"},
	}

	// Per-file hunk fold state: fileIndex → (hunkIndex → folded).
	// Default: all hunks unfolded.
	fileFoldStates := make(map[int]map[int]bool)

	for {
		// Show file picker.
		picked, err := picker.Pick(ctx, "gk diff — 파일 선택", items)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return nil // user cancelled
			}
			return err
		}

		// Parse selected file index.
		var fileIdx int
		if _, err := fmt.Sscanf(picked.Key, "%d", &fileIdx); err != nil {
			return fmt.Errorf("gk diff: 잘못된 파일 인덱스: %s", picked.Key)
		}

		// Enter file diff view loop (supports n/p navigation).
		action := showFileDiff(ctx, result, opts, fileIdx, fileFoldStates)
		if action == "quit" {
			return nil
		}
		// action == "back" → loop back to file picker
	}
}

// fileRenderCache memoises the most recent rendered body so that
// keystrokes which neither switch files nor toggle hunks can return
// the cached string instead of walking the entire diff again. Hot for
// any file with thousands of lines — the previous code re-rendered
// the full body on every n / p / b navigation event.
type fileRenderCache struct {
	fileIdx     int
	foldedHunks []int // sorted indices of currently-folded hunks
	body        string
	valid       bool
}

// snapshotFolded extracts the sorted list of folded hunk indices from
// a fold-state map, used as the cache key alongside fileIdx.
func snapshotFolded(state map[int]bool) []int {
	if len(state) == 0 {
		return nil
	}
	folded := make([]int, 0, len(state))
	for k, v := range state {
		if v {
			folded = append(folded, k)
		}
	}
	sort.Ints(folded)
	return folded
}

func sameIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// showFileDiff displays the diff for a single file in a ScrollSelectTUI
// viewport. Returns "back" to go back to the file picker, or "quit" to
// exit entirely. Supports n/p for next/previous file and Tab for hunk
// fold/unfold.
func showFileDiff(
	ctx context.Context,
	result *diff.DiffResult,
	opts diff.RenderOptions,
	startIdx int,
	fileFoldStates map[int]map[int]bool,
) string {
	idx := startIdx
	total := len(result.Files)
	var cache fileRenderCache

	for {
		if idx < 0 {
			idx = 0
		}
		if idx >= total {
			idx = total - 1
		}

		f := &result.Files[idx]

		// Ensure fold state map exists for this file.
		if fileFoldStates[idx] == nil {
			fileFoldStates[idx] = make(map[int]bool)
		}

		// Render the file diff with fold states — reuse cache on
		// re-entry if neither the file nor the fold state changed.
		foldKey := snapshotFolded(fileFoldStates[idx])
		var body string
		if cache.valid && cache.fileIdx == idx && sameIntSlice(cache.foldedHunks, foldKey) {
			body = cache.body
		} else {
			body = renderFileDiff(f, opts, fileFoldStates[idx])
			cache = fileRenderCache{
				fileIdx:     idx,
				foldedHunks: foldKey,
				body:        body,
				valid:       true,
			}
		}
		if strings.TrimSpace(body) == "" {
			if f.IsBinary {
				body = "Binary file differs"
			} else {
				body = "(빈 diff)"
			}
		}

		// Build title with file path and navigation info.
		path := f.NewPath
		if f.Status == diff.StatusDeleted {
			path = f.OldPath
		}
		title := fmt.Sprintf("%s (%d/%d)", path, idx+1, total)

		// Build options: navigation + fold toggle + back.
		options := buildDiffViewOptions(idx, total, f)

		choice, err := ui.ScrollSelectTUI(ctx, title, body, options)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return "back"
			}
			return "quit"
		}

		switch choice {
		case "next":
			if idx < total-1 {
				idx++
			}
		case "prev":
			if idx > 0 {
				idx--
			}
		case "toggle-fold":
			// Toggle all hunks in the current file.
			toggleAllHunks(f, fileFoldStates[idx])
		case "back":
			return "back"
		default:
			return "back"
		}
	}
}

// buildDiffViewOptions creates the ScrollSelectOption list for the file
// diff viewport. Includes n/p navigation, Tab fold toggle, and back.
func buildDiffViewOptions(idx, total int, f *diff.DiffFile) []ui.ScrollSelectOption {
	var opts []ui.ScrollSelectOption

	if idx < total-1 {
		opts = append(opts, ui.ScrollSelectOption{
			Key: "n", Value: "next", Display: "다음 파일",
		})
	}
	if idx > 0 {
		opts = append(opts, ui.ScrollSelectOption{
			Key: "p", Value: "prev", Display: "이전 파일",
		})
	}

	if len(f.Hunks) > 0 {
		opts = append(opts, ui.ScrollSelectOption{
			Key: "tab", Value: "toggle-fold", Display: "Hunk 접기/펼치기 토글",
		})
	}

	opts = append(opts, ui.ScrollSelectOption{
		Key: "b", Value: "back", Display: "파일 목록으로 돌아가기", IsDefault: true,
	})

	return opts
}

// toggleAllHunks toggles the fold state of all hunks in a file.
// If any hunk is unfolded, fold all; if all are folded, unfold all.
func toggleAllHunks(f *diff.DiffFile, foldState map[int]bool) {
	allFolded := true
	for i := range f.Hunks {
		if !foldState[i] {
			allFolded = false
			break
		}
	}

	for i := range f.Hunks {
		foldState[i] = !allFolded
	}
}
