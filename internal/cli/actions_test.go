package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

type fakeActionsClient struct {
	runs     []ghapi.WorkflowRun
	updates  []ghapi.WorkflowRun
	byID     map[int64]ghapi.WorkflowRun
	getCalls int
	// polls scripts the conditional poll answers in order; when empty, a poll
	// answers from byID (list) or updates (single run).
	polls     []fakeActionsPoll
	pollCalls int
	etags     []string // If-None-Match sent on each conditional poll
}

type fakeActionsPoll struct {
	runs        []ghapi.WorkflowRun
	etag        string
	notModified bool
	err         error
}

func (f *fakeActionsClient) nextPoll(etag string, fallback func() []ghapi.WorkflowRun) fakeActionsPoll {
	f.etags = append(f.etags, etag)
	i := f.pollCalls
	f.pollCalls++
	if i < len(f.polls) {
		return f.polls[i]
	}
	return fakeActionsPoll{runs: fallback(), etag: etag}
}

func (f *fakeActionsClient) ListWorkflowRunsForSHA(_ context.Context, _, _, _, etag string) ([]ghapi.WorkflowRun, string, bool, error) {
	p := f.nextPoll(etag, func() []ghapi.WorkflowRun {
		if f.byID != nil {
			out := make([]ghapi.WorkflowRun, 0, len(f.byID))
			for _, run := range f.byID {
				out = append(out, run)
			}
			return out
		}
		i := min(f.pollCalls-1, len(f.updates)-1)
		return []ghapi.WorkflowRun{f.updates[i]}
	})
	return p.runs, p.etag, p.notModified, p.err
}

func (f *fakeActionsClient) GetWorkflowRunConditional(_ context.Context, _, _ string, _ int64, etag string) (ghapi.WorkflowRun, string, bool, error) {
	p := f.nextPoll(etag, func() []ghapi.WorkflowRun {
		return []ghapi.WorkflowRun{f.updates[min(f.pollCalls-1, len(f.updates)-1)]}
	})
	var run ghapi.WorkflowRun
	if len(p.runs) > 0 {
		run = p.runs[0]
	}
	return run, p.etag, p.notModified, p.err
}

func (f *fakeActionsClient) ListWorkflowRuns(_ context.Context, _, _, _, _ string) ([]ghapi.WorkflowRun, error) {
	return f.runs, nil
}

func (f *fakeActionsClient) GetWorkflowRun(_ context.Context, _, _ string, id int64) (ghapi.WorkflowRun, error) {
	if f.byID != nil {
		f.getCalls++
		return f.byID[id], nil
	}
	i := f.getCalls
	f.getCalls++
	if i >= len(f.updates) {
		i = len(f.updates) - 1
	}
	return f.updates[i], nil
}

func TestActionsTokenUsesEnvironmentOnly(t *testing.T) {
	t.Setenv("GH_TOKEN", "first")
	t.Setenv("GITHUB_TOKEN", "second")
	if got := actionsToken(); got != "first" {
		t.Fatalf("actionsToken = %q", got)
	}
	t.Setenv("GH_TOKEN", "")
	if got := actionsToken(); got != "second" {
		t.Fatalf("actionsToken fallback = %q", got)
	}
}

func TestResolveActionsTargetUsesRemoteAndHEAD(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote get-url origin": {Stdout: "git@github.com:x-mesh/headroom.git\n"},
		"rev-parse HEAD":        {Stdout: "abc123\n"},
	}}
	owner, repo, sha, err := resolveActionsTarget(context.Background(), config.Config{}, runner, actionsWatchOptions{})
	if err != nil {
		t.Fatalf("resolveActionsTarget: %v", err)
	}
	if owner != "x-mesh" || repo != "headroom" || sha != "abc123" {
		t.Fatalf("target = %s/%s@%s", owner, repo, sha)
	}
}

