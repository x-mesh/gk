package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// ghScopeCmd builds a command carrying the shared scope flags, with --org
// optionally set (empty orgVal + set=true reproduces a bare `--org`).
func ghScopeCmd(t *testing.T, set bool, orgVal string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "pr"}
	addGitHubScopeFlags(cmd)
	if set {
		if err := cmd.Flags().Set("org", orgVal); err != nil {
			t.Fatalf("set --org: %v", err)
		}
	}
	return cmd
}

// userTypeServer returns an httptest server answering /users/{name} with the
// given GitHub account type ("Organization" or "User").
func userTypeServer(t *testing.T, accountType string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/users/") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"` + accountType + `"}`))
	}))
}

// TestResolveGitHubScopeOrgExplicit: `--org acme` → org: qualifier.
func TestResolveGitHubScopeOrgExplicit(t *testing.T) {
	srv := userTypeServer(t, "Organization")
	defer srv.Close()
	client := &ghapi.Client{APIBase: srv.URL}
	cmd := ghScopeCmd(t, true, "acme")

	prefix, _, err := resolveGitHubScope(context.Background(), cmd, nil, config.Config{}, &git.FakeRunner{}, client)
	if err != nil {
		t.Fatalf("resolveGitHubScope: %v", err)
	}
	if prefix != "org:acme" {
		t.Errorf("prefix = %q, want org:acme", prefix)
	}
}

// TestResolveGitHubScopeUserQualifier: a personal account flips to user:.
func TestResolveGitHubScopeUserQualifier(t *testing.T) {
	srv := userTypeServer(t, "User")
	defer srv.Close()
	client := &ghapi.Client{APIBase: srv.URL}
	cmd := ghScopeCmd(t, true, "octocat")

	prefix, _, err := resolveGitHubScope(context.Background(), cmd, nil, config.Config{}, &git.FakeRunner{}, client)
	if err != nil {
		t.Fatalf("resolveGitHubScope: %v", err)
	}
	if prefix != "user:octocat" {
		t.Errorf("prefix = %q, want user:octocat", prefix)
	}
}

// TestResolveGitHubScopeOrgFromConfig: bare --org falls back to github.owner.
func TestResolveGitHubScopeOrgFromConfig(t *testing.T) {
	srv := userTypeServer(t, "Organization")
	defer srv.Close()
	client := &ghapi.Client{APIBase: srv.URL}
	cmd := ghScopeCmd(t, true, orgFlagSentinel) // bare --org

	cfg := config.Config{GitHub: config.GitHubConfig{Owner: "acme"}}
	prefix, _, err := resolveGitHubScope(context.Background(), cmd, nil, cfg, &git.FakeRunner{}, client)
	if err != nil {
		t.Fatalf("resolveGitHubScope: %v", err)
	}
	if prefix != "org:acme" {
		t.Errorf("prefix = %q, want org:acme (from config)", prefix)
	}
}

// TestResolveGitHubScopeOrgFromOrigin: bare --org, no config → origin's owner.
func TestResolveGitHubScopeOrgFromOrigin(t *testing.T) {
	srv := userTypeServer(t, "Organization")
	defer srv.Close()
	client := &ghapi.Client{APIBase: srv.URL}
	cmd := ghScopeCmd(t, true, orgFlagSentinel)
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote get-url origin": {Stdout: "git@github.com:x-mesh/gk.git\n"},
	}}

	prefix, _, err := resolveGitHubScope(context.Background(), cmd, nil, config.Config{}, runner, client)
	if err != nil {
		t.Fatalf("resolveGitHubScope: %v", err)
	}
	if prefix != "org:x-mesh" {
		t.Errorf("prefix = %q, want org:x-mesh (from origin)", prefix)
	}
}

// TestResolveGitHubScopeOrgSpaceForm: `--org acme` space form (arg via NoOptDefVal).
func TestResolveGitHubScopeOrgSpaceForm(t *testing.T) {
	srv := userTypeServer(t, "Organization")
	defer srv.Close()
	client := &ghapi.Client{APIBase: srv.URL}
	cmd := ghScopeCmd(t, true, orgFlagSentinel) // bare --org; name arrives as arg

	prefix, _, err := resolveGitHubScope(context.Background(), cmd, []string{"acme"}, config.Config{}, &git.FakeRunner{}, client)
	if err != nil {
		t.Fatalf("resolveGitHubScope: %v", err)
	}
	if prefix != "org:acme" {
		t.Errorf("prefix = %q, want org:acme (space form)", prefix)
	}
}

// TestResolveGitHubScopeOrgNoOwner: bare --org, no config, no origin → error.
func TestResolveGitHubScopeOrgNoOwner(t *testing.T) {
	cmd := ghScopeCmd(t, true, orgFlagSentinel)
	// FakeRunner with no responses → remoteURL empty → no origin owner.
	_, _, err := resolveGitHubScope(context.Background(), cmd, nil, config.Config{}, &git.FakeRunner{}, &ghapi.Client{})
	if err == nil {
		t.Fatal("expected an error when no org can be resolved")
	}
}

// TestRunGitHubListRejectsBarePositional guards L2: `gk pr acme` (positional,
// no --org) errors instead of silently listing the current repo.
func TestRunGitHubListRejectsBarePositional(t *testing.T) {
	cmd := ghScopeCmd(t, false, "")
	err := runGitHubList(cmd, []string{"acme"}, true)
	if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Errorf("expected unexpected-argument error, got %v", err)
	}
}

