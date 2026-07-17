package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
