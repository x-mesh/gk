package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// tableView is a minimal, ANSI-aware table renderer — the replacement for
// bubbles/table inside the picker.
//
// Why not bubbles/table: it sizes and clips every cell with
// runewidth.Truncate, which counts escape bytes as visible columns. Feeding
// it coloured cells left two bad options — allocate the column by the
// ANSI-inflated length (columns then eat 10-25 screen columns of nothing,
// and the responsive fitter drops columns that would have fit), or allocate
// by the visible width and watch Truncate cut a cell mid-escape and smear
// colour across the row. This renderer measures with lipgloss.Width and
// clips with truncateVisible, so allocation == what the eye sees and escape
// sequences are never split.
//
// It deliberately keeps bubbles/table's method names (SetRows, SetColumns,
// Cursor, …) so the picker model reads the same as before the swap.
type tableView struct {
	cols []tableColumn
	rows []tableRow
	// cursor is the index into rows of the highlighted row.
	cursor int
	// height is how many DATA rows fit on screen at once; the header block
	// (titles + rule) is two more lines above them.
	height int
	// top is the first visible row — the scroll offset.
	top int
}

// tableColumn is a column header plus its allocation width, measured in
// VISIBLE columns (escape sequences excluded).
type tableColumn struct {
	Title string
	Width int
}

// tableRow is one row's cells, positionally matching the column slice.
// Cells may carry colour; every width decision ignores the escapes.
type tableRow []string

// headerRuleRune draws the line under the header, matching the single-line
// border bubbles/table used.
const headerRuleRune = "─"

// cellPadding is the one blank column on each side of every cell — the
// same gutter bubbles/table applied via Padding(0, 1), and the number
// fitColumns/distributeColumnWidths budget per column.
const cellPadding = 2

// Escape sequences that reset only what gk's cell helpers set. Full resets
// (\x1b[0m) would also clear the selected row's background mid-line.
const (
	seqResetFg   = "\x1b[39m"
	seqResetBold = "\x1b[22m"
)

var (
	tableHeaderStyle   = lipgloss.NewStyle().Bold(true)
	tableSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("231")).
				Background(lipgloss.Color("99")).
				Bold(true)
)

// newTableView builds a table showing visibleRows data rows at a time.
func newTableView(cols []tableColumn, rows []tableRow, visibleRows int) tableView {
	if visibleRows < 1 {
		visibleRows = 1
	}
	return tableView{cols: cols, rows: rows, height: visibleRows}
}

func (t *tableView) SetRows(rows []tableRow) {
	t.rows = rows
	if t.cursor > len(t.rows)-1 {
		t.cursor = len(t.rows) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
	t.clampScroll()
}

func (t *tableView) SetColumns(cols []tableColumn) { t.cols = cols }

func (t tableView) Columns() []tableColumn { return t.cols }

func (t tableView) Cursor() int { return t.cursor }

func (t *tableView) SetCursor(i int) {
	t.cursor = i
	t.clampCursor()
	t.clampScroll()
}

// SetHeight takes the height of the whole table block — header, rule and
// rows — and keeps the row count that leaves.
func (t *tableView) SetHeight(h int) {
	rows := h - 2
	if rows < 1 {
		rows = 1
	}
	t.height = rows
	t.clampScroll()
}

// SetWidth exists for symmetry with the previous bubbles/table call site.
// Column widths already carry the terminal width (reflowColumns fits them),
// so nothing here depends on it.
func (t *tableView) SetWidth(int) {}

// handleKey moves the cursor and reports whether the keystroke belonged to
// the table. The bindings mirror bubbles/table's defaults so muscle memory
// (and the pickers' own hotkeys, which get first refusal upstream) is
// unchanged.
func (t *tableView) handleKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "k":
		t.move(-1)
	case "down", "j":
		t.move(1)
	case "pgup", "b":
		t.move(-t.height)
	case "pgdown", "f":
		t.move(t.height)
	case "ctrl+u", "u":
		t.move(-t.height / 2)
	case "ctrl+d", "d":
		t.move(t.height / 2)
	case "home", "g":
		t.SetCursor(0)
	case "end", "G":
		t.SetCursor(len(t.rows) - 1)
	default:
		return false
	}
	return true
}

