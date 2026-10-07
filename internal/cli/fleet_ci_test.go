package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/testutil"
)

// pushedRepo returns a repo whose origin reads as GitHub but pushes to a
// local bare repository, so a push writes a real remote-tracking reflog.
func pushedRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	r := testutil.NewRepo(t)
	bare := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	r.AddRemote("origin", "git@github.com:x-mesh/gk.git")
	r.RunGit("config", "remote.origin.pushurl", bare)
	return r
}

func TestLastBranchTipReadsThePushedTip(t *testing.T) {
	r := pushedRepo(t)
	r.CreateBranch("feat/ci")
	r.Checkout("feat/ci")
	r.WriteFile("work.txt", "work\n")
	sha := r.Commit("work")
	r.RunGit("push", "-q", "origin", "HEAD:refs/heads/feat/ci")

	remote, got, at, ok := lastBranchTip(r.GitDir, "feat/ci")
	if !ok || remote != "origin" || got != sha || time.Since(at) > time.Minute {
		t.Fatalf("push = %s %s %v %v, want origin %s now", remote, got, at, ok, sha)
	}
	if _, _, _, ok := lastBranchTip(r.GitDir, "main"); ok {
		t.Fatalf("main has no remote-tracking ref but reads as having a tip")
	}
}

// A commit pushed from elsewhere (another clone, a PR merged on GitHub)
// reaches this clone by fetch; its CI is as much the branch's CI as a push.
func TestLastTipInReflogCountsFetchAndSkipsDeletion(t *testing.T) {
	path := t.TempDir() + "/main"
	writeTestFile(t, path, strings.Join([]string{
		"0000000 aaaaaaa gk <gk@x> 1791339000 +0900\tupdate by push",
		"aaaaaaa bbbbbbb gk <gk@x> 1791339100 +0900\tfetch origin +refs/heads/main:refs/remotes/origin/main: fast-forward",
		"bbbbbbb 0000000 gk <gk@x> 1791339200 +0900\tfetch: deleted",
		"",
	}, "\n"))
	sha, at, ok := lastTipInReflog(path)
	if !ok || sha != "bbbbbbb" || at.Unix() != 1791339100 {
		t.Fatalf("got %s %v %v, want the fetched tip", sha, at, ok)
	}
}

func TestFleetCITrackerPushForResolvesGitHubSlug(t *testing.T) {
	r := pushedRepo(t)
	r.RunGit("push", "-q", "origin", "HEAD:refs/heads/main")
	tr := newFleetCITracker(&fakeFleetCIClient{}, time.Now())

	p, ok := tr.pushFor(context.Background(), r.GitDir, "main")
	if !ok || p.slug != "x-mesh/gk" || p.sha == "" {
		t.Fatalf("push = %+v ok=%v", p, ok)
	}

	old := newFleetCITracker(&fakeFleetCIClient{}, time.Now().Add(fleetCIPushWindow+time.Minute))
	if _, ok := old.pushFor(context.Background(), r.GitDir, "main"); ok {
		t.Fatalf("a push older than the window is tracked")
	}
}

type fakeFleetCIClient struct {
	runs  []ghapi.WorkflowRun
	err   error
	calls int
}

func (f *fakeFleetCIClient) ListWorkflowRunsForSHA(context.Context, string, string, string, string) ([]ghapi.WorkflowRun, string, bool, error) {
	f.calls++
	return f.runs, "etag", false, f.err
}

func TestFleetCITrackerLifecycle(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	client := &fakeFleetCIClient{}
	tr := newFleetCITracker(client, t0)
	e := fleetEntryJSON{push: &fleetPush{slug: "x-mesh/gk", sha: "abc1234def", at: t0}}

	tr.annotate(&e, t0)
	if e.CI == nil || e.CI.State != "pending" || client.calls != 0 {
		t.Fatalf("before poll = %+v calls=%d, want pending without an API call", e.CI, client.calls)
	}

	client.runs = []ghapi.WorkflowRun{{ID: 1, Name: "CI", Status: "in_progress"}}
	mustPoll(t, tr, t0.Add(3*time.Second))
	tr.annotate(&e, t0)
	if e.CI.State != "running" || fleetCIGlyph(e.CI) != "◌" {
		t.Fatalf("running = %+v", e.CI)
	}

	client.runs = []ghapi.WorkflowRun{{ID: 1, Name: "CI", Status: "completed", Conclusion: "failure", HTMLURL: "https://x/1"}}
	mustPoll(t, tr, t0.Add(time.Minute))
	tr.annotate(&e, t0)
	if e.CI.State != "failure" || fleetCIFailedURL(e.CI) != "https://x/1" {
		t.Fatalf("failed = %+v", e.CI)
	}

	mustPoll(t, tr, t0.Add(time.Minute+orgExpectTimeout))
	calls := client.calls
	mustPoll(t, tr, t0.Add(10*time.Minute))
	if client.calls != calls {
		t.Fatalf("settled push polled again")
	}
}

