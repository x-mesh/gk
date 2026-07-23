package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Raw sequences rather than the cli package's helpers — the point of these
// tests is that the renderer measures *escaped* text correctly.
const (
	tGreen    = "\x1b[32m"
	tResetFg  = "\x1b[39m"
	tFaint    = "\x1b[2m"
	tResetDim = "\x1b[22m"
)

// hasSplitEscape reports whether s contains an ESC that is not followed by a
// complete CSI sequence — the corruption an ANSI-blind truncate produces.
func hasSplitEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		if i+1 >= len(s) || s[i+1] != '[' {
			return true
		}
		closed := false
		for j := i + 2; j < len(s); j++ {
			c := s[j]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				closed = true
				break
			}
			if (c < '0' || c > '9') && c != ';' {
				return true
			}
		}
		if !closed {
			return true
		}
	}
	return false
}

// TestCellAllocWidth_ExcludesColourEscapes is the core of the whole
// renderer swap: a coloured cell must be allocated the columns it occupies
// on screen, not the bytes it takes in memory. Measuring the escapes cost
// `gk sw` two or three whole columns on a narrow terminal.
func TestCellAllocWidth_ExcludesColourEscapes(t *testing.T) {
	plain := "● mesh-project-sync"
	coloured := tGreen + "●" + tResetFg + " mesh-project-sync"
	if got, want := cellAllocWidth(coloured), cellAllocWidth(plain); got != want {
		t.Fatalf("coloured cell measured %d, plain %d — colour must not cost width", got, want)
	}
}

func TestBuildColumnsFromHeaders_SizesByVisibleWidth(t *testing.T) {
	items := []PickerItem{
		{Cells: []string{tGreen + "●" + tResetFg + " feat/x", tFaint + "(local)" + tResetDim}},
	}
	cols := buildColumnsFromHeaders(items, []string{"BRANCH", "UPSTREAM"})
	if cols[0].Width != lipgloss.Width("● feat/x") {
		t.Errorf("BRANCH width = %d, want %d (visible width)", cols[0].Width, lipgloss.Width("● feat/x"))
	}
	// "(local)" is 7 visible columns, under the 6-column floor's reach but
	// still shorter than the header, so the header wins at 8.
	if cols[1].Width != lipgloss.Width("UPSTREAM") {
		t.Errorf("UPSTREAM width = %d, want %d", cols[1].Width, lipgloss.Width("UPSTREAM"))
	}
}

// TestFitColumns_ColouredCellsKeepFittingColumns is the regression guard for
// the reported bug: on a terminal that comfortably holds every column, colour
// alone used to push the fitter into dropping them.
func TestFitColumns_ColouredCellsKeepFittingColumns(t *testing.T) {
	items := []PickerItem{{Cells: []string{
		tGreen + "●" + tResetFg + " worktree-fix-peer-connect-controls",
		"↑ origin/feat/peer-grid-snapshot",
		"abc1234",
		"2h",
	}}}
	headers := []string{"BRANCH", "UPSTREAM", "HASH", "AGE"}
	prio := []int{100, 40, 10, 80}
	cols := buildColumnsFromHeaders(items, headers)
	// 36 + 32 + 7 + 6 visible, plus 2 padding each = 91.
	const width = 100
	if got := fitColumns(colWidths(cols), prio, width); len(got) != 4 {
		t.Fatalf("all four columns fit in %d cols, fitter kept %v (widths %v)",
			width, got, colWidths(cols))
	}
}

func TestFitCell_PadsAndClipsToExactWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		w    int
	}{
		{"pad plain", "ab", 6},
		{"pad coloured", tGreen + "ab" + tResetFg, 6},
		{"exact", "abcdef", 6},
		{"clip plain", "abcdefghij", 6},
		{"clip coloured", tGreen + "abcdefghij" + tResetFg, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fitCell(tc.in, tc.w)
			if lipgloss.Width(got) != tc.w {
				t.Errorf("visible width = %d, want %d (%q)", lipgloss.Width(got), tc.w, got)
			}
			if hasSplitEscape(got) {
				t.Errorf("escape sequence was cut: %q", got)
			}
		})
	}
}

