package forget

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// BarMode controls how Audit results are visualised in the terminal.
type BarMode int

const (
	// BarAuto picks filled when the output is an interactive terminal
	// with colour support, plain otherwise. Designed so piping the
	// command into grep / tee does not leak ANSI escapes.
	BarAuto BarMode = iota
	// BarFilled paints each row as a single line where the label
	// (path/blobs/size) sits on top of a coloured background that
	// covers a fraction of the row equal to the entry's share of the
	// heaviest entry. This is the htop / du-dust style.
	BarFilled
	// BarBlock keeps the label in plain text and adds a separate
	// column drawn with the sub-cell block glyphs `█▉▊▋▌▍▎▏`. Useful
	// when colour is unavailable but length-as-ratio still helps.
	BarBlock
	// BarNone is plain text — what the original analyze output looked
	// like. Default when stdout is not a TTY or when --no-color is
	// asserted.
	BarNone
)

// ParseBarMode is the spf13/cobra flag accessor.
func ParseBarMode(s string) (BarMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return BarAuto, nil
	case "filled":
		return BarFilled, nil
	case "block":
		return BarBlock, nil
	case "none", "plain", "off":
		return BarNone, nil
	default:
		return BarAuto, fmt.Errorf("unknown --bar mode %q (want auto|filled|block|none)", s)
	}
}

// RenderOpts captures every knob the renderer consults so the cobra
// layer does not have to plumb individual fields through.
type RenderOpts struct {
	Mode    BarMode
	NoColor bool
	Width   int // terminal columns; 0 = auto-detect, fall back to 100
}

// resolve folds Auto into Filled or None based on the runtime
// environment so callers downstream can switch on a concrete mode.
func (o RenderOpts) resolve() RenderOpts {
	if o.Width <= 0 {
		o.Width = detectWidth()
	}
	if o.Mode != BarAuto {
		return o
	}
	if o.NoColor || !isStdoutTerminal() {
		o.Mode = BarNone
	} else {
		o.Mode = BarFilled
	}
	return o
}

func detectWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return w
	}
	return 100
}

func isStdoutTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// RenderAudit returns a single string suitable for printing. Trailing
// newline is included so callers can `fmt.Fprint(w, RenderAudit(...))`
// without managing line endings.
//
// The function never panics on empty input — an empty entries slice
// produces a single line saying so. Same goes for a single entry: the
// "max" denominator falls back to that entry's TotalBytes so the bar
// still renders as full rather than dividing by zero.
func RenderAudit(entries []AuditEntry, opts RenderOpts) string {
	opts = opts.resolve()
	if len(entries) == 0 {
		return "no blobs found — repo has no history yet?\n"
	}

	var max int64 = 1
	for _, e := range entries {
		if e.TotalBytes > max {
			max = e.TotalBytes
		}
	}

	var sb strings.Builder
	for _, e := range entries {
		ratio := float64(e.TotalBytes) / float64(max)
		switch opts.Mode {
		case BarFilled:
			sb.WriteString(renderFilledRow(e, ratio, opts))
		case BarBlock:
			sb.WriteString(renderBlockRow(e, ratio, opts))
		default:
			sb.WriteString(renderPlainRow(e))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// renderFilledRow lays out the label inside a fixed-width slot, then
// applies a coloured background to the prefix that represents the
// entry's ratio. The label remains readable everywhere — the
// background is purely a length cue — so the line is parseable even
// when terminals render the colour differently than expected.
func renderFilledRow(e AuditEntry, ratio float64, opts RenderOpts) string {
	rowWidth := clampWidth(opts.Width-2, 60, 140)
	label := buildLabel(e, rowWidth)

	// Truncate-to-width safeguard. buildLabel pads to rowWidth, so this
	// only fires for unusually wide terminal sizes that overflow our
	// clamp (which we won't hit with the clamp above, but defensive).
	if len(label) > rowWidth {
		label = label[:rowWidth]
	}

	fill := int(float64(rowWidth) * ratio)
	if fill < 1 && e.TotalBytes > 0 {
		fill = 1
	}
	if fill > rowWidth {
		fill = rowWidth
	}

	colored := label[:fill]
	rest := label[fill:]

	bgFilled := pickBackground(e, opts.NoColor)
	bgEmpty := lipgloss.NewStyle().Faint(true)

	return bgFilled.Render(colored) + bgEmpty.Render(rest)
}

// renderBlockRow keeps the label as plain text and appends a
// sub-cell-precision block-glyph bar in a fixed-width column. Used
// when the user explicitly opts for block mode or when filled would
// be unreadable (rare). Mode is not auto-selected today.
func renderBlockRow(e AuditEntry, ratio float64, opts RenderOpts) string {
	const barWidth = 24
	bar := blockBar(ratio, barWidth)
	plain := renderPlainRow(e)
	// Append the bar so the row reads "label  ████░░░░".
	return strings.TrimRight(plain, "\n") + "  " + bar
}

func renderPlainRow(e AuditEntry) string {
	flag := ""
	if !e.InHEAD {
		flag = "  (history-only)"
	}
	return fmt.Sprintf("  %-50s %5d  total %10s  largest %10s%s",
		truncatePath(e.Path, 50),
		e.UniqueBlobs,
		HumanBytes(e.TotalBytes),
		HumanBytes(e.LargestBytes),
		flag,
	)
}

// buildLabel composes the row text used inside the filled bar. Padded
// to width with spaces on the right so the background colour covers
// "empty" cells too — otherwise the bar visually ends at the path
// length rather than at the ratio mark.
func buildLabel(e AuditEntry, width int) string {
	flag := ""
	if !e.InHEAD {
		flag = " (history)"
	}
	right := fmt.Sprintf(" %4d  %10s%s", e.UniqueBlobs, HumanBytes(e.TotalBytes), flag)
	pathRoom := width - len(right) - 2 // 2 for leading " "
	if pathRoom < 10 {
		pathRoom = 10
	}
	left := truncatePath(e.Path, pathRoom)
	row := " " + left + strings.Repeat(" ", max0(pathRoom-len(left))) + right
	if len(row) > width {
		return row[:width]
	}
	return row + strings.Repeat(" ", width-len(row))
}

// truncatePath shortens a path to fit `width` cells using a middle
// ellipsis. We keep the first segment and as much of the tail as
// possible — top-level dirs are usually informative, file basenames
// are critical, the middle is the part users tolerate eliding.
func truncatePath(path string, width int) string {
	if len(path) <= width || width < 6 {
		if len(path) > width {
			return path[:width]
		}
		return path
	}
	const ell = "…"
	keep := width - len(ell)
	headLen := keep / 3
	tailLen := keep - headLen
	return path[:headLen] + ell + path[len(path)-tailLen:]
}

// pickBackground returns the lipgloss style for the filled portion of
// a row. History-only entries are flagged with a warmer colour so
// they catch the eye — they are the highest-leverage forget targets
// because they no longer appear in HEAD.
func pickBackground(e AuditEntry, noColor bool) lipgloss.Style {
	if noColor {
		// Reverse video keeps the length cue without needing colour
		// support. A fair trade-off on monochrome terminals.
		return lipgloss.NewStyle().Reverse(true)
	}
	if !e.InHEAD {
		// 88 in the 256-colour palette is a deep red that contrasts
		// against white text without saturating the eye.
		return lipgloss.NewStyle().
			Background(lipgloss.Color("88")).
			Foreground(lipgloss.Color("231"))
	}
	// 24 = navy blue — calm by default, leaves attention budget for
	// the warm history-only rows.
	return lipgloss.NewStyle().
		Background(lipgloss.Color("24")).
		Foreground(lipgloss.Color("231"))
}

// blockBar renders a sub-cell bar like `████▉▌░░░░` of total `width`
// cells. ratio in [0, 1]; out-of-range values are clamped.
func blockBar(ratio float64, width int) string {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	full := int(ratio * float64(width))
	frac := ratio*float64(width) - float64(full)
	// Eighths glyphs at sub-cell precision. Indexed as runes (each
	// glyph is multi-byte UTF-8) so subIdx maps to the intended cell
	// rather than to the middle of a sequence.
	eighths := []rune(" ▏▎▍▌▋▊▉")
	subIdx := int(frac * 8)
	if subIdx < 0 {
		subIdx = 0
	}
	if subIdx > 7 {
		subIdx = 7
	}

	var b strings.Builder
	for i := 0; i < full; i++ {
		b.WriteString("█")
	}
	if subIdx > 0 && full < width {
		b.WriteRune(eighths[subIdx])
		full++
	}
	for i := full; i < width; i++ {
		b.WriteString("░")
	}
	return b.String()
}

func clampWidth(w, lo, hi int) int {
	if w < lo {
		return lo
	}
	if w > hi {
		return hi
	}
	return w
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