func (t *tableView) move(delta int) {
	if delta == 0 {
		delta = 1
	}
	t.cursor += delta
	t.clampCursor()
	t.clampScroll()
}

func (t *tableView) clampCursor() {
	if t.cursor >= len(t.rows) {
		t.cursor = len(t.rows) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
}

// clampScroll keeps the cursor inside the visible window and never scrolls
// past the last screenful.
func (t *tableView) clampScroll() {
	if t.height < 1 {
		t.height = 1
	}
	if t.cursor < t.top {
		t.top = t.cursor
	}
	if t.cursor >= t.top+t.height {
		t.top = t.cursor - t.height + 1
	}
	if maxTop := len(t.rows) - t.height; t.top > maxTop {
		t.top = maxTop
	}
	if t.top < 0 {
		t.top = 0
	}
}

func (t tableView) View() string {
	var b strings.Builder
	b.WriteString(tableHeaderStyle.Render(t.headerLine()))
	b.WriteString("\n")
	b.WriteString(strings.Repeat(headerRuleRune, t.totalWidth()))
	end := t.top + t.height
	if end > len(t.rows) {
		end = len(t.rows)
	}
	for i := t.top; i < end; i++ {
		b.WriteString("\n")
		b.WriteString(t.rowLine(i, i == t.cursor))
	}
	return b.String()
}

func (t tableView) totalWidth() int {
	w := 0
	for _, c := range t.cols {
		if c.Width > 0 {
			w += c.Width + cellPadding
		}
	}
	return w
}

func (t tableView) headerLine() string {
	var b strings.Builder
	for _, c := range t.cols {
		if c.Width <= 0 {
			continue
		}
		b.WriteString(" " + fitCell(c.Title, c.Width) + " ")
	}
	return b.String()
}

func (t tableView) rowLine(idx int, selected bool) string {
	var b strings.Builder
	for i, c := range t.cols {
		if c.Width <= 0 {
			continue
		}
		cell := ""
		if idx < len(t.rows) && i < len(t.rows[idx]) {
			cell = t.rows[idx][i]
		}
		b.WriteString(" " + fitCell(cell, c.Width) + " ")
	}
	if selected {
		return tableSelectedStyle.Render(b.String())
	}
	return b.String()
}

// fitCell renders s to exactly w visible columns — clipped when too wide,
// space-padded when too narrow — leaving any escape sequences intact.
func fitCell(s string, w int) string {
	if w <= 0 {
		return ""
	}
	vw := lipgloss.Width(s)
	if vw > w {
		return truncateVisible(s, w)
	}
	return s + strings.Repeat(" ", w-vw)
}

// clipToWidth shortens a one-line annotation to fit the terminal, leaving
// it untouched when it already does (or when the width is unknown). Unlike
// fitCell it never pads — trailing blanks on a hint line would only push
// the layout around.
func clipToWidth(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	return strings.TrimRight(truncateVisible(s, width), " ")
}

// truncateVisible clips s to w visible columns, appending "…" to mark the
// cut. Escape sequences pass through whole (they cost no width and must
// never be split), and a colour reset closes the clipped span so the
// truncation can't bleed into the next cell. The result is padded to
// exactly w so callers can concatenate cells without re-measuring.
func truncateVisible(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s + strings.Repeat(" ", w-lipgloss.Width(s))
	}
	var b strings.Builder
	limit := w - 1 // the ellipsis takes the last column
	used := 0
	inEscape := false
	sawEscape := false
	for _, r := range s {
		if inEscape {
			b.WriteRune(r)
			// CSI sequences end on a byte in @…~; letters cover every
			// sequence gk emits (colour, bold, faint, reset).
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		if r == '\x1b' {
			inEscape, sawEscape = true, true
			b.WriteRune(r)
			continue
		}
		rw := runewidth.RuneWidth(r)
		if used+rw > limit {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	if sawEscape {
		b.WriteString(seqResetFg + seqResetBold)
	}
	b.WriteString("…")
	used++
	if used < w {
		b.WriteString(strings.Repeat(" ", w-used))
	}
	return b.String()
}
