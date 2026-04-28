package ui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type confirmModel struct {
	title     string
	desc      string
	value     bool
	cancelled bool
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "y", "Y":
			m.value = true
			return m, tea.Quit
		case "n", "N":
			m.value = false
			return m, tea.Quit
		case "left", "h", "right", "l", "tab":
			m.value = !m.value
		case "enter":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m confirmModel) View() string {
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("99")).Bold(true)

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(m.title))
	if m.desc != "" {
		b.WriteString("\n  ")
		b.WriteString(hint.Render(m.desc))
	}
	b.WriteString("\n\n  ")

	yes, no := "[ Yes ]", "[ No ]"
	if m.value {
		b.WriteString(sel.Render(yes))
	} else {
		b.WriteString(yes)
	}
	b.WriteString("   ")
	if !m.value {
		b.WriteString(sel.Render(no))
	} else {
		b.WriteString(no)
	}
	b.WriteString("\n\n")
	b.WriteString(hint.Render("y/n · ←/→ · enter · esc cancel"))
	return b.String()
}

// ConfirmTUI presents a yes/no choice. Returns ErrPickerAborted on
// ctrl+c/esc and ErrNonInteractive when stdout/stdin aren't a TTY.
// On non-TTY, the caller decides (e.g. honour --yes flag).
func ConfirmTUI(ctx context.Context, title, desc string, defaultYes bool) (bool, error) {
	if !IsTerminal() {
		return defaultYes, ErrNonInteractive
	}
	prog := tea.NewProgram(
		confirmModel{title: title, desc: desc, value: defaultYes},
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
	)
	final, err := prog.Run()
	if err != nil {
		if ctx.Err() != nil {
			return false, ErrPickerAborted
		}
		return false, fmt.Errorf("confirm: %w", err)
	}
	m := final.(confirmModel)
	if m.cancelled {
		return false, ErrPickerAborted
	}
	return m.value, nil
}
