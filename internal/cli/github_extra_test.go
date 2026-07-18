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
