package ui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type promptTextModel struct {
	title     string
	input     textinput.Model
	cancelled bool
	submitted bool
}

func (m promptTextModel) Init() tea.Cmd { return textinput.Blink }

func (m promptTextModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyEnter:
			if strings.TrimSpace(m.input.Value()) == "" {
				return m, nil
			}
			m.submitted = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m promptTextModel) View() string {
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(m.title))
	b.WriteString("\n  ")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	b.WriteString(hint.Render("enter submit · esc cancel"))
	return b.String()
}

// PromptTextTUI presents a single-line text input. Returns the trimmed
// value on enter, ErrPickerAborted on ctrl+c/esc, ErrNonInteractive on
// non-TTY. Empty input is rejected (enter is a no-op until non-empty).
func PromptTextTUI(ctx context.Context, title, placeholder, initial string) (string, error) {
	if !IsTerminal() {
		return "", ErrNonInteractive
	}
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = ""
	in.CharLimit = 200
	in.Width = 60
	if initial != "" {
		in.SetValue(initial)
	}
	in.Focus()

	prog := tea.NewProgram(
		promptTextModel{title: title, input: in},
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
	)
	final, err := prog.Run()
	if err != nil {
		if ctx.Err() != nil {
			return "", ErrPickerAborted
		}
		return "", fmt.Errorf("prompt text: %w", err)
	}
	m := final.(promptTextModel)
	if m.cancelled || !m.submitted {
		return "", ErrPickerAborted
	}
	return strings.TrimSpace(m.input.Value()), nil
}
