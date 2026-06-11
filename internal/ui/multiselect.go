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
				m.errMsg = ""
				return m, nil
			}
		}
	}
	return m, nil
}

func (m multiSelectModel) View() string {
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("99")).Bold(true)

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(m.title))
	b.WriteString("\n\n")
	for i, it := range m.items {
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
	if m.preview != nil {
		previewTitle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
		body := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238")).
			Padding(0, 1)
		b.WriteString("\n")
		b.WriteString(previewTitle.Render("미리보기"))
		b.WriteString("\n")
		b.WriteString(body.Render(m.preview(m.selectedKeys())))
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

func runMultiSelect(ctx context.Context, title string, items []MultiSelectItem, preselect map[string]bool, preview func(selected []string) string, extras ...MultiSelectExtraKey) ([]string, error) {
	if !IsTerminal() {
		return nil, ErrNonInteractive
	}
	if len(items) == 0 && len(extras) == 0 {
		return nil, fmt.Errorf("no items to select")
	}
	pre := map[int]bool{}
	for i, it := range items {
		if preselect[it.Key] {
			pre[i] = true
		}
	}
	prog := tea.NewProgram(
		multiSelectModel{title: title, items: items, selected: pre, extras: extras, preview: preview},
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
