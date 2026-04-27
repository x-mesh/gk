package ui

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

func newTablePickerModelForTest(items []PickerItem) tablePickerModel {
	cols := []table.Column{{Title: "X", Width: 12}}
	rows := make([]table.Row, len(items))
	for i, it := range items {
		rows[i] = table.Row{pickerCell(it, 0)}
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(len(rows)+1),
	)
	return tablePickerModel{t: t, items: items, chosen: -1}
}

func TestTablePicker_EmptyItemsErrors(t *testing.T) {
	p := &TablePicker{}
	_, err := p.Pick(context.Background(), "title", nil)
	if err == nil {
		t.Fatal("expected error for empty items")
	}
}

func TestTablePicker_DownArrowMovesCursor(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	})
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(tablePickerModel)
	if m.t.Cursor() != 1 {
		t.Fatalf("expected cursor=1 after Down, got %d", m.t.Cursor())
	}
}

func TestTablePicker_EnterSelectsCurrentRow(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	})
	m, _ = updateAs(m, tea.KeyMsg{Type: tea.KeyDown})
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(tablePickerModel)
	if m.chosen != 1 {
		t.Fatalf("expected chosen=1, got %d", m.chosen)
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd (tea.Quit)")
	}
}

func TestTablePicker_EscAborts(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{{Key: "a", Display: "alpha"}})
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = got.(tablePickerModel)
	if !m.aborted {
		t.Fatal("expected aborted=true")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd (tea.Quit)")
	}
}

func TestTablePicker_CtrlCAborts(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{{Key: "a", Display: "alpha"}})
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = got.(tablePickerModel)
	if !m.aborted {
		t.Fatal("expected aborted=true on ctrl+c")
	}
}

func TestTablePicker_QAborts(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{{Key: "a", Display: "alpha"}})
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = got.(tablePickerModel)
	if !m.aborted {
		t.Fatal("expected aborted=true on q")
	}
}

func TestPickerCell_FallbackToDisplay(t *testing.T) {
	it := PickerItem{Display: "single"}
	if got := pickerCell(it, 0); got != "single" {
		t.Fatalf("expected 'single', got %q", got)
	}
	if got := pickerCell(it, 1); got != "" {
		t.Fatalf("expected empty for column 1, got %q", got)
	}
}

func TestPickerCell_UsesCellsWhenSet(t *testing.T) {
	it := PickerItem{Display: "ignore", Cells: []string{"a", "b", "c"}}
	for i, want := range []string{"a", "b", "c"} {
		if got := pickerCell(it, i); got != want {
			t.Fatalf("col %d: expected %q, got %q", i, want, got)
		}
	}
	if got := pickerCell(it, 3); got != "" {
		t.Fatalf("out-of-range column: expected empty, got %q", got)
	}
}

func TestDistributeColumnWidths_GivesSlackToWidest(t *testing.T) {
	cols := []table.Column{
		{Title: "A", Width: 5},
		{Title: "B", Width: 20},
		{Title: "C", Width: 8},
	}
	out := distributeColumnWidths(cols, 80)
	// Sum + padding = 5+20+8 + 2*3 = 39, slack = 41 → goes to col B
	if out[1].Width != 20+41 {
		t.Fatalf("expected col B width 61, got %d", out[1].Width)
	}
	if out[0].Width != 5 || out[2].Width != 8 {
		t.Fatalf("non-widest cols changed: %+v", out)
	}
}

func TestDistributeColumnWidths_NoSlack(t *testing.T) {
	cols := []table.Column{{Title: "A", Width: 50}}
	out := distributeColumnWidths(cols, 20)
	if out[0].Width != 50 {
		t.Fatalf("expected unchanged when total < sum, got %d", out[0].Width)
	}
}

// updateAs runs Update once and returns the typed model, panicking if
// the model type changes. Reduces test noise.
func updateAs(m tablePickerModel, msg tea.Msg) (tablePickerModel, tea.Cmd) {
	got, cmd := m.Update(msg)
	return got.(tablePickerModel), cmd
}
