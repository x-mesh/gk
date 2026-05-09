package ui

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
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
	filter := textinput.New()
	filter.Prompt = ""
	return tablePickerModel{t: t, items: items, all: items, chosen: -1, filterInput: filter}
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

func TestTablePicker_FilterRestoresCursorAfterEmptyResult(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	})
	m.filterActive = true
	m.filterInput.Focus()
	m.filterInput.SetValue("zzz")
	m.applyFilter()
	if len(m.items) != 0 {
		t.Fatalf("expected no filtered items, got %d", len(m.items))
	}

	m.filterInput.SetValue("alp")
	m.applyFilter()
	got, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.chosen != 0 {
		t.Fatalf("expected chosen=0 after filter restores rows, got %d", got.chosen)
	}
	if got.chosenItem.Key != "a" {
		t.Fatalf("expected alpha to be selected, got %q", got.chosenItem.Key)
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

func TestDistributeColumnWidths_ProportionalSplit(t *testing.T) {
	cols := []table.Column{
		{Title: "A", Width: 5},
		{Title: "B", Width: 20},
		{Title: "C", Width: 8},
	}
	out := distributeColumnWidths(cols, 80)
	// Sum + padding = 5+20+8 + 2*3 = 39, slack = 41, weightSum = 33.
	// Shares: A = 41*5/33 = 6, B = 41*20/33 = 24. C absorbs the rest = 41-6-24 = 11.
	if out[0].Width != 5+6 {
		t.Fatalf("col A: expected 11, got %d", out[0].Width)
	}
	if out[1].Width != 20+24 {
		t.Fatalf("col B: expected 44, got %d", out[1].Width)
	}
	if out[2].Width != 8+11 {
		t.Fatalf("col C: expected 19, got %d", out[2].Width)
	}
	// Total widths + padding must equal the requested terminal width.
	got := 0
	for _, c := range out {
		got += c.Width + 2
	}
	if got != 80 {
		t.Fatalf("widths + padding != 80: %d", got)
	}
}

func TestDistributeColumnWidths_EvenSplitWhenAllZero(t *testing.T) {
	cols := []table.Column{
		{Title: "A", Width: 0},
		{Title: "B", Width: 0},
	}
	out := distributeColumnWidths(cols, 20)
	// Slack = 20 - 0 - 4 (padding) = 16, even split = 8 each.
	if out[0].Width != 8 || out[1].Width != 8 {
		t.Fatalf("expected 8/8, got %d/%d", out[0].Width, out[1].Width)
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

// --- Exit / ExtraAction / Subtitle (added by review fix) ---

func newTablePickerWithExtras(items []PickerItem, extras []TablePickerExtraKey) tablePickerModel {
	m := newTablePickerModelForTest(items)
	m.extras = extras
	m.all = items
	return m
}

func TestTablePicker_ExitHotkeyCarriesExtraAction(t *testing.T) {
	items := []PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	}
	m := newTablePickerWithExtras(items, []TablePickerExtraKey{
		{Key: "n", Help: "n new", Exit: true},
	})
	got, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if got.chosenItem.ExtraAction != "n" {
		t.Errorf("expected ExtraAction=n, got %q", got.chosenItem.ExtraAction)
	}
	if got.chosenItem.Key != "a" {
		t.Errorf("ExtraAction should carry the cursor row, got Key=%q", got.chosenItem.Key)
	}
	if cmd == nil {
		t.Errorf("Exit hotkey should return tea.Quit cmd")
	}
}

func TestTablePicker_ExitHotkeyOnEmptyListStillFires(t *testing.T) {
	// Critical edge case: `n new branch` must work even when picker
	// is in the empty placeholder state — the contract is "Exit
	// quits regardless of items".
	m := newTablePickerWithExtras(nil, []TablePickerExtraKey{
		{Key: "n", Help: "n new", Exit: true},
	})
	got, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if got.chosenItem.ExtraAction != "n" {
		t.Errorf("Exit on empty must still set ExtraAction, got %q",
			got.chosenItem.ExtraAction)
	}
	if cmd == nil {
		t.Errorf("Exit on empty should return tea.Quit cmd")
	}
}

func TestTablePicker_NonExitHotkeyRunsOnPress(t *testing.T) {
	pressed := false
	items := []PickerItem{{Key: "a", Display: "alpha"}}
	m := newTablePickerWithExtras(items, []TablePickerExtraKey{
		{Key: "r", Help: "r toggle",
			OnPress: func() ([]PickerItem, []string, error) {
				pressed = true
				return items, nil, nil
			}},
	})
	got, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !pressed {
		t.Errorf("OnPress should have fired")
	}
	if got.chosenItem.ExtraAction != "" {
		t.Errorf("non-Exit OnPress must NOT set ExtraAction, got %q",
			got.chosenItem.ExtraAction)
	}
	if cmd != nil {
		t.Errorf("non-Exit OnPress must NOT quit; got cmd %v", cmd)
	}
}

func TestTablePicker_SubtitleRenderedAboveFilter(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{{Key: "a", Display: "alpha"}})
	m.subtitle = "on: main · hidden: 2 remote (r)"
	view := m.View()
	// Subtitle line must come before the filter line; "▸ " marker is
	// the contract for visibility on dark terminals.
	subIdx := strIdx(view, "▸ on: main")
	filterIdx := strIdx(view, "press / to filter")
	if subIdx < 0 {
		t.Fatalf("subtitle missing; got view: %q", view)
	}
	if filterIdx < 0 {
		t.Fatalf("filter line missing; got view: %q", view)
	}
	if subIdx > filterIdx {
		t.Errorf("subtitle should appear before filter; subIdx=%d filterIdx=%d",
			subIdx, filterIdx)
	}
}

func TestTablePicker_NoSubtitleHidesLine(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{{Key: "a", Display: "alpha"}})
	view := m.View()
	if strIdx(view, "▸ ") >= 0 {
		t.Errorf("empty subtitle must not render the marker line; got %q", view)
	}
}

func strIdx(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
