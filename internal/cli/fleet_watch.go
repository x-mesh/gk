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
// get faster AND idle cost drops. Unlike status --watch there are N worktrees
// sharing one process-wide directory budget (fsWatchMaxDirs), and the split
// is by ACTIVITY, not headcount: watchers exist to make the feed instant
// where changes are happening, so active worktrees (current checkout, paused
// op, or moved within the last hour — see fleetEntryActive) divide the whole
// budget among themselves and idle ones get none. An idle worktree costs nothing to skip —
// its row refreshes on the heartbeat, and the first change the heartbeat
// detects promotes it to active, so it gains a watcher one poll later (a
// one-time ≤heartbeat delay on first wake). A worktree too big even for its
// share still just rides the heartbeat — degrade, don't fail.
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
	// denied records grant failures (path → when), enforcing the retry
	// cooldown so an over-budget worktree isn't re-walked every poll.
	denied map[string]time.Time
}

// fleetWatchBudget splits the process watch budget (fd-aware on kqueue
// platforms — see fsWatchCostBudget) across n ACTIVE worktrees. The floor
// keeps a tiny share from making every watcher fail its walk when many
// worktrees are active at once — better a few over-budget worktrees on the
// heartbeat than none watched at all.
func fleetWatchBudget(n int) int {
	if n <= 0 {
		n = 1
	}
	per := fsWatchCostBudget() / n
	const floor = 64
	if per < floor {
		return floor
	}
	return per
}

// fleetWatchRetryCooldown is how long a worktree that failed to get a watcher
// (over budget, fd pressure) sits out before sync tries again. Without it the
// per-poll re-plan would re-walk the same too-big tree every few seconds.
const fleetWatchRetryCooldown = 2 * time.Minute

// fleetActiveWindow is how recently a worktree must have moved to stay
// "active" for watcher-budget purposes once its tree is clean again.
const fleetActiveWindow = time.Hour

// fleetEntryActive reports whether a worktree plausibly has an agent or a
// human in it right now — the signals the dashboard already computes: the
// current checkout (someone could start typing any second), a paused
// operation waiting on a resolution, or activity within the window. Bare
// dirtiness deliberately does NOT count: lastActive already advances to the
// newest dirty-file mtime, so "dirty and recent" is covered by the recency
// check — while a tree left dirty for two months is abandoned leftovers,
// not work in flight, and should neither hold a watcher nor pass the
// active view filter.
func fleetEntryActive(e fleetEntryJSON, now time.Time) bool {
	if e.Status == "error" || e.Path == "" {
		return false
	}
	if e.Current || e.Operation != "" {
		return true
	}
	return !e.lastActive.IsZero() && now.Sub(e.lastActive) <= fleetActiveWindow
}

// fleetWatchPlan is one sync's allocation decision, computed as a pure
// function so the policy is testable without filesystem watchers.
type fleetWatchPlan struct {
	grant  []string // active worktrees that should gain a watcher
	revoke []string // idle worktrees whose watcher should be freed (pressure)
	dirCap int      // per-watcher directory budget for this round's grants
}

// planFleetWatchers allocates the directory budget by activity. Active
// entries (plus any forced path — e.g. the zoomed worktree, which the user is
// staring at) divide the whole budget; idle entries get no new watcher. An
// idle entry that already HOLDS a watcher keeps it while there's no pressure
// (an established watcher costs nothing to keep), but the moment an active
// entry is missing one, every idle-held watcher is revoked to free budget —
// the re-walk cost lands on the idle side, where a heartbeat-paced feed is
// already the accepted service level. denied carries the retry cooldown:
// a path that failed a recent grant is not grantable this round, and —
// critically — exerts no pressure either, so cooldown-blocked misses never
// revoke idle holders for a grant that won't even be attempted.
func planFleetWatchers(entries []fleetEntryJSON, have map[string]bool, forced map[string]bool, denied map[string]time.Time, now time.Time) fleetWatchPlan {
	var missing, idleHolding []string
	activeCount := 0
	for _, e := range entries {
		if e.Status == "error" || e.Path == "" {
			continue
		}
		if fleetEntryActive(e, now) || forced[e.Path] {
			activeCount++
			if have[e.Path] {
				continue
			}
			if t, ok := denied[e.Path]; ok && now.Sub(t) < fleetWatchRetryCooldown {
				continue // cooling down — not grantable, not pressure
			}
			missing = append(missing, e.Path)
		} else if have[e.Path] {
			idleHolding = append(idleHolding, e.Path)
		}
	}
	plan := fleetWatchPlan{grant: missing, dirCap: fleetWatchBudget(activeCount)}
	if len(missing) > 0 {
		plan.revoke = idleHolding
	}
	return plan
}

