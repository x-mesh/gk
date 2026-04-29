package aicommit

import (
	"context"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestHeuristicMessageBuildAllLockfiles(t *testing.T) {
	g := provider.Group{
		Type:  "build",
		Files: []string{"go.sum", "package-lock.json"},
	}
	msg, ok := heuristicMessage(g, "en")
	if !ok {
		t.Fatal("want bypass for all-lockfile build group")
	}
	if msg.Model != heuristicModel {
		t.Errorf("Model: want %q, got %q", heuristicModel, msg.Model)
	}
	if msg.Attempts != 0 {
		t.Errorf("Attempts: want 0 (no LLM), got %d", msg.Attempts)
	}
	if msg.Subject != "update lockfiles" {
		t.Errorf("Subject: %q", msg.Subject)
	}
}

func TestHeuristicMessageBuildSingleManager(t *testing.T) {
	g := provider.Group{Type: "build", Files: []string{"go.sum"}}
	msg, ok := heuristicMessage(g, "en")
	if !ok {
		t.Fatal("want bypass")
	}
	if msg.Subject != "update go lockfile" {
		t.Errorf("Subject: %q", msg.Subject)
	}
}

func TestHeuristicMessageBuildKoreanSubject(t *testing.T) {
	g := provider.Group{Type: "build", Files: []string{"go.sum"}}
	msg, ok := heuristicMessage(g, "ko")
	if !ok {
		t.Fatal("want bypass")
	}
	if msg.Subject != "go 락파일 갱신" {
		t.Errorf("Subject: %q", msg.Subject)
	}
}

func TestHeuristicMessageBuildMixedFilesFallsThrough(t *testing.T) {
	g := provider.Group{
		Type:  "build",
		Files: []string{"go.sum", "Makefile"},
	}
	if _, ok := heuristicMessage(g, "en"); ok {
		t.Fatal("want LLM path when group has non-lockfile files")
	}
}

func TestHeuristicMessageCIAllWorkflows(t *testing.T) {
	g := provider.Group{
		Type:  "ci",
		Files: []string{".github/workflows/test.yml", ".github/workflows/lint.yml"},
	}
	msg, ok := heuristicMessage(g, "en")
	if !ok {
		t.Fatal("want bypass")
	}
	if msg.Subject != "update github workflows" {
		t.Errorf("Subject: %q", msg.Subject)
	}
}

func TestHeuristicMessageCIMixedDirs(t *testing.T) {
	g := provider.Group{
		Type:  "ci",
		Files: []string{".github/workflows/x.yml", ".gitlab/ci.yml"},
	}
	msg, ok := heuristicMessage(g, "en")
	if !ok {
		t.Fatal("want bypass")
	}
	if msg.Subject != "update CI configuration" {
		t.Errorf("Subject: %q", msg.Subject)
	}
}

func TestHeuristicMessageCIWithNonCIFile(t *testing.T) {
	g := provider.Group{
		Type:  "ci",
		Files: []string{".github/workflows/x.yml", "scripts/release.sh"},
	}
	if _, ok := heuristicMessage(g, "en"); ok {
		t.Fatal("want LLM path when CI group has non-CI file")
	}
}

func TestHeuristicMessageFeatNeverBypassed(t *testing.T) {
	g := provider.Group{Type: "feat", Files: []string{"main.go"}}
	if _, ok := heuristicMessage(g, "en"); ok {
		t.Fatal("feat groups must always go to the LLM")
	}
}

func TestHeuristicMessageEmptyGroup(t *testing.T) {
	g := provider.Group{Type: "build", Files: nil}
	if _, ok := heuristicMessage(g, "en"); ok {
		t.Fatal("empty group must not be bypassed")
	}
}

// TestComposeAllSkipsLLMForLockfileGroup verifies the bypass is wired
// into ComposeAll: the provider must NOT receive a Compose call when a
// build group is lockfile-only.
func TestComposeAllSkipsLLMForLockfileGroup(t *testing.T) {
	p := provider.NewFake()
	composeCalls := 0
	p.OnCompose = func(in provider.ComposeInput) {
		composeCalls++
	}
	groups := []provider.Group{
		{Type: "build", Files: []string{"go.sum", "package-lock.json"}},
	}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"build"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if composeCalls != 0 {
		t.Errorf("provider.Compose was called %d time(s); expected 0 (heuristic bypass)", composeCalls)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs: %d", len(msgs))
	}
	if msgs[0].Model != heuristicModel {
		t.Errorf("Model: want %q, got %q", heuristicModel, msgs[0].Model)
	}
}

func TestComposeAllStillCallsLLMForFeatGroup(t *testing.T) {
	p := provider.NewFake()
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: "add classifier", Model: "fake-v1"},
	}
	composeCalls := 0
	p.OnCompose = func(in provider.ComposeInput) { composeCalls++ }
	groups := []provider.Group{
		{Type: "feat", Files: []string{"main.go"}},
	}
	_, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if composeCalls != 1 {
		t.Errorf("provider.Compose calls: want 1, got %d", composeCalls)
	}
}
