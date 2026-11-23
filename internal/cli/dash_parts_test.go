package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// notePart is a slow part that writes into the snapshot's notes, so a test
// reads what the screen applied.
func notePart() *dashPart {
	return &dashPart{
		fetch:   func(context.Context) (func(*dashSnapshot), error) { return nil, errors.New("not run") },
		pending: func(s *dashSnapshot) { s.Notes = append(s.Notes, "pending") },
		failed:  func(s *dashSnapshot, err error) { s.Notes = append(s.Notes, "failed: "+err.Error()) },
	}
}

func writeNote(n string) func(*dashSnapshot) {
	return func(s *dashSnapshot) { s.Notes = append(s.Notes, n) }
}

func appliedNotes(p *dashPart) string {
	var s dashSnapshot
	p.apply(&s)
	return strings.Join(s.Notes, ", ")
}

func TestDashPartApply(t *testing.T) {
	p := notePart()
	if got := appliedNotes(p); got != "pending" {
		t.Errorf("before a read: %q", got)
	}
	p.done(nil, errors.New("boom"))
	if got := appliedNotes(p); got != "failed: boom" {
		t.Errorf("after a failed first read: %q, want no pending block", got)
	}
	p.done(writeNote("first"), nil)
	if got := appliedNotes(p); got != "first" {
		t.Errorf("after a read: %q", got)
	}
	// A failure keeps the last result and marks it.
	p.done(nil, errors.New("boom"))
	if got := appliedNotes(p); got != "first, failed: boom" {
		t.Errorf("after a failed refresh: %q", got)
	}
	p.done(writeNote("second"), nil)
	if got := appliedNotes(p); got != "second" {
		t.Errorf("after the next read: %q", got)
	}
}

func TestDashTUISlowPartsFillLater(t *testing.T) {
	m, clock := newTestDashTUI(t, false)
	m.width = 120
	a, b := notePart(), notePart()
	m.parts = []*dashPart{a, b}
	if m.Init(); !m.inFlight || !a.inFlight || !b.inFlight {
		t.Fatalf("Init did not start every read: main %v parts %v %v", m.inFlight, a.inFlight, b.inFlight)
	}
	if !a.inFlight || !b.inFlight {
		t.Fatalf("a part is not in flight")
	}
	if m.startParts(true) != nil {
		t.Errorf("a read in flight was started again")
	}

	// The main read ends first: the blocks are pending, the rest is shown.
	m.Update(okResult(m))
	if m.snap == nil || strings.Join(m.snap.Notes, ", ") != "pending, pending" {
		t.Fatalf("snapshot before the slow reads = %+v", m.snap)
	}
	if !strings.Contains(m.status(), "refreshing") {
		t.Errorf("status while a slow read runs = %q", m.status())
	}
	m.Update(dashPartMsg{part: 1, write: writeNote("b1")})
	if b.inFlight || !a.inFlight || !strings.Contains(m.vp.View(), "note: b1") {
		t.Fatalf("part result not shown: in flight %v %v\n%s", a.inFlight, b.inFlight, m.vp.View())
	}
	m.Update(dashPartMsg{part: 0, write: writeNote("a1")})
	if strings.Contains(m.status(), "refreshing") {
		t.Errorf("status after every read = %q", m.status())
	}

	// A later main read keeps the results. r repeats the slow reads at once;
	// an auto-refresh repeats them only when they are old.
	m.Update(key("r"))
	if !a.inFlight || !b.inFlight {
		t.Fatalf("r did not start the slow reads")
	}
	m.Update(okResult(m))
	if got := strings.Join(m.snap.Notes, ", "); got != "a1, b1" {
		t.Errorf("a main read dropped the slow blocks: %q", got)
	}
	m.Update(dashPartMsg{part: 0, write: writeNote("a2")})
	m.Update(dashPartMsg{part: 1, write: writeNote("b2")})
	m.startParts(false)
	if a.inFlight || b.inFlight {
		t.Errorf("an auto-refresh repeats a fresh slow read")
	}
	clock.advance(dashSlowMaxAge)
	m.startParts(false)
	if !a.inFlight || !b.inFlight {
		t.Errorf("an auto-refresh does not repeat an old slow read")
	}
	// A new day repeats them at once: the activity counts are by day.
	m.Update(dashPartMsg{part: 0, write: writeNote("a2")})
	m.Update(dashPartMsg{part: 1, write: writeNote("b2")})
	midnight := time.Date(clock.t.Year(), clock.t.Month(), clock.t.Day()+1, 0, 0, 0, 0, clock.t.Location())
	clock.t = midnight.Add(-time.Second)
	m.Update(dashPartMsg{part: 0, write: writeNote("a2")})
	m.Update(dashPartMsg{part: 1, write: writeNote("b2")})
	clock.advance(2 * time.Second)
	m.startParts(false)
	if !a.inFlight || !b.inFlight {
		t.Errorf("a slow read of the day before is not repeated after midnight")
	}

	// A failure keeps the block; a rate limit also stops the next reads.
	m.Update(okResult(m))
	m.Update(dashPartMsg{part: 0, err: errors.New("boom")})
	m.Update(okResult(m))
	if got := strings.Join(m.snap.Notes, ", "); got != "a2, failed: boom, b2" {
		t.Errorf("after a failure: %q", got)
	}
	reset := clock.now().Add(2 * time.Minute)
	m.Update(dashPartMsg{part: 1, err: &ghapi.RateLimitError{Status: "403", Reset: reset}})
	if !m.limited() {
		t.Fatalf("a rate-limited slow read does not stop the reads")
	}
	a.inFlight, b.inFlight = false, false
	if m.startParts(true); a.inFlight || b.inFlight {
		t.Errorf("a slow read started during a rate limit")
	}
}

