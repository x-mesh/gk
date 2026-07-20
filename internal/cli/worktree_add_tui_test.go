package cli

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sendKey(m addModel, key tea.KeyType) addModel {
	got, _ := m.Update(tea.KeyMsg{Type: key})
	return got.(addModel)
}

func typeRunes(m addModel, s string) addModel {
	for _, r := range s {
		got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = got.(addModel)
	}
	return m
}

// toName walks the form from its first step (the create toggle) into the
// name input, which is where most of these tests want to start.
func toName(m addModel) addModel { return sendKey(m, tea.KeyEnter) }

// The toggle leads because it selects between two different flows — the
// existing-branch path never reaches the name/branch/from inputs.
func TestAddModel_StartsOnCreateToggle(t *testing.T) {
	m := newAddModel("")
	if m.step != stepCreateBranchToggle {
		t.Fatalf("expected the form to open on the toggle, got %v", m.step)
	}
	if !m.create {
		t.Fatalf("default should stay 'new branch' so existing users are unaffected")
	}
}

func TestAddModel_NameRequiredBlocksAdvance(t *testing.T) {
	m := toName(newAddModel(""))
	// Press enter on an empty name → must NOT advance.
	m = sendKey(m, tea.KeyEnter)
	if m.step != stepName {
		t.Fatalf("expected to remain on stepName, got %v", m.step)
	}
}

func TestAddModel_HappyPathCreateBranch(t *testing.T) {
	m := toName(newAddModel(""))
	m = typeRunes(m, "feat/api")
	m = sendKey(m, tea.KeyEnter) // name → branch (default seeded)
	if m.step != stepBranch {
		t.Fatalf("expected stepBranch, got %v", m.step)
	}
	if !strings.HasPrefix(m.branch.Value(), "api") {
		t.Fatalf("expected branch seeded from filepath.Base, got %q", m.branch.Value())
	}
	m = sendKey(m, tea.KeyEnter) // branch → fromRef
	if m.step != stepFromRef {
		t.Fatalf("expected stepFromRef, got %v", m.step)
	}
	m = sendKey(m, tea.KeyEnter) // fromRef (blank ok) → done
	if m.step != stepDone {
		t.Fatalf("expected stepDone after fromRef enter, got %v", m.step)
	}
}

// Choosing "existing" must hand off immediately: the branch comes from a
// picker (its own bubbletea program), not from this form.
func TestAddModel_ExistingExitsToPicker(t *testing.T) {
	m := newAddModel("")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = got.(addModel)
	if m.create {
		t.Fatalf("expected create=false after space toggle")
	}
	m, cmd := func() (addModel, tea.Cmd) {
		g, c := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		return g.(addModel), c
	}()
	if !m.pickBranch {
		t.Fatalf("expected pickBranch=true so the caller runs the picker")
	}
	if cmd == nil {
		t.Fatalf("expected the program to quit and hand off")
	}
	if m.name.Value() != "" || m.branch.Value() != "" {
		t.Fatalf("existing path must not invent a name/branch, got name=%q branch=%q",
			m.name.Value(), m.branch.Value())
	}
}

// runWorktreeAddTUI translates that hand-off into a sentinel the caller
// can branch on, distinct from an outright cancel.
func TestAddModel_PickBranchSentinelIsNotCancel(t *testing.T) {
	if errors.Is(errAddPickExisting, errAddCancelled) {
		t.Fatal("hand-off must not be mistaken for a cancel")
	}
}

func TestAddModel_EscCancels(t *testing.T) {
	m := toName(newAddModel(""))
	m = typeRunes(m, "abc")
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = got.(addModel)
	if !m.cancelled {
		t.Fatalf("expected cancelled=true after esc")
	}
	if cmd == nil {
		t.Fatalf("expected non-nil cmd (tea.Quit)")
	}
}

func TestAddModel_CtrlCCancels(t *testing.T) {
	m := newAddModel("")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = got.(addModel)
	if !m.cancelled {
		t.Fatalf("expected cancelled=true after ctrl+c")
	}
}

func TestAddModel_BranchEmptyBlocks(t *testing.T) {
	m := toName(newAddModel(""))
	m = typeRunes(m, "abc")
	m = sendKey(m, tea.KeyEnter) // → branch (seeded from name)
	if m.step != stepBranch {
		t.Fatalf("expected stepBranch, got %v", m.step)
	}
	// Clearing the seeded value must re-block the step.
	m.branch.SetValue("")
	m = sendKey(m, tea.KeyEnter)
	if m.step != stepBranch {
		t.Fatalf("expected to remain on stepBranch with empty value, got %v", m.step)
	}
}