func TestTruncateVisible_MarksTheCutAndClosesColour(t *testing.T) {
	got := truncateVisible(tGreen+"abcdefghij"+tResetFg, 6)
	if !strings.Contains(got, "…") {
		t.Errorf("clipped cell should carry an ellipsis, got %q", got)
	}
	if !strings.Contains(got, tGreen) {
		t.Errorf("colour opening should survive, got %q", got)
	}
	// A clipped span must reset its own colour, or it bleeds into the
	// next cell on the row.
	if !strings.Contains(got, tResetFg) {
		t.Errorf("clipped cell should reset the foreground, got %q", got)
	}
	if lipgloss.Width(got) != 6 {
		t.Errorf("visible width = %d, want 6 (%q)", lipgloss.Width(got), got)
	}
}

func numberedRows(n int) []tableRow {
	rows := make([]tableRow, n)
	for i := range rows {
		rows[i] = tableRow{strings.Repeat("x", i%3+1)}
	}
	return rows
}

func TestTableView_ScrollWindowFollowsCursor(t *testing.T) {
	tv := newTableView([]tableColumn{{Title: "A", Width: 4}}, numberedRows(10), 3)
	tv.SetCursor(5)
	if tv.top != 3 {
		t.Fatalf("cursor 5 with 3 visible rows → top 3, got %d", tv.top)
	}
	// Header + rule + 3 rows.
	if lines := strings.Count(tv.View(), "\n") + 1; lines != 5 {
		t.Errorf("expected 5 rendered lines, got %d", lines)
	}
	tv.SetCursor(0)
	if tv.top != 0 {
		t.Errorf("cursor back at the top → top 0, got %d", tv.top)
	}
}

func TestTableView_KeyBindings(t *testing.T) {
	tv := newTableView([]tableColumn{{Title: "A", Width: 4}}, numberedRows(10), 4)
	cases := []struct {
		key  string
		want int
	}{
		{"down", 1},
		{"j", 2},
		{"G", 9},
		{"up", 8},
		{"g", 0},
		{"pgdown", 4},
		{"pgup", 0},
	}
	for _, tc := range cases {
		if !tv.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)}) &&
			!tv.handleKey(tea.KeyMsg{Type: keyTypeFor(tc.key)}) {
			t.Fatalf("key %q was not handled", tc.key)
		}
		if tv.Cursor() != tc.want {
			t.Fatalf("after %q cursor = %d, want %d", tc.key, tv.Cursor(), tc.want)
		}
	}
	if tv.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")}) {
		t.Error("unrelated key must fall through to the picker's own bindings")
	}
}

// keyTypeFor maps the named (non-rune) keys used above; rune keys return
// KeyRunes, which the caller already tried.
func keyTypeFor(name string) tea.KeyType {
	switch name {
	case "up":
		return tea.KeyUp
	case "down":
		return tea.KeyDown
	case "pgup":
		return tea.KeyPgUp
	case "pgdown":
		return tea.KeyPgDown
	case "home":
		return tea.KeyHome
	case "end":
		return tea.KeyEnd
	}
	return tea.KeyRunes
}

func TestTableView_SelectedRowSpansTheRow(t *testing.T) {
	tv := newTableView([]tableColumn{{Title: "A", Width: 4}, {Title: "B", Width: 4}}, numberedRows(3), 3)
	line := tv.rowLine(0, false)
	if lipgloss.Width(line) != tv.totalWidth() {
		t.Errorf("row width = %d, want %d", lipgloss.Width(line), tv.totalWidth())
	}
	if hasSplitEscape(line) {
		t.Errorf("row carries a broken escape: %q", line)
	}
}
