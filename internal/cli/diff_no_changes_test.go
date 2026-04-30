package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

// ---------------------------------------------------------------------------
// diffComparisonLabel — pure
// ---------------------------------------------------------------------------

func TestDiffComparisonLabel_Default(t *testing.T) {
	scope, label := diffComparisonLabel(false, nil)
	if scope != "default" {
		t.Errorf("scope = %q, want default", scope)
	}
	if !strings.Contains(label, "working tree") || !strings.Contains(label, "index") {
		t.Errorf("label = %q, want working-tree/index pair", label)
	}
}

func TestDiffComparisonLabel_Staged(t *testing.T) {
	scope, label := diffComparisonLabel(true, nil)
	if scope != "staged" {
		t.Errorf("scope = %q, want staged", scope)
	}
	if !strings.Contains(label, "HEAD") {
		t.Errorf("label = %q, want HEAD reference", label)
	}
}

func TestDiffComparisonLabel_ExplicitRef(t *testing.T) {
	scope, label := diffComparisonLabel(false, []string{"main"})
	if scope != "ref" {
		t.Errorf("scope = %q, want ref", scope)
	}
	if !strings.Contains(label, "main") {
		t.Errorf("label = %q, want explicit ref name", label)
	}
}

func TestDiffComparisonLabel_PathOnly(t *testing.T) {
	// `-- somefile` is just a path; should not be considered a ref.
	scope, _ := diffComparisonLabel(false, []string{"--", "somefile.go"})
	if scope != "default" {
		t.Errorf("scope = %q, want default (-- starts path list)", scope)
	}
}

func TestHasExplicitDiffRef(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"-U5"}, false},
		{[]string{"--", "x"}, false},
		{[]string{"main"}, true},
		{[]string{"main..HEAD"}, true},
		{[]string{"-U5", "main"}, true},
		{[]string{"main", "--", "x"}, true},
	}
	for _, c := range cases {
		got := hasExplicitDiffRef(c.args)
		if got != c.want {
			t.Errorf("hasExplicitDiffRef(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// countStagedFiles / hasUnstagedChanges — wrap FakeRunner
// ---------------------------------------------------------------------------

func TestCountStagedFiles_NoChanges(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached --name-only": {Stdout: ""},
		},
	}
	if got := countStagedFiles(context.Background(), fake); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestCountStagedFiles_Multiple(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached --name-only": {Stdout: "a.go\nb.go\nc.go\n"},
		},
	}
	if got := countStagedFiles(context.Background(), fake); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}

func TestCountStagedFiles_GitError(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached --name-only": {ExitCode: 128, Stderr: "fatal\n"},
		},
	}
	if got := countStagedFiles(context.Background(), fake); got != 0 {
		t.Errorf("got %d, want 0 on error", got)
	}
}

func TestHasUnstagedChanges_Clean(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --quiet": {ExitCode: 0},
		},
	}
	if hasUnstagedChanges(context.Background(), fake) {
		t.Error("clean working tree should report no unstaged changes")
	}
}

func TestHasUnstagedChanges_Dirty(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --quiet": {ExitCode: 1},
		},
	}
	if !hasUnstagedChanges(context.Background(), fake) {
		t.Error("dirty working tree should report unstaged changes")
	}
}

// ---------------------------------------------------------------------------
// renderDiffNoChanges — banner + smart hints
// ---------------------------------------------------------------------------

func TestRenderDiffNoChanges_DefaultWithStagedFiles(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached --name-only": {Stdout: "a.go\nb.go\nc.go\n"},
		},
	}
	buf := &bytes.Buffer{}
	renderDiffNoChanges(buf, context.Background(), fake, false, nil)

	out := stripANSI(buf.String())
	for _, want := range []string{
		"변경사항 없음",
		"working tree", "index",
		"staged 변경", "3 파일",
		"gk diff --staged",
		"gk diff HEAD",
		"gk diff <ref>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q\n%s", want, out)
		}
	}
}

func TestRenderDiffNoChanges_StagedWithUnstaged(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --quiet": {ExitCode: 1},
		},
	}
	buf := &bytes.Buffer{}
	renderDiffNoChanges(buf, context.Background(), fake, true, nil)

	out := stripANSI(buf.String())
	if !strings.Contains(out, "unstaged 변경 있음") {
		t.Errorf("expected unstaged hint:\n%s", out)
	}
	if !strings.Contains(out, "--staged") {
		t.Errorf("staged scope label should appear in banner:\n%s", out)
	}
}

func TestRenderDiffNoChanges_StagedNoUnstagedSuppressesProbe(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --quiet": {ExitCode: 0},
		},
	}
	buf := &bytes.Buffer{}
	renderDiffNoChanges(buf, context.Background(), fake, true, nil)

	out := stripANSI(buf.String())
	if strings.Contains(out, "unstaged 변경 있음") {
		t.Errorf("smart hint should be suppressed when unstaged is also clean:\n%s", out)
	}
}

func TestRenderDiffNoChanges_ExplicitRefSkipsProbes(t *testing.T) {
	fake := &git.FakeRunner{}
	buf := &bytes.Buffer{}
	renderDiffNoChanges(buf, context.Background(), fake, false, []string{"main"})

	out := stripANSI(buf.String())
	if !strings.Contains(out, "main") {
		t.Errorf("banner should name the ref:\n%s", out)
	}
	if strings.Contains(out, "staged 변경") || strings.Contains(out, "unstaged 변경") {
		t.Errorf("staged/unstaged probes must not fire for explicit refs:\n%s", out)
	}
	// Universal alternates still shown.
	if !strings.Contains(out, "gk diff HEAD") {
		t.Errorf("universal alternates missing for explicit-ref path:\n%s", out)
	}
}

func TestRenderDiffNoChanges_DefaultNoStagedSuppressesProbe(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached --name-only": {Stdout: ""},
		},
	}
	buf := &bytes.Buffer{}
	renderDiffNoChanges(buf, context.Background(), fake, false, nil)

	out := stripANSI(buf.String())
	if strings.Contains(out, "staged 변경") {
		t.Errorf("smart hint should be suppressed when nothing is staged:\n%s", out)
	}
	if !strings.Contains(out, "gk diff HEAD") {
		t.Errorf("universal alternate `gk diff HEAD` missing:\n%s", out)
	}
}
