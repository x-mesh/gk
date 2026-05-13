package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestDetectPromptInfo_Primary verifies that a fresh repo's root reports
// Linked=false — the load-bearing bit for "should the prompt show a marker?".
func TestDetectPromptInfo_Primary(t *testing.T) {
	repo := testutil.NewRepo(t)
	r := &git.ExecRunner{Dir: repo.Dir}
	info := detectPromptInfo(context.Background(), r)
	if info.Linked {
		t.Fatalf("expected Linked=false in primary worktree, got %+v", info)
	}
}

// TestDetectPromptInfo_Linked walks the full happy path through a real
// linked worktree, since the primary signal (--git-dir vs --git-common-dir)
// is the kind of thing that's brittle to mock.
func TestDetectPromptInfo_Linked(t *testing.T) {
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "linked")
	repo.RunGit("worktree", "add", "-b", "feat-x", wtPath)

	r := &git.ExecRunner{Dir: wtPath}
	info := detectPromptInfo(context.Background(), r)
	if !info.Linked {
		t.Fatalf("expected Linked=true in linked worktree, got %+v", info)
	}
	if info.Name != "linked" {
		t.Errorf("Name: want %q, got %q", "linked", info.Name)
	}
	if info.Branch != "feat-x" {
		t.Errorf("Branch: want %q, got %q", "feat-x", info.Branch)
	}
	// Path normalization — macOS symlinks /tmp → /private/tmp, so just
	// require the basename match instead of an absolute equality check.
	if filepath.Base(info.Path) != "linked" {
		t.Errorf("Path: want basename %q, got %q", "linked", info.Path)
	}
}

// TestDetectPromptInfo_OutsideRepo verifies prompt-safe silence — git
// errors must collapse to "not linked" so prompts that pipe this output
// never see noise.
func TestDetectPromptInfo_OutsideRepo(t *testing.T) {
	dir := t.TempDir()
	r := &git.ExecRunner{Dir: dir}
	info := detectPromptInfo(context.Background(), r)
	if info.Linked {
		t.Fatalf("expected Linked=false outside a repo, got %+v", info)
	}
}

// TestPromptInfo_JSONShape locks the field names since prompt frameworks
// (starship, p10k segments) parse this output verbatim — renaming a field
// breaks user config.
func TestPromptInfo_JSONShape(t *testing.T) {
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "lnk")
	repo.RunGit("worktree", "add", "-b", "topic/x", wtPath)

	r := &git.ExecRunner{Dir: wtPath}
	info := detectPromptInfo(context.Background(), r)

	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(raw)
	for _, key := range []string{`"linked":true`, `"name":"lnk"`, `"branch":"topic/x"`, `"path":`, `"repo":`} {
		if !strings.Contains(out, key) {
			t.Errorf("JSON missing %q: %s", key, out)
		}
	}
}

// TestFormatPromptInfo_PlainDedup checks that plain output collapses to
// "wt" (instead of "wt:<name>") when the worktree dir name equals the
// branch name — gk's default layout makes this the common case, and
// keeping the redundant suffix means triple-displaying the branch name
// across the prompt's directory, git_branch, and worktree segments.
func TestFormatPromptInfo_PlainDedup(t *testing.T) {
	cases := []struct {
		name string
		info promptInfo
		want string // trailing newline trimmed
	}{
		{"linked-name-eq-branch", promptInfo{Linked: true, Name: "fix-bug", Branch: "fix-bug", Repo: "gk"}, "wt"},
		{"linked-name-ne-branch", promptInfo{Linked: true, Name: "tmux", Branch: "feature/tmux", Repo: "gk"}, "wt:tmux"},
		{"primary", promptInfo{Linked: false, Repo: "gk", Branch: "main"}, ""},
		{"outside-repo", promptInfo{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := formatPromptInfo(&buf, tc.info, "plain"); err != nil {
				t.Fatalf("formatPromptInfo: %v", err)
			}
			got := strings.TrimRight(buf.String(), "\n")
			if got != tc.want {
				t.Errorf("plain output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFormatPromptInfo_Segment verifies the new --format=segment output
// designed to replace starship's $directory + $git_branch as a single
// "<repo>/<branch>" label.
func TestFormatPromptInfo_Segment(t *testing.T) {
	cases := []struct {
		name string
		info promptInfo
		want string
	}{
		{"primary", promptInfo{Linked: false, Repo: "gk", Branch: "main"}, "gk/main"},
		{"linked", promptInfo{Linked: true, Repo: "gk", Branch: "fix-bug", Name: "fix-bug"}, "gk/fix-bug"},
		{"detached", promptInfo{Linked: false, Repo: "gk", Branch: ""}, "gk"},
		{"outside-repo", promptInfo{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := formatPromptInfo(&buf, tc.info, "segment"); err != nil {
				t.Fatalf("formatPromptInfo: %v", err)
			}
			got := strings.TrimRight(buf.String(), "\n")
			if got != tc.want {
				t.Errorf("segment output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFormatPromptInfo_UnknownFormat verifies the error path stays
// stable — users with typo'd configs should see a clear message, not a
// silently-empty prompt segment.
func TestFormatPromptInfo_UnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	err := formatPromptInfo(&buf, promptInfo{Linked: true, Name: "x"}, "bogus")
	if err == nil {
		t.Fatalf("expected error for unknown format, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error message should name the bad format: %v", err)
	}
}

// TestDetectPromptInfo_BranchOnPrimary verifies the Branch field is
// populated in a primary worktree — needed so the "segment" format can
// render "<repo>/<branch>" without a separate git call upstream.
func TestDetectPromptInfo_BranchOnPrimary(t *testing.T) {
	repo := testutil.NewRepo(t)
	r := &git.ExecRunner{Dir: repo.Dir}
	info := detectPromptInfo(context.Background(), r)
	if info.Linked {
		t.Fatalf("expected Linked=false, got %+v", info)
	}
	if info.Branch == "" {
		t.Errorf("expected Branch to be populated in primary worktree, got %+v", info)
	}
}

// TestDetectPromptInfo_RepoOnPrimary verifies the Repo field is populated
// even in a primary worktree — prompts can then render a project label
// without an extra `git rev-parse` round-trip in the common case.
func TestDetectPromptInfo_RepoOnPrimary(t *testing.T) {
	repo := testutil.NewRepo(t)
	r := &git.ExecRunner{Dir: repo.Dir}
	info := detectPromptInfo(context.Background(), r)
	if info.Linked {
		t.Fatalf("expected Linked=false, got %+v", info)
	}
	if info.Repo == "" {
		t.Errorf("expected Repo to be populated in primary worktree, got %+v", info)
	}
	if info.Repo != filepath.Base(repo.Dir) {
		t.Errorf("Repo = %q, want %q (basename of %q)", info.Repo, filepath.Base(repo.Dir), repo.Dir)
	}
}
