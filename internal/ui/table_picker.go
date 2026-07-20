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
	"github.com/mattn/go-runewidth"
)

// cellAllocWidth measures a cell for column-width allocation. We use
// runewidth.StringWidth (ANSI-blind — counts each escape byte as
// visible) instead of lipgloss.Width so the resulting column width is
// large enough that bubbles/table's runewidth.Truncate never fires
// mid-escape on coloured cells. The allocation is a few cells wider
// than the truly visible content; that slack is harmless and beats
// broken ANSI rendering.
func cellAllocWidth(s string) int { return runewidth.StringWidth(s) }

// TablePickerExtraKey binds a custom keystroke to a callback that
// can replace the entire data set on the fly — items + headers. Use
// it for actions that change *what's listed* (e.g. "g toggle global").
// Key is a plain keystroke and only fires while the filter prompt is
// *not* focused; filter typing always wins over bare letters.
//
// When Exit is true, OnPress is ignored: pressing the key quits the
// picker and surfaces the cursor row's PickerItem with ExtraAction set
// to Key. Use this for actions whose handlers need to leave the picker
// (open a confirm dialog, prompt for input) — the caller dispatches on
// ExtraAction and re-enters the picker on the next loop iteration.
type TablePickerExtraKey struct {
	Key string
	// FilterKey is an optional second keystroke for the same action that
	// fires *even while the filter prompt is focused*. It must be a
	// modifier combo ("ctrl+r", "alt+r") — a plain rune would be
	// indistinguishable from filter text and become untypable. Both forms
	// work in nav mode, and ExtraAction always reports Key, so callers
	// dispatch on one name regardless of which was pressed.
	//
	// This intentionally shadows any same-combo binding inside the filter
	// textinput (e.g. ctrl+f's cursor-forward): reaching the action beats
	// an emacs editing shortcut on a one-line filter box.
	FilterKey string
	Help      string
	OnPress   func() (items []PickerItem, headers []string, err error)
	Exit      bool
}

// extraKeyMatches reports whether s triggers ex in nav mode — either the
// plain keystroke or its modifier alias, so a user who learns the ctrl
// form never has to switch back.
func extraKeyMatches(ex TablePickerExtraKey, s string) bool {
	return s == ex.Key || (ex.FilterKey != "" && s == ex.FilterKey)
}

// firesInFilter reports whether s triggers ex while the filter prompt is
// focused. Only modifier combos qualify — binding a bare rune here would
// make that character impossible to type into the filter.
func firesInFilter(ex TablePickerExtraKey, s string) bool {
	if ex.FilterKey == "" || s != ex.FilterKey {
		return false
	}
	return strings.HasPrefix(s, "ctrl+") || strings.HasPrefix(s, "alt+")
}

// extraLabel strips the leading keystroke from Help ("r remotes" →
// "remotes") so the same wording can be re-prefixed with the alias when
// the filter-mode help line is rendered.
func extraLabel(ex TablePickerExtraKey) string {
	return strings.TrimSpace(strings.TrimPrefix(ex.Help, ex.Key))
}

// TablePicker is the default in-process bubbletea picker — no fzf,
// no external binary. Items with PickerItem.Cells render as
// multi-column rows; otherwise the row falls back to PickerItem.Display
// in a single column. Headers is optional — when shorter than the
// column count it is right-padded with empties.
//
// Subtitle, when non-empty, is rendered as a faint single line above
// the filter prompt — used for ambient context like "in worktree: X"
// that callers want visible while the picker is open.
//
// FilterItems are hidden from the unfiltered list but included when the
// user types a filter query. They are useful for large secondary groups
// that should not crowd the initial view but should still be discoverable
// by name.
type TablePicker struct {
	Headers     []string
	Height      int // 0 → auto (min(items+headers+1, 12))
	Extras      []TablePickerExtraKey
	Subtitle    string
	FilterItems []PickerItem
	// InitialFilter pre-seeds the filter query so the picker opens with
	// the list already narrowed (in nav mode, not typing mode). Callers
	// that re-enter the picker in a loop use it to restore the residual
	// filter recovered from the previous PickerItem.FilterValue.
	InitialFilter string
	// ColumnPriority assigns each column a keep-weight by its header title:
	// when the terminal is too narrow to show every column, the lowest-
	// weight columns are dropped *whole* (cleanly, never mid-character)
	// until the rest fit. Higher weight = kept longer; headers absent from
	// the map default to 0. At least the highest-weight column always
	// survives. Keying on the title (not the index) keeps priorities correct
	// when an ExtraKey toggle swaps the column layout — e.g. `gk wt`'s
	// global view reorders BRANCH and inserts PROJECT. nil keeps the legacy
	// behaviour — render every column and let the terminal hard-clip the
	// right edge — so callers that don't opt in are unaffected. A faint
	// "+N cols" note reports any drop.
	ColumnPriority map[string]int
}

