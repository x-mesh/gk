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
	// TrackRef is the remote-tracking ref backing BranchName when the
	// user picked a remote-only branch. Non-empty means the local branch
	// does not exist yet and must be created tracking this ref.
	TrackRef string
}

// addStep enumerates the linear flow inside addModel.
type addStep int

const (
	stepCreateBranchToggle addStep = iota
	stepName
	stepBranch
	stepFromRef
	stepDone
)

// addModel collects the inputs needed by `git worktree add` in a single
// bubbletea program: create-branch toggle → name → branch → (optional)
// base ref. Each text input has its own Validate so the "submit on enter"
// path stays gated by m.input.Err == nil.
//
// The toggle comes first because it decides which of two different flows
// runs. Choosing "existing" quits immediately with pickBranch set: an
// existing branch is chosen from a picker (its own bubbletea program, so
// it cannot be nested here), and the worktree name is derived from that
// choice rather than invented up front.
type addModel struct {
	step       addStep
	name       textinput.Model
	branch     textinput.Model
	fromRef    textinput.Model
	head       string // resolved current branch, "HEAD" when detached/unknown
	create     bool   // create new branch?
	pickBranch bool   // user chose "existing" → caller runs the branch picker
	cancelled  bool
	width      int
}

// newAddModel builds the form. head is the invoking worktree's current
// branch; it is rendered into the from-field placeholder so the default
// base is visible instead of an opaque "blank = HEAD" — git bases new
// branches on HEAD and users routinely assume main. Empty head
// (detached, lookup failure) falls back to the literal "HEAD".
func newAddModel(head string) addModel {
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

	if strings.TrimSpace(head) == "" {
		head = "HEAD"
	}
	fromRef := textinput.New()
	fromRef.Placeholder = fmt.Sprintf("(blank = %s)  e.g. origin/main", head)
	fromRef.Prompt = "from  › "
	fromRef.CharLimit = 256
	fromRef.Width = 40
	// Validate: blank is allowed for fromRef.

	return addModel{
		step:    stepCreateBranchToggle,
		name:    name,
		branch:  branch,
		fromRef: fromRef,
		head:    head,
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
	case stepCreateBranchToggle:
		if !m.create {
			// Existing branch: nothing else can be asked here usefully.
			// Quit so the caller can run the branch picker, which then
			// supplies the name suggestion too.
			m.pickBranch = true
			m.step = stepDone
			return m, tea.Quit
		}
		m.step = stepName
		return m, m.name.Focus()
	case stepName:
		if m.name.Err != nil || strings.TrimSpace(m.name.Value()) == "" {
			return m, nil
		}
		m.name.Blur()
		// Sensible default: filepath.Base of the path-like name —
		// "feat/api" → "api". Only seed when empty so a back-edit
		// doesn't get clobbered.
		if strings.TrimSpace(m.branch.Value()) == "" {
			m.branch.SetValue(filepath.Base(strings.TrimSpace(m.name.Value())))
		}
		m.step = stepBranch
		return m, m.branch.Focus()
	case stepBranch:
		if m.branch.Err != nil || strings.TrimSpace(m.branch.Value()) == "" {
			return m, nil
		}
		m.branch.Blur()
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

	// Create-branch toggle
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
	if m.step == stepCreateBranchToggle {
		b.WriteString(hint.Render("←/→ or space to flip · enter to continue"))
	} else {
		b.WriteString(hint.Render("existing → pick from a list instead of typing"))
	}
	b.WriteString("\n\n")

	// Name
	if m.step >= stepName {
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
	}

	// Branch
	if m.step >= stepBranch {
		b.WriteString(m.branch.View())
		if m.branch.Err != nil && m.step == stepBranch {
			b.WriteString("\n  ")
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
				Render("✗ " + m.branch.Err.Error()))
		} else {
			// Only the create path reaches this step — the existing path
			// quits at the toggle and resolves its branch via the picker.
			b.WriteString("\n  ")
			b.WriteString(hint.Render("name of the new branch"))
		}
		b.WriteString("\n\n")
	}

	// From ref
	if m.step >= stepFromRef {
		b.WriteString(m.fromRef.View())
		b.WriteString("\n  ")
		b.WriteString(hint.Render(fmt.Sprintf("blank = %s (current); e.g. origin/main", m.head)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(hint.Render("enter next · esc cancel"))
	return b.String()
}

// runWorktreeAddTUI launches the addModel and returns the collected
// inputs. head labels the default base ref in the form (see
// newAddModel). Returns ErrAddCancelled when the user aborts.
func runWorktreeAddTUI(ctx context.Context, head string) (worktreeAddInputs, error) {
	prog := tea.NewProgram(
		newAddModel(head),
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
	if m.pickBranch {
		return worktreeAddInputs{}, errAddPickExisting
	}
	return worktreeAddInputs{
		Name:         strings.TrimSpace(m.name.Value()),
		CreateBranch: m.create,
		BranchName:   strings.TrimSpace(m.branch.Value()),
		FromRef:      strings.TrimSpace(m.fromRef.Value()),
	}, nil
}

// nameModel asks only for the worktree directory name, with the already
// chosen branch echoed above it for context. It exists as a separate
// program because the branch picker is itself a bubbletea program and the
// two cannot be nested.
//
// validate runs on submit rather than per keystroke: it resolves the
// managed path and stats it, which is too much work to repeat on every
// character typed.
type nameModel struct {
	input     textinput.Model
	branch    string
	detail    string // age / upstream summary of the chosen branch
	validate  func(string) error
	err       error
	cancelled bool
}

func newNameModel(branch, detail, suggestion string, validate func(string) error) nameModel {
	in := textinput.New()
	in.Prompt = "name   › "
	in.CharLimit = 256
	in.Width = 40
	in.SetValue(suggestion)
	in.CursorEnd()
	in.Focus()
	return nameModel{input: in, branch: branch, detail: detail, validate: validate}
}

func (m nameModel) Init() tea.Cmd { return textinput.Blink }

func (m nameModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyCtrlC:
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyEsc:
			// Esc steps back to the branch picker rather than abandoning
			// the whole flow — a mis-picked branch should cost one key.
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyEnter:
			if m.validate != nil {
				if err := m.validate(m.input.Value()); err != nil {
					m.err = err
					return m, nil
				}
			}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Any edit invalidates the last verdict; keeping it would leave a
	// stale "already exists" under a name the user just changed.
	m.err = nil
	return m, cmd
}

func (m nameModel) View() string {
	if m.cancelled {
		return ""
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("add worktree"))
	b.WriteString("\n\n")
	b.WriteString("  create › [ ] new branch   [x] existing\n")
	b.WriteString("  branch › " + m.branch)
	if m.detail != "" {
		b.WriteString("  " + hint.Render("("+m.detail+")"))
	}
	b.WriteString("\n\n")
	b.WriteString(m.input.View())
	b.WriteString("\n  ")
	if m.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
			Render("✗ " + m.err.Error()))
	} else {
		b.WriteString(hint.Render(m.branch + " 에서 따옴 · 그대로 쓰려면 enter"))
	}
	b.WriteString("\n\n")
	b.WriteString(hint.Render("enter 확정 · esc 브랜치 다시 고르기"))
	return b.String()
}

// runWorktreeNameTUI prompts for the worktree name of an already chosen
// branch. Returns errAddCancelled when the user backs out, which the
// caller reads as "return to the branch picker".
func runWorktreeNameTUI(ctx context.Context, branch, detail, suggestion string, validate func(string) error) (string, error) {
	prog := tea.NewProgram(
		newNameModel(branch, detail, suggestion, validate),
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
	)
	final, err := prog.Run()
	if err != nil {
		if ctx.Err() != nil {
			return "", errAddCancelled
		}
		return "", fmt.Errorf("name tui: %w", err)
	}
	m := final.(nameModel)
	if m.cancelled {
		return "", errAddCancelled
	}
	return strings.TrimSpace(m.input.Value()), nil
}

// errAddCancelled signals that the user aborted the add TUI.
var errAddCancelled = fmt.Errorf("add cancelled")

// errAddPickExisting signals that the user chose "existing branch" at the
// toggle, so the caller must run the branch picker and collect the
// worktree name from there.
var errAddPickExisting = fmt.Errorf("pick existing branch")
