package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func prJSON(number int, title, state string, draft bool) map[string]any {
	return map[string]any{
		"number":   number,
		"title":    title,
		"state":    state,
		"draft":    draft,
		"html_url": "https://github.com/x-mesh/gk/pull/42",
	}
}

func TestCreatePullRequestSucceeds(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/x-mesh/gk/pulls" {
			t.Errorf("request = %s %s, want POST /repos/x-mesh/gk/pulls", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", auth)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(t, w, prJSON(42, "feat: pr create", "open", true))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Token: "tok"}
	pr, err := c.CreatePullRequest(context.Background(), "x-mesh", "gk", NewPullRequest{
		Title: "feat: pr create",
		Body:  "the body",
		Head:  "feat/pr-create",
		Base:  "develop",
		Draft: true,
	})
	if err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	if pr.Number != 42 || pr.URL != "https://github.com/x-mesh/gk/pull/42" || !pr.Draft {
		t.Fatalf("pr = %+v", pr)
	}
	for key, want := range map[string]any{
		"title": "feat: pr create",
		"body":  "the body",
		"head":  "feat/pr-create",
		"base":  "develop",
		"draft": true,
	} {
		if got[key] != want {
			t.Errorf("request %s = %v, want %v", key, got[key], want)
		}
	}
}

// An empty body must not be sent at all: GitHub renders a literal empty
// description differently from an absent one.
func TestCreatePullRequestOmitsEmptyOptionalFields(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusCreated)
		writeJSON(t, w, prJSON(7, "t", "open", false))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Token: "tok"}
	if _, err := c.CreatePullRequest(context.Background(), "x-mesh", "gk", NewPullRequest{
		Title: "t", Head: "h", Base: "b",
	}); err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	if _, ok := got["body"]; ok {
		t.Errorf("body sent = %v, want absent", got["body"])
	}
	if _, ok := got["draft"]; ok {
		t.Errorf("draft sent = %v, want absent", got["draft"])
	}
}

func TestCreatePullRequestNeedsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s — the token check should short-circuit", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	if _, err := c.CreatePullRequest(context.Background(), "x-mesh", "gk", NewPullRequest{Head: "h", Base: "b"}); err == nil {
		t.Fatal("CreatePullRequest with no token: want error, got nil")
	}
}

// The two recoverable 422s must be distinguishable by the caller — one is
// "look at the existing PR", the other is "you have nothing to propose".
func TestCreatePullRequestClassifies422(t *testing.T) {
	cases := []struct {
		name    string
		detail  string
		wantErr error
	}{
		{"already exists", "A pull request already exists for x-mesh:feat/pr-create.", ErrPRAlreadyExists},
		{"no commits", "No commits between develop and feat/pr-create", ErrNoCommitsBetween},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				writeJSON(t, w, map[string]any{
					"message": "Validation Failed",
					"errors":  []map[string]any{{"resource": "PullRequest", "code": "custom", "message": tc.detail}},
				})
			}))
			defer srv.Close()

			c := &Client{APIBase: srv.URL, Token: "tok"}
			_, err := c.CreatePullRequest(context.Background(), "x-mesh", "gk", NewPullRequest{Head: "h", Base: "b"})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// An unclassified 422 must still carry GitHub's specific reason, which lives
// in errors[].message rather than the generic top-level "Validation Failed".
func TestCreatePullRequestSurfacesValidationDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeJSON(t, w, map[string]any{
			"message": "Validation Failed",
			"errors":  []map[string]any{{"resource": "PullRequest", "field": "base", "code": "invalid"}},
		})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Token: "tok"}
	_, err := c.CreatePullRequest(context.Background(), "x-mesh", "gk", NewPullRequest{Head: "h", Base: "nope"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if want := "base invalid"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to mention %q", err, want)
	}
}

// A branch can have several open PRs, one per base. The lookup must filter on
// base too, or a collision on head→develop reports the PR for head→main.
func TestFindOpenPRFiltersOnHeadAndBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("head"); got != "x-mesh:feat/pr-create" {
			t.Errorf("head = %q, want x-mesh:feat/pr-create", got)
		}
		if got := r.URL.Query().Get("base"); got != "develop" {
			t.Errorf("base = %q, want develop", got)
		}
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state = %q, want open", got)
		}
		writeJSON(t, w, []map[string]any{prJSON(42, "existing", "open", false)})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Token: "tok"}
	pr, found, err := c.FindOpenPR(context.Background(), "x-mesh", "gk", "x-mesh:feat/pr-create", "develop")
	if err != nil {
		t.Fatalf("FindOpenPR: %v", err)
	}
	if !found || pr.Number != 42 {
		t.Fatalf("pr = %+v, found = %v", pr, found)
	}
}

// An empty base means "any base" — the filter must be omitted, not sent empty.
func TestFindOpenPROmitsEmptyBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["base"]; ok {
			t.Errorf("base filter sent = %q, want absent", r.URL.Query().Get("base"))
		}
		writeJSON(t, w, []map[string]any{prJSON(42, "existing", "open", false)})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Token: "tok"}
	if _, _, err := c.FindOpenPR(context.Background(), "x-mesh", "gk", "x-mesh:feat/pr-create", ""); err != nil {
		t.Fatalf("FindOpenPR: %v", err)
	}
}

// No match is a normal answer, not a failure.
func TestFindOpenPRReportsNoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{})
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Token: "tok"}
	_, found, err := c.FindOpenPR(context.Background(), "x-mesh", "gk", "x-mesh:none", "main")
	if err != nil {
		t.Fatalf("FindOpenPR: %v", err)
	}
	if found {
		t.Fatal("found = true, want false")
	}
}
