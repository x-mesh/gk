package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An unknown field is still rejected, but the error now names the fields the
// tool DOES accept so a caller self-corrects instead of looping on the miss.
func TestStrictUnmarshalNamesAllowedFields(t *testing.T) {
	r, _ := newTestGitTools(t)
	res := dispatch(t, r, "git_log", `{"start_line":5}`)
	if !res.IsError {
		t.Fatalf("unknown field must error, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "allowed fields:") {
		t.Errorf("error must list allowed fields, got: %s", res.Content)
	}
	for _, want := range []string{"range", "limit", "paths", "author", "since"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("allowed fields missing %q: %s", want, res.Content)
		}
	}
}

// git_log accepts a singular `path` alias and author filtering.
func TestGitLogSingularPathAndAuthor(t *testing.T) {
	r, _ := newTestGitTools(t)

	res := dispatch(t, r, "git_log", `{"path":"a.go"}`)
	if res.IsError {
		t.Fatalf("git_log path alias: %s", res.Content)
	}
	if !strings.Contains(res.Content, "change greeting") {
		t.Errorf("expected a commit touching a.go: %s", res.Content)
	}

	// The fixture commits with author name "t".
	if res := dispatch(t, r, "git_log", `{"author":"t"}`); res.IsError || !strings.Contains(res.Content, "initial") {
		t.Errorf("author=t should match fixture commits: %s", res.Content)
	}
	miss := dispatch(t, r, "git_log", `{"author":"nobody-zzz"}`)
	if miss.IsError {
		t.Fatalf("author no-match should be empty, not error: %s", miss.Content)
	}
	if strings.Contains(miss.Content, "initial") {
		t.Errorf("author=nobody must not match: %s", miss.Content)
	}
}

// git_diff staged=true diffs the index against HEAD; a fully-staged file
// shows up there but NOT in the default working-tree diff.
func TestGitDiffStaged(t *testing.T) {
	runner, sb, root := gitRepoFixture(t)
	if err := os.WriteFile(filepath.Join(root, "staged.go"), []byte("package a\n\n// staged addition\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "staged.go")
	add.Dir = root
	add.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	g := &GitTools{Runner: runner, Sandbox: sb, DenyGlobs: sb.DenyGlobs}
	r := NewRegistry(nil, 0)
	RegisterGitTools(r, g)

	staged := dispatch(t, r, "git_diff", `{"staged":true}`)
	if staged.IsError {
		t.Fatalf("git_diff staged: %s", staged.Content)
	}
	if !strings.Contains(staged.Content, "staged.go") {
		t.Errorf("staged diff should include staged.go: %s", staged.Content)
	}
	// Default (unstaged) diff must NOT show a fully-staged file.
	if plain := dispatch(t, r, "git_diff", `{}`); strings.Contains(plain.Content, "staged.go") {
		t.Errorf("unstaged diff should not include staged-only file: %s", plain.Content)
	}
}
