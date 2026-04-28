package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TablePickerExtraKey binds a custom keystroke to a callback that
// can replace the entire data set on the fly — items + headers. Use
// it for actions that change *what's listed* (e.g. "g toggle global").
// The user must press the key while the filter prompt is *not*
// focused; filter typing always wins.
type TablePickerExtraKey struct {
	Key     string
	Help    string
	OnPress func() (items []PickerItem, headers []string, err error)
}

// TablePicker is a bubbletea-based replacement for FzfPicker. Items
// with PickerItem.Cells render as multi-column rows; otherwise the
// row falls back to PickerItem.Display in a single column. Headers
// is optional — when shorter than the column count it is right-padded
// with empties.
type TablePicker struct {
	Headers []string
	Height  int // 0 → auto (min(items+headers+1, 12))
	Extras  []TablePickerExtraKey
}

type tablePickerModel struct {
	t            table.Model
	items        []PickerItem // visible rows after filtering
	all          []PickerItem // original list — kept verbatim for re-filter
	chosen       int          // index into items at the moment of selection
	chosenItem   PickerItem   // resolved item (so we don't have to re-index)
	aborted      bool
	width        int
	filterInput  textinput.Model
	filterActive bool // true while the user is typing into the filter box
	extras       []TablePickerExtraKey
	headers      []string
	errMsg       string
}

func (m tablePickerModel) Init() tea.Cmd { return textinput.Blink }

func (m tablePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Filter mode is sticky — typing replaces table navigation
		// except for the few keys we explicitly forward (arrows, enter,
		// esc).
		if m.filterActive {
			switch msg.Type {
			case tea.KeyCtrlC:
				m.aborted = true
				return m, tea.Quit
			case tea.KeyEsc:
				// Clear the filter and exit filter mode.
				m.filterInput.SetValue("")
				m.filterInput.Blur()
				m.filterActive = false
				m.applyFilter()
				return m, nil
			case tea.KeyEnter:
				// Lock in whatever the cursor is on right now.
				if len(m.items) == 0 {
					return m, nil
				}
				m.chosen = m.t.Cursor()
				m.chosenItem = m.items[m.chosen]
				return m, tea.Quit
			case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
				var cmd tea.Cmd
				m.t, cmd = m.t.Update(msg)
				return m, cmd
			}
			// Forward everything else to the textinput, then refresh
			// the filtered row set.
			var cmd tea.Cmd
			m.filterInput, cmd = m.filterInput.Update(msg)
			m.applyFilter()
			return m, cmd
		}

		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.aborted = true
			return m, tea.Quit
		case "enter":
			if len(m.items) == 0 {
				return m, nil
			}
			m.chosen = m.t.Cursor()
			m.chosenItem = m.items[m.chosen]
			return m, tea.Quit
		case "/":
			m.filterActive = true
			m.filterInput.Focus()
			return m, nil
		default:
			s := msg.String()
			for _, ex := range m.extras {
				if s != ex.Key {
					continue
				}
				items, headers, err := ex.OnPress()
				if err != nil {
					m.errMsg = err.Error()
					return m, nil
				}
				m.errMsg = ""
				m.all = items
				if headers != nil {
					m.headers = headers
					m.t.SetColumns(buildColumnsFromHeaders(items, headers))
				}
				m.applyFilter()
				return m, nil
			}
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.t.SetWidth(msg.Width)
		// Leave room for filter line + help line + padding.
		h := msg.Height - 5
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

// applyFilter rebuilds the visible row list from the filter query.
// Empty query → everything; otherwise case-insensitive substring match
// on each cell (Display + every entry in Cells).
func (m *tablePickerModel) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filterInput.Value()))
	if q == "" {
		m.items = m.all
	} else {
		filtered := make([]PickerItem, 0, len(m.all))
		for _, it := range m.all {
			if itemMatchesFilter(it, q) {
				filtered = append(filtered, it)
			}
		}
		m.items = filtered
	}
	rows := make([]table.Row, len(m.items))
	colCount := len(m.t.Columns())
	for i, it := range m.items {
		row := make(table.Row, colCount)
		for j := 0; j < colCount; j++ {
			row[j] = pickerCell(it, j)
		}
		rows[i] = row
	}
	m.t.SetRows(rows)
	if m.t.Cursor() >= len(rows) {
		m.t.SetCursor(0)
	}
}