type tablePickerModel struct {
	t            table.Model
	items        []PickerItem // visible rows after filtering
	all          []PickerItem // original list — kept verbatim for re-filter
	filterOnly   []PickerItem // hidden unless a filter query matches
	chosen       int          // index into items at the moment of selection
	chosenItem   PickerItem   // resolved item (so we don't have to re-index)
	aborted      bool
	width        int
	filterInput  textinput.Model
	filterActive bool // true while the user is typing into the filter box
	extras       []TablePickerExtraKey
	headers      []string
	errMsg       string
	subtitle     string
	// priorityByHeader maps a header title to its keep-weight (see
	// TablePicker.ColumnPriority). visibleCols holds the indices (into
	// headers / item Cells) currently shown, in left-to-right order — the
	// projection that lets a row drop a column without disturbing the
	// underlying PickerItem data. hiddenCols counts how many were dropped,
	// for the View() note.
	priorityByHeader map[string]int
	visibleCols      []int
	hiddenCols       int
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
				// Leave typing mode but keep the narrowed list, so the
				// action hotkeys (delete, new, …) operate on the filtered
				// result. The filter value is retained; a second esc (in
				// nav mode) clears it, a third cancels.
				m.filterInput.Blur()
				m.filterActive = false
				return m, nil
			case tea.KeyEnter:
				// Lock in whatever the cursor is on right now.
				if !m.selectCursorItem() {
					return m, nil
				}
				return m, tea.Quit
			case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
				var cmd tea.Cmd
				m.t, cmd = m.t.Update(msg)
				return m, cmd
			}
			// Modifier aliases reach their action without leaving typing
			// mode. Checked before the textinput sees the key so the
			// action wins over any editing binding on the same combo.
			for _, ex := range m.extras {
				if !firesInFilter(ex, msg.String()) {
					continue
				}
				cmd, handled := m.runExtra(ex)
				if !handled {
					continue
				}
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
		case "ctrl+c", "q":
			m.aborted = true
			return m, tea.Quit
		case "esc":
			// Staged escape: if a filter is still narrowing the list, the
			// first esc clears it and restores the full list; otherwise
			// esc cancels the picker.
			if m.filterInput.Value() != "" {
				m.filterInput.SetValue("")
				m.applyFilter()
				return m, nil
			}
			m.aborted = true
			return m, tea.Quit
		case "enter":
			if !m.selectCursorItem() {
				return m, nil
			}
			return m, tea.Quit
		case "/":
			m.filterActive = true
			m.filterInput.Focus()
			return m, nil
		default:
			s := msg.String()
			for _, ex := range m.extras {
				if !extraKeyMatches(ex, s) {
					continue
				}
				cmd, handled := m.runExtra(ex)
				if !handled {
					continue
				}
				return m, cmd
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
		// Re-derive columns from full content widths every resize: drop the
		// lowest-priority columns that don't fit, then expand the survivors
		// to fill. Widening restores dropped columns the same way.
		m.reflowColumns(msg.Width)
	}
	var cmd tea.Cmd
	m.t, cmd = m.t.Update(msg)
	return m, cmd
}

// runExtra executes ex and reports whether it consumed the keystroke.
// An Exit extra captures the cursor row (so handlers can act on THIS
// branch), tags it with ExtraAction and quits; an OnPress extra swaps
// the data set in place. A non-Exit extra with no callback is inert —
// (nil, false) lets the caller keep scanning for another binding.
func (m *tablePickerModel) runExtra(ex TablePickerExtraKey) (tea.Cmd, bool) {
	if ex.Exit {
		m.selectCursorItem()
		m.chosenItem.ExtraAction = ex.Key
		return tea.Quit, true
	}
	if ex.OnPress == nil {
		return nil, false
	}
	items, headers, err := ex.OnPress()
	if err != nil {
		m.errMsg = err.Error()
		return nil, true
	}
	m.errMsg = ""
	m.all = items
	if headers != nil {
		// New column structure: re-fit it to the current width (reflow
		// rebuilds rows too). With no size yet, width 0 makes fitColumns
		// keep every column.
		m.headers = headers
		m.reflowColumns(m.width)
	} else {
		m.applyFilter()
	}
	return nil, true
}

