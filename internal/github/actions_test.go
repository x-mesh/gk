package github

import (
	"context"
	"errors"
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

func TestListRecentWorkflowRunsIsConditional(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Has("head_sha") {
			t.Errorf("unexpected head_sha filter: %s", r.URL.RawQuery)
		}
		if r.Header.Get("If-None-Match") == `W/"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"v1"`)
		writeJSON(t, w, map[string]any{"workflow_runs": []map[string]any{
			{"id": 7, "name": "CI", "status": "in_progress", "head_branch": "develop", "event": "push", "created_at": "2026-10-07T02:11:50Z"},
		}})
	}))
	defer srv.Close()
	c := &Client{APIBase: srv.URL}

	runs, etag, notModified, err := c.ListRecentWorkflowRuns(context.Background(), "x-mesh", "gk", "")
	if err != nil || notModified || etag != `W/"v1"` {
		t.Fatalf("first call: etag=%q notModified=%v err=%v", etag, notModified, err)
	}
	if len(runs) != 1 || runs[0].HeadBranch != "develop" || runs[0].Event != "push" || runs[0].CreatedAt.IsZero() {
		t.Fatalf("runs = %+v", runs)
	}
	runs, etag, notModified, err = c.ListRecentWorkflowRuns(context.Background(), "x-mesh", "gk", etag)
	if err != nil || !notModified || runs != nil || etag != `W/"v1"` {
		t.Fatalf("second call: runs=%v etag=%q notModified=%v err=%v", runs, etag, notModified, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestListRecentWorkflowRunsReportsRateLimitReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1791339600")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, _, _, err := (&Client{APIBase: srv.URL}).ListRecentWorkflowRuns(context.Background(), "x-mesh", "gk", "")
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Reset.Unix() != 1791339600 {
		t.Fatalf("err = %v", err)
	}
}
