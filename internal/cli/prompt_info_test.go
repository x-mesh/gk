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
	info := detectPromptInfo(context.Background(), r, promptIncludes{})
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
	info := detectPromptInfo(context.Background(), r, promptIncludes{})
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
	info := detectPromptInfo(context.Background(), r, promptIncludes{})
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
	info := detectPromptInfo(context.Background(), r, promptIncludes{})

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
	info := detectPromptInfo(context.Background(), r, promptIncludes{})
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
	info := detectPromptInfo(context.Background(), r, promptIncludes{})
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

// TestParsePromptIncludes covers the --include CSV parser. Each known
// token flips one bit; unknown tokens surface a clear error so prompt
// configs with typos fail loudly instead of silently rendering nothing.
func TestParsePromptIncludes(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		want    promptIncludes
		wantErr bool
	}{
		{"empty", "", promptIncludes{}, false},
		{"whitespace-only", "   ", promptIncludes{}, false},
		{"single", "wip", promptIncludes{wip: true}, false},
		{"all", "wip,dirty,ahead,behind,state", promptIncludes{wip: true, dirty: true, ahead: true, behind: true, state: true}, false},
		{"spaces-around-commas", " wip , dirty ", promptIncludes{wip: true, dirty: true}, false},
		{"trailing-comma", "wip,", promptIncludes{wip: true}, false},
		{"unknown", "wip,bogus", promptIncludes{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePromptIncludes(tc.spec)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parsePromptIncludes(%q) err=%v wantErr=%v", tc.spec, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got != tc.want {
				t.Errorf("parsePromptIncludes(%q) = %+v, want %+v", tc.spec, got, tc.want)
			}
		})
	}
}

// TestPlainTokens_SignalOrder locks the token order (wt, wip, ±, ↑, ↓,
// !state). Prompt configs that grep or positionally parse this line rely
// on the order, so any reshuffle is a breaking change worth catching.
func TestPlainTokens_SignalOrder(t *testing.T) {
	cases := []struct {
		name string
		info promptInfo
		want string
	}{
		{"clean-primary", promptInfo{Linked: false, Repo: "gk", Branch: "main"}, ""},
		{"wip-only", promptInfo{Linked: false, Repo: "gk", Branch: "main", WIP: true}, "wip"},
		{"dirty-only", promptInfo{Linked: false, Repo: "gk", Branch: "main", Dirty: 3}, "±3"},
		{"ahead-behind", promptInfo{Linked: false, Repo: "gk", Branch: "main", Ahead: 2, Behind: 1}, "↑2 ↓1"},
		{"state-only", promptInfo{Linked: false, Repo: "gk", Branch: "main", State: "rebase-merge"}, "!rebase-merge"},
		{
			"linked-everything",
			promptInfo{Linked: true, Name: "fix-bug", Branch: "fix-bug", Repo: "gk", WIP: true, Dirty: 5, Ahead: 1, Behind: 2, State: "merge"},
			"wt wip ±5 ↑1 ↓2 !merge",
		},
		{
			"linked-name-mismatch",
			promptInfo{Linked: true, Name: "tmux", Branch: "feature/tmux", Repo: "gk", Dirty: 1},
			"wt:tmux ±1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := formatPromptInfo(&buf, tc.info, "plain"); err != nil {
				t.Fatalf("formatPromptInfo: %v", err)
			}
			got := strings.TrimRight(buf.String(), "\n")
			if got != tc.want {
				t.Errorf("plain = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetectPromptInfo_Dirty exercises the porcelain count path against
// a real repo with a mix of staged, unstaged, and untracked entries.
// Counting individual git status lines is the unit prompts want — one
// number per "noisy path", not per byte of diff.
func TestDetectPromptInfo_Dirty(t *testing.T) {
	repo := testutil.NewRepo(t)
	// Commit a baseline tracked file so the test can then modify it,
	// covering the "M" porcelain code in addition to "A" and "??".
	repo.WriteFile("tracked.txt", "v1\n")
	repo.RunGit("add", "tracked.txt")
	repo.RunGit("commit", "-m", "seed tracked.txt")

	// Seed three porcelain-visible entries: one staged add, one modified
	// tracked file, one untracked file.
	repo.WriteFile("staged.txt", "staged\n")
	repo.RunGit("add", "staged.txt")
	repo.WriteFile("tracked.txt", "v2 modified\n")
	repo.WriteFile("untracked.txt", "u\n")

	r := &git.ExecRunner{Dir: repo.Dir}
	info := detectPromptInfo(context.Background(), r, promptIncludes{dirty: true})
	if info.Dirty != 3 {
		t.Errorf("Dirty = %d, want 3", info.Dirty)
	}
}

// TestDetectPromptInfo_WIP confirms a WIP-subject HEAD is flagged when
// --include=wip is on. Uses the baked-in defaults (wip pattern) since
// detectPromptWIP intentionally skips config loading for speed.
func TestDetectPromptInfo_WIP(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a\n")
	repo.RunGit("add", "a.txt")
	repo.RunGit("commit", "-m", "WIP: still wiring this up")

	r := &git.ExecRunner{Dir: repo.Dir}
	info := detectPromptInfo(context.Background(), r, promptIncludes{wip: true})
	if !info.WIP {
		t.Errorf("expected WIP=true for 'WIP: ...' subject, got %+v", info)
	}

	// Sanity: include off → WIP stays false even when HEAD is a WIP commit.
	infoOff := detectPromptInfo(context.Background(), r, promptIncludes{})
	if infoOff.WIP {
		t.Errorf("expected WIP=false when include flag is off, got %+v", infoOff)
	}
}
