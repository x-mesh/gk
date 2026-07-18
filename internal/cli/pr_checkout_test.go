package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func TestPRCheckoutWith(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"fetch origin pull/98/head:pr/98": {Stdout: ""},
		"switch pr/98":                    {Stdout: ""},
	}}
	msg, err := prCheckoutWith(context.Background(), runner, "origin", "pr/98", 98)
	if err != nil {
		t.Fatalf("prCheckoutWith: %v", err)
	}
	if !strings.Contains(msg, "PR #98") || !strings.Contains(msg, "pr/98") {
		t.Errorf("unexpected message: %q", msg)
	}
	joined := ""
	for _, c := range runner.Calls {
		joined += strings.Join(c.Args, " ") + "\n"
	}
	for _, want := range []string{"fetch origin pull/98/head:pr/98", "switch pr/98"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %q in:\n%s", want, joined)
		}
	}
}

func TestPRCheckoutTargetSameRepoKeepsRemote(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote get-url upstream": {Stdout: "git@github.com:x-mesh/gk.git\n"},
	}}
	source, local, err := prCheckoutTarget(context.Background(), runner, config.Config{Remote: "upstream"}, "x-mesh", "gk", 42)
	if err != nil {
		t.Fatalf("prCheckoutTarget: %v", err)
	}
	if source != "upstream" || local != "pr/42" {
		t.Fatalf("source/local = %q/%q, want upstream/pr/42", source, local)
	}
}

func TestPRCheckoutTargetCrossRepoUsesSelectedRepository(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote get-url origin": {Stdout: "git@work:x-mesh/gk.git\n"},
	}}
	source, local, err := prCheckoutTarget(context.Background(), runner, config.Config{}, "other", "project", 42)
	if err != nil {
		t.Fatalf("prCheckoutTarget: %v", err)
	}
	if source != "git@work:other/project.git" {
		t.Fatalf("source = %q, want selected repository on opaque SSH host alias", source)
	}
	if local != "pr/other/project/42" {
		t.Fatalf("local = %q, want repo-qualified branch", local)
	}

	if _, err := prCheckoutWith(context.Background(), runner, source, local, 42); err != nil {
		t.Fatalf("prCheckoutWith: %v", err)
	}
	joined := ""
	for _, c := range runner.Calls {
		joined += strings.Join(c.Args, " ") + "\n"
	}
	if !strings.Contains(joined, "fetch git@work:other/project.git pull/42/head:pr/other/project/42") {
		t.Fatalf("selected repository was not used for fetch:\n%s", joined)
	}
	if strings.Contains(joined, "fetch origin pull/42/head") {
		t.Fatalf("cross-repo checkout must not fetch the same PR number from origin:\n%s", joined)
	}
}

func TestRewriteRemoteRepoSanitizesURLCredentials(t *testing.T) {
	got, err := rewriteRemoteRepo("https://oauth:secret@example.github.com/x-mesh/gk.git?token=also-secret", "other", "repo")
	if err != nil {
		t.Fatalf("rewriteRemoteRepo HTTPS: %v", err)
	}
	if got != "https://example.github.com/other/repo.git" {
		t.Fatalf("rewritten HTTPS URL = %q", got)
	}

	got, err = rewriteRemoteRepo("ssh://git:secret@example.github.com/x-mesh/gk.git", "other", "repo")
	if err != nil {
		t.Fatalf("rewriteRemoteRepo SSH: %v", err)
	}
	if got != "ssh://git@example.github.com/other/repo.git" {
		t.Fatalf("rewritten SSH URL = %q", got)
	}
}

func TestRemoteDisplayNameRedactsURLCredentials(t *testing.T) {
	got := remoteDisplayName("https://oauth:secret@github.com/other/repo.git?token=also-secret#fragment")
	if got != "https://github.com/other/repo.git" {
		t.Fatalf("remoteDisplayName = %q", got)
	}
}

func TestRewriteRemoteRepoMalformedURLDoesNotLeakCredentials(t *testing.T) {
	_, err := rewriteRemoteRepo("https://oauth:secret%ZZ@github.com/x-mesh/gk.git", "other", "repo")
	if err == nil {
		t.Fatal("expected malformed URL error")
	}
	if strings.Contains(err.Error(), "oauth") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "%ZZ") {
		t.Fatalf("credential-bearing raw URL leaked in error: %v", err)
	}
}

func TestPRCheckoutWithSanitizesCredentialBearingGitError(t *testing.T) {
	const key = "fetch origin pull/7/head:pr/7"
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		key: {
			ExitCode: 128,
			Stderr:   "fatal: unable to access 'https://oauth:secret@github.com/owner/repo.git?token=also-secret/': denied",
		},
	}}
	_, err := prCheckoutWith(context.Background(), runner, "origin", "pr/7", 7)
	if err == nil {
		t.Fatal("expected fetch error")
	}
	msg := err.Error()
	if strings.Contains(msg, "oauth") || strings.Contains(msg, "secret") || strings.Contains(msg, "token=") {
		t.Fatalf("credential-bearing Git error leaked: %v", err)
	}
	if !strings.Contains(msg, "https://github.com/owner/repo.git") {
		t.Fatalf("sanitized remote context missing: %v", err)
	}
	var exitErr *git.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 128 {
		t.Fatalf("sanitized error did not preserve ExitError semantics: %T %v", err, err)
	}
}
