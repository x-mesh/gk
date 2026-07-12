package cli

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

func zoomTestModel() fleetModel {
	return fleetModel{
		cmd:      &cobra.Command{},
		interval: time.Second,
		entries: []fleetEntryJSON{
			{Path: "/wt/a", Branch: "develop", Status: "clean"},
			{Path: "/wt/b", Branch: "feat/auth", Status: "dirty"},
			{Path: "/wt/c", Branch: "broken", Status: "error"},
		},
		width:  100,
		height: 40,
	}
}

func keyMsg(s string) tea.KeyMsg {
	if s == "esc" {
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestFleetZoomOpen covers entering the zoom: the embedded model is armed
// (embedded, no self-driven timers — the returned cmd is the one refresh),
// the cursor follows the target, and the breadcrumb names branch + position.
func TestFleetZoomOpen(t *testing.T) {
	m := zoomTestModel()
	model, cmd := m.openZoom("/wt/b")
	fm := model.(fleetModel)

	if fm.zoom == nil || fm.zoomPath != "/wt/b" {
		t.Fatalf("zoom not opened: %+v", fm.zoomPath)
	}
	if !fm.zoom.embedded {
		t.Error("zoom model not marked embedded")
	}
	if cmd == nil {
		t.Error("openZoom returned no initial refresh cmd")
	}
	if fm.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (parked on the zoom target)", fm.cursor)
	}

	crumb := fm.zoomBreadcrumb()
	for _, want := range []string{"feat/auth", "(2/2)", "esc back"} {
		if !strings.Contains(crumb, want) {
			t.Errorf("breadcrumb missing %q in:\n%s", want, crumb)
		}
	}
	if !strings.Contains(fm.View(), "feat/auth") {
		t.Error("zoomed View missing the breadcrumb")
	}
}

// TestFleetZoomNeighbor: '[' / ']' walk the visible, non-error targets and
// wrap; a lone target reports nowhere to go.
func TestFleetZoomNeighbor(t *testing.T) {
	m := zoomTestModel()
	model, _ := m.openZoom("/wt/b")
	fm := model.(fleetModel)

	// Error entries are not zoom targets: 2 targets, so +1 from b wraps to a.
	if path, ok := fm.zoomNeighbor(1); !ok || path != "/wt/a" {
		t.Errorf("neighbor(+1) = %q, %v; want /wt/a", path, ok)
	}
	if path, ok := fm.zoomNeighbor(-1); !ok || path != "/wt/a" {
		t.Errorf("neighbor(-1) = %q, %v; want /wt/a", path, ok)
	}

	solo := fm
	solo.entries = fm.entries[1:2] // only /wt/b
	if _, ok := solo.zoomNeighbor(1); ok {
		t.Error("lone target should have no neighbor")
	}
}

// TestFleetZoomKeys: esc pops back to the table, q quits the whole program,
// and a stale changeFrameMsg (from a replaced target) is dropped while a
// current one lands in the embedded feed.
func TestFleetZoomKeys(t *testing.T) {
	m := zoomTestModel()
	model, _ := m.openZoom("/wt/b")
	fm := model.(fleetModel)

	popped, _ := fm.Update(keyMsg("esc"))
	if pm := popped.(fleetModel); pm.zoom != nil || pm.zoomPath != "" {
		t.Error("esc did not pop the zoom")
	}

	quit, _ := fm.Update(keyMsg("q"))
	if qm := quit.(fleetModel); !qm.quitting {
		t.Error("q in zoom did not quit")
	}

	// Frame routing: stale generation dropped, current generation applied.
	stale := changeFrameMsg{gen: fm.zoomGen - 1, events: []changeEvent{{path: "old.go"}}}
	next, _ := fm.Update(stale)
	if nm := next.(fleetModel); len(nm.zoom.events) != 0 {
		t.Error("stale frame was applied to the zoom feed")
	}
	fresh := changeFrameMsg{gen: fm.zoomGen, ts: time.Now(), events: []changeEvent{{path: "new.go"}}}
	next, _ = fm.Update(fresh)
	if nm := next.(fleetModel); len(nm.zoom.events) != 1 {
		t.Error("current frame was not applied to the zoom feed")
	}
}

// TestFleetZoomAutoPop: when a poll no longer carries the zoomed worktree
// (reaped mid-watch), the zoom pops instead of watching a dead path.
func TestFleetZoomAutoPop(t *testing.T) {
	m := zoomTestModel()
	model, _ := m.openZoom("/wt/b")
	fm := model.(fleetModel)

	next, _ := fm.Update(fleetDataMsg{entries: []fleetEntryJSON{
		{Path: "/wt/a", Branch: "develop", Status: "clean"},
	}})
	if nm := next.(fleetModel); nm.zoom != nil {
		t.Error("zoom did not pop after its worktree vanished")
	}
}