// applyFilter rebuilds the visible row list from the filter query.
// Empty query → everything; otherwise case-insensitive substring match
// on each cell (Display + every entry in Cells).
func (m *tablePickerModel) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filterInput.Value()))
	if q == "" {
		m.items = m.all
	} else {
		filtered := make([]PickerItem, 0, len(m.all)+len(m.filterOnly))
		seen := make(map[string]struct{}, len(m.all)+len(m.filterOnly))
		for _, source := range [][]PickerItem{m.all, m.filterOnly} {
			for _, it := range source {
				if itemMatchesFilter(it, q) {
					if it.Key != "" {
						if _, ok := seen[it.Key]; ok {
							continue
						}
						seen[it.Key] = struct{}{}
					}
					filtered = append(filtered, it)
				}
			}
		}
		m.items = filtered
	}
	vc := m.visibleCols
	if len(vc) == 0 {
		vc = identityCols(len(m.t.Columns()))
	}
	rows := make([]table.Row, len(m.items))
	for i, it := range m.items {
		row := make(table.Row, len(vc))
		for k, ci := range vc {
			row[k] = pickerCell(it, ci)
		}
		rows[i] = row
	}
	m.t.SetRows(rows)
	if len(rows) == 0 {
		return
	}
	if m.t.Cursor() < 0 || m.t.Cursor() >= len(rows) {
		m.t.SetCursor(0)
	}
}

func (m *tablePickerModel) selectCursorItem() bool {
	if len(m.items) == 0 {
		return false
	}
	cursor := m.t.Cursor()
	if cursor < 0 || cursor >= len(m.items) {
		cursor = 0
		m.t.SetCursor(cursor)
	}
	m.chosen = cursor
	m.chosenItem = m.items[cursor]
	return true
}

