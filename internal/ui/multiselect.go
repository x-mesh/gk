package ui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// MultiSelectItem is one row in a MultiSelectTUI list. Key is returned
// to the caller for the chosen rows; Display is what's drawn.
type MultiSelectItem struct {
	Key     string
	Display string
}

// MultiSelectExtraKey binds a custom key to a callback that returns a
// fresh items list. Use it for actions that change *what's listed* —
// e.g. an "include remote" toggle. Currently-selected keys survive the
// reload (re-checked if still present).
type MultiSelectExtraKey struct {
	Key     string
	Help    string
	OnPress func() (items []MultiSelectItem, preselect map[string]bool, err error)
}

type multiSelectModel struct {
	title     string
	hint      string
	items     []MultiSelectItem
	cursor    int
	selected  map[int]bool
	cancelled bool
	extras    []MultiSelectExtraKey
	errMsg    string

	// height is the terminal height from the last WindowSizeMsg (0 until
	// one arrives). A list taller than the terminal scrolls from offset, so
	// the title and the cursor row stay on screen.
	height int
	offset int

	// detail, when set, describes the row under the cursor in the box under
	// the list, above the preview. previewTitle labels that box.
	detail       func(cursorKey string) string
	previewTitle string
	// header is a column header drawn right above the rows.
	header string

	// preview, when set, is re-rendered under the list on every toggle
	// with the currently-selected keys (in item order). It lets the user
	// SEE what a combination produces before committing — the wizard's
	// log/status layer pickers feed it sample command output.
	preview func(selected []string) string
}

// selectedKeys returns the checked keys in item order.
func (m multiSelectModel) selectedKeys() []string {
	out := make([]string, 0, len(m.selected))
	for i, it := range m.items {
		if m.selected[i] {
			out = append(out, it.Key)
		}
	}
	return out
}

func (m multiSelectModel) Init() tea.Cmd { return nil }

func (m multiSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	next.keepCursorVisible()
	return next, cmd
}

func (m multiSelectModel) update(msg tea.Msg) (multiSelectModel, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = ws.Height
		return m, nil
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "ctrl+c", "esc", "q":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case " ", "x":
			if m.selected[m.cursor] {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = true
			}
		case "a":
			// toggle all
			if len(m.selected) == len(m.items) {
				m.selected = map[int]bool{}
			} else {
				m.selected = make(map[int]bool, len(m.items))
				for i := range m.items {
					m.selected[i] = true
				}
			}
		case "enter":
			return m, tea.Quit
		default:
			for _, ex := range m.extras {
				if k.String() != ex.Key {
					continue
				}
				// Preserve the user's current selection across the
				// reload so toggling "include remote" doesn't drop
				// the boxes they already ticked.
				keep := map[string]bool{}
				for i, it := range m.items {
					if m.selected[i] {
						keep[it.Key] = true
					}
				}
				cursorKey := ""
				if m.cursor < len(m.items) {
					cursorKey = m.items[m.cursor].Key
				}
				newItems, newPre, err := ex.OnPress()
				if err != nil {
					m.errMsg = err.Error()
					return m, nil
				}
				if newPre == nil {
					newPre = map[string]bool{}
				}
				for k := range keep {
					newPre[k] = true
				}
				m.items = newItems
				m.selected = map[int]bool{}
				for i, it := range newItems {
					if newPre[it.Key] {
						m.selected[i] = true
					}
				}
				if m.cursor >= len(newItems) {
					m.cursor = 0
				}
				// A reload that only reorders (a sort) keeps the cursor on
				// the row it was on, not on whatever moved to its index.
				for i, it := range newItems {
					if it.Key == cursorKey {
						m.cursor = i
						break
					}
				}
				m.errMsg = ""
				return m, nil
			}
		}
	}
	return m, nil
}

// multiSelectTopMargin keeps rows free above the list. A terminal that pins
// the running command's header over the top of the screen hid the title and
// the first lines of a list that filled the whole height.
const multiSelectTopMargin = 3

// listRows is how many item rows fit the terminal, or 0 when the whole list
// fits (or the height is unknown) and renders without scrolling. It counts
// the lines View draws around the items: the title and a blank line, the
// preview box, a blank line and the help, an error line, and the two scroll
// markers.
func (m multiSelectModel) listRows() int {
	if m.height <= 0 {
		return 0
	}
	reserved := lipgloss.Height(m.title) + 3 + multiSelectTopMargin
	if m.header != "" {
		reserved++
	}
	if box, ok := m.boxContent(); ok {
		reserved += lipgloss.Height(box) + 4
	}
	if m.errMsg != "" {
		reserved++
	}
	avail := m.height - reserved
	if len(m.items) <= avail {
		return 0
	}
	return max(avail-2, 1)
}