// newFleetWatchSet establishes watchers for the given worktrees. The set is
// returned even when it starts EMPTY — an all-idle fleet legitimately grants
// nothing at startup, and the per-poll sync is what later promotes a woken
// worktree into a watcher. Returning nil here (the old behavior) killed that
// promotion loop for the whole session. Callers that need "is anything
// actually watched right now" ask hasAny(), not nil-ness.
func newFleetWatchSet(ctx context.Context, entries []fleetEntryJSON) *fleetWatchSet {
	ws := &fleetWatchSet{
		watchers: map[string]*fsWatcher{},
		events:   make(chan string, 8),
		dirCap:   fleetWatchBudget(len(entries)),
		denied:   map[string]time.Time{},
	}
	ws.sync(ctx, entries)
	return ws
}

// hasAny reports whether the set currently holds at least one live watcher —
// the question behind "are fs events driving refreshes right now". nil-safe.
func (ws *fleetWatchSet) hasAny() bool {
	if ws == nil {
		return false
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return len(ws.watchers) > 0
}

// sync reconciles the watcher set with the current fleet using the activity
// plan: newly active worktrees get a watcher (best-effort within the budget),
// idle holders are revoked under pressure, vanished ones are closed. forced
// paths (the zoomed worktree) always count as active. Safe to call from a
// tea.Cmd goroutine; re-planning every poll is what makes the allocation
// self-correcting as activity moves between worktrees.
func (ws *fleetWatchSet) sync(ctx context.Context, entries []fleetEntryJSON, forced ...string) {
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
	have := make(map[string]bool, len(ws.watchers))
	for path := range ws.watchers {
		have[path] = true
	}
	forcedSet := map[string]bool{}
	for _, p := range forced {
		if p != "" {
			forcedSet[p] = true
		}
	}

	plan := planFleetWatchers(entries, have, forcedSet, ws.denied, time.Now())
	ws.dirCap = plan.dirCap
	for _, path := range plan.revoke {
		if fw, ok := ws.watchers[path]; ok {
			fw.Close()
			delete(ws.watchers, path)
		}
	}
	for _, path := range plan.grant {
		runner := &git.ExecRunner{Dir: path, ExtraEnv: []string{"GIT_OPTIONAL_LOCKS=0"}}
		fw, ok := newFSWatcher(ctx, runner, fsWatchDebounce, ws.dirCap)
		if !ok {
			// Over budget / fd pressure / unusable — this worktree rides the
			// heartbeat and sits out the cooldown before the next attempt.
			ws.denied[path] = time.Now()
			continue
		}
		delete(ws.denied, path)
		ws.watchers[path] = fw
		go ws.forward(path, fw)
	}
	for path, fw := range ws.watchers {
		if !want[path] {
			fw.Close()
			delete(ws.watchers, path)
		}
	}
	// A vanished worktree's cooldown must vanish with it: a worktree
	// recreated at the same path is a fresh tree, not the one that failed.
	for path := range ws.denied {
		if !want[path] {
			delete(ws.denied, path)
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
// background (the walk of a new worktree is too slow for Update). forced
// paths are treated as active regardless of their signals — the zoomed
// worktree must react instantly while the user is looking at it.
func fleetSyncWatchersCmd(ctx context.Context, ws *fleetWatchSet, entries []fleetEntryJSON, forced ...string) tea.Cmd {
	if ws == nil {
		return nil
	}
	snapshot := make([]fleetEntryJSON, len(entries))
	copy(snapshot, entries)
	return func() tea.Msg {
		ws.sync(ctx, snapshot, forced...)
		return nil
	}
}

// fleetTickInterval is the poll cadence: the configured interval when polling
// drives the dashboard, the slow heartbeat when fsnotify does (events carry
// the real-time work; the poll only catches what no watcher saw, e.g. an
// index-only `git add` or an unwatched over-budget worktree). The question is
// whether any watcher is LIVE right now — an empty set (all-idle fleet) must
// keep polling at the configured interval, not coast on a heartbeat nothing
// is feeding.
func fleetTickInterval(interval time.Duration, ws *fleetWatchSet) time.Duration {
	if !ws.hasAny() {
		return interval
	}
	if interval > fsHeartbeatInterval {
		return interval
	}
	return fsHeartbeatInterval
}