// buildColumnsFromHeaders rebuilds table.Column slice from a fresh
// header list, sized to fit the widest cell content per column. Used
// by ExtraKey callbacks that replace the column structure (e.g. local
// vs global modes that surface different column sets).
func buildColumnsFromHeaders(items []PickerItem, headers []string) []table.Column {
	colCount := len(headers)
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
	return cols
}

func itemMatchesFilter(it PickerItem, q string) bool {
	if strings.Contains(strings.ToLower(it.Display), q) {
		return true
	}
	for _, c := range it.Cells {
		if strings.Contains(strings.ToLower(c), q) {
			return true
		}
	}
	return false
}

func (m tablePickerModel) View() string {
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	var filterLine string
	if m.filterActive {
		filterLine = "filter: " + m.filterInput.View()
	} else if v := m.filterInput.Value(); v != "" {
		filterLine = hintStyle.Render("filter: " + v + "  (press / to edit)")
	} else {
		filterLine = hintStyle.Render("press / to filter")
	}
	helpLine := "↑/↓ navigate · enter select · / filter · esc/q cancel"
	for _, ex := range m.extras {
		helpLine = ex.Help + " · " + helpLine
	}
	help := hintStyle.Render(helpLine)
	out := filterLine + "\n" + m.t.View() + "\n" + help
	if m.errMsg != "" {
		out += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
			Render("✗ "+m.errMsg)
	}
	return out
}

// Pick renders the items as a bubbles/table and returns the selected
// PickerItem, or ErrPickerAborted on cancel. Output is forced to
// stderr so callers can capture the chosen value on stdout via the
// post-Run path.
func (p *TablePicker) Pick(ctx context.Context, title string, items []PickerItem) (PickerItem, error) {
	if len(items) == 0 {
		return PickerItem{}, errors.New("no items to pick")
	}
	if !IsTerminal() {
		// Non-TTY callers must use FallbackPicker explicitly; bubbletea
		// would block trying to open /dev/tty.
		return PickerItem{}, ErrNonInteractive
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

	filter := textinput.New()
	filter.Placeholder = "type to filter…"
	filter.Prompt = ""
	filter.CharLimit = 64
	filter.Width = 40

	prog := tea.NewProgram(
		tablePickerModel{
			t:           t,
			items:       items,
			all:         items,
			chosen:      -1,
			filterInput: filter,
			extras:      p.Extras,
			headers:     headers,
		},
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
	return m.chosenItem, nil
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

// distributeColumnWidths reflows column widths to fill the terminal.
// Slack is split *proportionally* to each column's current (content-
// derived) width so a wide BRANCH column also expands when UPSTREAM is
// the longest — instead of one column hoarding the slack. The last
// column absorbs any rounding remainder so widths sum exactly to total.
// Honours bubbles/table's per-cell padding.
func distributeColumnWidths(cols []table.Column, total int) []table.Column {
	if total <= 0 || len(cols) == 0 {
		return cols
	}
	const padding = 2
	out := make([]table.Column, len(cols))
	copy(out, cols)

	sum := 0
	for _, c := range out {
		sum += c.Width + padding
	}
	if sum >= total {
		return out
	}
	slack := total - sum

	weightSum := 0
	for _, c := range out {
		weightSum += c.Width
	}
	if weightSum == 0 {
		share := slack / len(out)
		for i := range out {
			out[i].Width += share
		}
		return out
	}
	given := 0
	for i := 0; i < len(out)-1; i++ {
		share := slack * out[i].Width / weightSum
		out[i].Width += share
		given += share
	}
	out[len(out)-1].Width += slack - given
	return out
}
