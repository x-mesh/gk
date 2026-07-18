package cli

import (
	"testing"

	ghapi "github.com/x-mesh/gk/internal/github"
)

func TestParseFollowTriggersDefault(t *testing.T) {
	ts, err := parseFollowTriggers(nil)
	if err != nil {
		t.Fatalf("default parse: %v", err)
	}
	if len(ts) != 1 || ts[0].kind != "pr" || ts[0].verb != "merged" {
		t.Fatalf("default trigger should be pr:merged, got %+v", ts)
	}
}

func TestParseFollowTriggersValid(t *testing.T) {
	ts, err := parseFollowTriggers([]string{"pr:label=deploy", "issue:closed", "pr:review"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ts[0].verb != "label" || ts[0].value != "deploy" {
		t.Errorf("pr:label=deploy → %+v", ts[0])
	}
	if ts[2].verb != "review" || ts[2].value != "approved" {
		t.Errorf("pr:review should default to approved, got %+v", ts[2])
	}
}

func TestParseFollowTriggersInvalid(t *testing.T) {
	for _, bad := range []string{"", "pr", "pr:", "bogus:opened", "pr:frobnicate", "pr:label", "issue:review"} {
		if _, err := parseFollowTriggers([]string{bad}); err == nil {
			t.Errorf("%q should be a parse error", bad)
		}
	}
}

func TestTriggerMatches(t *testing.T) {
	merged := ghapi.RepoEvent{Type: "PullRequestEvent", Action: "closed", PRMerged: true, PRBase: "main", PRNumber: 5}
	closedNotMerged := ghapi.RepoEvent{Type: "PullRequestEvent", Action: "closed", PRMerged: false, PRBase: "main"}
	labeled := ghapi.RepoEvent{Type: "PullRequestEvent", Action: "labeled", Label: "deploy"}
	review := ghapi.RepoEvent{Type: "PullRequestReviewEvent", Action: "submitted", ReviewState: "approved"}
	issueLabeled := ghapi.RepoEvent{Type: "IssuesEvent", Action: "labeled", Label: "bug"}

	mustParse := func(s string) followTrigger {
		ts, err := parseFollowTriggers([]string{s})
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return ts[0]
	}

	// pr:merged matches a merge; not a mere close.
	if !mustParse("pr:merged").matches(merged, "") {
		t.Error("pr:merged should match a merged PR")
	}
	if mustParse("pr:merged").matches(closedNotMerged, "") {
		t.Error("pr:merged must NOT match a closed-unmerged PR")
	}
	// branch filter: pr:merged into main only.
	if !mustParse("pr:merged").matches(merged, "main") {
		t.Error("pr:merged into main should match when base=main")
	}
	if mustParse("pr:merged").matches(merged, "develop") {
		t.Error("pr:merged into main must NOT match when following develop")
	}
	// label / review / issue label
	if !mustParse("pr:label=deploy").matches(labeled, "") {
		t.Error("pr:label=deploy should match")
	}
	if mustParse("pr:label=other").matches(labeled, "") {
		t.Error("pr:label=other must not match a deploy label")
	}
	if !mustParse("pr:review").matches(review, "") {
		t.Error("pr:review (approved) should match")
	}
	if !mustParse("issue:label=bug").matches(issueLabeled, "") {
		t.Error("issue:label=bug should match")
	}
}

func TestFollowTriggersNeedAPI(t *testing.T) {
	mergedOnly, _ := parseFollowTriggers([]string{"pr:merged"})
	if followTriggersNeedAPI(mergedOnly) {
		t.Error("pr:merged alone is ref-approximable, should NOT need the API")
	}
	withLabel, _ := parseFollowTriggers([]string{"pr:merged", "issue:closed"})
	if !followTriggersNeedAPI(withLabel) {
		t.Error("issue:closed cannot be ref-approximated, should need the API")
	}
}
