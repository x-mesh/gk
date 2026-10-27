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
	"github.com/x-mesh/gk/internal/ui"
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

func TestGHPickerRefreshRefetchesCachedQuery(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		items := make([]map[string]any, calls)
		for i := range items {
			items[i] = pickerSearchItem("x-mesh", "gk", i+1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": calls, "items": items})
	}))
	defer srv.Close()

	p := newGHPicker(&cobra.Command{Use: "pr"}, &ghapi.Client{APIBase: srv.URL}, cacheRunner(t, ""), &config.Config{}, true, "repo", githubSearchFilters{typeFilter: "is:pr", state: "open"})
	p.setScopeFromPrefix("repo:x-mesh/gk")

	if _, _, _, err := p.fetch(context.Background()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, _, _, err := p.fetch(context.Background()); err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("repeated query made %d API calls before refresh, want 1", calls)
	}

	p.refresh()
	issues, _, total, err := p.fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch after refresh: %v", err)
	}
	if calls != 2 || total != 2 || len(issues) != 2 {
		t.Fatalf("after refresh calls/total/len = %d/%d/%d, want 2/2/2 (a new search)", calls, total, len(issues))
	}

}

// TestGHPickerAAndRReloadInPlace: a and r must load through OnPress, not Exit,
// so the picker stays on screen while GitHub answers.
func TestGHPickerAAndRReloadInPlace(t *testing.T) {
	load := func() (ui.TablePickerReload, error) { return ui.TablePickerReload{}, nil }
	p := &ghPicker{}
	seen := map[string]bool{}
	for _, ex := range p.extras(load, load) {
		if ex.Key == "a" || ex.Key == "r" {
			seen[ex.Key] = true
			if ex.Exit || ex.OnPress == nil {
				t.Errorf("%s: Exit=%v OnPress=%v, want an in-place OnPress load", ex.Key, ex.Exit, ex.OnPress != nil)
			}
		}
	}
	if !seen["a"] || !seen["r"] {
		t.Fatalf("extras missing a or r: %v", seen)
	}
}

// TestGHPickerReloadCommitsOnApply: the toggle load leaves the picker's state
// alone until Apply, then switches state, rows, subtitle, and cache together.
func TestGHPickerReloadCommitsOnApply(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		n := 1
		if !strings.Contains(r.URL.Query().Get("q"), "is:open") {
			n = 2
		}
		items := make([]map[string]any, n)
		for i := range items {
			items[i] = pickerSearchItem("x-mesh", "gk", i+1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": n, "items": items})
	}))
	defer srv.Close()

	p := newGHPicker(&cobra.Command{Use: "pr"}, &ghapi.Client{APIBase: srv.URL}, cacheRunner(t, ""), &config.Config{}, true, "repo", githubSearchFilters{typeFilter: "is:pr", state: "open"})
	p.setScopeFromPrefix("repo:x-mesh/gk")
	ctx := context.Background()
	issues, _, _, err := p.fetch(ctx)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	_, byKey := p.pickerItems(issues)

	next := p.filters
	next.state = toggledState(next.state)
	reload, err := p.reload(ctx, "repo:x-mesh/gk", next, false, &byKey)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if p.filters.state != "open" || len(byKey) != 1 || len(p.fetchCache) != 1 {
		t.Fatalf("before Apply: state=%q rows=%d cache=%d, want open/1/1 untouched", p.filters.state, len(byKey), len(p.fetchCache))
	}
	if len(reload.Items) != 2 || !strings.Contains(reload.Subtitle, " · all · 2 item(s)") {
		t.Fatalf("reload items=%d subtitle=%q, want 2 rows under all", len(reload.Items), reload.Subtitle)
	}
	reload.Apply()
	if p.filters.state != "all" || len(byKey) != 2 || len(p.fetchCache) != 2 {
		t.Fatalf("after Apply: state=%q rows=%d cache=%d, want all/2/2", p.filters.state, len(byKey), len(p.fetchCache))
	}

	// Toggling back is answered from the cache; refresh asks GitHub again.
	back := p.filters
	back.state = toggledState(back.state)
	if _, err := p.reload(ctx, "repo:x-mesh/gk", back, false, &byKey); err != nil || calls != 2 {
		t.Fatalf("cached toggle: err=%v calls=%d, want no new search", err, calls)
	}
	if _, err := p.reload(ctx, "repo:x-mesh/gk", p.filters, true, &byKey); err != nil || calls != 3 {
		t.Fatalf("refresh: err=%v calls=%d, want a new search", err, calls)
	}
}

// TestGHPickerReloadFailureKeepsState: a failed search (rate limit) is an
// error for the picker to show, not a reason to change state or quit.
func TestGHPickerReloadFailureKeepsState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newGHPicker(&cobra.Command{Use: "pr"}, &ghapi.Client{APIBase: srv.URL}, cacheRunner(t, ""), &config.Config{}, true, "repo", githubSearchFilters{typeFilter: "is:pr", state: "open"})
	byKey := map[string]ghapi.Issue{"u1": {Number: 1}}
	next := p.filters
	next.state = "all"
	if _, err := p.reload(context.Background(), "repo:x-mesh/gk", next, false, &byKey); err == nil {
		t.Fatal("reload against a failing server returned no error")
	}
	if p.filters.state != "open" || len(byKey) != 1 {
		t.Fatalf("after failure: state=%q rows=%d, want open/1", p.filters.state, len(byKey))
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

// TestGHPickerRowsShowState: under state all, the ITEM cell tells an open PR
// from a merged or closed one instead of printing PR#N for every row.
func TestGHPickerRowsShowState(t *testing.T) {
	issues := []ghapi.Issue{
		{Number: 1, Title: "open", State: "open", IsPR: true, URL: "u1"},
		{Number: 2, Title: "merged", State: "closed", IsPR: true, Merged: true, URL: "u2"},
		{Number: 3, Title: "closed", State: "closed", IsPR: true, URL: "u3"},
	}
	items, _ := (&ghPicker{}).rows(issues)
	want := []string{"PR#1", "merged#2", "closed#3"}
	for i, it := range items {
		if it.Cells[0] != want[i] {
			t.Errorf("row %d ITEM = %q, want %q", i, it.Cells[0], want[i])
		}
		if !strings.HasPrefix(it.Display, want[i]+" ") {
			t.Errorf("row %d Display = %q, want prefix %q", i, it.Display, want[i])
		}
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