// The reads start together, so the main read can succeed after a slow read
// was rate limited. That success must not lift the slow read's limit.
func TestDashTUIMainSuccessKeepsTheSlowRateLimit(t *testing.T) {
	m, clock := newTestDashTUI(t, true)
	m.parts = []*dashPart{notePart(), notePart()}
	m.Init()
	reset := clock.now().Add(5 * time.Minute)
	m.Update(dashPartMsg{part: 0, err: &ghapi.RateLimitError{Status: "403", Reset: reset}})
	m.Update(okResult(m))
	if !m.limited() {
		t.Fatalf("the main read's success lifted the slow read's rate limit")
	}
	if !m.nextAt.Equal(reset) {
		t.Errorf("next tick at %v, want the reset %v", m.nextAt, reset)
	}
	if !strings.Contains(m.status(), "rate limited") && !strings.Contains(m.status(), "refreshing") {
		t.Errorf("status = %q", m.status())
	}
	// A failure of another kind keeps the limit.
	m.Update(dashPartMsg{part: 1, err: errors.New("boom")})
	if !m.limited() || !strings.Contains(m.status(), "rate limited") {
		t.Errorf("another failure lifted the limit: status %q", m.status())
	}
	// A slow read started with the limited one keeps the limit when it ends
	// later with success. The limit ends at its reset.
	m.Update(dashPartMsg{part: 1, write: writeNote("ok")})
	if !m.limited() {
		t.Errorf("a slow read started before the limit lifted it")
	}
	clock.advance(5 * time.Minute)
	if m.limited() {
		t.Errorf("the limit outlived its reset")
	}
}

func TestDashTUIWithoutSlowParts(t *testing.T) {
	m, _ := newTestDashTUI(t, false)
	m.Init()
	m.Update(okResult(m))
	if m.snap == nil || len(m.snap.Notes) != 0 || strings.Contains(m.status(), "refreshing") {
		t.Errorf("snapshot %+v, status %q", m.snap, m.status())
	}
}

// The screen's main read leaves the slow reads out, and the screen sends each
// of them one time.
func TestDashScreenSplitsTheSlowReads(t *testing.T) {
	r, oid := dashCmdRepo(t)
	count := func(f *dashFake) (total, activity, commits, unreleased int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, q := range f.queries {
			switch {
			case strings.Contains(q, "o0: search"):
				activity++
			case strings.Contains(q, "history(since"):
				commits++
			case strings.Contains(q, "repositoryOwner("):
				unreleased++
			}
		}
		return len(f.queries), activity, commits, unreleased
	}
	screen := func(f *dashFake, prefix string) *dashTUI {
		resetForecastMemo()
		env := dashEnv{client: &ghapi.Client{Token: "tok", APIBase: f.server(t, oid).URL}, runner: &git.ExecRunner{Dir: r.Dir}, prefix: prefix, remote: "origin"}
		m := newDashScreen(context.Background(), env, time.Minute, false)
		t.Cleanup(m.cancel)
		return m
	}
	readParts := func(m *dashTUI) dashSnapshot {
		var snap dashSnapshot
		for _, p := range m.parts {
			p.read(context.Background())
			p.apply(&snap)
		}
		return snap
	}

	f := &dashFake{orgRepos: []any{dashOrgRepoNode("o/r")}}
	m := screen(f, "org:o")
	snap, err := m.fetch(context.Background())
	if err != nil {
		t.Fatalf("main read: %v", err)
	}
	if total, activity, commits, unreleased := count(f); total != 2 || activity+commits+unreleased != 0 {
		t.Errorf("main read sent %d queries, %d of them slow", total, activity+commits+unreleased)
	}
	if snap.UnreleasedRepos != nil || snap.Activity == nil || snap.Activity.Opened != nil || snap.Activity.Commits != nil {
		t.Errorf("main read filled a slow block: %+v, %+v", snap.UnreleasedRepos, snap.Activity)
	}
	slow := readParts(m)
	if total, activity, commits, unreleased := count(f); total != 5 || activity != 1 || commits != 1 || unreleased != 1 {
		t.Errorf("after the slow reads: %d queries (%d activity, %d commits, %d unreleased)", total, activity, commits, unreleased)
	}
	if a := slow.Activity; a == nil || a.Opened == nil || a.Opened.Total != 28 || a.Merged.Total != 21 || a.Commits == nil || a.Commits.Total != 14 {
		t.Errorf("activity = %+v", slow.Activity)
	}
	if u := slow.UnreleasedRepos; u == nil || len(u.Items) != 1 {
		t.Errorf("unreleased = %+v", u)
	}

	// A repository counts its commits and its unreleased PRs locally.
	f = &dashFake{}
	m = screen(f, "repo:o/r")
	if len(m.parts) != 1 {
		t.Fatalf("a repository screen has %d slow reads, want the PR activity only", len(m.parts))
	}
	if _, err := m.fetch(context.Background()); err != nil {
		t.Fatalf("main read: %v", err)
	}
	slow = readParts(m)
	if total, activity, commits, unreleased := count(f); total != 3 || activity != 1 || commits+unreleased != 0 {
		t.Errorf("repository: %d queries (%d activity, %d commits, %d unreleased)", total, activity, commits, unreleased)
	}
	if slow.Activity == nil || slow.Activity.Opened.Total != 28 || slow.UnreleasedRepos != nil {
		t.Errorf("repository slow blocks = %+v, %+v", slow.Activity, slow.UnreleasedRepos)
	}
}

