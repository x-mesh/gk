package cli

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	ghapi "github.com/x-mesh/gk/internal/github"
)

type fakeOrgDiscovery struct {
	repos   []ghapi.Repo
	changed bool
	err     error
}

func (f *fakeOrgDiscovery) poll(context.Context) ([]ghapi.Repo, bool, error) {
	changed := f.changed
	f.changed = false
	return f.repos, changed, f.err
}

func (f *fakeOrgDiscovery) push(name string, at time.Time) {
	for i := range f.repos {
		if f.repos[i].Name == name {
			f.repos[i].PushedAt = at
		}
	}
	f.changed = true
}

type fakeOrgRuns struct {
	runs  map[string][]ghapi.WorkflowRun
	byID  map[string]ghapi.WorkflowRun
	gets  []int64
	err   error
	calls int
}

func (f *fakeOrgRuns) ListRecentWorkflowRuns(_ context.Context, _, repo, _ string) ([]ghapi.WorkflowRun, string, bool, error) {
	f.calls++
	if f.err != nil {
		return nil, "", false, f.err
	}
	return f.runs[repo], "etag", false, nil
}

func (f *fakeOrgRuns) GetWorkflowRun(_ context.Context, _, repo string, id int64) (ghapi.WorkflowRun, error) {
	f.gets = append(f.gets, id)
	return f.byID[repo+"#"+strconv.FormatInt(id, 10)], nil
}

func eventKinds(evs []actionsOrgEvent) string {
	kinds := make([]string, len(evs))
	for i, ev := range evs {
		kinds[i] = ev.Kind
	}
	return strings.Join(kinds, ",")
}

func mustStep(t *testing.T, w *orgActionsWatcher, now time.Time) []actionsOrgEvent {
	t.Helper()
	evs, err := w.step(context.Background(), now)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	return evs
}

func TestOrgWatcherReportsPushLifecycle(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: t0.Add(-time.Hour)}}}
	runs := &fakeOrgRuns{runs: map[string][]ghapi.WorkflowRun{}}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, runs, t0)

	if evs := mustStep(t, w, t0); len(evs) != 0 || runs.calls != 0 {
		t.Fatalf("baseline evs=%v calls=%d, want silent and no run polls", evs, runs.calls)
	}

	push := t0.Add(10 * time.Second)
	disc.push("gk", push)
	if got := eventKinds(mustStep(t, w, push)); got != "ci-expecting" {
		t.Fatalf("after push = %q", got)
	}

	runs.runs["gk"] = []ghapi.WorkflowRun{
		{ID: 1, Name: "Old", Status: "completed", Conclusion: "success", CreatedAt: t0.Add(-time.Hour)},
		{ID: 2, Name: "CI", Status: "in_progress", HeadBranch: "develop", CreatedAt: push.Add(2 * time.Second)},
	}
	evs := mustStep(t, w, push.Add(5*time.Second))
	if got := eventKinds(evs); got != "ci-start" || evs[0].RunID != 2 || evs[0].Branch != "develop" {
		t.Fatalf("run start = %+v", evs)
	}
	if got := eventKinds(mustStep(t, w, push.Add(8*time.Second))); got != "" {
		t.Fatalf("unchanged run = %q", got)
	}

	runs.runs["gk"][1].Status, runs.runs["gk"][1].Conclusion = "completed", "failure"
	evs = mustStep(t, w, push.Add(5*time.Minute))
	if got := eventKinds(evs); got != "ci-end" || evs[0].Conclusion != "failure" {
		t.Fatalf("run end = %+v", evs)
	}
	// The repo stays active one more window for follow-up workflows (e.g. a
	// deploy triggered by workflow_run), then retires without a ci-none.
	if got := eventKinds(mustStep(t, w, push.Add(5*time.Minute+orgExpectTimeout))); got != "" {
		t.Fatalf("retire = %q", got)
	}
	if _, ok := w.active["gk"]; ok {
		t.Fatalf("repo still active after its runs finished and the window passed")
	}
}

func TestOrgWatcherReportsNoRunAfterTimeout(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: t0.Add(-time.Hour)}}}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, &fakeOrgRuns{}, t0)
	mustStep(t, w, t0)

	disc.push("gk", t0)
	mustStep(t, w, t0)
	if got := eventKinds(mustStep(t, w, t0.Add(orgExpectTimeout-time.Second))); got != "" {
		t.Fatalf("before timeout = %q", got)
	}
	if got := eventKinds(mustStep(t, w, t0.Add(orgExpectTimeout))); got != "ci-none" {
		t.Fatalf("at timeout = %q", got)
	}
}

