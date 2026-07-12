package cli

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x-mesh/gk/internal/git"
)

// --- fsnotify trigger for the fleet dashboard ----------------------------------
//
// Like `gk status --watch`, fleet is polling-first with an fsnotify upgrade:
// when watchers can be established the poll drops to a slow heartbeat
// (fsHeartbeatInterval) and file events drive refreshes instead — reactions
// get faster AND idle cost drops. Unlike status --watch there are N worktrees,
// so N watchers split one process-wide directory budget: each gets
// fsWatchMaxDirs / N. A worktree too big for its share simply doesn't get a
// watcher (its changes ride on the heartbeat) — degrade, don't fail.
//
// Backpressure: events request a poll; while one poll is in flight further
// events only set a pending flag, so a burst of saves across worktrees
// coalesces into at most one queued re-poll (the model owns that state).

// fleetWatchSet owns the per-worktree watchers and fans their debounced
// signals into one events channel carrying the origin worktree path.
type fleetWatchSet struct {
	mu       sync.Mutex
	watchers map[string]*fsWatcher
	events   chan string
	dirCap   int
	closed   bool
}

// fleetWatchBudget splits the process descriptor budget across n worktrees.
// The floor keeps a tiny share from making every watcher fail its walk when
// the fleet is large — better a few over-budget worktrees on the heartbeat
// than none watched at all.
func fleetWatchBudget(n int) int {
	if n <= 0 {
		n = 1
	}
	per := fsWatchMaxDirs / n
	const floor = 64
	if per < floor {
		return floor
	}
	return per
}

// newFleetWatchSet establishes watchers for the given worktrees. Returns nil
// when not a single worktree could be watched — the caller stays pure-polling.
func newFleetWatchSet(ctx context.Context, entries []fleetEntryJSON) *fleetWatchSet {
	ws := &fleetWatchSet{
		watchers: map[string]*fsWatcher{},
		events:   make(chan string, 8),
		dirCap:   fleetWatchBudget(len(entries)),
	}
	ws.sync(ctx, entries)
	if len(ws.watchers) == 0 {
		return nil
	}
	return ws
}

// sync reconciles the watcher set with the current fleet: new worktrees get a
// watcher (best-effort within the budget), vanished ones are closed. Safe to
// call from a tea.Cmd goroutine.
func (ws *fleetWatchSet) sync(ctx context.Context, entries []fleetEntryJSON) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return
	}
	want := map[string]bool{}
	for _, e := range entries {
		if e.Status != "error" && e.Path != "" {
			want[e.Path] = true
		}
	}
	// Re-split the budget for the CURRENT fleet size so worktrees added after
	// startup don't stack fixed initial-size shares past the process budget.
	// Established watchers keep their walk (re-registering is expensive) —
	// only new ones pick up the tightened cap; a grown fleet therefore trends
	// toward the correct split as worktrees come and go.
	ws.dirCap = fleetWatchBudget(len(want))
	for _, e := range entries {
		if !want[e.Path] {
			continue
		}
		if _, ok := ws.watchers[e.Path]; ok {
			continue
		}
		runner := &git.ExecRunner{Dir: e.Path, ExtraEnv: []string{"GIT_OPTIONAL_LOCKS=0"}}
		fw, ok := newFSWatcher(ctx, runner, fsWatchDebounce, ws.dirCap)
		if !ok {
			continue // over budget / unusable — this worktree rides the heartbeat
		}
		ws.watchers[e.Path] = fw
		go ws.forward(e.Path, fw)
	}
	for path, fw := range ws.watchers {
		if !want[path] {
			fw.Close()
			delete(ws.watchers, path)
		}
	}
}

// forward relays one watcher's debounced signals into the shared events
// channel, tagged with the worktree path. Drops on a full channel — events
// are level-triggered ("something changed, go look"), not a queue.
func (ws *fleetWatchSet) forward(path string, fw *fsWatcher) {
	for range fw.events {
		select {
		case ws.events <- path:
		default:
		}
	}
}

// hasWatcher reports whether this worktree has its own fs watcher — i.e. its
// changes reach the dashboard as events, not on the heartbeat. The zoom view
// uses it for a truthful live-vs-poll header indicator.
func (ws *fleetWatchSet) hasWatcher(path string) bool {
	if ws == nil {
		return false
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	_, ok := ws.watchers[path]
	return ok
}

// Close tears down every watcher. The events channel stays open (readers see
// no more sends); the TUI is quitting anyway.
func (ws *fleetWatchSet) Close() {
	if ws == nil {
		return
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.closed = true
	for path, fw := range ws.watchers {
		fw.Close()
		delete(ws.watchers, path)
	}
}

// fleetFSMsg reports a filesystem event in one worktree.
type fleetFSMsg struct{ path string }

// waitFleetFSCmd blocks on the shared events channel and surfaces the next
// filesystem wake-up as a message. Re-armed by Update after each receipt.
func waitFleetFSCmd(ws *fleetWatchSet) tea.Cmd {
	if ws == nil {
		return nil
	}
	return func() tea.Msg {
		path, ok := <-ws.events
		if !ok {
			return nil
		}
		return fleetFSMsg{path: path}
	}
}

// fleetSyncWatchersCmd reconciles watchers with the latest fleet in the
// background (the walk of a new worktree is too slow for Update).
func fleetSyncWatchersCmd(ctx context.Context, ws *fleetWatchSet, entries []fleetEntryJSON) tea.Cmd {
	if ws == nil {
		return nil
	}
	snapshot := make([]fleetEntryJSON, len(entries))
	copy(snapshot, entries)
	return func() tea.Msg {
		ws.sync(ctx, snapshot)
		return nil
	}
}

// fleetTickInterval is the poll cadence: the configured interval when polling
// drives the dashboard, the slow heartbeat when fsnotify does (events carry
// the real-time work; the poll only catches what no watcher saw, e.g. an
// index-only `git add` or an unwatched over-budget worktree).
func fleetTickInterval(interval time.Duration, ws *fleetWatchSet) time.Duration {
	if ws == nil {
		return interval
	}
	if interval > fsHeartbeatInterval {
		return interval
	}
	return fsHeartbeatInterval
}
