package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func TestIsRepoNotFoundErr(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"https-not-found", "remote: Repository not found.\nfatal: repository 'https://github.com/x/y.git/' not found", true},
		{"ssh-missing", "ERROR: Repository not found.\nfatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights\nand the repository exists.", true},
		{"ssh-access", "fatal: Could not read from remote repository.\nPlease make sure you have the correct access rights\nand the repository exists.", true},
		{"does-not-exist", "ERROR: repository does not exist", true},
		// Real other failures must NOT trigger creation.
		{"non-ff", "! [rejected] main -> main (non-fast-forward)", false},
		{"auth", "fatal: Authentication failed for 'https://github.com/x/y.git/'", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRepoNotFoundErr(tc.in); got != tc.want {
				t.Errorf("isRepoNotFoundErr(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsGitHubHost(t *testing.T) {
	yes := []string{"github.com", "GitHub.com", "github.example.com", "ghe.github.acme.com"}
	no := []string{"gitlab.com", "bitbucket.org", "git.sr.ht", ""}
	for _, h := range yes {
		if !isGitHubHost(h) {
			t.Errorf("isGitHubHost(%q) = false, want true", h)
		}
	}
	for _, h := range no {
		if isGitHubHost(h) {
			t.Errorf("isGitHubHost(%q) = true, want false", h)
		}
	}
}

// The create target is derived from the origin URL — both SSH and HTTPS
// forms must resolve to the same owner/repo slug.
func TestParseRemoteMetaForCreate(t *testing.T) {
	cases := map[string]struct{ owner, repo, host string }{
		"git@github.com:x-mesh/space-mesh.git":       {"x-mesh", "space-mesh", "github.com"},
		"https://github.com/x-mesh/space-mesh.git":   {"x-mesh", "space-mesh", "github.com"},
		"https://github.com/x-mesh/space-mesh":       {"x-mesh", "space-mesh", "github.com"},
		"ssh://git@github.com/x-mesh/space-mesh.git": {"x-mesh", "space-mesh", "github.com"},
	}
	for url, want := range cases {
		m := config.ParseRemoteMeta(url)
		if m.Owner != want.owner || m.Repo != want.repo || m.Host != want.host {
			t.Errorf("ParseRemoteMeta(%q) = %+v, want owner=%s repo=%s host=%s",
				url, m, want.owner, want.repo, want.host)
		}
	}
	// A non-owner/repo URL yields an empty meta so creation is skipped.
	if m := config.ParseRemoteMeta("git@example.com:justhost"); m.Owner != "" {
		t.Errorf("bare host URL should not resolve to owner/repo: %+v", m)
	}
}

func TestIsYesAndPublicSuffix(t *testing.T) {
	for _, s := range []string{"y", "Y", "yes", "YES", " yes "} {
		if !isYes(s) {
			t.Errorf("isYes(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"n", "no", "", "yep", "sure"} {
		if isYes(s) {
			t.Errorf("isYes(%q) = true, want false", s)
		}
	}
	if publicFlagSuffix(true) != " --public" || publicFlagSuffix(false) != "" {
		t.Error("publicFlagSuffix wrong")
	}
}

// gk push on an unborn HEAD (no commits) must fail with a clear "nothing
// to push" message and a `gk commit` remedy — not git's cryptic
// "src refspec ... does not match any", and never reaching the push.
func TestPushUnbornHEADGuard(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify --quiet HEAD^{commit}": {ExitCode: 128, Stderr: ""},
		},
	}
	err := unbornHEADPushError(context.Background(), fake, "main")
	if err == nil {
		t.Fatal("want an error on unborn HEAD")
	}
	if !strings.Contains(err.Error(), "nothing to push") {
		t.Errorf("err = %q, want a 'nothing to push' message", err.Error())
	}
	rem := RemediesFrom(err)
	if len(rem) == 0 || rem[0].Command != "gk commit" {
		t.Errorf("remedy = %+v, want gk commit", rem)
	}
	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "push" {
			t.Error("must NOT attempt git push on an unborn HEAD")
		}
	}
}