func TestOrgWatcherCatchUpReportsOnlyRunsInFlight(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	pushed := t0.Add(-2 * time.Minute)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: pushed}}}
	runs := &fakeOrgRuns{runs: map[string][]ghapi.WorkflowRun{"gk": {
		{ID: 1, Name: "Lint", Status: "completed", Conclusion: "success", CreatedAt: pushed},
		{ID: 2, Name: "CI", Status: "in_progress", CreatedAt: pushed},
	}}}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, runs, t0)

	evs := mustStep(t, w, t0)
	if got := eventKinds(evs); got != "ci-start" || evs[0].RunID != 2 {
		t.Fatalf("catch-up = %+v", evs)
	}
}

func TestOrgWatcherFiltersWorkflow(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: t0.Add(-time.Hour)}}}
	runs := &fakeOrgRuns{runs: map[string][]ghapi.WorkflowRun{"gk": {
		{ID: 1, Name: "Pages", Status: "queued", CreatedAt: t0},
		{ID: 2, Name: "CI", Status: "queued", CreatedAt: t0},
	}}}
	w := newOrgActionsWatcher("x-mesh", "CI", disc.poll, runs, t0)
	mustStep(t, w, t0)

	disc.push("gk", t0)
	evs := mustStep(t, w, t0)
	if got := eventKinds(evs); got != "ci-expecting,ci-start" || evs[1].RunID != 2 {
		t.Fatalf("filtered = %+v", evs)
	}
}

func TestOrgWatcherStopsOnRateLimit(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "a", PushedAt: t0}, {Name: "b", PushedAt: t0}}}
	runs := &fakeOrgRuns{err: &ghapi.RateLimitError{Reset: t0.Add(time.Minute)}}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, runs, t0)

	_, err := w.step(context.Background(), t0)
	var rl *ghapi.RateLimitError
	if !errors.As(err, &rl) || runs.calls != 1 {
		t.Fatalf("err=%v calls=%d, want rate limit after the first repo", err, runs.calls)
	}
}

func TestOrgActionsLoopFailsOnFirstDiscoveryError(t *testing.T) {
	disc := &fakeOrgDiscovery{err: errors.New("404 Not Found")}
	w := newOrgActionsWatcher("nobody", "", disc.poll, &fakeOrgRuns{}, time.Now())
	err := runOrgActionsLoop(context.Background(), w, time.Millisecond,
		func(actionsOrgEvent) error { return nil }, func(error) {},
		func(context.Context, time.Duration) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

func TestOrgActionsLoopSleepsUntilRateLimitReset(t *testing.T) {
	t0 := time.Now()
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: t0}}}
	runs := &fakeOrgRuns{err: &ghapi.RateLimitError{Reset: t0.Add(10 * time.Minute)}}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, runs, t0)
	ctx, cancel := context.WithCancel(context.Background())
	var waited time.Duration
	err := runOrgActionsLoop(ctx, w, time.Second,
		func(actionsOrgEvent) error { return nil }, func(error) {},
		func(_ context.Context, d time.Duration) error { waited = d; cancel(); return context.Canceled })
	if err != nil {
		t.Fatalf("err = %v, want nil after cancellation", err)
	}
	if waited < 9*time.Minute {
		t.Fatalf("waited = %v, want until the reset", waited)
	}
}

func TestReadActionsWatchOptionsRejectsOrgWithSingleRunFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--org", "x-mesh", "--repo", "x-mesh/gk"},
		{"--org", "x-mesh", "--sha", "abc"},
		{"--org", "x-mesh", "--run", "7"},
	} {
		cmd := newActionsWatchFlagsCmd()
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		if _, err := readActionsWatchOptions(cmd); err == nil || !strings.Contains(err.Error(), "--org cannot be combined") {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
	cmd := newActionsWatchFlagsCmd()
	if err := cmd.ParseFlags([]string{"--org", "x-mesh", "--workflow", "CI"}); err != nil {
		t.Fatal(err)
	}
	if opts, err := readActionsWatchOptions(cmd); err != nil || opts.org != "x-mesh" || opts.workflow != "CI" {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
}

func newActionsWatchFlagsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "watch"}
	addActionsWatchFlags(cmd)
	return cmd
}

