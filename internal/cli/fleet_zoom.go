package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/x-mesh/gk/internal/git"
)

// --- in-process zoom ('w') ------------------------------------------------------
//
// The drill-down used to suspend the fleet TUI and exec a child
// `gk status --watch --repo <path>` — a full context switch that rebuilt the
// target's fs watchers from scratch and froze the dashboard until the child
// exited. The zoom embeds the same changeWatchModel inside the fleet program
// instead: switching is instant, fleet keeps gathering in the background, and
// '[' / ']' hop between worktrees without the exit-move-reenter round trip.
//
// Ownership: the embedded model arms NO timers or fs listeners of its own.
// Fleet drives it — fleetFSMsg for the zoomed path triggers an immediate
// refresh, every fleetDataMsg doubles as its heartbeat, and fleetClockMsg
// mirrors into its wall-clock. changeFrameMsg carries the producing model's
// generation so a frame from a replaced target is dropped, not blended.

// openZoom starts (or retargets) the zoom view on one worktree and parks the
// fleet cursor on it, so popping back lands where the user was looking.
func (m fleetModel) openZoom(path string) (tea.Model, tea.Cmd) {
	m.zoomGen++
	zm := newChangeWatchModel(m.cmd, fleetTickInterval(m.interval, m.ws))
	zm.runner = &git.ExecRunner{Dir: path}
	zm.root = path // a linked worktree's toplevel is the worktree path itself
	zm.embedded = true
	zm.gen = m.zoomGen
	zm.fsLive = m.ws.hasWatcher(path)
	zm.captureDash = captureStatusFrameFor(path)
	zm.width, zm.height = m.width, zoomBodyHeight(m.height)
	m.zoom = zm
	m.zoomPath = path
	m.cursorTo(path)
	return m, zm.refreshCmd()
}

// closeZoom pops back to the fleet table. Bumping the generation makes any
// in-flight frame of the closed view fall on the floor.
func (m *fleetModel) closeZoom() {
	m.zoom = nil
	m.zoomPath = ""
	m.zoomGen++
}

// handleZoomKey is the zoom view's key router: navigation belongs to fleet
// (quit / pop / hop worktrees), everything else the embedded watch model
// already knows how to handle.
func (m fleetModel) handleZoomKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		m.ws.Close()
		return m, tea.Quit
	case "esc", "w":
		m.closeZoom()
		return m, nil
	case "[":
		if path, ok := m.zoomNeighbor(-1); ok {
			return m.openZoom(path)
		}
		return m, nil
	case "]":
		if path, ok := m.zoomNeighbor(1); ok {
			return m.openZoom(path)
		}
		return m, nil
	case "r", "p", " ", "c", "s":
		_, cmd := m.zoom.Update(msg)
		return m, cmd
	}
	return m, nil
}

// zoomTargets is the ordered list of zoomable worktrees: exactly what the
// table shows (filter/sort/fold-aware), minus unreachable entries — '[' / ']'
// walk the list the user sees.
func (m fleetModel) zoomTargets() []fleetEntryJSON {
	var out []fleetEntryJSON
	if m.multi {
		for _, r := range m.rows {
			if !r.header && r.entry.Status != "error" {
				out = append(out, r.entry)
			}
		}
		return out
	}
	for _, e := range m.viewEntries() {
		if e.Status != "error" {
			out = append(out, e)
		}
	}
	return out
}

// zoomNeighbor returns the next (+1) / previous (-1) zoom target, wrapping.
// ok is false when there is nowhere to go (a single target, or none). A
// vanished current target resolves to the first — a defined landing spot
// beats a dead hop.
func (m fleetModel) zoomNeighbor(dir int) (string, bool) {
	ts := m.zoomTargets()
	if len(ts) == 0 {
		return "", false
	}
	idx := -1
	for i, e := range ts {
		if e.Path == m.zoomPath {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ts[0].Path, true
	}
	next := (idx + dir + len(ts)) % len(ts)
	if ts[next].Path == m.zoomPath {
		return "", false
	}
	return ts[next].Path, true
}

// cursorTo parks the cursor on the row showing this worktree (no-op when the
// path isn't visible, e.g. filtered out or inside a folded repo).
func (m *fleetModel) cursorTo(path string) {
	if m.multi {
		for i, r := range m.rows {
			if !r.header && r.entry.Path == path {
				m.cursor = i
				return
			}
		}
		return
	}
	for i, e := range m.viewEntries() {
		if e.Path == path {
			m.cursor = i
			return
		}
	}
}

// hasEntry reports whether any gathered worktree still lives at path —
// checked against entries, not the filtered view, so a filter change never
// reads as "worktree removed".
func (m fleetModel) hasEntry(path string) bool {
	for _, e := range m.entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

// zoomBreadcrumb is the zoom view's first line: where you are in the fleet
// ("gk fleet ▸ branch (n/N)") with the navigation keys — the keys fleet owns,
// as opposed to the watch-action keybar the embedded view renders itself.
func (m fleetModel) zoomBreadcrumb() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	bold := lipgloss.NewStyle().Bold(true)

	label := filepath.Base(m.zoomPath)
	pos := ""
	ts := m.zoomTargets()
	for i, e := range ts {
		if e.Path == m.zoomPath {
			if e.Branch != "" {
				label = e.Branch
			}
			pos = fmt.Sprintf(" (%d/%d)", i+1, len(ts))
			break
		}
	}
	left := bold.Render("gk watch") + dim.Render(" ▸ ") + bold.Render(label) + dim.Render(pos)
	hint := "[/] worktree · esc back · q quit"

	width := m.width
	if width <= 0 || width > 120 {
		width = 80
	}
	gap := width - runewidth.StringWidth("gk watch ▸ "+label+pos) - runewidth.StringWidth(hint)
	if gap < 2 {
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + dim.Render(hint)
}

// zoomBodyHeight is the embedded view's height budget: the terminal minus the
// breadcrumb line. Zero (unknown size) lets the embedded view fall back to
// its own uncapped default.
func zoomBodyHeight(h int) int {
	if h <= 1 {
		return 0
	}
	return h - 1
}

// captureStatusFrameFor returns a dashboard-capture function ([s] in the
// zoom) for one worktree. The in-process capture the standalone watch uses
// (captureStatusFrame → runStatusOnce) renders whatever the global --repo
// flag points at — the repo fleet started in, not the zoom target — so the
// zoom shells out to its own binary instead. --no-color because the pipe has
// no TTY (detection would strip color anyway); explicit keeps it
// deterministic.
func captureStatusFrameFor(path string) func() string {
	return func() string {
		self, err := os.Executable()
		if err != nil {
			self = os.Args[0]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, self, "status", "--repo", path, "--no-color").Output()
		return string(out)
	}
}
