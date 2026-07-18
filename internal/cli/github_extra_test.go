package cli

import (
	"strings"
	"testing"
)

func TestQuoteQualifier(t *testing.T) {
	if got := quoteQualifier("bug"); got != "bug" {
		t.Errorf("single word should not be quoted, got %q", got)
	}
	if got := quoteQualifier("good first issue"); got != `"good first issue"` {
		t.Errorf("multi-word should be quoted, got %q", got)
	}
}

func TestGitHubSearchURL(t *testing.T) {
	pr := gitHubSearchURL("repo:x-mesh/gk is:pr is:open")
	if !strings.Contains(pr, "type=pullrequests") {
		t.Errorf("PR query should use the pullrequests tab: %s", pr)
	}
	if !strings.Contains(pr, "q=repo%3Ax-mesh%2Fgk+is%3Apr+is%3Aopen") {
		t.Errorf("query should be URL-encoded: %s", pr)
	}
	iss := gitHubSearchURL("repo:x-mesh/gk is:issue is:open")
	if !strings.Contains(iss, "type=issues") {
		t.Errorf("issue query should use the issues tab: %s", iss)
	}
}

func TestGitHubFiltersIsPlainOpen(t *testing.T) {
	if !(githubSearchFilters{state: "open"}).isPlainOpen() {
		t.Error("bare open should be plain")
	}
	if (githubSearchFilters{state: "open", mine: true}).isPlainOpen() {
		t.Error("--mine is not plain")
	}
	if (githubSearchFilters{state: "open", labels: []string{"bug"}}).isPlainOpen() {
		t.Error("a label filter is not plain")
	}
	if (githubSearchFilters{state: "closed"}).isPlainOpen() {
		t.Error("closed is not plain-open")
	}
}

// TestSetScopeFromPrefix guards the bug where the interactive picker re-derived
// the org from config/origin and silently dropped an explicit `--org <name>`.
func TestSetScopeFromPrefix(t *testing.T) {
	cases := []struct {
		prefix     string
		wantKind   string
		wantOrg    string
		wantIsUser bool
	}{
		{"org:jinwoo-j", "org", "jinwoo-j", false},
		{"user:jinwoo-j", "org", "jinwoo-j", true},
		{"repo:x-mesh/gk", "repo", "", false},
		{"involves:@me", "inbox", "", false},
	}
	for _, c := range cases {
		p := &ghPicker{kind: "repo"}
		p.setScopeFromPrefix(c.prefix)
		if p.kind != c.wantKind || p.orgName != c.wantOrg || p.orgIsUser != c.wantIsUser {
			t.Errorf("%s → kind=%q org=%q isUser=%v, want %q/%q/%v",
				c.prefix, p.kind, p.orgName, p.orgIsUser, c.wantKind, c.wantOrg, c.wantIsUser)
		}
	}
	// repo prefix is cached so the picker does not re-resolve it
	p := &ghPicker{}
	p.setScopeFromPrefix("repo:x-mesh/gk")
	if p.repoPrefix != "repo:x-mesh/gk" {
		t.Errorf("repo prefix should be cached, got %q", p.repoPrefix)
	}
}
