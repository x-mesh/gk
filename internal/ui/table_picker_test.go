package ui

import (
	"context"
	"strings"
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

func TestTablePicker_FilterIncludesHiddenFilterItems(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{
		{Key: "local:main", Display: "main"},
	})
	m.filterOnly = []PickerItem{
		{Key: "remote:origin/tmux", Display: "tmux remote: origin"},
	}
	m.filterActive = true
	m.filterInput.Focus()
	m.filterInput.SetValue("tmux")
	m.applyFilter()
	if len(m.items) != 1 {
		t.Fatalf("expected hidden remote filter match, got %d items", len(m.items))
	}
	if m.items[0].Key != "remote:origin/tmux" {
		t.Fatalf("expected remote item, got %q", m.items[0].Key)
	}
}

func TestTablePicker_FilterDedupesVisibleAndHiddenItems(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{
		{Key: "remote:origin/tmux", Display: "tmux remote: origin"},
	})
	m.filterOnly = []PickerItem{
		{Key: "remote:origin/tmux", Display: "tmux remote: origin"},
	}
	m.filterActive = true
	m.filterInput.Focus()
	m.filterInput.SetValue("tmux")
	m.applyFilter()
	if len(m.items) != 1 {
		t.Fatalf("expected duplicate remote to appear once, got %d items", len(m.items))
	}
}

func TestTablePicker_FilterKeepsMultipleKeylessHiddenItems(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{
		{Display: "local tmux"},
	})
	m.filterOnly = []PickerItem{
		{Display: "remote tmux one"},
		{Display: "remote tmux two"},
	}
	m.filterActive = true
	m.filterInput.Focus()
	m.filterInput.SetValue("tmux")
	m.applyFilter()
	if len(m.items) != 3 {
		t.Fatalf("expected keyless matches to be preserved, got %d items", len(m.items))
	}
	if m.items[1].Display != "remote tmux one" || m.items[2].Display != "remote tmux two" {
		t.Fatalf("unexpected keyless filter results: %+v", m.items)
	}
}

func TestTablePicker_InitialFilterSeedsNarrowedNavMode(t *testing.T) {
	p := &TablePicker{InitialFilter: "alp"}
	// Drive only the seeding logic Pick() performs, without a TTY.
	m := newTablePickerModelForTest([]PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	})
	m.filterInput.SetValue(p.InitialFilter)
	m.applyFilter()
	if m.filterActive {
		t.Fatal("seeded filter should land in nav mode (filterActive=false)")
	}
	if len(m.items) != 1 || m.items[0].Key != "a" {
		t.Fatalf("expected narrowed to alpha, got %d items", len(m.items))
	}
	// An exit-action key in nav mode must still carry the residual filter
	// out via chosenItem so the caller can re-seed.
	m.extras = []TablePickerExtraKey{{Key: "x", Exit: true}}
	got, _ := updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	got.chosenItem.FilterValue = got.filterInput.Value() // mirrors Pick()'s return path
	if got.chosenItem.FilterValue != "alp" {
		t.Fatalf("expected residual filter 'alp', got %q", got.chosenItem.FilterValue)
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

// Esc while typing a filter leaves typing mode but keeps the narrowed
// list, so action hotkeys can act on the filtered row.
func TestTablePicker_EscInFilterKeepsNarrowedList(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	})
	m.filterActive = true
	m.filterInput.Focus()
	m.filterInput.SetValue("alp")
	m.applyFilter()

	m, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.filterActive {
		t.Fatal("esc should leave typing mode (filterActive=false)")
	}
	if cmd != nil {
		t.Fatal("esc out of typing mode must not quit the picker")
	}
	if m.filterInput.Value() != "alp" {
		t.Fatalf("filter value should be retained, got %q", m.filterInput.Value())
	}
	if len(m.items) != 1 || m.items[0].Key != "a" {
		t.Fatalf("narrowed list should survive esc, got %+v", m.items)
	}
}

