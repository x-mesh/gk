package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

// SummaryRow is a compact key/value row for verbose command output.
type SummaryRow struct {
	Key   string
	Value string
	Note  string
}

// ProgressBar renders a static Bubbles progress bar for one-shot CLI output.
func ProgressBar(percent float64, width int) string {
	if width <= 0 {
		width = 24
	}
	percent = clampPercent(percent)
	bar := progress.New(
		progress.WithWidth(width),
		progress.WithDefaultScaledGradient(),
	)
	return bar.ViewAs(percent)
}

// PlainProgressBar is the no-color counterpart to ProgressBar.
func PlainProgressBar(percent float64, width int) string {
	if width <= 0 {
		width = 24
	}
	percent = clampPercent(percent)
	percentText := fmt.Sprintf(" %3.0f%%", percent*100)
	barWidth := width - len(percentText)
	if barWidth < 1 {
		barWidth = 1
	}
	filled := int(math.Round(float64(barWidth) * percent))
	if filled > barWidth {
		filled = barWidth
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + percentText
}

// SummaryTable renders a quiet, ANSI-aware key/value block. It deliberately
// avoids borders so verbose output still behaves like normal command output.
func SummaryTable(rows []SummaryRow) string {
	return summaryTable(rows, true)
}

// PlainSummaryTable renders the same layout without style escape codes.
func PlainSummaryTable(rows []SummaryRow) string {
	return summaryTable(rows, false)
}

func summaryTable(rows []SummaryRow, styled bool) string {
	if len(rows) == 0 {
		return ""
	}

	keyWidth := 0
	valueWidth := 0
	for _, row := range rows {
		if w := lipgloss.Width(row.Key); w > keyWidth {
			keyWidth = w
		}
		if w := lipgloss.Width(row.Value); w > valueWidth {
			valueWidth = w
		}
	}

	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		key := fmt.Sprintf("%-*s", keyWidth, row.Key)
		if styled {
			key = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(key)
		}
		value := fmt.Sprintf("%-*s", valueWidth, row.Value)
		b.WriteString(key)
		b.WriteString("  ")
		b.WriteString(value)
		if row.Note != "" {
			b.WriteString("  ")
			if styled {
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(row.Note))
			} else {
				b.WriteString(row.Note)
			}
		}
	}
	return b.String()
}

func clampPercent(percent float64) float64 {
	if percent < 0 {
		return 0
	}
	if percent > 1 {
		return 1
	}
	return percent
}