// boxContent is what the box under the list shows: the cursor row's detail,
// then the preview of the selection. ok is false when neither is set.
func (m multiSelectModel) boxContent() (string, bool) {
	var parts []string
	if m.detail != nil && m.cursor < len(m.items) {
		parts = append(parts, m.detail(m.items[m.cursor].Key))
	}
	if m.preview != nil {
		parts = append(parts, m.preview(m.selectedKeys()))
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

func (m *multiSelectModel) keepCursorVisible() {
	rows := m.listRows()
	if rows == 0 {
		m.offset = 0
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	m.offset = max(min(m.offset, len(m.items)-rows), 0)
}

func (m multiSelectModel) View() string {
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("99")).Bold(true)

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(m.title))
	b.WriteString("\n\n")
	if m.header != "" {
		b.WriteString(hint.Render(m.header))
		b.WriteString("\n")
	}
	start, end := 0, len(m.items)
	rows := m.listRows()
	if rows > 0 {
		start, end = m.offset, min(m.offset+rows, len(m.items))
		b.WriteString(hint.Render(scrollMarker("↑", start)))
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		it := m.items[i]
		mark := "[ ]"
		if m.selected[i] {
			mark = "[x]"
		}
		line := mark + " " + it.Display
		if i == m.cursor {
			b.WriteString(sel.Render(" " + line + " "))
		} else {
			b.WriteString(" " + line + " ")
		}
		b.WriteString("\n")
	}
	if rows > 0 {
		b.WriteString(hint.Render(scrollMarker("↓", len(m.items)-end)))
		b.WriteString("\n")
	}
	if box, ok := m.boxContent(); ok {
		previewTitle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
		body := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238")).
			Padding(0, 1)
		label := m.previewTitle
		if label == "" {
			label = "미리보기"
		}
		b.WriteString("\n")
		b.WriteString(previewTitle.Render(label))
		b.WriteString("\n")
		b.WriteString(body.Render(box))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	helpLine := "↑/↓ navigate · space toggle · a toggle all · enter confirm · esc cancel"
	for _, ex := range m.extras {
		helpLine = ex.Help + " · " + helpLine
	}
	if m.hint != "" {
		helpLine = m.hint + " · " + helpLine
	}
	b.WriteString(hint.Render(helpLine))
	if m.errMsg != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
			Render("✗ " + m.errMsg))
	}
	return b.String()
}

// scrollMarker tells how many rows are hidden past one edge of a scrolled
// list; an empty line when none are.
func scrollMarker(arrow string, hidden int) string {
	if hidden <= 0 {
		return ""
	}
	return fmt.Sprintf(" %s %d more", arrow, hidden)
}

// MultiSelectTUI presents a checkbox list where the user toggles items
// with space, confirms with enter, and aborts with esc/ctrl+c. Returns
// the selected Keys (in original order) or ErrPickerAborted.
// preselect can pre-check items by Key. extras attach custom keys that
// can replace the items list on the fly (see MultiSelectExtraKey).
func MultiSelectTUI(ctx context.Context, title string, items []MultiSelectItem, preselect map[string]bool, extras ...MultiSelectExtraKey) ([]string, error) {
	return runMultiSelect(ctx, title, items, preselect, nil, extras...)
}

// MultiSelectPreviewTUI is MultiSelectTUI with a live preview pane: preview
// is called with the currently-selected keys after every toggle and its
// output is drawn under the list. Used by `gk config setup` to show what a
// log/status layer combination will actually look like.
func MultiSelectPreviewTUI(ctx context.Context, title string, items []MultiSelectItem, preselect map[string]bool, preview func(selected []string) string) ([]string, error) {
	return runMultiSelect(ctx, title, items, preselect, preview)
}

// MultiSelectOptions adds what MultiSelectWith shows besides the checkbox
// rows. Every field is optional.
type MultiSelectOptions struct {
	// Detail describes the row under the cursor, in the box under the list.
	Detail func(cursorKey string) string
	// Preview summarises the selection, in the same box under Detail.
	Preview func(selected []string) string
	// BoxTitle labels that box; "미리보기" when empty.
	BoxTitle string
	// Header is a column header drawn right above the rows, aligned with
	// Display after the 5-cell " [x] " prefix.
	Header string
	Extras []MultiSelectExtraKey
}

// MultiSelectWith is MultiSelectTUI with a detail line for the cursor row,
// a selection preview, and extra keys together.
func MultiSelectWith(ctx context.Context, title string, items []MultiSelectItem, preselect map[string]bool, opts MultiSelectOptions) ([]string, error) {
	return runMultiSelectModel(ctx, multiSelectModel{
		title: title, items: items, extras: opts.Extras, preview: opts.Preview,
		detail: opts.Detail, previewTitle: opts.BoxTitle, header: opts.Header,
	}, preselect)
}

func runMultiSelect(ctx context.Context, title string, items []MultiSelectItem, preselect map[string]bool, preview func(selected []string) string, extras ...MultiSelectExtraKey) ([]string, error) {
	return runMultiSelectModel(ctx, multiSelectModel{title: title, items: items, extras: extras, preview: preview}, preselect)
}

func runMultiSelectModel(ctx context.Context, model multiSelectModel, preselect map[string]bool) ([]string, error) {
	if !IsTerminal() {
		return nil, ErrNonInteractive
	}
	if len(model.items) == 0 && len(model.extras) == 0 {
		return nil, fmt.Errorf("no items to select")
	}
	model.selected = map[int]bool{}
	for i, it := range model.items {
		if preselect[it.Key] {
			model.selected[i] = true
		}
	}
	prog := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
	)
	final, err := prog.Run()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrPickerAborted
		}
		return nil, fmt.Errorf("multiselect: %w", err)
	}
	m := final.(multiSelectModel)
	if m.cancelled {
		return nil, ErrPickerAborted
	}
	return m.selectedKeys(), nil
}
