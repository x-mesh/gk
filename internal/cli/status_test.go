package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// newStatusCmd builds a fresh cobra command backed by runStatus for testing.
func newStatusCmd(t *testing.T, repoDir string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	cmd := &cobra.Command{
		Use:  "status",
		RunE: runStatus,
	}
	cmd.SetOut(buf)
	// override flagRepo so ExecRunner points at the temp repo
	flagRepo = repoDir
	return cmd, buf
}

func TestRunStatus_Clean(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "working tree clean") {
		t.Errorf("expected 'working tree clean', got:\n%s", out)
	}
}

func TestRunStatus_Untracked(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	r.WriteFile("newfile.txt", "hello\n")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "untracked:") {
		t.Errorf("expected 'untracked:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "newfile.txt") {
		t.Errorf("expected 'newfile.txt' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "??") {
		t.Errorf("expected '??' marker, got:\n%s", out)
	}
}

func TestRunStatus_Modified(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	// create and commit a file first
	r.WriteFile("tracked.txt", "original\n")
	r.RunGit("add", "tracked.txt")
	r.RunGit("commit", "-m", "add tracked.txt")
	// now modify it without staging
	r.WriteFile("tracked.txt", "modified\n")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "modified:") {
		t.Errorf("expected 'modified:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "tracked.txt") {
		t.Errorf("expected 'tracked.txt' in output, got:\n%s", out)
	}
}

func TestRunStatus_Staged(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	// create and commit a file first
	r.WriteFile("staged.txt", "original\n")
	r.RunGit("add", "staged.txt")
	r.RunGit("commit", "-m", "add staged.txt")
	// modify and stage
	r.WriteFile("staged.txt", "staged content\n")
	r.RunGit("add", "staged.txt")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "staged:") {
		t.Errorf("expected 'staged:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "staged.txt") {
		t.Errorf("expected 'staged.txt' in output, got:\n%s", out)
	}
}

func TestConflictHunkCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "merge.ts")
	body := `line1
<<<<<<< HEAD
ours
=======
theirs
>>>>>>> branch-a
middle
<<<<<<< HEAD
more-ours
=======
more-theirs
>>>>>>> branch-a
tail
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if got := conflictHunkCount(dir, "merge.ts"); got != 2 {
		t.Errorf("hunks = %d, want 2", got)
	}
}

func TestConflictAnatomy(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.ts"), []byte("<<<<<<< a\nx\n=======\ny\n>>>>>>> b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := conflictAnatomy(dir, git.StatusEntry{XY: "UU", Path: "f.ts", Kind: git.KindUnmerged})
	for _, want := range []string{"1 hunk", "both modified"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "hunks") {
		t.Errorf("singular hunk should not render plural, got %q", got)
	}
}

func TestBuildStatusTree_CollapsesSingletons(t *testing.T) {
	entries := []git.StatusEntry{
		{Path: "src/api/v2/auth.ts", XY: ".M", Kind: 0},
		{Path: "src/api/user.ts", XY: ".M", Kind: 0},
		{Path: "src/foo.ts", XY: ".M", Kind: 0},
	}
	root := buildStatusTree(entries)

	src, ok := root.children["src"]
	if !ok {
		t.Fatalf("expected 'src' child, got keys: %v", keysOf(root.children))
	}
	// 'src' should have 2 children: 'api' (dir with 2 kids) and 'foo.ts' (leaf)
	if len(src.children) != 2 {
		t.Errorf("expected 2 children under src, got %d: %v", len(src.children), keysOf(src.children))
	}
	// 'api' should have 2 leaves: 'v2/auth.ts' (collapsed) and 'user.ts'.
	api, ok := src.children["api"]
	if !ok {
		t.Fatalf("expected 'api' child, got %v", keysOf(src.children))
	}
	if _, ok := api.children["v2/auth.ts"]; !ok {
		t.Errorf("expected v2/auth.ts collapsed leaf under api, got %v", keysOf(api.children))
	}
}

func keysOf(m map[string]*treeNode) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRenderStatusTree_Output(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	entries := []git.StatusEntry{
		{Path: "src/api/user.ts", XY: ".M"},
		{Path: "src/foo.ts", XY: "A."},
		{Path: "README.md", XY: "??", Kind: git.KindUntracked},
	}
	buf := &bytes.Buffer{}
	renderStatusTree(buf, entries)
	out := buf.String()
	for _, want := range []string{"src/", "api/", "user.ts", "foo.ts", "README.md", "├─", "└─"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatAge(t *testing.T) {
	cases := []struct {
		dur  time.Duration
		want string
	}{
		{30 * time.Second, ""},
		{2 * time.Minute, "2m"},
		{90 * time.Minute, "1h"},
		{25 * time.Hour, "1d"},
		{10 * 24 * time.Hour, "10d"},
		{20 * 24 * time.Hour, "2w"},
		{90 * 24 * time.Hour, "3mo"},
		{400 * 24 * time.Hour, "1y"},
	}
	for _, tc := range cases {
		if got := formatAge(tc.dur); got != tc.want {
			t.Errorf("formatAge(%s) = %q, want %q", tc.dur, got, tc.want)
		}
	}
}

func TestUntrackedAge(t *testing.T) {
	dir := t.TempDir()
	recent := filepath.Join(dir, "recent.txt")
	old := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(recent, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// backdate old.txt by 30 days
	past := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if got := untrackedAge(dir, "recent.txt"); got != "" {
		t.Errorf("recent file should report empty age, got %q", got)
	}
	got := untrackedAge(dir, "old.txt")
	if !strings.HasSuffix(got, "d") && !strings.HasSuffix(got, "w") && !strings.HasSuffix(got, "mo") {
		t.Errorf("old file should report day/week/month age, got %q", got)
	}
}

func TestRenderTypesChip(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	mk := func(paths ...string) []git.StatusEntry {
		out := make([]git.StatusEntry, 0, len(paths))
		for _, p := range paths {
			out = append(out, git.StatusEntry{Path: p})
		}
		return out
	}

	t.Run("basic extensions", func(t *testing.T) {
		got := renderTypesChip(mk("a.ts", "b.ts", "c.ts", "d.md", "e.md", "x.lock"))
		for _, want := range []string{"types:", ".ts×3", ".md×2", ".lock×1"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
	})

	t.Run("lockfile collapsed", func(t *testing.T) {
		got := renderTypesChip(mk("package-lock.json", "go.sum", "Cargo.lock"))
		if !strings.Contains(got, ".lock×2") {
			t.Errorf("expected .lock to collapse to ×2 (package-lock + Cargo), got %q", got)
		}
	})

	t.Run("no extension falls back to basename", func(t *testing.T) {
		got := renderTypesChip(mk("Makefile", "Dockerfile"))
		if !strings.Contains(got, "Makefile×1") || !strings.Contains(got, "Dockerfile×1") {
			t.Errorf("expected basename fallback, got %q", got)
		}
	})

	t.Run("too many kinds returns empty", func(t *testing.T) {
		paths := make([]string, 0, 41)
		for i := 0; i < 41; i++ {
			paths = append(paths, fmt.Sprintf("f.e%d", i))
		}
		if got := renderTypesChip(mk(paths...)); got != "" {
			t.Errorf("expected empty for >40 kinds, got %q", got)
		}
	})
}

func TestRenderProgressMeter(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	mk := func(c, s, m, u int) groupedEntries {
		g := groupedEntries{}
		for i := 0; i < c; i++ {
			g.Unmerged = append(g.Unmerged, git.StatusEntry{})
		}
		for i := 0; i < s; i++ {
			g.Staged = append(g.Staged, git.StatusEntry{})
		}
		for i := 0; i < m; i++ {
			g.Modified = append(g.Modified, git.StatusEntry{})
		}
		for i := 0; i < u; i++ {
			g.Untracked = append(g.Untracked, git.StatusEntry{})
		}
		return g
	}

	t.Run("empty", func(t *testing.T) {
		if got := renderProgressMeter(groupedEntries{}); got != "" {
			t.Errorf("expected empty for 0 entries, got %q", got)
		}
	})

	t.Run("mixed shows verbs", func(t *testing.T) {
		got := renderProgressMeter(mk(1, 3, 5, 1))
		for _, want := range []string{"clean:", "30%", "resolve 1", "stage 5", "commit 3", "discard-or-track 1"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
	})

	t.Run("all staged shows 100%", func(t *testing.T) {
		got := renderProgressMeter(mk(0, 4, 0, 0))
		if !strings.Contains(got, "100%") {
			t.Errorf("expected 100%% for all staged, got %q", got)
		}
		if strings.Contains(got, "░") {
			t.Errorf("expected no empty cells at 100%%, got %q", got)
		}
	})
}

func TestRenderDensityBar(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	mk := func(c, s, m, u int) groupedEntries {
		g := groupedEntries{}
		for i := 0; i < c; i++ {
			g.Unmerged = append(g.Unmerged, git.StatusEntry{Kind: git.KindUnmerged})
		}
		for i := 0; i < s; i++ {
			g.Staged = append(g.Staged, git.StatusEntry{})
		}
		for i := 0; i < m; i++ {
			g.Modified = append(g.Modified, git.StatusEntry{})
		}
		for i := 0; i < u; i++ {
			g.Untracked = append(g.Untracked, git.StatusEntry{Kind: git.KindUntracked})
		}
		return g
	}

	t.Run("empty", func(t *testing.T) {
		if got := renderDensityBar(groupedEntries{}); got != "" {
			t.Errorf("expected empty for 0 entries, got %q", got)
		}
	})

	t.Run("mixed", func(t *testing.T) {
		got := renderDensityBar(mk(1, 5, 2, 8))
		for _, want := range []string{"tree:", "1C", "5S", "2M", "8?", "16 files"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
		// Verify total bar cells == 20 (width), composed of 4 glyph kinds.
		totalCells := strings.Count(got, "▓") + strings.Count(got, "█") +
			strings.Count(got, "▒") + strings.Count(got, "░")
		if totalCells != 20 {
			t.Errorf("expected 20 bar cells, got %d (bar=%q)", totalCells, got)
		}
	})

	t.Run("single kind", func(t *testing.T) {
		got := renderDensityBar(mk(0, 0, 0, 3))
		if !strings.Contains(got, "3?") {
			t.Errorf("expected 3? marker, got %q", got)
		}
		if !strings.Contains(got, strings.Repeat("░", 20)) {
			t.Errorf("expected 20 ░ cells, got %q", got)
		}
	})
}

func TestRenderDivergenceGauge(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	cases := []struct {
		name           string
		ahead, behind  int
		mustContain    []string
		mustNotContain []string
	}{
		{
			name:  "in sync",
			ahead: 0, behind: 0,
			mustContain:    []string{"[········│········]", "in sync"},
			mustNotContain: []string{"↑", "↓"},
		},
		{
			name:  "ahead only",
			ahead: 2, behind: 0,
			mustContain:    []string{"······▓▓│········", "↑2"},
			mustNotContain: []string{"↓"},
		},
		{
			name:  "behind only",
			ahead: 0, behind: 3,
			mustContain:    []string{"········│▓▓▓·····", "↓3"},
			mustNotContain: []string{"↑"},
		},
		{
			name:  "both",
			ahead: 1, behind: 4,
			mustContain: []string{"↑1", "↓4"},
		},
		{
			name:  "clamp",
			ahead: 50, behind: 0,
			mustContain: []string{"▓▓▓▓▓▓▓▓│", "↑50"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderDivergenceGauge(tc.ahead, tc.behind)
			for _, s := range tc.mustContain {
				if !strings.Contains(got, s) {
					t.Errorf("gauge missing %q\ngot: %s", s, got)
				}
			}
			for _, s := range tc.mustNotContain {
				if strings.Contains(got, s) {
					t.Errorf("gauge should not contain %q\ngot: %s", s, got)
				}
			}
		})
	}
}

func TestRunStatus_Conflict(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)

	// create conflict: two branches modify same file
	r.WriteFile("conflict.txt", "base\n")
	r.RunGit("add", "conflict.txt")
	r.RunGit("commit", "-m", "add conflict.txt")

	r.RunGit("checkout", "-b", "branch-a")
	r.WriteFile("conflict.txt", "branch-a content\n")
	r.RunGit("add", "conflict.txt")
	r.RunGit("commit", "-m", "branch-a change")

	r.RunGit("checkout", "main")
	r.WriteFile("conflict.txt", "main content\n")
	r.RunGit("add", "conflict.txt")
	r.RunGit("commit", "-m", "main change")

	// attempt merge to create conflict
	_, mergeErr := r.TryGit("merge", "--no-ff", "branch-a")
	if mergeErr == nil {
		// no conflict occurred — skip
		t.Skip("no merge conflict produced; skipping conflict test")
	}

	// verify conflict state
	runner := &git.ExecRunner{Dir: r.Dir}
	client := git.NewClient(runner)
	st, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("client.Status: %v", err)
	}

	hasConflict := false
	for _, e := range st.Entries {
		if e.Kind == git.KindUnmerged {
			hasConflict = true
			break
		}
	}
	if !hasConflict {
		t.Skip("no unmerged entries found; skipping conflict test")
	}

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "conflicts:") {
		t.Errorf("expected 'conflicts:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "conflict.txt") {
		t.Errorf("expected 'conflict.txt' in output, got:\n%s", out)
	}
}