// After esc out of typing mode, an action hotkey fires on the filtered row.
func TestTablePicker_ActionHotkeyAfterFilterEsc(t *testing.T) {
	items := []PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	}
	m := newTablePickerWithExtras(items, []TablePickerExtraKey{
		{Key: "d", Help: "d delete", Exit: true},
	})
	m.filterActive = true
	m.filterInput.Focus()
	m.filterInput.SetValue("bet")
	m.applyFilter()
	m, _ = updateAs(m, tea.KeyMsg{Type: tea.KeyEsc})

	got, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if got.chosenItem.ExtraAction != "d" {
		t.Errorf("expected ExtraAction=d after filter esc, got %q", got.chosenItem.ExtraAction)
	}
	if got.chosenItem.Key != "b" {
		t.Errorf("action should target the filtered row, got Key=%q", got.chosenItem.Key)
	}
	if cmd == nil {
		t.Fatal("exit hotkey should quit the picker")
	}
}

// Staged escape: with a filter still applied, the first nav-mode esc clears
// the filter instead of aborting.
func TestTablePicker_EscClearsFilterBeforeAbort(t *testing.T) {
	m := newTablePickerModelForTest([]PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	})
	m.filterInput.SetValue("alp")
	m.applyFilter()

	// First esc (nav mode, filter set) → clear filter, stay open.
	m, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.aborted {
		t.Fatal("first esc should clear the filter, not abort")
	}
	if cmd != nil {
		t.Fatal("clearing the filter must not quit the picker")
	}
	if m.filterInput.Value() != "" {
		t.Fatalf("filter should be cleared, got %q", m.filterInput.Value())
	}
	if len(m.items) != 2 {
		t.Fatalf("full list should be restored, got %d items", len(m.items))
	}

	// Second esc (no filter) → abort.
	m, _ = updateAs(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.aborted {
		t.Fatal("second esc with no filter should abort")
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

// The reported bug: with the filter focused, a bare action letter was
// swallowed as filter text, so the action was unreachable without esc.
// The ctrl alias fires instead, and ExtraAction still reports the plain
// Key so callers keep one dispatch name.
func TestTablePicker_FilterAliasFiresWhileTyping(t *testing.T) {
	items := []PickerItem{
		{Key: "a", Display: "alpha"},
		{Key: "b", Display: "beta"},
	}
	m := newTablePickerWithExtras(items, []TablePickerExtraKey{
		{Key: "r", FilterKey: "ctrl+r", Help: "r remotes", Exit: true},
	})
	m.filterActive = true
	m.filterInput.Focus()

	got, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyCtrlR})
	if got.chosenItem.ExtraAction != "r" {
		t.Errorf("ctrl alias should fire in filter mode, got ExtraAction=%q",
			got.chosenItem.ExtraAction)
	}
	if cmd == nil {
		t.Error("Exit alias should quit the picker")
	}
	if v := got.filterInput.Value(); v != "" {
		t.Errorf("alias must not leak into the filter text, got %q", v)
	}
}

// The filter-mode help bar is the only place the aliases are advertised,
// so it must list them rather than just telling the user to press esc.
func TestTablePicker_FilterHelpAdvertisesAliases(t *testing.T) {
	m := newTablePickerWithExtras([]PickerItem{{Key: "a", Display: "alpha"}},
		[]TablePickerExtraKey{
			{Key: "r", FilterKey: "ctrl+r", Help: "r remotes", Exit: true},
			{Key: "n", Help: "n new", Exit: true}, // no alias → not advertised
		})
	m.filterActive = true

	view := m.View()
	if !strings.Contains(view, "ctrl+r remotes") {
		t.Errorf("filter help should advertise the alias, got:\n%s", view)
	}
	if strings.Contains(view, "ctrl+ new") || strings.Contains(view, "n new") {
		t.Errorf("alias-less extras must not appear in filter help, got:\n%s", view)
	}
}

