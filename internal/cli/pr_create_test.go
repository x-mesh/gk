package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func TestTitleFromBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   string
	}{
		{"feat/pr-create", "feat: pr create"},
		{"fix/token_leak", "fix: token leak"},
		{"feat/nested/thing", "feat: nested thing"},
		// Not a Conventional Commit type — the prefix is part of the name,
		// not a type, so it must not be promoted to one.
		{"jinwoo/experiment", "jinwoo experiment"},
		{"hotfix", "hotfix"},
		// A type with nothing after it has no name to render; keep the
		// branch as-is rather than emitting a bare "feat: ".
		{"feat/", "feat/"},
	}
	for _, tc := range cases {
		if got := titleFromBranch(tc.branch); got != tc.want {
			t.Errorf("titleFromBranch(%q) = %q, want %q", tc.branch, got, tc.want)
		}
	}
}

// A single commit's subject already became the title, so repeating it as the
// body is noise.
func TestBodyFromCommitsSkipsSingleCommit(t *testing.T) {
	if got := bodyFromCommits([]string{"feat: one thing"}); got != "" {
		t.Errorf("body = %q, want empty", got)
	}
	got := bodyFromCommits([]string{"feat: two", "fix: one"})
	if !strings.Contains(got, "- feat: two") || !strings.Contains(got, "- fix: one") {
		t.Errorf("body = %q, want both subjects as list items", got)
	}
}

func TestPRCommitSubjects(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"log --format=%s develop..feat/x": {Stdout: "feat: b\n\nfix: a\n"},
	}}
	got, err := prCommitSubjects(context.Background(), r, "develop", "feat/x")
	if err != nil {
		t.Fatalf("prCommitSubjects: %v", err)
	}
	if len(got) != 2 || got[0] != "feat: b" || got[1] != "fix: a" {
		t.Fatalf("subjects = %#v", got)
	}
}

// The head branch must exist on the remote before GitHub can see it, and the
// blocked state must carry `gk push` as the remedy so an agent can run it.
func TestRequirePushedHeadBlocksUnpushedBranch(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify --quiet refs/remotes/origin/feat/x": {ExitCode: 1},
	}}
	err := requirePushedHead(context.Background(), r, config.Config{}, "feat/x")
	if err == nil {
		t.Fatal("want a blocked error, got nil")
	}
	if state := stateFrom(err); state != envStateBlocked {
		t.Errorf("state = %q, want %q", state, envStateBlocked)
	}
	if code := codeFrom(err); code != "branch_not_pushed" {
		t.Errorf("code = %q, want branch_not_pushed", code)
	}
	remedies := RemediesFrom(err)
	if len(remedies) != 1 || remedies[0].Command != "gk push" {
		t.Errorf("remedies = %#v, want one 'gk push'", remedies)
	}
}

// A pushed branch that has since gained local commits would open a PR missing
// the latest work — block that too, with its own code.
func TestRequirePushedHeadBlocksUnpushedCommits(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify --quiet refs/remotes/origin/feat/x": {Stdout: "abc123\n"},
		"rev-list --left-right --count origin/feat/x...feat/x":  {Stdout: "0\t2\n"},
	}}
	err := requirePushedHead(context.Background(), r, config.Config{}, "feat/x")
	if err == nil {
		t.Fatal("want a blocked error, got nil")
	}
	if code := codeFrom(err); code != "branch_ahead_remote" {
		t.Errorf("code = %q, want branch_ahead_remote", code)
	}
}

// The mirror case: the remote is ahead. The count of local-only commits is 0,
// so a one-sided check waves it through — but the PR would then carry commits
// that the title and body were never computed from.
func TestRequirePushedHeadBlocksWhenRemoteIsAhead(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify --quiet refs/remotes/origin/feat/x": {Stdout: "abc123\n"},
		"rev-list --left-right --count origin/feat/x...feat/x":  {Stdout: "3\t0\n"},
	}}
	err := requirePushedHead(context.Background(), r, config.Config{}, "feat/x")
	if err == nil {
		t.Fatal("want a blocked error, got nil")
	}
	if code := codeFrom(err); code != "branch_behind_remote" {
		t.Errorf("code = %q, want branch_behind_remote", code)
	}
	remedies := RemediesFrom(err)
	if len(remedies) != 1 || remedies[0].Command != "gk pull" {
		t.Errorf("remedies = %#v, want one 'gk pull'", remedies)
	}
}

func TestRequirePushedHeadAllowsSyncedBranch(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify --quiet refs/remotes/origin/feat/x": {Stdout: "abc123\n"},
		"rev-list --left-right --count origin/feat/x...feat/x":  {Stdout: "0\t0\n"},
	}}
	if err := requirePushedHead(context.Background(), r, config.Config{}, "feat/x"); err != nil {
		t.Fatalf("requirePushedHead: %v", err)
	}
}

func TestParseLeftRightCount(t *testing.T) {
	cases := []struct {
		out          string
		wantL, wantR int
	}{
		{"0\t2\n", 0, 2},
		{"3\t0\n", 3, 0},
		{"1 4", 1, 4},
		// Unparsable output must read as "no objection" rather than blocking.
		{"", 0, 0},
		{"garbage\n", 0, 0},
	}
	for _, tc := range cases {
		l, r := parseLeftRightCount(tc.out)
		if l != tc.wantL || r != tc.wantR {
			t.Errorf("parseLeftRightCount(%q) = (%d, %d), want (%d, %d)", tc.out, l, r, tc.wantL, tc.wantR)
		}
	}
}

// The configured remote, not a hardcoded "origin", decides which ref is
// checked — a repo whose remote is "upstream" must not be told to push to a
// remote it does not have.
func TestRequirePushedHeadHonorsConfiguredRemote(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify --quiet refs/remotes/upstream/feat/x": {Stdout: "abc123\n"},
		"rev-list --left-right --count upstream/feat/x...feat/x":  {Stdout: "0\t0\n"},
	}}
	if err := requirePushedHead(context.Background(), r, config.Config{Remote: "upstream"}, "feat/x"); err != nil {
		t.Fatalf("requirePushedHead: %v", err)
	}
}

// A rev-list that fails (e.g. a corrupt ref) must not block a PR whose
// branch demonstrably exists on the remote — the count is an extra check,
// not a precondition.
func TestRequirePushedHeadIgnoresCountFailure(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify --quiet refs/remotes/origin/feat/x": {Stdout: "abc123\n"},
		"rev-list --left-right --count origin/feat/x...feat/x":  {ExitCode: 128},
	}}
	if err := requirePushedHead(context.Background(), r, config.Config{}, "feat/x"); err != nil {
		t.Fatalf("requirePushedHead: %v", err)
	}
}
