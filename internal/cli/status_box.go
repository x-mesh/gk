package cli

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/ui"
)

// statusDensity reports the effective density mode for this invocation.
// Resolution order:
//  1. CLI verbose flag (`-v` / `--verbose`, count >= 1) — escalates to
//     rich for a single call regardless of config.
//  2. status.density in .gk.yaml ("rich" or "normal").
//  3. default "normal".
//
// Returns "rich" or "normal".
func statusDensity(cmd *cobra.Command, cfg *config.Config) string {
	if statusVerbose >= 1 {
		return "rich"
	}
	if cfg != nil {
		switch strings.ToLower(strings.TrimSpace(cfg.Status.Density)) {
		case "rich":
			return "rich"
		}
	}
	return "normal"
}

// boxStyle holds the four corners + sides used to draw a single-line
// titled box. The square style is the default; ASCII fallback can be
// added if a future TTY refuses Unicode line drawing.
var boxStyle = struct {
	tl, tr, bl, br string
	h, v           string
}{
	tl: "┌", tr: "┐",
	bl: "└", br: "┘",
	h: "─", v: "│",
}

// renderBox wraps the given content lines in a square box with a
// leading title slot. Width adapts to TTY (defaults to 80 when no TTY
// is present); content lines are padded to the inner width but not
// truncated — overflow is rare in status because each line was already
// wouldOverflow-checked upstream. NoColor is honoured automatically by
// the dim helper.
//
// Output format:
//
//	┌─ <title> ──────────────────┐
//	│ <line1>                    │
//	│ <line2>                    │
//	└────────────────────────────┘
//
// Title may be empty, in which case the top border is uninterrupted.
// Each line is rendered with a single-space gutter on the left and
// trailing padding to the right wall.
func renderBox(title string, lines []string) string {
	width := 80
	if w, ok := ui.TTYWidth(); ok && w > 20 {
		width = w
	}
	if width < 30 {
		// On very narrow TTYs the box itself becomes more visual noise
		// than the content. Render the lines inline with a faint label
		// instead.
		var b strings.Builder
		if title != "" {
			b.WriteString(color.New(color.Faint).Sprint(title + ":"))
			b.WriteString("\n")
		}
		for _, ln := range lines {
			b.WriteString(ln)
			b.WriteString("\n")
		}
		return b.String()
	}

	dim := color.New(color.Faint).SprintFunc()
	var b strings.Builder

	// Top border with title.
	top := boxStyle.tl + boxStyle.h
	if title != "" {
		top += " " + title + " "
	}
	pad := width - visibleWidth(top) - 1 // -1 for tr corner
	if pad < 0 {
		pad = 0
	}
	top += strings.Repeat(boxStyle.h, pad) + boxStyle.tr
	b.WriteString(dim(top))
	b.WriteString("\n")

	// Body. Inner width = total - 2 walls - 2 gutter spaces.
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	for _, ln := range lines {
		vis := visibleWidth(ln)
		var pad string
		if vis < inner {
			pad = strings.Repeat(" ", inner-vis)
		}
		b.WriteString(dim(boxStyle.v))
		b.WriteString(" ")
		b.WriteString(ln)
		b.WriteString(pad)
		b.WriteString(" ")
		b.WriteString(dim(boxStyle.v))
		b.WriteString("\n")
	}

	// Bottom border.
	bot := boxStyle.bl + strings.Repeat(boxStyle.h, width-2) + boxStyle.br
	b.WriteString(dim(bot))
	b.WriteString("\n")

	return b.String()
}

// renderNextActionBlock produces the highlighted next-step strip
// rendered between section boxes in rich mode. The bar is a horizontal
// rule above and below a single-or-two-line action body, mirroring the
// "do this next" call-out pattern of richer dashboards.
//
//	────────────────────────────────────────────
//	 →  next:  <command>
//	    why:   <one-line reason>
//	────────────────────────────────────────────
//
// `why` may be empty; in that case only the next line is shown. The
// arrow prefix uses magenta+bold to stand out from the box rules.
func renderNextActionBlock(next, why string) string {
	width := 80
	if w, ok := ui.TTYWidth(); ok && w > 20 {
		width = w
	}
	dim := color.New(color.Faint).SprintFunc()
	arrow := color.New(color.FgMagenta, color.Bold).Sprint("→")
	rule := dim(strings.Repeat("─", width))

	var b strings.Builder
	b.WriteString(rule)
	b.WriteString("\n")
	fmt.Fprintf(&b, " %s  %s  %s\n", arrow, dim("next:"), next)
	if why != "" {
		fmt.Fprintf(&b, "    %s   %s\n", dim("why:"), dim(why))
	}
	b.WriteString(rule)
	b.WriteString("\n")
	return b.String()
}
