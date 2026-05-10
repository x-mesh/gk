package cli

import (
	"strings"

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

// statusLayout resolves the rich-mode section framing. Defaults to bar
// when the config is missing or unknown — matches the documented
// fallback in ui.ParseSectionLayout.
func statusLayout(cfg *config.Config) ui.SectionLayout {
	if cfg == nil {
		return ui.SectionLayoutBar
	}
	return ui.ParseSectionLayout(cfg.Status.Layout)
}

// renderSection wraps ui.RenderSection with the project-wide Solarized
// palette (resolved via ui.SectionColor) and the configured layout.
// The optional summary slot lets each call site put a one-line
// headline next to the title; passing "" omits it.
//
// Section colouring is delegated to ui.SectionColor so other commands
// (`gk doctor`, `gk pull`, `gk merge`) can reuse the same palette
// without re-declaring colours — keeping "violet = divergence" or
// "mustard = working tree" consistent across the whole CLI.
func renderSection(title, summary string, lines []string, layout ui.SectionLayout) string {
	return ui.RenderSection(title, summary, lines, ui.SectionOpts{
		Layout: layout,
		Color:  ui.SectionColor(title),
	})
}

// renderNextAction is the layout-aware footer that closes a rich-mode
// status report. The next command is hoisted into the title's summary
// slot so the very first thing the eye lands on is the action; the
// optional `why` explanation moves into the body.
func renderNextAction(next, why string, layout ui.SectionLayout) string {
	return ui.RenderNextAction(next, why, ui.SectionOpts{
		Layout: layout,
		Color:  ui.SectionColor("next"),
	})
}