// The plain letter must keep feeding the filter box — that is the whole
// reason the alias exists, so it must not regress into a hotkey.
func TestTablePicker_PlainKeyStillTypesWhileFiltering(t *testing.T) {
	items := []PickerItem{{Key: "a", Display: "alpha"}}
	m := newTablePickerWithExtras(items, []TablePickerExtraKey{
		{Key: "r", FilterKey: "ctrl+r", Help: "r remotes", Exit: true},
	})
	m.filterActive = true
	m.filterInput.Focus()

	got, _ := updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if got.chosenItem.ExtraAction != "" {
		t.Errorf("bare letter must not fire the action, got %q", got.chosenItem.ExtraAction)
	}
	if v := got.filterInput.Value(); v != "r" {
		t.Errorf("bare letter should reach the filter box, got %q", v)
	}
}

// In nav mode both forms work, so learning the ctrl form is not a
// one-way door back to the plain letter.
func TestTablePicker_AliasAlsoWorksInNavMode(t *testing.T) {
	items := []PickerItem{{Key: "a", Display: "alpha"}}
	m := newTablePickerWithExtras(items, []TablePickerExtraKey{
		{Key: "r", FilterKey: "ctrl+r", Help: "r remotes", Exit: true},
	})
	got, cmd := updateAs(m, tea.KeyMsg{Type: tea.KeyCtrlR})
	if got.chosenItem.ExtraAction != "r" || cmd == nil {
		t.Errorf("nav-mode alias should fire, got ExtraAction=%q cmd=%v",
			got.chosenItem.ExtraAction, cmd != nil)
	}
}

// A bare rune in FilterKey is a misconfiguration — honouring it would
// make that character impossible to type into the filter.
func TestTablePicker_PlainRuneAliasIgnoredInFilter(t *testing.T) {
	items := []PickerItem{{Key: "r", Display: "alpha"}}
	m := newTablePickerWithExtras(items, []TablePickerExtraKey{
		{Key: "r", FilterKey: "x", Help: "r remotes", Exit: true},
	})
	m.filterActive = true
	m.filterInput.Focus()

	got, _ := updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got.chosenItem.ExtraAction != "" {
		t.Errorf("plain-rune alias must not fire in filter mode, got %q",
			got.chosenItem.ExtraAction)
	}
	if v := got.filterInput.Value(); v != "x" {
		t.Errorf("plain-rune alias should stay filter text, got %q", v)
	}
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

// newResponsiveModelForTest builds a multi-column model wired for the
// responsive-drop path: ASCII-only cells keep content widths predictable so
// the WindowSizeMsg thresholds are deterministic.
func newResponsiveModelForTest(items []PickerItem, headers []string, prio map[string]int) tablePickerModel {
	colCount := len(headers)
	cols := make([]table.Column, colCount)
	for i := range cols {
		cols[i] = table.Column{Title: headers[i], Width: 8}
	}
	tbl := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(len(items)+1),
	)
	filter := textinput.New()
	filter.Prompt = ""
	return tablePickerModel{
		t: tbl, items: items, all: items, chosen: -1, filterInput: filter,
		headers: headers, priorityByHeader: prio, visibleCols: identityCols(colCount),
	}
}

// TestFitColumns_DropsLowestPriorityUntilFit pins the responsive-layout
// core: drop the lowest-weight column (rightmost on a tie) until the rest
// fit, never below one column; nil priority disables dropping.
func TestFitColumns_DropsLowestPriorityUntilFit(t *testing.T) {
	// Visible widths + padding(2): BRANCH 10, UPSTREAM 10, HASH 6, AGE 6 → 40.
	widths := []int{10, 10, 6, 6}
	prio := []int{100, 40, 10, 80} // BRANCH, UPSTREAM, HASH, AGE

	cases := []struct {
		name  string
		prio  []int
		total int
		want  []int
	}{
		{"wide keeps all", prio, 100, []int{0, 1, 2, 3}},
		{"drop HASH first", prio, 34, []int{0, 1, 3}},
		{"drop down to BRANCH+AGE", prio, 28, []int{0, 3}},
		{"tiny keeps BRANCH only", prio, 12, []int{0}},
		{"nil priority never drops", nil, 4, []int{0, 1, 2, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fitColumns(widths, tc.prio, tc.total); !equalInts(got, tc.want) {
				t.Errorf("fitColumns(total=%d) = %v, want %v", tc.total, got, tc.want)
			}
		})
	}
}

