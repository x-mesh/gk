package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// searchItem builds one /search/issues item. A non-empty prURL adds the
// pull_request object that marks the row as a PR.
func searchItem(owner, repo string, number int, title, prURL string) map[string]any {
	item := map[string]any{
		"number":         number,
		"title":          title,
		"state":          "open",
		"html_url":       "https://github.com/" + owner + "/" + repo + "/issues/" + strconv.Itoa(number),
		"repository_url": "https://api.github.com/repos/" + owner + "/" + repo,
		"updated_at":     "2026-01-02T15:04:05Z",
		"user":           map[string]any{"login": "octocat"},
		"labels":         []map[string]any{{"name": "bug"}},
	}
	if prURL != "" {
		item["pull_request"] = map[string]any{"url": prURL}
	}
	return item
}

func TestSearchIssuesParsesPRsAndIssues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/issues" {
			t.Errorf("path = %q, want /search/issues", r.URL.Path)
		}
		if q := r.URL.Query().Get("q"); q != "repo:x-mesh/gk is:open" {
			t.Errorf("q = %q", q)
		}
		writeJSON(t, w, map[string]any{
			"total_count": 2,
			"items": []map[string]any{
				searchItem("x-mesh", "gk", 42, "a pull request", "https://api.github.com/repos/x-mesh/gk/pulls/42"),
				searchItem("x-mesh", "gk", 7, "an issue", ""),
			},
		})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	issues, err := c.SearchIssues(context.Background(), "repo:x-mesh/gk is:open")
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(issues))
	}
	pr := issues[0]
	if !pr.IsPR || pr.Number != 42 || pr.Owner != "x-mesh" || pr.Repo != "gk" || pr.Author != "octocat" {
		t.Fatalf("pr row = %+v", pr)
	}
	if len(pr.Labels) != 1 || pr.Labels[0] != "bug" {
		t.Fatalf("pr labels = %v", pr.Labels)
	}
	if issues[1].IsPR {
		t.Fatalf("row 1 should be an issue, not a PR: %+v", issues[1])
	}
}

func TestSearchIssuesPaginates(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := r.URL.Query().Get("page")
		items := make([]map[string]any, 0, 100)
		if page == "1" {
			for i := 0; i < 100; i++ {
				items = append(items, searchItem("x-mesh", "gk", i+1, "row", ""))
			}
		} else {
			items = append(items, searchItem("x-mesh", "gk", 999, "last", ""))
		}
		writeJSON(t, w, map[string]any{"total_count": 101, "items": items})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	issues, err := c.SearchIssues(context.Background(), "org:x-mesh is:open")
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != 101 {
		t.Fatalf("len(issues) = %d, want 101", len(issues))
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestSearchIssuesRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"rate limit exceeded"}`))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	_, err := c.SearchIssues(context.Background(), "org:x-mesh is:open")
	if err == nil {
		t.Fatal("expected a rate-limit error, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "rate-limited") {
		t.Fatalf("error = %q, want it to mention rate-limited", got)
	}
}

func TestOwnerType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/x-mesh" {
			t.Errorf("path = %q, want /users/x-mesh", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{"type": "Organization"})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	typ, err := c.OwnerType(context.Background(), "x-mesh")
	if err != nil {
		t.Fatalf("OwnerType: %v", err)
	}
	if typ != "Organization" {
		t.Fatalf("type = %q, want Organization", typ)
	}
}

// TestOwnerTypeUser covers the User branch — the value that flips
// resolveGitHubScope to the user: qualifier.
func TestOwnerTypeUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"type": "User"})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	typ, err := c.OwnerType(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("OwnerType: %v", err)
	}
	if typ != "User" {
		t.Fatalf("type = %q, want User", typ)
	}
}

// TestSearchCount reads total_count without paging the rows in — the path
// `gk context --include=github` relies on.
func TestSearchCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != "1" {
			t.Errorf("per_page = %q, want 1 (count should not page rows)", got)
		}
		writeJSON(t, w, map[string]any{
			"total_count": 42,
			"items":       []map[string]any{searchItem("x-mesh", "gk", 1, "one", "")},
		})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	n, err := c.SearchCount(context.Background(), "repo:x-mesh/gk is:open is:pr")
	if err != nil {
		t.Fatalf("SearchCount: %v", err)
	}
	if n != 42 {
		t.Fatalf("count = %d, want 42", n)
	}
}

// TestSearchIssues429 covers the 429 rate-limit branch.
func TestSearchIssues429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message":"too many"}`))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	_, err := c.SearchIssues(context.Background(), "org:x-mesh is:open")
	if err == nil || !strings.Contains(err.Error(), "rate-limited") {
		t.Fatalf("429 should be a rate-limit error, got %v", err)
	}
}

// TestSearchIssues403PermissionNotRateLimit: a 403 WITH quota remaining is a
// permission error, not a rate limit — it must fall through to the generic
// body, not be mislabeled rate-limited.
func TestSearchIssues403PermissionNotRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "50")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"forbidden"}`))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	_, err := c.SearchIssues(context.Background(), "org:secret is:open")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "rate-limited") {
		t.Fatalf("403 with quota remaining must NOT be labeled rate-limited, got %v", err)
	}
}
