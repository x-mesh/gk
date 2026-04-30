package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/resolve"
	"github.com/x-mesh/gk/internal/ui"
)

// FormatHunkDiff renders a conflict hunk in classic - / + diff form.
// Retained as the legacy formatter for callers that don't have line-
// number metadata; the resolve TUI uses the richer formatHunkRich
// (with line numbers, side labels, and optional context lines).
func FormatHunkDiff(hunk resolve.ConflictHunk) string {
	var b strings.Builder
	green := color.New(color.FgGreen)
	red := color.New(color.FgRed)
	for _, line := range hunk.Ours {
		_, _ = green.Fprintf(&b, "- %s\n", line)
	}
	for _, line := range hunk.Theirs {
		_, _ = red.Fprintf(&b, "+ %s\n", line)
	}
	return b.String()
}

// formatHunkRich renders a single conflict region as a two-pane block:
//
//	▌ HEAD ────────────────────────────── current branch
//	│ 187  ·     case '1':                            ← context
//	│ 189  ◀         e, keep, err := parseV2Ordinary(...)
//	│ 190  ◀         if err != nil { ...
//	▌ cd98609 (subject) ───────────────────── incoming
//	│ 195  ▶         e, ok, err := parseV2Ordinary(...)
//	│ 201  ·     out = append(out, e)                 ← context
//
// region carries line numbers and surrounding context. When nil, the
// formatter falls back to printing without line numbers — that path is
// exercised by parser failures or files we couldn't re-open.
func formatHunkRich(hunk resolve.ConflictHunk, region *git.ConflictRegion) string {
	bold := color.New(color.Bold).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()

	oursLabel := pickLabel(hunk.OursLabel, "HEAD")
	theirsLabel := pickLabel(hunk.TheirsLabel, "incoming")

	var b strings.Builder

	// Ours header.
	writeSideHeader(&b, red("▌"), bold(truncateLabel(oursLabel, 60)),
		"current branch (ours)", faint)

	if region != nil && region.ContextBefore != nil {
		writeNumberedLine(&b, region.ContextBefore.LineNum, faint("·"), region.ContextBefore.Text)
	}
	if region != nil {
		for _, ln := range region.Ours {
			writeNumberedLine(&b, ln.LineNum, red("◀"), ln.Text)
		}
	} else {
		for _, line := range hunk.Ours {
			writeNumberedLine(&b, 0, red("◀"), line)
		}
	}

	b.WriteString("\n")

	// Theirs header.
	writeSideHeader(&b, green("▌"), bold(truncateLabel(theirsLabel, 60)),
		"incoming (theirs)", faint)

	if region != nil {
		for _, ln := range region.Theirs {
			writeNumberedLine(&b, ln.LineNum, green("▶"), ln.Text)
		}
		if region.ContextAfter != nil {
			writeNumberedLine(&b, region.ContextAfter.LineNum, faint("·"), region.ContextAfter.Text)
		}
	} else {
		for _, line := range hunk.Theirs {
			writeNumberedLine(&b, 0, green("▶"), line)
		}
	}

	return b.String()
}

const resolveTUIDividerWidth = 65

func writeSideHeader(b *strings.Builder, bar, label string, descriptor string, faint func(...interface{}) string) {
	rule := strings.Repeat("─", maxIntPositive(0, resolveTUIDividerWidth-visualWidth(label)-len(descriptor)-6))
	fmt.Fprintf(b, "%s %s %s %s\n", bar, label, faint(rule), faint(descriptor))
}

func writeNumberedLine(b *strings.Builder, lineNum int, marker, text string) {
	if lineNum > 0 {
		fmt.Fprintf(b, "│ %4d  %s  %s\n", lineNum, marker, text)
	} else {
		fmt.Fprintf(b, "│       %s  %s\n", marker, text)
	}
}

// pickLabel returns label when non-empty, fallback otherwise. Used to
// give the user something to read even when the conflict markers were
// produced without their typical "HEAD" / branch-name suffix.
func pickLabel(label, fallback string) string {
	if strings.TrimSpace(label) == "" {
		return fallback
	}
	return label
}

