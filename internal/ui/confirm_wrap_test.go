package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestConfirmView_WrapsDescription keeps every description line on screen:
// the terminal cuts a line wider than itself, and in the cleanup -i loss
// confirmation that cut the loss off behind a long worktree path.
func TestConfirmView_WrapsDescription(t *testing.T) {
	long := "/very/long/worktree/path/that/fills/the/line/agent-1234: could not check for uncommitted changes (git status: fatal: broken)"
	desc := long + "\nsecond line"
	m, _ := confirmModel{title: "remove?", desc: desc}.Update(tea.WindowSizeMsg{Width: 50, Height: 20})
	view := StripANSI(m.(confirmModel).View())
	lines := strings.Split(view, "\n")
	var body []string
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			break
		}
		if lipgloss.Width(l) > 50 {
			t.Errorf("line wider than the terminal: %q", l)
		}
		if !strings.HasPrefix(l, "  ") {
			t.Errorf("description line not indented: %q", l)
		}
		body = append(body, strings.TrimSpace(l))
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if got := squash(strings.Join(body, " ")); got != squash(desc) {
		t.Fatalf("description text lost in wrapping:\n got %q\nwant %q", got, squash(desc))
	}
}
