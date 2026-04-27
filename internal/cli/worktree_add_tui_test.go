package cli

import (
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

func TestAddModel_NameRequiredBlocksAdvance(t *testing.T) {
	m := newAddModel()
	// Press enter on an empty name → must NOT advance.
	m = sendKey(m, tea.KeyEnter)
	if m.step != stepName {
		t.Fatalf("expected to remain on stepName, got %v", m.step)
	}
}

func TestAddModel_HappyPathCreateBranch(t *testing.T) {
	m := newAddModel()
	m = typeRunes(m, "feat/api")
	m = sendKey(m, tea.KeyEnter) // name → toggle
	if m.step != stepCreateBranchToggle {
		t.Fatalf("expected stepCreateBranchToggle, got %v", m.step)
	}
	m = sendKey(m, tea.KeyEnter) // toggle → branch (default seeded)
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

func TestAddModel_ExistingBranchSkipsFromRef(t *testing.T) {
	m := newAddModel()
	m = typeRunes(m, "wt-x")
	m = sendKey(m, tea.KeyEnter) // → toggle
	// Flip to existing-branch mode
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = got.(addModel)
	if m.create {
		t.Fatalf("expected create=false after space toggle")
	}
	m = sendKey(m, tea.KeyEnter) // toggle → branch
	m = typeRunes(m, "main")
	m = sendKey(m, tea.KeyEnter) // branch → done (skip fromRef)
	if m.step != stepDone {
		t.Fatalf("expected stepDone (fromRef skipped), got %v", m.step)
	}
	if m.fromRef.Value() != "" {
		t.Fatalf("expected fromRef empty when skipping, got %q", m.fromRef.Value())
	}
}

func TestAddModel_EscCancels(t *testing.T) {
	m := newAddModel()
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
	m := newAddModel()
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = got.(addModel)
	if !m.cancelled {
		t.Fatalf("expected cancelled=true after ctrl+c")
	}
}

func TestAddModel_BranchEmptyBlocks(t *testing.T) {
	m := newAddModel()
	m = typeRunes(m, "abc")
	m = sendKey(m, tea.KeyEnter) // → toggle
	// Flip to existing-branch (so we don't get the seeded default).
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = got.(addModel)
	m = sendKey(m, tea.KeyEnter) // → branch
	if m.step != stepBranch {
		t.Fatalf("expected stepBranch, got %v", m.step)
	}
	// Branch is empty → enter must not advance.
	m = sendKey(m, tea.KeyEnter)
	if m.step != stepBranch {
		t.Fatalf("expected to remain on stepBranch with empty value, got %v", m.step)
	}
}

func TestAddModel_FromRefAcceptsBlank(t *testing.T) {
	m := newAddModel()
	m = typeRunes(m, "x")
	m = sendKey(m, tea.KeyEnter) // → toggle
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
