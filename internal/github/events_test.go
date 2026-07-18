package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListRepoEventsParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/x-mesh/gk/events" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("X-Poll-Interval", "60")
		writeJSON(t, w, []map[string]any{
			{
				"id": "1002", "type": "PullRequestEvent", "created_at": "2026-07-18T10:00:00Z",
				"actor":   map[string]any{"login": "octocat"},
				"payload": map[string]any{"action": "closed", "pull_request": map[string]any{"number": 42, "title": "merge me", "merged": true, "base": map[string]any{"ref": "main"}, "head": map[string]any{"ref": "feat/x"}}},
			},
			{
				"id": "1001", "type": "IssuesEvent", "created_at": "2026-07-18T09:00:00Z",
				"actor":   map[string]any{"login": "hubot"},
				"payload": map[string]any{"action": "labeled", "issue": map[string]any{"number": 7, "title": "a bug"}, "label": map[string]any{"name": "bug"}},
			},
		})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	events, etag, notModified, poll, err := c.ListRepoEvents(context.Background(), "x-mesh", "gk", "")
	if err != nil {
		t.Fatalf("ListRepoEvents: %v", err)
	}
	if notModified {
		t.Fatal("first call should not be 304")
	}
	if etag != `"abc123"` || poll != 60 {
		t.Errorf("etag=%q poll=%d", etag, poll)
	}
	if len(events) != 2 {
		t.Fatalf("len(events)=%d, want 2", len(events))
	}
	pr := events[0]
	if pr.Type != "PullRequestEvent" || pr.Action != "closed" || !pr.PRMerged || pr.PRNumber != 42 || pr.PRBase != "main" || pr.Actor != "octocat" {
		t.Fatalf("pr event = %+v", pr)
	}
	iss := events[1]
	if iss.Type != "IssuesEvent" || iss.Action != "labeled" || iss.Label != "bug" || iss.IssueNumber != 7 {
		t.Fatalf("issue event = %+v", iss)
	}
}

func TestListRepoEventsNotModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"abc123"` {
			t.Errorf("If-None-Match = %q, want the prior etag", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	events, etag, notModified, _, err := c.ListRepoEvents(context.Background(), "x-mesh", "gk", `"abc123"`)
	if err != nil {
		t.Fatalf("ListRepoEvents: %v", err)
	}
	if !notModified || len(events) != 0 {
		t.Fatalf("expected 304 with no events, got notModified=%v len=%d", notModified, len(events))
	}
	if etag != `"abc123"` {
		t.Errorf("304 should keep the prior etag, got %q", etag)
	}
}

func TestEventIDLess(t *testing.T) {
	if !EventIDLess("1001", "1002") {
		t.Error("1001 < 1002")
	}
	if EventIDLess("1002", "1001") {
		t.Error("1002 not < 1001")
	}
	if EventIDLess("5", "5") {
		t.Error("equal ids are not less")
	}
	// large ids beyond int32
	if !EventIDLess("48000000000", "48000000001") {
		t.Error("large numeric ids compare numerically")
	}
}
