package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	ghapi "github.com/x-mesh/gk/internal/github"
)

func pickerSearchItem(owner, repo string, number int) map[string]any {
	return map[string]any{
		"number":         number,
		"title":          fmt.Sprintf("row %d", number),
		"state":          "open",
		"html_url":       fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, number),
		"repository_url": fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo),
		"updated_at":     "2026-01-02T15:04:05Z",
		"user":           map[string]any{"login": "octocat"},
		"pull_request":   map[string]any{"url": "https://api.github.com/pulls/1"},
	}
}

func TestGHPickerFetchIsBoundedCachedAndWarmsExactCount(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Errorf("page = %q, want one bounded page", got)
		}
		total := 250
		if !strings.Contains(r.URL.Query().Get("q"), "is:open") {
			total = 300
		}
		items := make([]map[string]any, 100)
		for i := range items {
			items[i] = pickerSearchItem("x-mesh", "gk", i+1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": total, "items": items})
	}))
	defer srv.Close()

	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	cfg := &config.Config{GitHub: config.GitHubConfig{Counts: config.GitHubCountsConfig{WarmOnList: true}}}
	p := newGHPicker(&cobra.Command{Use: "pr"}, &ghapi.Client{APIBase: srv.URL}, runner, cfg, true, "repo", githubSearchFilters{typeFilter: "is:pr", state: "open"})
	p.setScopeFromPrefix("repo:x-mesh/gk")

	issues, _, total, err := p.fetch(context.Background())
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(issues) != ghInteractiveLimit || total != 250 {
		t.Fatalf("len/total = %d/%d, want %d/250", len(issues), total, ghInteractiveLimit)
	}
	if _, _, _, err := p.fetch(context.Background()); err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("same query made %d API calls, want 1", calls)
	}

	cached, ok := readGitHubCounts(context.Background(), runner, "x-mesh/gk")
	if !ok || cached.OpenPRs != 250 {
		t.Fatalf("interactive warm count = %+v, want exact total_count 250", cached)
	}

	p.filters.state = "all"
	if _, _, total, err = p.fetch(context.Background()); err != nil || total != 300 {
		t.Fatalf("changed query fetch total/error = %d/%v", total, err)
	}
	p.filters.state = "open"
	if _, _, _, err := p.fetch(context.Background()); err != nil {
		t.Fatalf("return to cached query: %v", err)
	}
	if calls != 2 {
		t.Fatalf("query cache calls = %d, want one call per distinct query", calls)
	}
}

func TestGHPickerExplicitPickUsesNonTTYNumberedFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 2,
			"items": []map[string]any{
				pickerSearchItem("x-mesh", "gk", 1),
				pickerSearchItem("other", "repo", 2),
			},
		})
	}))
	defer srv.Close()

	cmd := &cobra.Command{Use: "pr"}
	cmd.SetIn(strings.NewReader("2\n"))
	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	p := newGHPicker(cmd, &ghapi.Client{APIBase: srv.URL}, cacheRunner(t, ""), &config.Config{}, true, "inbox", githubSearchFilters{typeFilter: "is:pr", state: "open"})
	opened := ""
	p.openURL = func(target string) error {
		opened = target
		return nil
	}

	if err := p.runForEnvironment(context.Background(), true); err != nil {
		t.Fatalf("explicit fallback pick: %v", err)
	}
	if opened != "https://github.com/other/repo/pull/2" {
		t.Fatalf("opened = %q, want numbered selection 2", opened)
	}
	if got := stderr.String(); !strings.Contains(got, " 1) PR#1 row 1") || !strings.Contains(got, " 2) PR#2 row 2") {
		t.Fatalf("numbered fallback was not rendered:\n%s", got)
	}
}

func TestShouldRunGHPickerEnvironmentPolicy(t *testing.T) {
	cases := []struct {
		name                        string
		explicitPick, list, prompts bool
		want                        bool
	}{
		{name: "non-TTY default stays static", want: false},
		{name: "explicit pick overrides non-TTY", explicitPick: true, want: true},
		{name: "TTY default is interactive", prompts: true, want: true},
		{name: "list forces static on TTY", list: true, prompts: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRunGHPicker(tc.explicitPick, tc.list, tc.prompts); got != tc.want {
				t.Fatalf("shouldRunGHPicker = %v, want %v", got, tc.want)
			}
		})
	}
}