func TestOrgWatcherCatchUpRetiresSilently(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: t0.Add(-2 * time.Minute)}}}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, &fakeOrgRuns{}, t0)
	mustStep(t, w, t0)

	if got := eventKinds(mustStep(t, w, t0.Add(orgExpectTimeout))); got != "" {
		t.Fatalf("catch-up retire = %q, want no ci-none for a push the stream never announced", got)
	}
	if _, ok := w.active["gk"]; ok {
		t.Fatalf("catch-up repo still active")
	}
}

func TestOrgWatcherKeepsInFlightRunsAcrossRepush(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: t0.Add(-time.Hour)}}}
	runs := &fakeOrgRuns{runs: map[string][]ghapi.WorkflowRun{}}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, runs, t0)
	mustStep(t, w, t0)

	disc.push("gk", t0)
	mustStep(t, w, t0)
	runs.runs["gk"] = []ghapi.WorkflowRun{{ID: 1, Name: "CI", Status: "in_progress", CreatedAt: t0.Add(2 * time.Second)}}
	if got := eventKinds(mustStep(t, w, t0.Add(5*time.Second))); got != "ci-start" {
		t.Fatalf("first run = %q", got)
	}

	repush := t0.Add(5 * time.Minute)
	disc.push("gk", repush)
	runs.runs["gk"][0].Status, runs.runs["gk"][0].Conclusion = "completed", "success"
	evs := mustStep(t, w, repush)
	if got := eventKinds(evs); got != "ci-expecting,ci-end" || evs[1].RunID != 1 {
		t.Fatalf("after re-push = %+v, want the first push's run to end", evs)
	}
}

func TestOrgWatcherFetchesInFlightRunMissingFromPage(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: t0.Add(-time.Hour)}}}
	runs := &fakeOrgRuns{runs: map[string][]ghapi.WorkflowRun{}}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, runs, t0)
	mustStep(t, w, t0)

	disc.push("gk", t0)
	runs.runs["gk"] = []ghapi.WorkflowRun{{ID: 1, Name: "CI", Status: "in_progress", CreatedAt: t0}}
	mustStep(t, w, t0)

	runs.runs["gk"] = []ghapi.WorkflowRun{{ID: 2, Name: "Dependabot", Status: "queued", CreatedAt: t0.Add(-time.Hour)}}
	runs.byID = map[string]ghapi.WorkflowRun{"gk#1": {ID: 1, Name: "CI", Status: "completed", Conclusion: "success", CreatedAt: t0}}
	evs := mustStep(t, w, t0.Add(time.Minute))
	if got := eventKinds(evs); got != "ci-end" || evs[0].RunID != 1 || len(runs.gets) != 1 {
		t.Fatalf("evs=%+v gets=%v, want ci-end via a direct fetch", evs, runs.gets)
	}
}

func TestOrgWatcherKeepsStateOnNotModified(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	disc := &fakeOrgDiscovery{repos: []ghapi.Repo{{Name: "gk", PushedAt: t0.Add(-time.Hour)}}}
	nm := &notModifiedRuns{}
	w := newOrgActionsWatcher("x-mesh", "", disc.poll, nm, t0)
	mustStep(t, w, t0)
	disc.push("gk", t0)
	w.step(context.Background(), t0)
	w.active["gk"].etag = "e1"

	if got := eventKinds(mustStep(t, w, t0.Add(orgExpectTimeout))); got != "ci-none" {
		t.Fatalf("timeout under 304 = %q", got)
	}
	if nm.lastETag != "e1" {
		t.Fatalf("etag sent = %q, want the stored one", nm.lastETag)
	}
}

type notModifiedRuns struct{ lastETag string }

func (n *notModifiedRuns) ListRecentWorkflowRuns(_ context.Context, _, _, etag string) ([]ghapi.WorkflowRun, string, bool, error) {
	n.lastETag = etag
	return nil, etag, true, nil
}

func (n *notModifiedRuns) GetWorkflowRun(context.Context, string, string, int64) (ghapi.WorkflowRun, error) {
	return ghapi.WorkflowRun{}, errors.New("unexpected GetWorkflowRun")
}