func mustPoll(t *testing.T, tr *fleetCITracker, now time.Time) {
	t.Helper()
	if err := tr.poll(context.Background(), now); err != nil {
		t.Fatalf("poll: %v", err)
	}
}

func TestFleetCITrackerNoRunSettlesAsNone(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	tr := newFleetCITracker(&fakeFleetCIClient{}, t0)
	e := fleetEntryJSON{push: &fleetPush{slug: "x-mesh/gk", sha: "abc", at: t0}}
	tr.annotate(&e, t0)

	mustPoll(t, tr, t0.Add(orgExpectTimeout-time.Second))
	tr.annotate(&e, t0)
	if e.CI.State != "pending" {
		t.Fatalf("before timeout = %s", e.CI.State)
	}
	mustPoll(t, tr, t0.Add(orgExpectTimeout))
	tr.annotate(&e, t0)
	if e.CI.State != "none" || fleetCIGlyph(e.CI) != "" {
		t.Fatalf("after timeout = %+v", e.CI)
	}
}

func TestFleetCITrackerOffWithoutToken(t *testing.T) {
	tr := newFleetCITracker(nil, time.Now())
	e := fleetEntryJSON{push: &fleetPush{slug: "x-mesh/gk", sha: "abc", at: time.Now()}}
	tr.annotate(&e, time.Now())
	if e.CI == nil || e.CI.State != "off" || !strings.Contains(e.CI.Reason, "GH_TOKEN") {
		t.Fatalf("ci = %+v", e.CI)
	}
	plain := fleetEntryJSON{}
	tr.annotate(&plain, time.Now())
	if plain.CI != nil {
		t.Fatalf("an unpushed branch shows CI %+v", plain.CI)
	}
}

func TestFleetCITrackerBacksOffOnRateLimit(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	client := &fakeFleetCIClient{err: &ghapi.RateLimitError{Reset: t0.Add(time.Hour)}}
	tr := newFleetCITracker(client, t0)
	e := fleetEntryJSON{push: &fleetPush{slug: "x-mesh/gk", sha: "abc", at: t0}}
	tr.annotate(&e, t0)

	if err := tr.poll(context.Background(), t0); err == nil {
		t.Fatalf("rate limit not reported")
	}
	if err := tr.poll(context.Background(), t0.Add(time.Minute)); err != nil || client.calls != 1 {
		t.Fatalf("polled during backoff: err=%v calls=%d", err, client.calls)
	}
}

func eventKindsOf(evs []fleetStreamEvent) string {
	kinds := make([]string, len(evs))
	for i, ev := range evs {
		kinds[i] = ev.Kind
	}
	return strings.Join(kinds, ",")
}

func TestFleetCITransitions(t *testing.T) {
	base := fleetStreamEvent{Path: "/wt", Branch: "main"}
	pending := &fleetCIJSON{State: "pending", SHA: "s1"}
	running := &fleetCIJSON{State: "running", SHA: "s1", Runs: []fleetCIRunJSON{{ID: 1, Workflow: "CI", Status: "in_progress"}}}
	failed := &fleetCIJSON{State: "failure", SHA: "s1", Runs: []fleetCIRunJSON{{ID: 1, Workflow: "CI", Status: "completed", Conclusion: "failure"}}}

	cases := []struct {
		name       string
		prev, curr *fleetCIJSON
		want       string
	}{
		{"push seen", nil, pending, "ci-expecting"},
		{"run starts", pending, running, "ci-start"},
		{"unchanged", running, running, ""},
		{"run ends", running, failed, "ci-end"},
		{"fast run", pending, failed, "ci-start,ci-end"},
		{"new push", failed, &fleetCIJSON{State: "pending", SHA: "s2"}, "ci-expecting"},
		{"no run", pending, &fleetCIJSON{State: "none", SHA: "s1"}, "ci-none"},
		{"off", nil, &fleetCIJSON{State: "off", SHA: "s1"}, ""},
	}
	for _, c := range cases {
		evs := fleetCITransitions(c.prev, c.curr, base)
		if got := eventKindsOf(evs); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		for _, ev := range evs {
			if ev.SHA != c.curr.SHA || ev.Path != "/wt" {
				t.Errorf("%s: event %+v lacks sha/path", c.name, ev)
			}
		}
	}
	evs := fleetCITransitions(running, failed, base)
	if evs[0].Conclusion != "failure" || evs[0].RunID != 1 || evs[0].Workflow != "CI" {
		t.Fatalf("ci-end = %+v", evs[0])
	}
}

