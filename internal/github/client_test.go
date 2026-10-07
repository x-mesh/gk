package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func repoJSON(owner, name, desc string, private bool) map[string]any {
	return map[string]any{
		"name":        name,
		"full_name":   owner + "/" + name,
		"description": desc,
		"updated_at":  "2026-01-02T15:04:05Z",
		"private":     private,
		"owner":       map[string]any{"login": owner},
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func TestListReposOrgSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/x-mesh/repos" {
			t.Errorf("path = %q, want /orgs/x-mesh/repos", r.URL.Path)
		}
		writeJSON(t, w, []map[string]any{repoJSON("x-mesh", "gk", "git kit", false)})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	repos, err := c.ListRepos(context.Background(), "x-mesh")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "gk" || repos[0].Owner != "x-mesh" {
		t.Fatalf("repos = %+v", repos)
	}
}

func TestListReposFallsBackToUserWhenNotAnOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/JINWOO-J/repos":
			w.WriteHeader(http.StatusNotFound)
		case "/users/JINWOO-J/repos":
			writeJSON(t, w, []map[string]any{repoJSON("JINWOO-J", "playground", "", false)})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	repos, err := c.ListRepos(context.Background(), "JINWOO-J")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "playground" {
		t.Fatalf("repos = %+v", repos)
	}
}

func TestListReposUsesAuthenticatedUserReposForOwnLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer tok"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		switch r.URL.Path {
		case "/orgs/JINWOO-J/repos":
			w.WriteHeader(http.StatusNotFound)
		case "/user":
			writeJSON(t, w, map[string]any{"login": "JINWOO-J"})
		case "/user/repos":
			writeJSON(t, w, []map[string]any{
				repoJSON("JINWOO-J", "private-thing", "", true),
				repoJSON("someone-else", "not-mine", "", false),
			})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Token: "tok"}
	repos, err := c.ListRepos(context.Background(), "JINWOO-J")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "private-thing" || !repos[0].Private {
		t.Fatalf("repos = %+v, want only the caller's own repo", repos)
	}
}

func TestListReposPropagatesNonNotFoundError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	if _, err := c.ListRepos(context.Background(), "x-mesh"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestListReposPaginates(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := r.URL.Query().Get("page")
		if page == "1" {
			repos := make([]map[string]any, 100)
			for i := range repos {
				repos[i] = repoJSON("x-mesh", "repo1", "", false)
			}
			writeJSON(t, w, repos)
			return
		}
		writeJSON(t, w, []map[string]any{repoJSON("x-mesh", "repo2", "", false)})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	repos, err := c.ListRepos(context.Background(), "x-mesh")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 101 {
		t.Fatalf("len(repos) = %d, want 101 (100 + 1 across two pages)", len(repos))
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestPushedRepoPollerUsesETagAndKeepsLastResult(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if got := r.URL.Query().Get("sort"); got != "pushed" {
			t.Errorf("sort = %q", got)
		}
		if r.Header.Get("If-None-Match") == `"e1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"e1"`)
		repo := repoJSON("x-mesh", "gk", "", false)
		repo["pushed_at"] = "2026-10-07T02:11:48Z"
		writeJSON(t, w, []map[string]any{repo})
	}))
	defer srv.Close()
	p := &PushedRepoPoller{Client: &Client{APIBase: srv.URL}, Owner: "x-mesh", Limit: 5}

	repos, changed, err := p.Poll(context.Background())
	if err != nil || !changed || len(repos) != 1 || repos[0].PushedAt.IsZero() {
		t.Fatalf("first poll: repos=%+v changed=%v err=%v", repos, changed, err)
	}
	repos, changed, err = p.Poll(context.Background())
	if err != nil || changed || len(repos) != 1 || repos[0].Name != "gk" {
		t.Fatalf("second poll: repos=%+v changed=%v err=%v", repos, changed, err)
	}
	if paths[0] != "/orgs/x-mesh/repos?type=all&sort=pushed&direction=desc&per_page=5" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestPushedRepoPollerFallsBackToUserAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/JINWOO-J/repos":
			w.WriteHeader(http.StatusNotFound)
		case "/users/JINWOO-J/repos":
			writeJSON(t, w, []map[string]any{repoJSON("JINWOO-J", "playground", "", false)})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &PushedRepoPoller{Client: &Client{APIBase: srv.URL}, Owner: "JINWOO-J"}

	repos, _, err := p.Poll(context.Background())
	if err != nil || len(repos) != 1 || repos[0].Name != "playground" {
		t.Fatalf("repos=%+v err=%v", repos, err)
	}
}

func TestAsRateLimitIgnoresPermissionFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	forbidden := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"42"}}}
	if rl := asRateLimit(forbidden, now); rl != nil {
		t.Fatalf("403 with quota left = %v, want nil", rl)
	}
	secondary := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Retry-After": {"30"}}}
	if rl := asRateLimit(secondary, now); rl == nil || !rl.Reset.Equal(now.Add(30*time.Second)) {
		t.Fatalf("Retry-After = %v", rl)
	}
}
