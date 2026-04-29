package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// newPromptModel constructs a model the same way PromptTextTUI does so
// tests exercise the real Update/View paths without spinning up a
// bubbletea program.
func newPromptModel(initial string) promptTextModel {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 200
	in.Width = 60
	if initial != "" {
		in.SetValue(initial)
	}
	in.Focus()
	return promptTextModel{title: "test", input: in}
}

func TestPromptTextModel_EnterEmptyIsNoOp(t *testing.T) {
	t.Parallel()
	m := newPromptModel("")
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := out.(promptTextModel)
	if got.submitted {
		t.Errorf("Enter on empty input must NOT submit")
	}
	if got.cancelled {
		t.Errorf("Enter on empty input must NOT cancel")
	}
}

func TestPromptTextModel_EnterWithValueSubmits(t *testing.T) {
	t.Parallel()
	m := newPromptModel("feat/x")
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := out.(promptTextModel)
	if !got.submitted {
		t.Errorf("Enter with value should submit")
	}
	if cmd == nil {
		t.Errorf("Enter submit should return tea.Quit cmd")
	}
}

func TestPromptTextModel_EnterTrimsWhitespace(t *testing.T) {
	t.Parallel()
	m := newPromptModel("  feat/x  ")
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := out.(promptTextModel)
	if !got.submitted {
		t.Fatalf("expected submission")
	}
	// Whitespace-only is rejected (len(TrimSpace)==0 path).
	m2 := newPromptModel("    ")
	out2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got2 := out2.(promptTextModel)
	if got2.submitted {
		t.Errorf("whitespace-only input must NOT submit")
	}
}

func TestPromptTextModel_EscCancels(t *testing.T) {
	t.Parallel()
	m := newPromptModel("typed")
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := out.(promptTextModel)
	if !got.cancelled {
		t.Errorf("Esc should cancel")
	}
	if cmd == nil {
		t.Errorf("Esc should return tea.Quit cmd")
	}
}

func TestPromptTextModel_CtrlCCancels(t *testing.T) {
	t.Parallel()
	m := newPromptModel("typed")
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := out.(promptTextModel)
	if !got.cancelled {
		t.Errorf("Ctrl+C should cancel")
	}
	if cmd == nil {
		t.Errorf("Ctrl+C should return tea.Quit cmd")
	}
}

func TestPromptTextModel_ViewIncludesTitleAndHint(t *testing.T) {
	t.Parallel()
	m := newPromptModel("")
	view := m.View()
	if !strings.Contains(view, "test") {
		t.Errorf("view should include the title, got %q", view)
	}
	if !strings.Contains(view, "enter submit") || !strings.Contains(view, "esc cancel") {
		t.Errorf("view should include the help hint, got %q", view)
	}
}

func TestPromptTextTUI_NonTTYReturnsErrNonInteractive(t *testing.T) {
	t.Parallel()
	// In `go test` the stdin/out aren't a TTY so IsTerminal() is false.
	// PromptTextTUI must short-circuit with ErrNonInteractive instead
	// of trying to spin up bubbletea (which would block).
	v, err := PromptTextTUI(t.Context(), "title", "placeholder", "")
	if err != ErrNonInteractive {
		t.Errorf("expected ErrNonInteractive, got %v (value %q)", err, v)
	}
}
