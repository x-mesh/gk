package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListWorkflowRunsFiltersDisplayNameAndUsesToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("head_sha"); got != "abc123" {
			t.Errorf("head_sha = %q", got)
		}
		writeJSON(t, w, map[string]any{"workflow_runs": []map[string]any{
			{"id": 11, "name": "Pages", "head_sha": "abc123", "status": "queued", "html_url": "https://example.test/11"},
			{"id": 12, "name": "CI", "head_sha": "abc123", "status": "completed", "conclusion": "success", "html_url": "https://example.test/12"},
		}})
	}))
	defer srv.Close()

	runs, err := (&Client{APIBase: srv.URL, Token: "token"}).ListWorkflowRuns(context.Background(), "x-mesh", "headroom", "abc123", "Pages")
	if err != nil {
		t.Fatalf("ListWorkflowRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != 11 || runs[0].Name != "Pages" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestGetWorkflowRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/x-mesh/headroom/actions/runs/11"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		writeJSON(t, w, map[string]any{"id": 11, "name": "Pages", "status": "completed", "conclusion": "success", "html_url": "https://example.test/11"})
	}))
	defer srv.Close()

	run, err := (&Client{APIBase: srv.URL}).GetWorkflowRun(context.Background(), "x-mesh", "headroom", 11)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if run.Status != "completed" || run.Conclusion != "success" {
		t.Fatalf("run = %+v", run)
	}
}
