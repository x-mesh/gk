package cli

import "github.com/fatih/color"

// colorOff reports whether ANSI should be stripped — honors both
// fatih/color's global (which auto-flips for non-TTY captures) and
// gk's own --no-color flag so test harnesses that set either signal
// get the plain text path.
func colorOff() bool {
	return color.NoColor || NoColorFlag()
}

// Cell color helpers — emit foreground (or bold) ANSI sequences that
// reset ONLY the attribute they set, never the background. This lets
// bubbles/table's Selected style (purple background on the cursor row)
// stay visible across colored spans within a cell.
//
// fatih/color uses `\x1b[0m` (full reset), which clears the row's
// background mid-cell — the cursor highlight visually breaks. These
// helpers replace fatih's usage strictly inside Cells (Display strings
// can keep using fatih since they're rendered without a row style).

const (
	ansiFgRed     = "\x1b[31m"
	ansiFgGreen   = "\x1b[32m"
	ansiFgYellow  = "\x1b[33m"
	ansiFgCyan    = "\x1b[36m"
	ansiBold      = "\x1b[1m"
	ansiFaint     = "\x1b[2m"
	ansiResetFg   = "\x1b[39m"
	ansiResetBold = "\x1b[22m"
)

func cellRed(s string) string {
	if colorOff() {
		return s
	}
	return ansiFgRed + s + ansiResetFg
}

func cellGreen(s string) string {
	if colorOff() {
		return s
	}
	return ansiFgGreen + s + ansiResetFg
}

func cellYellow(s string) string {
	if colorOff() {
		return s
	}
	return ansiFgYellow + s + ansiResetFg
}

func cellCyan(s string) string {
	if colorOff() {
		return s
	}
	return ansiFgCyan + s + ansiResetFg
}

func cellRedBold(s string) string {
	if colorOff() {
		return s
	}
	return ansiBold + ansiFgRed + s + ansiResetFg + ansiResetBold
}

func cellGreenBold(s string) string {
	if colorOff() {
		return s
	}
	return ansiBold + ansiFgGreen + s + ansiResetFg + ansiResetBold
}

func cellCyanBold(s string) string {
	if colorOff() {
		return s
	}
	return ansiBold + ansiFgCyan + s + ansiResetFg + ansiResetBold
}

func cellFaint(s string) string {
	if colorOff() {
		return s
	}
	return ansiFaint + s + ansiResetBold
}

func cellBold(s string) string {
	if colorOff() {
		return s
	}
	return ansiBold + s + ansiResetBold
}

// osc8Link wraps text in an OSC 8 terminal hyperlink to url so supporting
// terminals render it clickable. Non-interactive output (colorOff) or a
// missing url returns the text plain, so no escape leaks into a pipe/file;
// terminals without OSC 8 support ignore the sequence and show text unchanged.
func osc8Link(url, text string) string {
	if colorOff() || url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}
