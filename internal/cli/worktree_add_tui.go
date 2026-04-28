package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// worktreeAddInputs is the result of a successful run of addModel.
type worktreeAddInputs struct {
	Name         string
	CreateBranch bool
	BranchName   string
	FromRef      string
}

// addStep enumerates the linear flow inside addModel.
type addStep int

const (
	stepName addStep = iota
	stepCreateBranchToggle
	stepBranch
	stepFromRef
	stepDone
)

// addModel collects the four inputs needed by `git worktree add` in a
// single bubbletea program: name → create-branch toggle → branch →
// (optional) base ref. Each text input has its own Validate so the
// "submit on enter" path stays gated by m.input.Err == nil.
type addModel struct {
	step      addStep
	name      textinput.Model
	branch    textinput.Model
	fromRef   textinput.Model
	create    bool // create new branch?
	cancelled bool
	width     int
}

func newAddModel() addModel {
	name := textinput.New()
	name.Placeholder = "feat/api"
	name.Prompt = "name › "
	name.CharLimit = 256
	name.Width = 40
	name.Validate = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("name is required")
		}
		return nil
	}
	name.Focus()

	branch := textinput.New()
	branch.Placeholder = "branch name"
	branch.Prompt = "branch › "
	branch.CharLimit = 256
	branch.Width = 40
	branch.Validate = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("branch is required")
		}
		return nil
	}

	fromRef := textinput.New()
	fromRef.Placeholder = "(blank = HEAD)  e.g. origin/main"
	fromRef.Prompt = "from  › "
	fromRef.CharLimit = 256
	fromRef.Width = 40
	// Validate: blank is allowed for fromRef.

	return addModel{
		step:    stepName,
		name:    name,
		branch:  branch,
		fromRef: fromRef,
		create:  true,
	}
}

func (m addModel) Init() tea.Cmd { return textinput.Blink }

func (m addModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyEnter:
			return m.advance()
		case tea.KeyTab:
			// On the create-branch toggle step, tab is also "next".
			if m.step == stepCreateBranchToggle {
				return m.advance()
			}
		case tea.KeySpace:
			if m.step == stepCreateBranchToggle {
				m.create = !m.create
				return m, nil
			}
		case tea.KeyLeft, tea.KeyRight:
			if m.step == stepCreateBranchToggle {
				m.create = !m.create
				return m, nil
			}
		}
	}

	// Forward keys to the focused textinput.
	var cmd tea.Cmd
	switch m.step {
	case stepName:
		m.name, cmd = m.name.Update(msg)
	case stepBranch:
		m.branch, cmd = m.branch.Update(msg)
	case stepFromRef:
		m.fromRef, cmd = m.fromRef.Update(msg)
	}
	return m, cmd
}

// advance moves to the next step when the current step is valid, or
// terminates the program when stepDone is reached.
func (m addModel) advance() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepName:
		if m.name.Err != nil || strings.TrimSpace(m.name.Value()) == "" {
			return m, nil
		}
		m.name.Blur()
		m.step = stepCreateBranchToggle
		return m, nil
	case stepCreateBranchToggle:
		if m.create {
			// Sensible default: filepath.Base of the path-like name —
			// "feat/api" → "api". Only seed when empty so a back-edit
			// doesn't get clobbered.
			if strings.TrimSpace(m.branch.Value()) == "" {
				m.branch.SetValue(filepath.Base(strings.TrimSpace(m.name.Value())))
			}
		}
		m.step = stepBranch
		return m, m.branch.Focus()
	case stepBranch:
		if m.branch.Err != nil || strings.TrimSpace(m.branch.Value()) == "" {
			return m, nil
		}
		m.branch.Blur()
		if !m.create {
			// Skip the base-ref step when checking out an existing branch.
			m.step = stepDone
			return m, tea.Quit
		}
		m.step = stepFromRef
		return m, m.fromRef.Focus()
	case stepFromRef:
		// Blank fromRef is fine.
		m.step = stepDone
		return m, tea.Quit
	}
	return m, nil
}

func (m addModel) View() string {
	if m.cancelled || m.step == stepDone {
		return ""
	}

	title := lipgloss.NewStyle().Bold(true).
		Render("add worktree")
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")

	// Name
	b.WriteString(m.name.View())
	if m.name.Err != nil && m.step == stepName {
		b.WriteString("\n  ")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
			Render("✗ " + m.name.Err.Error()))
	} else {
		b.WriteString("\n  ")
		b.WriteString(hint.Render("relative → managed base; absolute → used as-is"))
	}
	b.WriteString("\n\n")

	// Create-branch toggle
	if m.step >= stepCreateBranchToggle {
		toggle := "[ ] new branch   [x] existing"
		if m.create {
			toggle = "[x] new branch   [ ] existing"
		}
		caret := "  "
		if m.step == stepCreateBranchToggle {
			caret = "› "
		}
		b.WriteString(caret)
		b.WriteString("create › " + toggle)
		b.WriteString("\n  ")
		b.WriteString(hint.Render("←/→ or space to flip · enter to continue"))
		b.WriteString("\n\n")
	}

	// Branch
	if m.step >= stepBranch {
		b.WriteString(m.branch.View())
		if m.branch.Err != nil && m.step == stepBranch {
			b.WriteString("\n  ")
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
				Render("✗ " + m.branch.Err.Error()))
		} else {
			desc := "existing branch to check out"
			if m.create {
				desc = "name of the new branch"
			}
			b.WriteString("\n  ")
			b.WriteString(hint.Render(desc))
		}
		b.WriteString("\n\n")
	}

	// From ref (only when creating)
	if m.step >= stepFromRef && m.create {
		b.WriteString(m.fromRef.View())
		b.WriteString("\n  ")
		b.WriteString(hint.Render("blank = HEAD; e.g. origin/main"))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(hint.Render("enter next · esc cancel"))
	return b.String()
}

// runWorktreeAddTUI launches the addModel and returns the collected
// inputs. Returns ErrAddCancelled when the user aborts.
func runWorktreeAddTUI(ctx context.Context) (worktreeAddInputs, error) {
	prog := tea.NewProgram(
		newAddModel(),
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
	)
	final, err := prog.Run()
	if err != nil {
		if ctx.Err() != nil {
			return worktreeAddInputs{}, errAddCancelled
		}
		return worktreeAddInputs{}, fmt.Errorf("add tui: %w", err)
	}
	m := final.(addModel)
	if m.cancelled {
		return worktreeAddInputs{}, errAddCancelled
	}
	return worktreeAddInputs{
		Name:         strings.TrimSpace(m.name.Value()),
		CreateBranch: m.create,
		BranchName:   strings.TrimSpace(m.branch.Value()),
		FromRef:      strings.TrimSpace(m.fromRef.Value()),
	}, nil
}

// errAddCancelled signals that the user aborted the add TUI.
var errAddCancelled = fmt.Errorf("add cancelled")
