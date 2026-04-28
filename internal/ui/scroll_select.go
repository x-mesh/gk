package ui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ScrollSelectOption is one keystroke-bound action attached to a
// ScrollSelectTUI screen. The Key must be a single character the user
// can press while the body has focus (e.g. "k", "e", "o"). When
// IsDefault is true, pressing Enter triggers this option — useful for
// the "happy path" choice (e.g. "keep" on a commit review).
type ScrollSelectOption struct {
	Key       string
	Value     string
	Display   string
	IsDefault bool
}

type scrollSelectModel struct {
	title     string
	vp        viewport.Model
	options   []ScrollSelectOption
	chosen    string
	cancelled bool
	ready     bool
	body      string
}

func (m scrollSelectModel) Init() tea.Cmd { return nil }

func (m scrollSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Match cancellation by KeyType *first* so partial escape
		// sequences in alt-screen mode can't slip past the string
		// match. KeyMsg.String() can return "" or a derived label
		// for synthetic events; KeyType is the authoritative field.
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyEnter:
			for _, opt := range m.options {
				if opt.IsDefault {
					m.chosen = opt.Value
					return m, tea.Quit
				}
			}
		}
		s := msg.String()
		if s == "ctrl+c" || s == "esc" || s == "escape" {
			m.cancelled = true
			return m, tea.Quit
		}
		for _, opt := range m.options {
			if s == opt.Key {
				m.chosen = opt.Value
				return m, tea.Quit
			}
		}
	case tea.WindowSizeMsg:
		// Reserve title (1) + spacer (1) + each option (1) + help (1)
		// + 2 rows for the viewport's box border.
		reserved := 4 + len(m.options)
		available := msg.Height - reserved
		if available < 5 {
			available = 5
		}
		contentLines := strings.Count(m.body, "\n")
		if !strings.HasSuffix(m.body, "\n") {
			contentLines++
		}
		if contentLines < 1 {
			contentLines = 1
		}
		h := contentLines
		if h > available {
			h = available
		}
		// Account for the border eating 2 columns of horizontal width.
		w := msg.Width - 2
		if w < 20 {
			w = 20
		}
		if !m.ready {
			m.vp = viewport.New(w, h)
			m.vp.SetContent(m.body)
			m.ready = true
		} else {
			m.vp.Width = w
			m.vp.Height = h
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m scrollSelectModel) View() string {
	if !m.ready {
		return ""
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("99"))

	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n")
	b.WriteString(box.Render(m.vp.View()))
	b.WriteString("\n")
	for _, opt := range m.options {
		label := "[" + opt.Key + "]"
		if opt.IsDefault {
			label = "[" + opt.Key + "/enter]"
		}
		b.WriteString("  ")
		b.WriteString(keyStyle.Render(label))
		b.WriteString(" " + opt.Display + "\n")
	}
	b.WriteString(hint.Render("j/k scroll · esc cancel"))
	return b.String()
}

// ScrollSelectTUI renders a scrollable body above a fixed list of
// keystroke-bound actions. Useful when the user needs to read context
// (a diff, a commit summary) before picking a strategy. Returns the
// chosen Value, or ErrPickerAborted on esc/ctrl+c.
func ScrollSelectTUI(ctx context.Context, title, body string, options []ScrollSelectOption) (string, error) {
	if !IsTerminal() {
		return "", ErrNonInteractive
	}
	if len(options) == 0 {
		return "", fmt.Errorf("scroll select: no options")
	}
	vp := viewport.New(80, 16)
	vp.SetContent(body)

	prog := tea.NewProgram(
		scrollSelectModel{title: title, vp: vp, body: body, options: options},
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
		tea.WithAltScreen(),
	)
	final, err := prog.Run()
	if err != nil {
		if ctx.Err() != nil {
			return "", ErrPickerAborted
		}
		return "", fmt.Errorf("scroll select: %w", err)
	}
	m := final.(scrollSelectModel)
	if m.cancelled {
		return "", ErrPickerAborted
	}
	return m.chosen, nil
}