func TestDashSlowPartsFailures(t *testing.T) {
	r, oid := dashCmdRepo(t)
	f := &dashFake{activityFails: true, orgFails: true}
	env := dashEnv{client: &ghapi.Client{Token: "tok", APIBase: f.server(t, oid).URL}, runner: &git.ExecRunner{Dir: r.Dir}, prefix: "org:o", remote: "origin"}
	prev := newDashSeries([]int{1, 2})
	prevRepos := &dashOrgUnreleasedJSON{Total: 3, Scanned: 3, Items: []dashRepoUnreleasedRow{{Repo: "o/r"}}}
	snap := dashSnapshot{Activity: &dashActivityJSON{Opened: prev, Commits: prev}, UnreleasedRepos: prevRepos}
	for _, p := range dashSlowParts(env, time.Now) {
		write, err := p.fetchWithin(context.Background())
		if err == nil || write != nil {
			t.Fatalf("a 502 is no error: %v", err)
		}
		p.failed(&snap, err)
	}
	a := snap.Activity
	// The rows of the read before stay, and the shared results are not changed.
	if a.Opened.Total != 3 || !strings.Contains(a.Opened.Error, "timed out") || a.Merged.Days != nil || a.Merged.Error == "" || a.Commits.Total != 3 || a.Commits.Error == "" {
		t.Errorf("activity after failures = %+v %+v %+v", a.Opened, a.Merged, a.Commits)
	}
	if u := snap.UnreleasedRepos; len(u.Items) != 1 || !strings.Contains(u.Error, "timed out") {
		t.Errorf("unreleased after a failure = %+v", u)
	}
	if prev.Error != "" || prevRepos.Error != "" {
		t.Errorf("a failure changed a shared result: %q, %q", prev.Error, prevRepos.Error)
	}
}

func TestDashCmdActivity(t *testing.T) {
	r, oid := dashCmdRepo(t)
	t.Run("a failed activity query does not fail the snapshot", func(t *testing.T) {
		withAgentMode(t, false)
		out, err := runDashCmd(t, r.Dir, (&dashFake{activityFails: true}).server(t, oid), "tok")
		if err != nil {
			t.Fatalf("gk dash: %v\n%s", err, out)
		}
		if !strings.Contains(out, "#7") || !strings.Contains(out, "PRs opened ? · merged ?") || !strings.Contains(out, "could not read: github activity query timed out") {
			t.Errorf("output:\n%s", out)
		}
	})
	t.Run("JSON", func(t *testing.T) {
		withAgentMode(t, true)
		out, err := runDashCmd(t, r.Dir, (&dashFake{}).server(t, oid), "tok")
		if err != nil {
			t.Fatalf("gk dash: %v\n%s", err, out)
		}
		for _, want := range []string{`"activity"`, `"opened"`, `"merged"`, `"since"`, `"total": 28`, `"days"`} {
			if !strings.Contains(out, want) {
				t.Errorf("JSON has no %s:\n%s", want, out)
			}
		}
		if strings.Contains(out, `"pending"`) {
			t.Errorf("one snapshot has a pending block:\n%s", out)
		}
	})
}
