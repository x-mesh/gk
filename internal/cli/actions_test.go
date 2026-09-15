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
	getCalls int
}

func (f *fakeActionsClient) ListWorkflowRuns(_ context.Context, _, _, _, _ string) ([]ghapi.WorkflowRun, error) {
	return f.runs, nil
}

func (f *fakeActionsClient) GetWorkflowRun(_ context.Context, _, _ string, _ int64) (ghapi.WorkflowRun, error) {
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

func TestWatchActionsRunRequiresSelection(t *testing.T) {
	client := &fakeActionsClient{runs: []ghapi.WorkflowRun{{ID: 1}, {ID: 2}}}
	_, err := watchActionsRun(context.Background(), client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "--workflow or --run") {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchActionsRunPollsToSuccess(t *testing.T) {
	client := &fakeActionsClient{
		runs: []ghapi.WorkflowRun{{ID: 11, Status: "in_progress"}},
		updates: []ghapi.WorkflowRun{{
			ID: 11, Name: "Pages", Status: "completed", Conclusion: "success", HTMLURL: "https://example.test/11",
		}},
	}
	run, err := watchActionsRun(context.Background(), client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatalf("watchActionsRun: %v", err)
	}
	if run.ID != 11 || client.getCalls != 1 {
		t.Fatalf("run = %+v, getCalls = %d", run, client.getCalls)
	}
}

func TestWatchActionsRunReturnsFailedConclusion(t *testing.T) {
	client := &fakeActionsClient{runs: []ghapi.WorkflowRun{{
		ID: 11, Status: "completed", Conclusion: "failure", HTMLURL: "https://example.test/11",
	}}}
	_, err := watchActionsRun(context.Background(), client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, func(context.Context, time.Duration) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "failure") || !strings.Contains(err.Error(), "https://example.test/11") {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchActionsRunStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeActionsClient{runs: []ghapi.WorkflowRun{{ID: 11, Status: "in_progress"}}}
	_, err := watchActionsRun(ctx, client, "x-mesh", "headroom", "abc", actionsWatchOptions{interval: time.Millisecond}, waitActionsInterval)
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
