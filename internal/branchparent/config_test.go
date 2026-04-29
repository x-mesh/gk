package branchparent

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func newConfigWithRunner(r git.Runner) *Config {
	return NewConfig(git.NewClient(r))
}

func TestConfig_GetParent_Set(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent": {Stdout: "feat/parent\n"},
		},
	}
	cfg := newConfigWithRunner(r)
	got, err := cfg.GetParent(context.Background(), "feat/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "feat/parent" {
		t.Errorf("want %q, got %q", "feat/parent", got)
	}
}

func TestConfig_GetParent_Unset(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent": {ExitCode: 1},
		},
	}
	cfg := newConfigWithRunner(r)
	got, err := cfg.GetParent(context.Background(), "feat/x")
	if err != nil {
		t.Fatalf("unset must not error: %v", err)
	}
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestConfig_SetParent(t *testing.T) {
	r := &git.FakeRunner{}
	cfg := newConfigWithRunner(r)
	if err := cfg.SetParent(context.Background(), "feat/x", "main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(r.Calls))
	}
	got := strings.Join(r.Calls[0].Args, " ")
	if got != "config branch.feat/x.gk-parent main" {
		t.Errorf("want %q, got %q", "config branch.feat/x.gk-parent main", got)
	}
}

func TestConfig_UnsetParent_Idempotent(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --unset branch.feat/x.gk-parent": {ExitCode: 5},
		},
	}
	cfg := newConfigWithRunner(r)
	if err := cfg.UnsetParent(context.Background(), "feat/x"); err != nil {
		t.Errorf("unset of absent key must be idempotent, got: %v", err)
	}
}

func TestConfig_RoundTrip(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent": {Stdout: "main\n"},
		},
	}
	cfg := newConfigWithRunner(r)

	if err := cfg.SetParent(context.Background(), "feat/x", "main"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := cfg.GetParent(context.Background(), "feat/x")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "main" {
		t.Errorf("want main, got %q", got)
	}
	if err := cfg.UnsetParent(context.Background(), "feat/x"); err != nil {
		t.Fatalf("unset: %v", err)
	}
}