func TestBuildSearchQuery(t *testing.T) {
	cases := []struct {
		name       string
		prefix     string
		typeFilter string
		state      string
		mine       bool
		want       string
	}{
		{"repo pr open", "repo:x-mesh/gk", "is:pr", "open", false, "repo:x-mesh/gk is:pr is:open"},
		{"org issue all mine", "org:acme", "is:issue", "all", true, "org:acme is:issue author:@me"},
		{"repo pr closed", "repo:x-mesh/gk", "is:pr", "closed", false, "repo:x-mesh/gk is:pr is:closed"},
		{"inbox untyped open", "involves:@me", "", "open", false, "involves:@me is:open"},
		{"unknown state defaults open", "repo:x-mesh/gk", "is:pr", "weird", false, "repo:x-mesh/gk is:pr is:open"},
	}
	for _, tc := range cases {
		if got := buildSearchQuery(tc.prefix, tc.typeFilter, tc.state, tc.mine); got != tc.want {
			t.Errorf("%s: buildSearchQuery = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestResolveGitHubScopeRepo covers the no-flag path: scope from origin.
func TestResolveGitHubScopeRepo(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote get-url origin": {Stdout: "git@github.com:x-mesh/gk.git\n"},
	}}
	cmd := &cobra.Command{Use: "pr"}
	addGitHubScopeFlags(cmd)

	// --org not passed → repo scope; client is unused on this path.
	prefix, label, err := resolveGitHubScope(context.Background(), cmd, nil, config.Config{}, runner, nil)
	if err != nil {
		t.Fatalf("resolveGitHubScope: %v", err)
	}
	if prefix != "repo:x-mesh/gk" || label != prefix {
		t.Fatalf("prefix = %q, label = %q, want repo:x-mesh/gk", prefix, label)
	}
}

// TestResolveGitHubScopeRepoNonGitHub errors clearly on a non-github origin.
func TestResolveGitHubScopeRepoNonGitHub(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote get-url origin": {Stdout: "git@gitlab.com:x-mesh/gk.git\n"},
	}}
	cmd := &cobra.Command{Use: "pr"}
	addGitHubScopeFlags(cmd)

	if _, _, err := resolveGitHubScope(context.Background(), cmd, nil, config.Config{}, runner, nil); err == nil {
		t.Fatal("expected an error for a non-github.com origin")
	}
}

func TestRenderGitHubTableAligns(t *testing.T) {
	// Two repos (org scope) → repo column shown; verify columns align on the
	// PLAIN text even though the output carries ANSI color.
	issues := []ghapi.Issue{
		{Number: 4, Title: "short", State: "open", Owner: "x-mesh", Repo: "gk", Author: "a", IsPR: true, Labels: []string{"bug", "docs"}},
		{Number: 128, Title: "a much longer title here", State: "open", Owner: "x-mesh", Repo: "term-mesh", Author: "bb", IsPR: false},
	}
	var buf strings.Builder
	renderGitHubTable(&buf, "org:x-mesh", issues, false, false)
	out := buf.String()
	if !strings.Contains(out, "x-mesh/gk") || !strings.Contains(out, "PR#4") || !strings.Contains(out, "issue#128") {
		t.Fatalf("table missing expected glued ref tokens:\n%s", out)
	}
	if !strings.Contains(out, "bug, docs") {
		t.Errorf("labels should appear in the table, got:\n%s", out)
	}
	if strings.Contains(out, "repo:") {
		t.Errorf("header should humanize the scope, got:\n%s", out)
	}
}

func TestRenderGitHubTableURLColumn(t *testing.T) {
	issues := []ghapi.Issue{
		{Number: 4, Title: "t", State: "open", Owner: "x-mesh", Repo: "gk", Author: "a", IsPR: true, URL: "https://github.com/x-mesh/gk/pull/4"},
	}
	// without --url: no URL in output
	var plain strings.Builder
	renderGitHubTable(&plain, "repo:x-mesh/gk", issues, false, false)
	if strings.Contains(plain.String(), "https://github.com") {
		t.Errorf("URL should not appear without --url:\n%s", plain.String())
	}
	// with --url: the bare URL appears (terminals auto-link it)
	var withURL strings.Builder
	renderGitHubTable(&withURL, "repo:x-mesh/gk", issues, false, true)
	if !strings.Contains(withURL.String(), "https://github.com/x-mesh/gk/pull/4") {
		t.Errorf("--url should show the full URL:\n%s", withURL.String())
	}
}

func TestHumanGitHubScope(t *testing.T) {
	if got := humanGitHubScope("repo:x-mesh/gk"); got != "x-mesh/gk" {
		t.Errorf("repo scope should drop prefix, got %q", got)
	}
	if got := humanGitHubScope("org:acme"); got != "org:acme" {
		t.Errorf("org scope should be kept, got %q", got)
	}
}

func TestOSC8Link(t *testing.T) {
	// colorOff() is true under `go test` (no TTY), so links degrade to plain —
	// exactly the pipe/file contract; escapes must never leak.
	if got := osc8Link("https://x/1", "PR#4"); got != "PR#4" {
		t.Errorf("non-TTY should be plain, got %q", got)
	}
	if got := osc8Link("", "PR#4"); got != "PR#4" {
		t.Errorf("empty url should be plain, got %q", got)
	}
}