func TestActionsOrgFallbackUsesOwnerOnlyWithoutRemote(t *testing.T) {
	noRemote := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote get-url origin": {ExitCode: 2, Stderr: "error: No such remote 'origin'"},
	}}
	withRemote := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote get-url origin": {Stdout: "git@github.com:x-mesh/gk.git\n"},
	}}
	owned := config.Config{GitHub: config.GitHubConfig{Owner: "x-mesh"}}
	cases := []struct {
		name   string
		cfg    config.Config
		runner git.Runner
		opts   actionsWatchOptions
		want   string
	}{
		{"no remote", owned, noRemote, actionsWatchOptions{}, "x-mesh"},
		{"workflow kept", owned, noRemote, actionsWatchOptions{workflow: "CI"}, "x-mesh"},
		{"remote wins", owned, withRemote, actionsWatchOptions{}, ""},
		{"owner unset", config.Config{}, noRemote, actionsWatchOptions{}, ""},
		{"repo flag", owned, noRemote, actionsWatchOptions{repo: "x-mesh/gk"}, ""},
		{"sha flag", owned, noRemote, actionsWatchOptions{sha: "abc"}, ""},
		{"run flag", owned, noRemote, actionsWatchOptions{run: 7}, ""},
	}
	for _, tc := range cases {
		if got := actionsOrgFallback(context.Background(), tc.cfg, tc.runner, tc.opts); got != tc.want {
			t.Errorf("%s: actionsOrgFallback = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// One push can start the same workflow twice (push and pull_request), so
// several matches are watched together instead of rejected.
func TestWatchActionsRunsWaitsForEveryMatch(t *testing.T) {
	client := &fakeActionsClient{
		runs: []ghapi.WorkflowRun{{ID: 1, Name: "CI", Status: "in_progress"}, {ID: 2, Name: "CI", Status: "queued"}},
		byID: map[int64]ghapi.WorkflowRun{
			1: {ID: 1, Name: "CI", Status: "completed", Conclusion: "success"},
			2: {ID: 2, Name: "CI", Status: "completed", Conclusion: "skipped"},
		},
	}
	runs, err := watchActionsRuns(context.Background(), client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// One list request refreshes both runs; no per-run requests.
	if len(runs) != 2 || client.pollCalls != 1 || client.getCalls != 0 || runs[1].Conclusion != "skipped" {
		t.Fatalf("runs = %+v pollCalls = %d getCalls = %d", runs, client.pollCalls, client.getCalls)
	}
}

func TestWatchActionsRunsFailsWhenAnyMatchFails(t *testing.T) {
	client := &fakeActionsClient{runs: []ghapi.WorkflowRun{
		{ID: 1, Name: "CI", Status: "completed", Conclusion: "success"},
		{ID: 2, Name: "Lint", Status: "completed", Conclusion: "failure", HTMLURL: "https://example.test/2"},
	}}
	_, err := watchActionsRuns(context.Background(), client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "1 of 2") || !strings.Contains(err.Error(), "https://example.test/2") || client.getCalls != 0 {
		t.Fatalf("err = %v getCalls = %d", err, client.getCalls)
	}
}

func TestWatchActionsRunPollsToSuccess(t *testing.T) {
	client := &fakeActionsClient{
		runs: []ghapi.WorkflowRun{{ID: 11, Status: "in_progress"}},
		updates: []ghapi.WorkflowRun{{
			ID: 11, Name: "Pages", Status: "completed", Conclusion: "success", HTMLURL: "https://example.test/11",
		}},
	}
	runs, err := watchActionsRuns(context.Background(), client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatalf("watchActionsRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != 11 || client.pollCalls != 1 {
		t.Fatalf("runs = %+v, pollCalls = %d", runs, client.pollCalls)
	}
}

// TestWatchActionsRunsPollsConditionally: each poll sends the ETag from the
// previous answer, and a 304 leaves the runs as they were.
func TestWatchActionsRunsPollsConditionally(t *testing.T) {
	done := ghapi.WorkflowRun{ID: 1, Status: "completed", Conclusion: "success"}
	client := &fakeActionsClient{
		runs: []ghapi.WorkflowRun{{ID: 1, Status: "in_progress"}},
		polls: []fakeActionsPoll{
			{runs: []ghapi.WorkflowRun{{ID: 1, Status: "in_progress"}}, etag: `"e1"`},
			{notModified: true, etag: `"e1"`},
			{runs: []ghapi.WorkflowRun{done}, etag: `"e2"`},
		},
	}
	runs, err := watchActionsRuns(context.Background(), client, "x-mesh", "gk", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatalf("watchActionsRuns: %v", err)
	}
	if runs[0].Conclusion != "success" || client.getCalls != 0 {
		t.Fatalf("runs = %+v getCalls = %d", runs, client.getCalls)
	}
	if want := []string{"", `"e1"`, `"e1"`}; strings.Join(client.etags, ",") != strings.Join(want, ",") {
		t.Fatalf("If-None-Match per poll = %q, want %q", client.etags, want)
	}
}

// TestWatchActionsRunsWaitsOutRateLimit: a rate-limited poll waits until the
// reset and reports it, instead of failing the watch.
func TestWatchActionsRunsWaitsOutRateLimit(t *testing.T) {
	reset := time.Now().Add(time.Hour)
	client := &fakeActionsClient{
		runs: []ghapi.WorkflowRun{{ID: 1, Status: "in_progress"}},
		polls: []fakeActionsPoll{
			{err: &ghapi.RateLimitError{Status: "403 Forbidden", Reset: reset}},
			{runs: []ghapi.WorkflowRun{{ID: 1, Status: "completed", Conclusion: "success"}}},
		},
	}
	var waits []time.Duration
	var reported time.Time
	opts := actionsWatchOptions{interval: time.Millisecond, onRateLimit: func(r time.Time) { reported = r }}
	runs, err := watchActionsRuns(context.Background(), client, "x-mesh", "gk", "abc", opts, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	})
	if err != nil || runs[0].Conclusion != "success" {
		t.Fatalf("err = %v runs = %+v, want the watch to survive the rate limit", err, runs)
	}
	if !reported.Equal(reset) {
		t.Errorf("onRateLimit reset = %v, want %v", reported, reset)
	}
	if len(waits) != 2 || waits[1] < 59*time.Minute {
		t.Fatalf("waits = %v, want the second wait to last until the reset", waits)
	}
}

// TestWatchActionsRunUsesConditionalGet: --run polls that one run with its
// ETag instead of listing the commit's runs.
func TestWatchActionsRunUsesConditionalGet(t *testing.T) {
	client := &fakeActionsClient{
		updates: []ghapi.WorkflowRun{{ID: 7, Status: "in_progress"}},
		polls: []fakeActionsPoll{
			{notModified: true, etag: `"r1"`},
			{runs: []ghapi.WorkflowRun{{ID: 7, Status: "completed", Conclusion: "success"}}, etag: `"r2"`},
		},
	}
	client.byID = map[int64]ghapi.WorkflowRun{7: {ID: 7, Status: "in_progress"}}
	runs, err := watchActionsRuns(context.Background(), client, "x-mesh", "gk", "", actionsWatchOptions{run: 7, interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err != nil || runs[0].Conclusion != "success" {
		t.Fatalf("err = %v runs = %+v", err, runs)
	}
	if client.getCalls != 1 || client.pollCalls != 2 {
		t.Fatalf("getCalls = %d pollCalls = %d, want 1 initial get and 2 conditional polls", client.getCalls, client.pollCalls)
	}
}

// TestWatchActionsRunsFetchesRunMissingFromList: a run that fell off the
// commit's run list is asked for on its own so the watch can finish.
func TestWatchActionsRunsFetchesRunMissingFromList(t *testing.T) {
	client := &fakeActionsClient{
		runs:  []ghapi.WorkflowRun{{ID: 1, Status: "in_progress"}},
		polls: []fakeActionsPoll{{runs: []ghapi.WorkflowRun{{ID: 2, Status: "completed"}}, etag: `"e1"`}},
		byID:  map[int64]ghapi.WorkflowRun{1: {ID: 1, Status: "completed", Conclusion: "success"}},
	}
	runs, err := watchActionsRuns(context.Background(), client, "x-mesh", "gk", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err != nil || runs[0].Conclusion != "success" || client.getCalls != 1 {
		t.Fatalf("err = %v runs = %+v getCalls = %d", err, runs, client.getCalls)
	}
}

func TestWatchActionsRunReturnsFailedConclusion(t *testing.T) {
	client := &fakeActionsClient{runs: []ghapi.WorkflowRun{{
		ID: 11, Status: "completed", Conclusion: "failure", HTMLURL: "https://example.test/11",
	}}}
	_, err := watchActionsRuns(context.Background(), client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "failure") || !strings.Contains(err.Error(), "https://example.test/11") {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchActionsRunStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeActionsClient{runs: []ghapi.WorkflowRun{{ID: 11, Status: "in_progress"}}}
	_, err := watchActionsRuns(ctx, client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, waitActionsInterval)
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