func TestAddModel_HeadLabelsFromPlaceholder(t *testing.T) {
	m := newAddModel("bug-fix")
	if !strings.Contains(m.fromRef.Placeholder, "blank = bug-fix") {
		t.Fatalf("placeholder should name the current branch, got %q", m.fromRef.Placeholder)
	}
	// Walk to the fromRef step and check the hint line names it too.
	m = toName(m)
	m = typeRunes(m, "x")
	m = sendKey(m, tea.KeyEnter) // → branch (default seeded)
	m = sendKey(m, tea.KeyEnter) // → fromRef
	if !strings.Contains(m.View(), "blank = bug-fix (current)") {
		t.Fatalf("fromRef hint should name the current branch, view:\n%s", m.View())
	}
}

func TestAddModel_EmptyHeadFallsBackToHEAD(t *testing.T) {
	m := newAddModel("")
	if !strings.Contains(m.fromRef.Placeholder, "blank = HEAD") {
		t.Fatalf("empty head should fall back to HEAD, got %q", m.fromRef.Placeholder)
	}
}

func TestAddModel_FromRefAcceptsBlank(t *testing.T) {
	m := toName(newAddModel(""))
	m = typeRunes(m, "x")
	m = sendKey(m, tea.KeyEnter) // → branch (default seeded)
	m = sendKey(m, tea.KeyEnter) // → fromRef
	if m.step != stepFromRef {
		t.Fatalf("expected stepFromRef, got %v", m.step)
	}
	m = sendKey(m, tea.KeyEnter)
	if m.step != stepDone {
		t.Fatalf("blank fromRef should advance to done, got %v", m.step)
	}
}

// --- nameModel ---

func sendNameKey(m nameModel, msg tea.KeyMsg) (nameModel, tea.Cmd) {
	got, cmd := m.Update(msg)
	return got.(nameModel), cmd
}

func TestNameModel_SeedsSuggestionAndSubmits(t *testing.T) {
	m := newNameModel("feat/relay-agent-notify", "7h", "relay-agent-notify", nil)
	if m.input.Value() != "relay-agent-notify" {
		t.Fatalf("expected the suggestion pre-filled, got %q", m.input.Value())
	}
	got, cmd := sendNameKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.cancelled || cmd == nil {
		t.Fatalf("enter on a valid name should submit, cancelled=%v", got.cancelled)
	}
}

// A rejected name keeps the prompt open — the user edits in place rather
// than restarting the flow.
func TestNameModel_InvalidNameStaysOpen(t *testing.T) {
	boom := errors.New("/tmp/x already exists and is not empty")
	m := newNameModel("feat/x", "", "x", func(string) error { return boom })
	got, cmd := sendNameKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("invalid name must not quit the prompt")
	}
	if got.err == nil || !strings.Contains(got.View(), "already exists") {
		t.Fatalf("the reason should be shown inline, view:\n%s", got.View())
	}
	// Typing clears the stale verdict so it can't outlive the name it judged.
	got, _ = sendNameKey(got, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if got.err != nil {
		t.Fatalf("editing should clear the previous error, got %v", got.err)
	}
}

// Esc means "wrong branch", not "abandon everything" — the caller loops
// back to the picker.
func TestNameModel_EscBacksOut(t *testing.T) {
	m := newNameModel("feat/x", "", "x", nil)
	got, cmd := sendNameKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !got.cancelled || cmd == nil {
		t.Fatalf("esc should cancel this prompt, cancelled=%v", got.cancelled)
	}
}

func TestNameModel_ShowsChosenBranchForConfirmation(t *testing.T) {
	m := newNameModel("feat/relay-agent-notify", "7h · ↑ origin/feat/relay-agent-notify", "relay-agent-notify", nil)
	view := m.View()
	if !strings.Contains(view, "feat/relay-agent-notify") {
		t.Fatalf("the chosen branch must stay visible, view:\n%s", view)
	}
	if !strings.Contains(view, "7h") {
		t.Fatalf("age is the recall signal — it must show, view:\n%s", view)
	}
}
