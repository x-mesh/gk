package branchparent

import (
	"context"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func newResolverWithRunner(r git.Runner) *Resolver {
	return NewResolver(git.NewClient(r))
}

func TestResolver_ExplicitParentExists(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent":          {Stdout: "feat/parent\n"},
			"rev-parse --verify --quiet refs/heads/feat/parent": {Stdout: "abc\n"},
		},
	}
	res := newResolverWithRunner(r)
	parent, source, ok := res.ResolveParent(context.Background(), "feat/x")
	if !ok || parent != "feat/parent" || source != SourceExplicit {
		t.Fatalf("want feat/parent/explicit/true, got %s/%s/%v", parent, source, ok)
	}
}

func TestResolver_ExplicitParentMissing(t *testing.T) {
	// Explicit value is set but the branch was deleted — must NOT return it.
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent":          {Stdout: "feat/gone\n"},
			"rev-parse --verify --quiet refs/heads/feat/gone": {ExitCode: 1},
		},
	}
	res := newResolverWithRunner(r)
	_, _, ok := res.ResolveParent(context.Background(), "feat/x")
	if ok {
		t.Fatal("must return ok=false when explicit parent ref is missing")
	}
}

func TestResolver_NoExplicitNoInference(t *testing.T) {
	// No config, no inference (Phase 1 stub) → ok=false.
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent": {ExitCode: 1},
		},
	}
	res := newResolverWithRunner(r)
	_, _, ok := res.ResolveParent(context.Background(), "feat/x")
	if ok {
		t.Fatal("Phase 1 stub must return ok=false when no explicit value")
	}
}

func TestResolver_EmptyBranch(t *testing.T) {
	r := &git.FakeRunner{}
	res := newResolverWithRunner(r)
	_, _, ok := res.ResolveParent(context.Background(), "")
	if ok {
		t.Fatal("empty branch must return ok=false")
	}
	if len(r.Calls) != 0 {
		t.Errorf("empty branch must not invoke runner, got %d calls", len(r.Calls))
	}
}

func TestResolveBase_FallbackWhenNoParent(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent": {ExitCode: 1},
		},
	}
	res := newResolverWithRunner(r)
	got := res.ResolveBase(context.Background(), "feat/x", "main")
	if got != "main" {
		t.Errorf("want fallback to main, got %q", got)
	}
}

func TestResolveBase_UsesParent(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent":          {Stdout: "feat/parent\n"},
			"rev-parse --verify --quiet refs/heads/feat/parent": {Stdout: "abc\n"},
		},
	}
	res := newResolverWithRunner(r)
	got := res.ResolveBase(context.Background(), "feat/x", "main")
	if got != "feat/parent" {
		t.Errorf("want feat/parent, got %q", got)
	}
}

func TestResolveBaseExplained_ReturnsSource(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent":          {Stdout: "feat/parent\n"},
			"rev-parse --verify --quiet refs/heads/feat/parent": {Stdout: "abc\n"},
		},
	}
	res := newResolverWithRunner(r)
	base, source, resolved := res.ResolveBaseExplained(context.Background(), "feat/x", "main")
	if !resolved || base != "feat/parent" || source != SourceExplicit {
		t.Errorf("want feat/parent/explicit/true, got %s/%s/%v", base, source, resolved)
	}
}

func TestResolveBaseExplained_FallbackKeepsCfgBase(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/x.gk-parent": {ExitCode: 1},
		},
	}
	res := newResolverWithRunner(r)
	base, source, resolved := res.ResolveBaseExplained(context.Background(), "feat/x", "main")
	if resolved {
		t.Fatal("must return resolved=false on fallback")
	}
	if base != "main" || source != "" {
		t.Errorf("want main/'', got %s/%s", base, source)
	}
}
