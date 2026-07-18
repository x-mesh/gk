package cli

import (
	"context"
	"io"
	"testing"
	"time"

	ghapi "github.com/x-mesh/gk/internal/github"
)

func mergedEvent(id string, base string) ghapi.RepoEvent {
	return ghapi.RepoEvent{ID: id, Type: "PullRequestEvent", Action: "closed", PRMerged: true, PRBase: base, PRNumber: 1, PRTitle: "x"}
}

// eventsEngineHarness wires a stub poller + dispatch recorder into a cycle.
type eventsEngineHarness struct {
	dispatched []map[string]string // env per dispatch, in order
	dispatchRC int
	poller     eventPoller
}

func (h *eventsEngineHarness) opts(t *testing.T, triggers []followTrigger, branch string) followEventsOpts {
	return followEventsOpts{
		slug:     "x-mesh/gk",
		branch:   branch,
		triggers: triggers,
		poll:     h.poller,
		dispatch: func(_ context.Context, env []string) (int, error) {
			m := map[string]string{}
			for _, kv := range env {
				for i := 0; i < len(kv); i++ {
					if kv[i] == '=' {
						m[kv[:i]] = kv[i+1:]
						break
					}
				}
			}
			h.dispatched = append(h.dispatched, m)
			return h.dispatchRC, nil
		},
		now:    time.Now,
		out:    io.Discard,
		runner: cacheRunner(t, ""),
	}
}

func mustTriggers(t *testing.T, s ...string) []followTrigger {
	ts, err := parseFollowTriggers(s)
	if err != nil {
		t.Fatalf("parse triggers %v: %v", s, err)
	}
	return ts
}

func TestFollowEventsBaselineDoesNotFire(t *testing.T) {
	h := &eventsEngineHarness{
		poller: func(_ context.Context, _ string) ([]ghapi.RepoEvent, string, bool, error) {
			return []ghapi.RepoEvent{mergedEvent("1005", "main"), mergedEvent("1004", "main")}, `"e"`, false, nil
		},
	}
	opts := h.opts(t, mustTriggers(t, "pr:merged"), "main")
	baseline := true
	cur, err := followEventsCycle(context.Background(), opts, followEventCursor{}, &baseline)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if len(h.dispatched) != 0 {
		t.Errorf("baseline run must not dispatch, got %d", len(h.dispatched))
	}
	if cur.LastID != "1005" {
		t.Errorf("baseline cursor should be newest (1005), got %q", cur.LastID)
	}
	if baseline {
		t.Error("baseline flag should be cleared after first run")
	}
}

func TestFollowEventsFiresNewMatchingOldestFirst(t *testing.T) {
	h := &eventsEngineHarness{
		poller: func(_ context.Context, _ string) ([]ghapi.RepoEvent, string, bool, error) {
			// newest-first from the API
			return []ghapi.RepoEvent{mergedEvent("1007", "main"), mergedEvent("1006", "main")}, `"e"`, false, nil
		},
	}
	opts := h.opts(t, mustTriggers(t, "pr:merged"), "main")
	baseline := false
	cur, err := followEventsCycle(context.Background(), opts, followEventCursor{LastID: "1005"}, &baseline)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if len(h.dispatched) != 2 {
		t.Fatalf("expected 2 dispatches, got %d", len(h.dispatched))
	}
	if cur.LastID != "1007" {
		t.Errorf("cursor should advance to 1007, got %q", cur.LastID)
	}
	if h.dispatched[0]["GK_TRIGGER"] != "pr:merged" {
		t.Errorf("env missing GK_TRIGGER: %v", h.dispatched[0])
	}
}

func TestFollowEventsBranchFilter(t *testing.T) {
	h := &eventsEngineHarness{
		poller: func(_ context.Context, _ string) ([]ghapi.RepoEvent, string, bool, error) {
			return []ghapi.RepoEvent{mergedEvent("1008", "develop")}, `"e"`, false, nil
		},
	}
	opts := h.opts(t, mustTriggers(t, "pr:merged"), "main") // following main
	baseline := false
	_, err := followEventsCycle(context.Background(), opts, followEventCursor{LastID: "1000"}, &baseline)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if len(h.dispatched) != 0 {
		t.Errorf("a merge into develop must not fire when following main, got %d", len(h.dispatched))
	}
}

func TestFollowEventsHookFailureStopsAndRetains(t *testing.T) {
	h := &eventsEngineHarness{
		dispatchRC: 3, // hook exits non-zero
		poller: func(_ context.Context, _ string) ([]ghapi.RepoEvent, string, bool, error) {
			return []ghapi.RepoEvent{mergedEvent("1010", "main"), mergedEvent("1009", "main")}, `"e"`, false, nil
		},
	}
	opts := h.opts(t, mustTriggers(t, "pr:merged"), "main")
	baseline := false
	cur, err := followEventsCycle(context.Background(), opts, followEventCursor{LastID: "1008"}, &baseline)
	if err == nil {
		t.Fatal("a non-zero hook exit must return an error (to trip backoff)")
	}
	// 1009 fails → cursor must NOT advance past it, so it retries next cycle.
	if cur.LastID != "1008" {
		t.Errorf("cursor must stay at 1008 for retry, got %q", cur.LastID)
	}
}

func TestFollowEventsNotModifiedNoop(t *testing.T) {
	h := &eventsEngineHarness{
		poller: func(_ context.Context, _ string) ([]ghapi.RepoEvent, string, bool, error) {
			return nil, `"e"`, true, nil
		},
	}
	opts := h.opts(t, mustTriggers(t, "pr:merged"), "main")
	baseline := false
	if _, err := followEventsCycle(context.Background(), opts, followEventCursor{LastID: "1000"}, &baseline); err != nil {
		t.Fatalf("304 must be a no-op, got %v", err)
	}
	if len(h.dispatched) != 0 {
		t.Error("304 must not dispatch")
	}
}

func TestFollowEngineDecision(t *testing.T) {
	cases := []struct {
		engine                    string
		hasOn, needAPI, hasToken  bool
		wantEvents, wantRefFellBk bool
	}{
		{"ref", true, true, true, false, false},      // ref forced → never events
		{"events", false, false, false, true, false}, // events forced → always events
		{"auto", false, false, true, false, false},   // no --on → branch mirror
		{"auto", true, false, true, true, false},     // --on + token → events
		{"auto", true, true, false, true, false},     // --on + no token + API-only → events (public)
		{"auto", true, false, false, false, true},    // --on + no token + merge-only → ref fallback
	}
	for _, c := range cases {
		gotE, gotF := followEngineDecision(c.engine, c.hasOn, c.needAPI, c.hasToken)
		if gotE != c.wantEvents || gotF != c.wantRefFellBk {
			t.Errorf("decision(%q,on=%v,api=%v,tok=%v) = (%v,%v), want (%v,%v)",
				c.engine, c.hasOn, c.needAPI, c.hasToken, gotE, gotF, c.wantEvents, c.wantRefFellBk)
		}
	}
}
