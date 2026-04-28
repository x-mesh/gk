package ui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type editTextModel struct {
	title     string
	desc      string
	ta        textarea.Model
	saved     bool
	cancelled bool
	ready     bool
	width     int
	height    int
}

func (m editTextModel) Init() tea.Cmd { return textarea.Blink }

func (m editTextModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyCtrlD:
			// Submit on ctrl+d so plain Enter remains a newline (the
			// natural multi-line editing affordance).
			m.saved = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Reserve title (1) + desc (1) + spacer (1) + help (1) +
		// 2 rows of border around the textarea.
		const reserved = 6
		h := msg.Height - reserved
		if h < 4 {
			h = 4
		}
		w := msg.Width - 4 // border + small horizontal margin
		if w < 20 {
			w = 20
		}
		m.ta.SetWidth(w)
		m.ta.SetHeight(h)
		m.ready = true
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m editTextModel) View() string {
	if m.saved || m.cancelled || !m.ready {
		return ""
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("99"))

	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title))
	if m.desc != "" {
		b.WriteString("\n")
		b.WriteString(hint.Render(m.desc))
	}
	b.WriteString("\n")
	b.WriteString(box.Render(m.ta.View()))
	b.WriteString("\n")
	b.WriteString(hint.Render("ctrl+d save · esc cancel · enter newline"))
	return b.String()
}

// EditTextTUI opens a multi-line textarea pre-populated with initial,
// returns the edited value when the user presses ctrl+d, or
// ErrPickerAborted on esc/ctrl+c. width/height pin the editor's visible
// size — pass zero for sensible defaults.
func EditTextTUI(ctx context.Context, title, desc, initial string, width, height int) (string, error) {
	if !IsTerminal() {
		return "", ErrNonInteractive
	}
	ta := textarea.New()
	ta.SetValue(initial)
	if width > 0 {
		ta.SetWidth(width)
	} else {
		ta.SetWidth(72)
	}
	if height > 0 {
		ta.SetHeight(height)
	} else {
		ta.SetHeight(8)
	}
	ta.ShowLineNumbers = false
	ta.Focus()

	prog := tea.NewProgram(
		editTextModel{title: title, desc: desc, ta: ta},
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
		return "", fmt.Errorf("edit text: %w", err)
	}
	m := final.(editTextModel)
	if m.cancelled {
		return "", ErrPickerAborted
	}
	return m.ta.Value(), nil
}
