package ui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TablePicker is a bubbletea-based replacement for FzfPicker. Items
// with PickerItem.Cells render as multi-column rows; otherwise the
// row falls back to PickerItem.Display in a single column. Headers
// is optional — when shorter than the column count it is right-padded
// with empties.
type TablePicker struct {
	Headers []string
	Height  int // 0 → auto (min(items+headers+1, 12))
}

type tablePickerModel struct {
	t       table.Model
	items   []PickerItem
	chosen  int
	aborted bool
	width   int
}

func (m tablePickerModel) Init() tea.Cmd { return nil }

func (m tablePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.aborted = true
			return m, tea.Quit
		case "enter":
			m.chosen = m.t.Cursor()
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.t.SetWidth(msg.Width)
		// Leave room for the help line + padding.
		h := msg.Height - 4
		if h < 5 {
			h = 5
		}
		m.t.SetHeight(h)
		m.t.SetColumns(distributeColumnWidths(m.t.Columns(), msg.Width))
	}
	var cmd tea.Cmd
	m.t, cmd = m.t.Update(msg)
	return m, cmd
}

func (m tablePickerModel) View() string {
	help := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Render("↑/↓ navigate · enter select · esc/q cancel")
	return m.t.View() + "\n" + help
}

// Pick renders the items as a bubbles/table and returns the selected
// PickerItem, or ErrPickerAborted on cancel. Output is forced to
// stderr so callers can capture the chosen value on stdout via the
// post-Run path.
func (p *TablePicker) Pick(ctx context.Context, title string, items []PickerItem) (PickerItem, error) {
	if len(items) == 0 {
		return PickerItem{}, errors.New("no items to pick")
	}

	colCount := 1
	for _, it := range items {
		if n := len(it.Cells); n > colCount {
			colCount = n
		}
	}

	headers := make([]string, colCount)
	copy(headers, p.Headers)

	cols := make([]table.Column, colCount)
	for i := 0; i < colCount; i++ {
		w := lipgloss.Width(headers[i])
		for _, it := range items {
			if l := lipgloss.Width(pickerCell(it, i)); l > w {
				w = l
			}
		}
		if w < 6 {
			w = 6
		}
		cols[i] = table.Column{Title: headers[i], Width: w}
	}

	rows := make([]table.Row, len(items))
	for i, it := range items {
		row := make(table.Row, colCount)
		for j := 0; j < colCount; j++ {
			row[j] = pickerCell(it, j)
		}
		rows[i] = row
	}

	height := p.Height
	if height <= 0 {
		height = len(rows) + 1
		if height > 12 {
			height = 12
		}
	}

	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(height),
	)

	styles := table.DefaultStyles()
	styles.Header = styles.Header.
		Bold(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true)
	styles.Selected = styles.Selected.
		Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("99")).
		Bold(true)
	t.SetStyles(styles)

	prog := tea.NewProgram(
		tablePickerModel{t: t, items: items, chosen: -1},
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
	)
	final, err := prog.Run()
	if err != nil {
		// Context cancellation surfaces as a wrapped error here. Treat it
		// as an abort so callers using errors.Is(err, ErrPickerAborted)
		// keep working.
		if ctx.Err() != nil {
			return PickerItem{}, ErrPickerAborted
		}
		return PickerItem{}, fmt.Errorf("table picker: %w", err)
	}
	m := final.(tablePickerModel)
	if m.aborted || m.chosen < 0 {
		return PickerItem{}, ErrPickerAborted
	}
	return items[m.chosen], nil
}

func pickerCell(it PickerItem, idx int) string {
	if idx < len(it.Cells) {
		return it.Cells[idx]
	}
	if idx == 0 {
		return it.Display
	}
	return ""
}

// distributeColumnWidths reflows column widths to fill the terminal,
// giving the slack to the widest column (typically the path column).
// Honours bubbles/table's per-cell padding so the result stays inside
// the viewport.
func distributeColumnWidths(cols []table.Column, total int) []table.Column {
	if total <= 0 || len(cols) == 0 {
		return cols
	}
	const padding = 2
	sum := 0
	for _, c := range cols {
		sum += c.Width + padding
	}
	if sum >= total {
		return cols
	}
	idx := 0
	for i := range cols {
		if cols[i].Width > cols[idx].Width {
			idx = i
		}
	}
	out := make([]table.Column, len(cols))
	copy(out, cols)
	out[idx].Width += total - sum
	return out
}