func TestFleetBranchCellKeepsMarkersInsideTheCell(t *testing.T) {
	e := fleetEntryJSON{Branch: "feature/a-very-long-branch-name", Current: true, Operation: "rebase 1/2",
		CI: &fleetCIJSON{State: "failure"}}
	cell := fleetBranchCell(e, 18)
	if n := len([]rune(cell)); n > 18+3 {
		t.Fatalf("cell %q is %d runes, want <= 21", cell, n)
	}
	if !strings.HasSuffix(cell, "* ⏸ ✗") {
		t.Fatalf("cell %q lost a marker", cell)
	}
	plain := fleetBranchCell(fleetEntryJSON{Branch: "main", Current: true}, 18)
	if plain != "main*" {
		t.Fatalf("plain = %q", plain)
	}
}

func TestWithFleetCIBaselineSettlesOnlyTheFirstGather(t *testing.T) {
	client := &fakeFleetCIClient{runs: []ghapi.WorkflowRun{{ID: 1, Name: "CI", Status: "completed", Conclusion: "success"}}}
	tr := newFleetCITracker(client, time.Now())
	gather := withFleetCIBaseline(tr, func(context.Context) ([]fleetEntryJSON, error) {
		e := fleetEntryJSON{push: &fleetPush{slug: "x-mesh/gk", sha: "abc", at: time.Now()}}
		tr.annotate(&e, time.Now()) // what gatherFleetRepo does
		return []fleetEntryJSON{e}, nil
	})

	first, _ := gather(context.Background())
	if first[0].CI == nil || first[0].CI.State != "success" || client.calls != 1 {
		t.Fatalf("first = %+v calls=%d, want the finished run in the baseline", first[0].CI, client.calls)
	}
	if _, _ = gather(context.Background()); client.calls != 1 {
		t.Fatalf("later gathers polled synchronously: calls=%d", client.calls)
	}
}

type blockingFleetCIClient struct{}

func (blockingFleetCIClient) ListWorkflowRunsForSHA(ctx context.Context, _, _, _, _ string) ([]ghapi.WorkflowRun, string, bool, error) {
	<-ctx.Done()
	return nil, "", false, ctx.Err()
}

func TestSettleFirstIsBoundedByTheRepoTimeout(t *testing.T) {
	tr := newFleetCITracker(blockingFleetCIClient{}, time.Now())
	e := fleetEntryJSON{push: &fleetPush{slug: "x-mesh/gk", sha: "abc", at: time.Now()}}
	tr.annotate(&e, time.Now())
	entries := []fleetEntryJSON{e}

	done := make(chan struct{})
	go func() {
		tr.settleFirst(context.Background(), entries)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(fleetRepoTimeout + 2*time.Second):
		t.Fatalf("settleFirst blocked past the repo timeout on a stalled GitHub call")
	}
	if entries[0].CI == nil || entries[0].CI.State != "pending" {
		t.Fatalf("ci = %+v, want pending after a timed-out poll", entries[0].CI)
	}
}

func TestLastTipInReflogDropsTheLineCutBySeek(t *testing.T) {
	path := t.TempDir() + "/main"
	filler := strings.Repeat("x", fleetCIReflogTail)
	// The seek lands inside this line; parsing its tail would yield a
	// garbage SHA.
	writeTestFile(t, path, "0000000 junk gk <gk@x> 1791339000 +0900\t"+filler+" a b c 1791339000 +0900\tupdate by push\n")
	if sha, _, ok := lastTipInReflog(path); ok {
		t.Fatalf("parsed %q from a line cut by the tail seek", sha)
	}
}

func TestFleetCITrackerOldPushResolvesOnFirstPoll(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	pushed := now.Add(-3 * time.Hour)
	client := &fakeFleetCIClient{}
	tr := newFleetCITracker(client, now)
	e := fleetEntryJSON{push: &fleetPush{slug: "x-mesh/gk", sha: "abc", at: pushed}}
	tr.annotate(&e, now)

	mustPoll(t, tr, now)
	tr.annotate(&e, now)
	if e.CI.State != "none" {
		t.Fatalf("hours-old push without runs = %s, want none on the first poll", e.CI.State)
	}

	client.runs = []ghapi.WorkflowRun{{ID: 9, Name: "CI", Status: "completed", Conclusion: "success"}}
	done := fleetEntryJSON{push: &fleetPush{slug: "x-mesh/gk", sha: "def", at: pushed}}
	tr.annotate(&done, now)
	mustPoll(t, tr, now)
	tr.annotate(&done, now)
	if done.CI.State != "success" || fleetCIGlyph(done.CI) != "✓" {
		t.Fatalf("hours-old passed push = %+v", done.CI)
	}
}
