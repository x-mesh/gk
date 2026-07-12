package cli

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// --- machine-readable event stream (`gk fleet --events`) -----------------------
//
// The one-shot `--json` snapshot answers "what is the fleet's state"; the
// stream answers "tell me when it changes" — an orchestrator subscribes once
// instead of polling and diffing snapshots itself. Output is NDJSON: one
// event object per line. Under GK_AGENT a single header frame
// {"schema":1,"state":"streaming",...} precedes the events, so envelope
// consumers can recognize the mode switch without a schema collision — the
// {state,ok,result} envelope stays the contract for one-shot commands, and
// "streaming" marks this deliberate exception (the flag name avoids
// `--follow`, which would collide with the `gk follow` command).

// fleetStreamEvent is one NDJSON line. Kind decides which fields are set:
// file-changed (file/note[/added/removed/symbols]), status-changed (from/to),
// op-start/op-end (operation), land-ready (—).
type fleetStreamEvent struct {
	TS      string `json:"ts"`
	Kind    string `json:"kind"`
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Path    string `json:"path"`
	File    string `json:"file,omitempty"`
	Note    string `json:"note,omitempty"`
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`
	// Symbols are the changed-function names of a file-changed event
	// (feed-stats mode only) — extracted from git's hunk function contexts,
	// so an orchestrator can react to WHAT changed, not just which file.
	Symbols   []string `json:"symbols,omitempty"`
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
	Operation string   `json:"operation,omitempty"`
}

// fleetTransitions diffs two fleet snapshots into state-transition events:
// status flips, paused operations starting/ending, and branches becoming
// land-ready. Worktrees new to the fleet are a baseline — no events.
func fleetTransitions(prev, curr []fleetEntryJSON, ts time.Time) []fleetStreamEvent {
	prevBy := make(map[string]fleetEntryJSON, len(prev))
	for _, e := range prev {
		prevBy[e.Path] = e
	}
	stamp := ts.Format(time.RFC3339)
	var evs []fleetStreamEvent
	for _, e := range curr {
		p, seen := prevBy[e.Path]
		if !seen || e.Status == "error" || p.Status == "error" {
			continue
		}
		base := fleetStreamEvent{TS: stamp, Repo: e.Repo, Branch: e.Branch, Path: e.Path}
		if p.Status != e.Status {
			ev := base
			ev.Kind = "status-changed"
			ev.From, ev.To = p.Status, e.Status
			evs = append(evs, ev)
		}
		if p.Operation == "" && e.Operation != "" {
			ev := base
			ev.Kind = "op-start"
			ev.Operation = e.Operation
			evs = append(evs, ev)
		}
		if p.Operation != "" && e.Operation == "" {
			ev := base
			ev.Kind = "op-end"
			ev.Operation = p.Operation
			evs = append(evs, ev)
		}
		if !p.LandReady && e.LandReady {
			ev := base
			ev.Kind = "land-ready"
			evs = append(evs, ev)
		}
	}
	return evs
}

// feedEventsToStream converts the change-feed events of one poll into stream
// events (kind file-changed; a cleared file carries note "cleared").
func feedEventsToStream(feed []fleetFeedEvent) []fleetStreamEvent {
	var evs []fleetStreamEvent
	for _, ev := range feed {
		note := ev.note
		if ev.cleared {
			note = "cleared"
		}
		var symbols []string
		if ev.symbols != "" {
			// The display string joins extracted names (comma-free by
			// construction) with ", ", so the split is lossless.
			symbols = strings.Split(ev.symbols, ", ")
		}
		evs = append(evs, fleetStreamEvent{
			TS: ev.ts.Format(time.RFC3339), Kind: "file-changed",
			Repo: ev.repo, Branch: ev.branch, Path: ev.wt,
			File: ev.path, Note: note, Added: ev.added, Removed: ev.removed,
			Symbols: symbols,
		})
	}
	return evs
}

// runFleetEvents is the streaming loop: gather → diff → emit, driven by
// fsnotify when available with the poll demoted to a heartbeat (same upgrade
// as the TUI). Runs until ctx is cancelled (Ctrl-C / supervisor shutdown).
func runFleetEvents(ctx context.Context, cmd *cobra.Command, gather func(context.Context) ([]fleetEntryJSON, error), interval time.Duration, notify map[string]string) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)

	if AgentOut() {
		header := struct {
			Schema int    `json:"schema"`
			State  string `json:"state"`
			Result any    `json:"result"`
		}{Schema: 1, State: "streaming", Result: map[string]any{"mode": "fleet-events"}}
		if err := enc.Encode(header); err != nil {
			return err
		}
	}

	entries, err := gather(ctx)
	if err != nil {
		return err
	}
	// First gather is the baseline: state is recorded, nothing is emitted.
	_, sigState := applyFeedDiff(map[string]map[string]fileSig{}, entries, nil, time.Now())
	prev := entries

	ws := newFleetWatchSet(ctx, entries)
	defer ws.Close()
	tick := time.NewTicker(fleetTickInterval(interval, ws))
	defer tick.Stop()

	var fsCh <-chan string
	if ws != nil {
		fsCh = ws.events
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case <-fsCh:
			// Debounced already; drain fell-behind signals so a burst across
			// worktrees coalesces into this one refresh.
			for drained := false; !drained; {
				select {
				case <-fsCh:
				default:
					drained = true
				}
			}
		}

		entries, err = gather(ctx)
		if err != nil {
			continue // transient gather failure: keep streaming, retry next wake
		}
		now := time.Now()
		var feed []fleetFeedEvent
		feed, sigState = applyFeedDiff(sigState, entries, nil, now)
		evs := append(fleetTransitions(prev, entries, now), feedEventsToStream(feed)...)
		for _, ev := range evs {
			if err := enc.Encode(ev); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err // broken pipe: the subscriber went away
			}
			fireFleetNotify(ctx, notify, ev)
		}
		if ws != nil {
			ws.sync(ctx, entries)
		}
		prev = entries
	}
}

// fireFleetNotify runs the opt-in fleet.notify hook matching a transition
// event, if configured. Fire-and-forget via `sh -c` with GK_FLEET_* context in
// the environment; output is discarded (the stream owns stdout) — a hook that
// needs visibility should write somewhere itself.
func fireFleetNotify(ctx context.Context, notify map[string]string, ev fleetStreamEvent) {
	if len(notify) == 0 {
		return
	}
	var key string
	switch {
	case ev.Kind == "status-changed" && ev.To == "conflict":
		key = "conflict"
	case ev.Kind == "op-start":
		key = "paused"
	case ev.Kind == "land-ready":
		key = "land_ready"
	default:
		return
	}
	cmdStr := notify[key]
	if cmdStr == "" {
		return
	}
	c := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	c.Dir = ev.Path
	c.Env = append(c.Environ(),
		"GK_FLEET_KIND="+ev.Kind,
		"GK_FLEET_BRANCH="+ev.Branch,
		"GK_FLEET_PATH="+ev.Path,
		"GK_FLEET_REPO="+ev.Repo,
		"GK_FLEET_OPERATION="+ev.Operation,
	)
	c.Stdout, c.Stderr = io.Discard, io.Discard
	go func() { _ = c.Run() }()
}
