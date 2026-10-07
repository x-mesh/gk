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
	if len(runs) != 2 || client.getCalls != 2 || runs[1].Conclusion != "skipped" {
		t.Fatalf("runs = %+v getCalls = %d", runs, client.getCalls)
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
	if len(runs) != 1 || runs[0].ID != 11 || client.getCalls != 1 {
		t.Fatalf("runs = %+v, getCalls = %d", runs, client.getCalls)
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
