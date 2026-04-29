package cli

import "github.com/fatih/color"

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
	if color.NoColor {
		return s
	}
	return ansiFgRed + s + ansiResetFg
}

func cellGreen(s string) string {
	if color.NoColor {
		return s
	}
	return ansiFgGreen + s + ansiResetFg
}

func cellYellow(s string) string {
	if color.NoColor {
		return s
	}
	return ansiFgYellow + s + ansiResetFg
}

func cellCyan(s string) string {
	if color.NoColor {
		return s
	}
	return ansiFgCyan + s + ansiResetFg
}

func cellRedBold(s string) string {
	if color.NoColor {
		return s
	}
	return ansiBold + ansiFgRed + s + ansiResetFg + ansiResetBold
}

func cellFaint(s string) string {
	if color.NoColor {
		return s
	}
	return ansiFaint + s + ansiResetBold
}
