package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/git"
)

// truncateLabelRunes shortens s to at most max runes, appending an
// ellipsis when truncation actually happens. Operates on a rune slice
// so it never splits a multibyte codepoint — important for Korean,
// Japanese, Chinese, and emoji that frequently appear in branch names
// and commit subjects.
func truncateLabelRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// renderInlineConflicts shows the first conflict region of the first
// unmerged file in detail (with line numbers + side markers), then
// summarises any remaining regions and files in compact form. The
// design goal is "single screen": users get enough signal to decide
// whether the resolve is trivial (one variable rename) or needs an
// editor session, without the message itself becoming the spam.
//
// repoDir scopes file paths — empty means runtime CWD.
func renderInlineConflicts(w io.Writer, repoDir string, files []string) {
	if len(files) == 0 {
		return
	}

	first := files[0]
	regions := loadConflictRegions(repoDir, first)
	if len(regions) == 0 {
		// No parseable regions — either the file vanished, the markers
		// were malformed, or we lost a race with the user resolving
		// them mid-print. Fall through silently; the upstream caller
		// already listed the file by name so the user has a hook.
		return
	}

	red := color.RedString
	green := color.GreenString
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()

	totalLines := git.TotalConflictLines(regions)
	regionWord := "region"
	if len(regions) != 1 {
		regionWord = "regions"
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s %s    %s\n",
		red("✗"),
		bold(first),
		faint(fmt.Sprintf("%d %s · %d conflicting lines",
			len(regions), regionWord, totalLines)))
	fmt.Fprintln(w, "  "+strings.Repeat("─", inlineDividerWidth))

	// First region rendered inline.
	r := regions[0]
	fmt.Fprintf(w, "    region 1/%d  ·  lines %d–%d\n\n",
		len(regions), r.StartMarkerLine, r.EndMarkerLine)

	renderConflictSide(w, r.OursLabel, "HEAD", "current branch",
		r.ContextBefore, r.Ours, nil,
		red, "▌", "◀", faint)
	fmt.Fprintln(w)
	renderConflictSide(w, r.TheirsLabel, "incoming", "incoming",
		nil, r.Theirs, r.ContextAfter,
		green, "▌", "▶", faint)

	if len(regions) > 1 {
		ranges := make([]string, 0, len(regions)-1)
		for _, rr := range regions[1:] {
			ranges = append(ranges, fmt.Sprintf("L%d–%d", rr.StartMarkerLine, rr.EndMarkerLine))
		}
		moreWord := "regions"
		if len(regions)-1 == 1 {
			moreWord = "region"
		}
		fmt.Fprintf(w, "\n    %s\n", faint(fmt.Sprintf(
			"+ %d more %s:  %s",
			len(regions)-1, moreWord, strings.Join(ranges, ",  "))))
	}

	if len(files) > 1 {
		moreFiles := "files"
		if len(files)-1 == 1 {
			moreFiles = "file"
		}
		fmt.Fprintf(w, "\n  %s\n", faint(fmt.Sprintf(
			"+ %d more %s with conflicts: %s",
			len(files)-1, moreFiles, strings.Join(files[1:], ", "))))
	}
}

// inlineDividerWidth controls the horizontal rule below the file
// header. 65 columns fits comfortably in a 80-column terminal with
// the surrounding indentation.
const inlineDividerWidth = 65

// inlineConflictMaxLinesPerSide caps how much of a single side we
// show inline; larger conflicts are clearly editor work, so we just
// note "+N more lines" and stop. Even-numbered so head/tail split
// cleanly when truncating.
const inlineConflictMaxLinesPerSide = 12

// renderConflictSide prints one side of a conflict region — header
// row, optional context line above, the side content (with truncation
// for oversized regions), and an optional context line below.
func renderConflictSide(
	w io.Writer,
	label, fallbackLabel, descriptor string,
	contextAbove *git.ConflictLine,
	body []git.ConflictLine,
	contextBelow *git.ConflictLine,
	sideColor func(string, ...interface{}) string,
	bar, marker string,
	faint func(...interface{}) string,
) {
	bold := color.New(color.Bold).SprintFunc()
	displayLabel := label
	if displayLabel == "" {
		displayLabel = fallbackLabel
	}
	// Truncate very long labels (incoming commit subjects can run on)
	// to keep the header on one line. Use a rune slice so multibyte
	// runes (CJK / emoji) are not chopped mid-codepoint, which would
	// produce invalid UTF-8 in the rendered terminal output.
	displayLabel = truncateLabelRunes(displayLabel, 60)

	// Header rule width is visual: count runes, not bytes, so a
	// "main" label (4 runes) and a "메인 브랜치" label (7 runes) get
	// equal-feeling rules. Byte length would shrink the rule far too
	// much for any non-ASCII label.
	labelRunes := utf8.RuneCountInString(displayLabel)
	descriptorRunes := utf8.RuneCountInString(descriptor)
	headerRule := strings.Repeat("─", maxInt(0, inlineDividerWidth-labelRunes-descriptorRunes-6))
	fmt.Fprintf(w, "    %s %s %s %s\n",
		sideColor(bar),
		bold(displayLabel),
		faint(headerRule),
		faint(descriptor))

	if contextAbove != nil {
		renderConflictLine(w, faint, "·", contextAbove.LineNum, contextAbove.Text)
	}
	renderConflictBody(w, body, sideColor, marker)
	if contextBelow != nil {
		renderConflictLine(w, faint, "·", contextBelow.LineNum, contextBelow.Text)
	}
}

func renderConflictBody(w io.Writer, body []git.ConflictLine, sideColor func(string, ...interface{}) string, marker string) {
	if len(body) <= inlineConflictMaxLinesPerSide {
		for _, ln := range body {
			renderConflictLine(w, func(a ...interface{}) string { return sideColor("%s", fmt.Sprint(a...)) },
				marker, ln.LineNum, ln.Text)
		}
		return
	}
	half := inlineConflictMaxLinesPerSide / 2
	head := body[:half]
	tail := body[len(body)-half:]
	wrap := func(a ...interface{}) string { return sideColor("%s", fmt.Sprint(a...)) }
	for _, ln := range head {
		renderConflictLine(w, wrap, marker, ln.LineNum, ln.Text)
	}
	fmt.Fprintf(w, "    │       %s\n",
		color.New(color.Faint).Sprintf("… +%d more lines on this side", len(body)-inlineConflictMaxLinesPerSide))
	for _, ln := range tail {
		renderConflictLine(w, wrap, marker, ln.LineNum, ln.Text)
	}
}

// renderConflictLine prints a single content row: gutter rail, the
// 4-digit line number, the side marker (◀ / ▶ / ·), and the original
// text of the line (untouched — leading whitespace preserved).
func renderConflictLine(w io.Writer, paint func(...interface{}) string, marker string, lineNum int, text string) {
	fmt.Fprintf(w, "    │ %4d  %s  %s\n", lineNum, paint(marker), text)
}

func loadConflictRegions(repoDir, file string) []git.ConflictRegion {
	path := file
	if repoDir != "" && !filepath.IsAbs(path) {
		path = filepath.Join(repoDir, path)
	}
	regions, err := git.ParseConflictMarkers(path)
	if err != nil {
		return nil
	}
	return regions
}