// TestFitColumns_TieDropsRightmost: equal priority → the rightmost column is
// sacrificed first, so the layout collapses from the right.
func TestFitColumns_TieDropsRightmost(t *testing.T) {
	widths := []int{10, 10, 10} // each 12 with padding
	prio := []int{50, 50, 50}
	if got := fitColumns(widths, prio, 24); !equalInts(got, []int{0, 1}) {
		t.Errorf("total=24 got %v, want [0 1] (drop rightmost)", got)
	}
	if got := fitColumns(widths, prio, 12); !equalInts(got, []int{0}) {
		t.Errorf("total=12 got %v, want [0]", got)
	}
}

// TestTablePicker_ResponsiveDropsAndRestores drives WindowSizeMsg through the
// model: AGE outranks UPSTREAM/HASH so it survives a narrow width, and
// widening restores every column.
func TestTablePicker_ResponsiveDropsAndRestores(t *testing.T) {
	items := []PickerItem{
		{Key: "develop", Cells: []string{"branchAAAA", "upstreamBB", "hashCC", "age"}},
		{Key: "main", Cells: []string{"branchDDDD", "upstreamEE", "hashFF", "age"}},
	}
	headers := []string{"BRANCH", "UPSTREAM", "HASH", "AGE"}
	prio := map[string]int{"BRANCH": 100, "UPSTREAM": 40, "HASH": 10, "AGE": 80}
	m := newResponsiveModelForTest(items, headers, prio)

	// Wide: every column visible, nothing hidden.
	got, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	m = got.(tablePickerModel)
	if !equalInts(m.visibleCols, []int{0, 1, 2, 3}) || m.hiddenCols != 0 {
		t.Fatalf("wide: visibleCols=%v hidden=%d, want all visible", m.visibleCols, m.hiddenCols)
	}

	// One column over budget → HASH (lowest weight) drops, AGE stays.
	got, _ = m.Update(tea.WindowSizeMsg{Width: 34, Height: 20})
	m = got.(tablePickerModel)
	if !equalInts(m.visibleCols, []int{0, 1, 3}) {
		t.Fatalf("medium: visibleCols=%v, want [0 1 3] (HASH dropped, AGE kept)", m.visibleCols)
	}
	if m.hiddenCols != 1 {
		t.Errorf("medium: hiddenCols=%d, want 1", m.hiddenCols)
	}

	// Narrow → collapse to BRANCH + AGE.
	got, _ = m.Update(tea.WindowSizeMsg{Width: 28, Height: 20})
	m = got.(tablePickerModel)
	if !equalInts(m.visibleCols, []int{0, 3}) {
		t.Fatalf("narrow: visibleCols=%v, want [0 3] (BRANCH+AGE)", m.visibleCols)
	}
	if m.hiddenCols != 2 {
		t.Errorf("narrow: hiddenCols=%d, want 2", m.hiddenCols)
	}
	// The hidden-column note renders beside the (absent) subtitle.
	if strIdx(m.View(), "+2 cols") < 0 {
		t.Errorf("narrow view missing hidden-cols note:\n%s", m.View())
	}

	// Widen back → all columns restored, note gone.
	got, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	m = got.(tablePickerModel)
	if !equalInts(m.visibleCols, []int{0, 1, 2, 3}) || m.hiddenCols != 0 {
		t.Fatalf("re-widen: visibleCols=%v hidden=%d, want all restored", m.visibleCols, m.hiddenCols)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