// truncateLabel ensures the side header fits on one line — incoming
// commit subjects can be quite long, especially when the marker
// includes "(commit subject)" parenthetical.
func truncateLabel(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// visualWidth returns the rune count of s, ignoring ANSI escape codes
// that wrap the visible label. The header rule width depends on it.
func visualWidth(s string) int {
	stripped := stripANSIForWidth(s)
	return len([]rune(stripped))
}

func stripANSIForWidth(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func maxIntPositive(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// pluralS returns "s" for n != 1; mirrors plural() in pull.go but kept
// local to avoid coupling resolve_tui.go to that file's helpers.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// formatResolveTitle builds the screen title. With region info, shows
// the precise file lines covered; without, just the conflict ordinal.
func formatResolveTitle(path string, idx, total int, region *git.ConflictRegion) string {
	if total <= 0 {
		total = idx
	}
	if region != nil {
		return fmt.Sprintf("%s — region %d/%d · lines %d–%d",
			path, idx, total, region.StartMarkerLine, region.EndMarkerLine)
	}
	return fmt.Sprintf("%s — region %d/%d", path, idx, total)
}

// buildResolveOptions assembles the keystroke menu shown below the
// scrollable diff. ours/theirs labels embed the actual ref/branch name
// (parsed from the conflict marker) and the hunk line counts so the
// user knows exactly what each choice keeps.
func buildResolveOptions(hunk resolve.ConflictHunk, ai *resolve.HunkResolution) []ui.ScrollSelectOption {
	oursLabel := truncateLabel(pickLabel(hunk.OursLabel, "HEAD"), 50)
	theirsLabel := truncateLabel(pickLabel(hunk.TheirsLabel, "incoming"), 50)

	oursDisplay := fmt.Sprintf("ours — keep %s (%d line%s)",
		oursLabel, len(hunk.Ours), pluralS(len(hunk.Ours)))
	theirsDisplay := fmt.Sprintf("theirs — accept %s (%d line%s)",
		theirsLabel, len(hunk.Theirs), pluralS(len(hunk.Theirs)))

	if ai != nil {
		opts := []ui.ScrollSelectOption{
			{Key: "o", Value: "ours", Display: oursDisplay},
			{Key: "t", Value: "theirs", Display: theirsDisplay},
			{Key: "m", Value: "merged",
				Display: fmt.Sprintf("merged — AI combined (%d lines): %s",
					len(ai.ResolvedLines), ai.Rationale)},
		}
		// Trim before lowering — an AI response with leading or trailing
		// whitespace ("ours ", " theirs") would otherwise silently miss
		// the equality check and leave every option without a default.
		rec := strings.TrimSpace(strings.ToLower(string(ai.Strategy)))
		for i := range opts {
			if opts[i].Value == rec {
				opts[i].IsDefault = true
				break
			}
		}
		return opts
	}
	return []ui.ScrollSelectOption{
		{Key: "o", Value: "ours", Display: oursDisplay},
		{Key: "t", Value: "theirs", Display: theirsDisplay},
	}
}

// countHunks returns how many conflict regions live in segs.
func countHunks(segs []resolve.Segment) int {
	n := 0
	for _, s := range segs {
		if s.Hunk != nil {
			n++
		}
	}
	return n
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

		// Try to enrich with line numbers via the marker parser. Errors
		// are tolerated — formatHunkRich gracefully falls back when no
		// region info is available.
		regions, _ := git.ParseConflictMarkers(cf.Path)
		totalHunks := countHunks(cf.Segments)

		aiRes := aiResolutions[cf.Path] // may be nil
		hunkIdx := 0

		for _, seg := range cf.Segments {
			if seg.Hunk == nil {
				continue
			}

			var region *git.ConflictRegion
			if hunkIdx < len(regions) {
				region = &regions[hunkIdx]
			}

			diff := formatHunkRich(*seg.Hunk, region)
			title := formatResolveTitle(cf.Path, hunkIdx+1, totalHunks, region)

			var aiHunk *resolve.HunkResolution
			if aiRes != nil && hunkIdx < len(aiRes) {
				aiHunk = &aiRes[hunkIdx]
			}
			options := buildResolveOptions(*seg.Hunk, aiHunk)

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
				if aiHunk != nil {
					hr.ResolvedLines = aiHunk.ResolvedLines
					hr.Rationale = aiHunk.Rationale
				}
			}

			fileRes.Resolutions = append(fileRes.Resolutions, hr)
			hunkIdx++
		}

		results = append(results, fileRes)
	}

	return results, nil
}