// buildColumnsFromHeaders rebuilds table.Column slice from a fresh
// header list, sized to fit the widest cell content per column. Used
// by ExtraKey callbacks that replace the column structure (e.g. local
// vs global modes that surface different column sets).
func buildColumnsFromHeaders(items []PickerItem, headers []string) []table.Column {
	colCount := len(headers)
	cols := make([]table.Column, colCount)
	for i := 0; i < colCount; i++ {
		w := cellAllocWidth(headers[i])
		for _, it := range items {
			if l := cellAllocWidth(pickerCell(it, i)); l > w {
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

// hiddenColsNote renders the "columns were dropped, widen to see them" hint
// shown beside the subtitle when responsive dropping has hidden columns.
func hiddenColsNote(n int) string {
	unit := "col"
	if n != 1 {
		unit = "cols"
	}
	return fmt.Sprintf("+%d %s · widen", n, unit)
}

// identityCols returns [0,1,…,n-1] — the no-projection column mapping used
// when responsive dropping is off or no width is known yet.
func identityCols(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// fitColumns decides which columns to show when the terminal can't hold
// them all. Starting from every column it drops the lowest-priority one
// (ties broken by dropping the rightmost) until the survivors — plus
// per-cell padding — fit `total`, never going below a single column.
//
// widths must be the *allocation* widths the renderer assigns each column
// (table.Column.Width). That is deliberately what bubbles/table pads every
// cell out to on screen, so allocation width == on-screen occupancy — the
// number that has to fit the terminal. Measuring instead by ANSI-stripped
// visible width would under-count (colour escapes inflate allocation) and
// fitColumns would keep columns the terminal then hard-clips anyway.
//
// priority[i] is column i's keep-weight (higher kept longer); indices past
// the slice count as 0. It returns the kept indices in their original
// left-to-right order. A nil priority disables dropping.
func fitColumns(widths []int, priority []int, total int) []int {
	keep := identityCols(len(widths))
	if priority == nil || total <= 0 || len(widths) <= 1 {
		return keep
	}
	const padding = 2
	used := func(idxs []int) int {
		s := 0
		for _, i := range idxs {
			s += widths[i] + padding
		}
		return s
	}
	prio := func(i int) int {
		if i >= 0 && i < len(priority) {
			return priority[i]
		}
		return 0
	}
	for len(keep) > 1 && used(keep) > total {
		drop := 0
		for k := 1; k < len(keep); k++ {
			pk, pd := prio(keep[k]), prio(keep[drop])
			// Lowest priority wins; on a tie the rightmost column goes first.
			if pk < pd || (pk == pd && keep[k] > keep[drop]) {
				drop = k
			}
		}
		keep = append(keep[:drop], keep[drop+1:]...)
	}
	return keep
}

// colWidths extracts the allocation widths from a column slice — the input
// fitColumns measures against.
func colWidths(cols []table.Column) []int {
	out := make([]int, len(cols))
	for i, c := range cols {
		out[i] = c.Width
	}
	return out
}

// resolvePriority projects priorityByHeader onto the current header order,
// returning the per-index weight slice fitColumns consumes. Returns nil when
// no priorities are configured, which leaves responsive dropping off.
func (m *tablePickerModel) resolvePriority() []int {
	if m.priorityByHeader == nil {
		return nil
	}
	prio := make([]int, len(m.headers))
	for i, h := range m.headers {
		prio[i] = m.priorityByHeader[h]
	}
	return prio
}

// reflowColumns re-derives the displayed columns for a terminal width:
// content-size every column from the full item set (so widths stay stable
// across filtering), drop the lowest-priority ones that don't fit, expand
// the survivors to fill, and rebuild the rows through the new projection.
func (m *tablePickerModel) reflowColumns(width int) {
	full := buildColumnsFromHeaders(m.all, m.headers)
	// Fit by the allocation widths the renderer will actually use — those
	// equal on-screen occupancy (bubbles pads each cell to col.Width).
	// Resolve each column's weight from its header title so a layout swap
	// (e.g. the global toggle) keeps priorities aligned to the right columns.
	m.visibleCols = fitColumns(colWidths(full), m.resolvePriority(), width)
	m.hiddenCols = len(full) - len(m.visibleCols)
	shown := make([]table.Column, len(m.visibleCols))
	for k, ci := range m.visibleCols {
		shown[k] = full[ci]
	}
	if width > 0 {
		shown = distributeColumnWidths(shown, width)
	}
	// Clear the rows before swapping columns: bubbles/table re-renders inside
	// SetColumns, and renderRow indexes m.cols by each row cell — so a stale
	// wide row against a freshly narrowed column set panics. Empty rows make
	// the swap safe in both directions; applyFilter then repopulates them
	// projected through the new visibleCols.
	m.t.SetRows(nil)
	m.t.SetColumns(shown)
	m.applyFilter()
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
	subtitleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Bold(true)
	var filterLine string
	if m.filterActive {
		filterLine = "filter: " + m.filterInput.View()
	} else if v := m.filterInput.Value(); v != "" {
		filterLine = hintStyle.Render("filter: " + v + "  (press / to edit)")
	} else {
		filterLine = hintStyle.Render("press / to filter")
	}
	var helpLine string
	if m.filterActive {
		// While typing, single-letter hotkeys feed the filter box. Any
		// action carrying a modifier alias still works, so advertise those
		// instead of only telling the user esc unlocks the plain letters.
		var aliases []string
		for _, ex := range m.extras {
			if ex.FilterKey == "" {
				continue
			}
			aliases = append(aliases, ex.FilterKey+" "+extraLabel(ex))
		}
		if len(aliases) > 0 {
			helpLine = strings.Join(aliases, " · ") +
				" · ↑/↓ navigate · enter select · esc nav mode · ctrl+c cancel"
		} else {
			helpLine = "↑/↓ navigate · enter select · esc → then action keys on results · ctrl+c cancel"
		}
	} else {
		if m.filterInput.Value() != "" {
			helpLine = "↑/↓ navigate · enter select · / edit filter · esc clear filter · q cancel"
		} else {
			helpLine = "↑/↓ navigate · enter select · / filter · esc/q cancel"
		}
		for _, ex := range m.extras {
			helpLine = ex.Help + " · " + helpLine
		}
	}
	help := hintStyle.Render(helpLine)
	out := ""
	if m.subtitle != "" || m.hiddenCols > 0 {
		var line string
		if m.subtitle != "" {
			line = subtitleStyle.Render("▸ " + m.subtitle)
		}
		if m.hiddenCols > 0 {
			note := hintStyle.Render(hiddenColsNote(m.hiddenCols))
			if line != "" {
				line += "  " + note
			} else {
				line = note
			}
		}
		out += line + "\n"
	}
	out += filterLine + "\n" + m.t.View() + "\n" + help
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
		w := cellAllocWidth(headers[i])
		for _, it := range items {
			if l := cellAllocWidth(pickerCell(it, i)); l > w {
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

	model := tablePickerModel{
		t:                t,
		items:            items,
		all:              items,
		filterOnly:       p.FilterItems,
		chosen:           -1,
		filterInput:      filter,
		extras:           p.Extras,
		headers:          headers,
		subtitle:         p.Subtitle,
		priorityByHeader: p.ColumnPriority,
		// Start 1:1 (all columns); the first WindowSizeMsg reflows to the
		// fitted set once the terminal width is known.
		visibleCols: identityCols(colCount),
	}
	// Pre-seed the filter so the picker opens already narrowed, in nav
	// mode (filterActive stays false) — the user can act on the filtered
	// rows immediately without pressing esc first.
	if p.InitialFilter != "" {
		model.filterInput.SetValue(p.InitialFilter)
		model.applyFilter()
	}

	prog := tea.NewProgram(
		model,
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
	if m.aborted {
		return PickerItem{}, ErrPickerAborted
	}
	// Exit-action hotkeys may fire with no row under cursor (chosen == -1
	// is the initial state, retained when items is empty). Surface them
	// anyway so callers like `n new branch` work in empty pickers.
	if m.chosen < 0 && m.chosenItem.ExtraAction == "" {
		return PickerItem{}, ErrPickerAborted
	}
	// Carry the residual filter out so a re-entering caller can re-seed it.
	m.chosenItem.FilterValue = m.filterInput.Value()
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
