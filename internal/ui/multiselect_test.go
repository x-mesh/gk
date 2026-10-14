package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func multiSelectFixture(n int, preview func([]string) string) tea.Model {
	items := make([]MultiSelectItem, n)
	for i := range items {
		items[i] = MultiSelectItem{Key: fmt.Sprint(i), Display: fmt.Sprintf("item-%02d", i)}
	}
	return multiSelectModel{title: "pick", items: items, selected: map[int]bool{}, preview: preview}
}

// TestMultiSelect_ScrollsListTallerThanTerminal keeps the title and the
// cursor row on screen: a list drawn whole would push both off the top.
func TestMultiSelect_ScrollsListTallerThanTerminal(t *testing.T) {
	for _, preview := range []func([]string) string{nil, func(sel []string) string { return fmt.Sprintf("%d selected", len(sel)) }} {
		m := multiSelectFixture(50, preview)
		m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
		view := m.View()
		if h := lipgloss.Height(view); h > 20-multiSelectTopMargin {
			t.Fatalf("view is %d lines; a 20-line terminal must keep %d rows free above it", h, multiSelectTopMargin)
		}
		if !strings.Contains(view, "pick") || !strings.Contains(view, "item-00") || strings.Contains(view, "item-49") || !strings.Contains(view, "↓") {
			t.Fatalf("first page should show the title, the top rows and a ↓ marker:\n%s", view)
		}
		for range 30 {
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		}
		view = m.View()
		if h := lipgloss.Height(view); h > 20 {
			t.Fatalf("scrolled view is %d lines", h)
		}
		if !strings.Contains(view, "item-30") || strings.Contains(view, "item-00") || !strings.Contains(view, "↑") || !strings.Contains(view, "pick") {
			t.Fatalf("after moving down 30 rows the cursor row and a ↑ marker must show:\n%s", view)
		}
	}
}

func TestMultiSelect_ShortListRendersWhole(t *testing.T) {
	m := multiSelectFixture(5, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	view := m.View()
	if !strings.Contains(view, "item-00") || !strings.Contains(view, "item-04") || strings.Contains(view, "more") {
		t.Fatalf("a list that fits must render every row and no marker:\n%s", view)
	}
}

// TestMultiSelect_DetailHeaderFitAndFollowCursor covers the cleanup layout: a
// column header, a detail box for the cursor row, and a reload that reorders
// the rows. The view must stay within the terminal, the detail must follow
// the cursor, and a reorder must keep the cursor on the same row.
func TestMultiSelect_DetailHeaderFitAndFollowCursor(t *testing.T) {
	items := make([]MultiSelectItem, 40)
	for i := range items {
		items[i] = MultiSelectItem{Key: fmt.Sprint(i), Display: fmt.Sprintf("item-%02d", i)}
	}
	reversed := make([]MultiSelectItem, len(items))
	for i, it := range items {
		reversed[len(items)-1-i] = it
	}
	var m tea.Model = multiSelectModel{
		title: "pick", items: items, selected: map[int]bool{}, header: "COLUMNS",
		detail:  func(key string) string { return "detail of " + key + "\nline 2\nline 3" },
		preview: func(sel []string) string { return fmt.Sprintf("%d selected", len(sel)) },
		extras: []MultiSelectExtraKey{{Key: "s", OnPress: func() ([]MultiSelectItem, map[string]bool, error) {
			return reversed, nil, nil
		}}},
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for range 5 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	view := m.View()
	if h := lipgloss.Height(view); h > 24-multiSelectTopMargin {
		t.Fatalf("view is %d lines; a 24-line terminal must keep %d rows free above it", h, multiSelectTopMargin)
	}
	if !strings.Contains(view, "COLUMNS") || !strings.Contains(view, "detail of 5") {
		t.Fatalf("header and the cursor row's detail must show:\n%s", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if view := m.View(); !strings.Contains(view, "detail of 5") || !strings.Contains(view, "item-05") {
		t.Fatalf("after a reorder the cursor must stay on item 5:\n%s", view)
	}
}
