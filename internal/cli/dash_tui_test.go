package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	ghapi "github.com/x-mesh/gk/internal/github"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestDashTUI(t *testing.T, watch bool) (*dashTUI, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	m := newDashTUI(context.Background(), func(context.Context) (dashSnapshot, error) { return dashSnapshot{}, nil }, time.Minute, watch)
	m.now = clock.now
	t.Cleanup(m.cancel)
	return m, clock
}

func okResult(m *dashTUI) dashResultMsg {
	return dashResultMsg{seq: m.seq, snap: dashSnapshot{Scope: "repo:o/r", FetchedAt: time.Now()}}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestDashTUIStartsWithWatchOff(t *testing.T) {
	m, _ := newTestDashTUI(t, false)
	m.Init()
	if !m.inFlight || m.seq != 1 {
		t.Fatalf("Init did not start the first read: %+v", m)
	}
	m.Update(okResult(m))
	if m.scheduled || m.snap == nil {
		t.Errorf("watch off scheduled a refresh (%v) or dropped the snapshot", m.scheduled)
	}
	if !strings.Contains(m.status(), "auto-refresh off") {
		t.Errorf("status = %q", m.status())
	}
}

func TestDashTUIWatchFlagStartsOn(t *testing.T) {
	m, clock := newTestDashTUI(t, true)
	m.Init()
	m.Update(okResult(m))
	if !m.scheduled || !m.nextAt.Equal(clock.now().Add(time.Minute)) {
		t.Fatalf("scheduled %v at %v, want a tick in one minute", m.scheduled, m.nextAt)
	}
	clock.advance(time.Minute)
	m.Update(dashTickMsg{gen: m.gen})
	if !m.inFlight || m.seq != 2 {
		t.Errorf("tick did not refresh: seq %d", m.seq)
	}
	if !strings.Contains(m.status(), "refreshing") {
		t.Errorf("status = %q", m.status())
	}
}

// w on, off, on leaves one pending tick, and a tick from before the toggle
// starts no read.
func TestDashTUIToggleDropsOldTicks(t *testing.T) {
	m, clock := newTestDashTUI(t, false)
	m.Init()
	m.Update(okResult(m))
	clock.advance(10 * time.Second)

	m.Update(key("w"))
	first := m.gen
	if !m.watch || !m.scheduled || !m.nextAt.Equal(clock.now().Add(50*time.Second)) {
		t.Fatalf("w on: watch %v scheduled %v at %v, want the rest of the minute", m.watch, m.scheduled, m.nextAt)
	}
	m.Update(key("w"))
	if m.watch || m.scheduled {
		t.Fatalf("w off left watch %v scheduled %v", m.watch, m.scheduled)
	}
	m.Update(key("w"))
	m.Update(dashTickMsg{gen: first})
	if m.inFlight || m.seq != 1 {
		t.Errorf("a tick from before the toggle refreshed: seq %d", m.seq)
	}
	clock.advance(50 * time.Second)
	m.Update(dashTickMsg{gen: m.gen})
	if !m.inFlight || m.seq != 2 {
		t.Errorf("the current tick did not refresh: seq %d", m.seq)
	}
}

func TestDashTUIToggleOnAfterTheIntervalReadsAtOnce(t *testing.T) {
	m, clock := newTestDashTUI(t, false)
	m.Init()
	m.Update(okResult(m))
	clock.advance(2 * time.Minute)
	m.Update(key("w"))
	if !m.inFlight || m.seq != 2 || m.scheduled {
		t.Errorf("w after the interval: inFlight %v seq %d scheduled %v, want a read now", m.inFlight, m.seq, m.scheduled)
	}
}

func TestDashTUINoOverlappingReads(t *testing.T) {
	m, _ := newTestDashTUI(t, true)
	m.Init()
	m.Update(key("r"))
	m.Update(dashTickMsg{gen: m.gen})
	if m.seq != 1 {
		t.Fatalf("a read started while one was in flight: seq %d", m.seq)
	}
	stale := dashResultMsg{seq: 0, err: errors.New("old")}
	m.Update(stale)
	if !m.inFlight || m.err != nil {
		t.Errorf("a result of another sequence was used")
	}
	m.Update(okResult(m))
	m.Update(key("r"))
	gen := m.gen
	m.Update(okResult(m))
	if m.seq != 2 || !m.scheduled || m.gen != gen {
		t.Errorf("r: seq %d scheduled %v", m.seq, m.scheduled)
	}
}

func TestDashTUIFailureKeepsTheSnapshot(t *testing.T) {
	m, clock := newTestDashTUI(t, true)
	m.Init()
	m.Update(okResult(m))
	good := m.snap
	clock.advance(time.Minute)
	m.Update(dashTickMsg{gen: m.gen})
	m.Update(dashResultMsg{seq: m.seq, err: errors.New("github issue query timed out (502 Bad Gateway)")})
	if m.snap != good || m.err == nil {
		t.Fatalf("snapshot %p (want %p), err %v", m.snap, good, m.err)
	}
	if !m.scheduled || !m.nextAt.Equal(clock.now().Add(time.Minute)) {
		t.Errorf("after a failure: scheduled %v at %v, want the next interval, not a retry now", m.scheduled, m.nextAt)
	}
	if s := m.status(); !strings.Contains(s, "refresh failed") || !strings.Contains(s, "last success") {
		t.Errorf("status = %q", s)
	}
}

func TestDashTUIRateLimitWaits(t *testing.T) {
	m, clock := newTestDashTUI(t, true)
	m.Init()
	reset := clock.now().Add(10 * time.Minute)
	m.Update(dashResultMsg{seq: m.seq, err: &ghapi.RateLimitError{Status: "403", Reset: reset}})
	if !m.nextAt.Equal(reset) {
		t.Fatalf("next tick at %v, want the reset %v", m.nextAt, reset)
	}
	m.Update(key("r"))
	if m.inFlight {
		t.Errorf("r sent a request during the rate limit")
	}
	clock.advance(time.Minute)
	m.Update(dashTickMsg{gen: m.gen})
	if m.inFlight || !m.nextAt.Equal(reset) {
		t.Errorf("a tick during the limit: inFlight %v next %v", m.inFlight, m.nextAt)
	}
	if !strings.Contains(m.status(), "rate limited") {
		t.Errorf("status = %q", m.status())
	}
	clock.advance(10 * time.Minute)
	m.Update(dashTickMsg{gen: m.gen})
	if !m.inFlight {
		t.Errorf("no read after the reset")
	}
}

func TestDashTUIQuitCancels(t *testing.T) {
	m, _ := newTestDashTUI(t, false)
	_, cmd := m.Update(key("q"))
	if cmd == nil || m.ctx.Err() == nil {
		t.Errorf("q did not quit and cancel: cmd %v ctx %v", cmd, m.ctx.Err())
	}
}

func TestDashCmdIntervalAndWatchFlags(t *testing.T) {
	withAgentMode(t, false)
	r, oid := dashCmdRepo(t)
	srv := (&dashFake{}).server(t, oid)
	if _, err := runDashCmd(t, r.Dir, srv, "tok", "--interval", "10s"); err == nil || !strings.Contains(err.Error(), "minimum") {
		t.Errorf("--interval 10s: err = %v", err)
	}
	// Tests have no terminal.
	if _, err := runDashCmd(t, r.Dir, srv, "tok", "--watch"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("--watch without a terminal: err = %v", err)
	}
}
