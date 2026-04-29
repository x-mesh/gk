package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
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

// Default viz now renders the tree layout, so instead of a section
// header (`untracked:`), we assert on the XY marker and the path
// — both preserved under tree view. Callers who want the old layout
// can pass `--vis=none`.
func TestRunStatus_Untracked(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	r.WriteFile("newfile.txt", "hello\n")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "newfile.txt") {
		t.Errorf("expected 'newfile.txt' in output, got:\n%s", out)
	}
	// Default XY style is "labels"; untracked rendered as "new".
	if !strings.Contains(out, "new") {
		t.Errorf("expected 'new' label, got:\n%s", out)
	}
}

func TestRunStatus_Modified(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	r.WriteFile("tracked.txt", "original\n")
	r.RunGit("add", "tracked.txt")
	r.RunGit("commit", "-m", "add tracked.txt")
	r.WriteFile("tracked.txt", "modified\n")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	// Default XY style is "labels"; worktree-modified renders as "mod".
	if !strings.Contains(out, "mod") {
		t.Errorf("expected 'mod' label, got:\n%s", out)
	}
	if !strings.Contains(out, "tracked.txt") {
		t.Errorf("expected 'tracked.txt' in output, got:\n%s", out)
	}
}

func TestRunStatus_Staged(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := testutil.NewRepo(t)
	r.WriteFile("staged.txt", "original\n")
	r.RunGit("add", "staged.txt")
	r.RunGit("commit", "-m", "add staged.txt")
	r.WriteFile("staged.txt", "staged content\n")
	r.RunGit("add", "staged.txt")

	cmd, buf := newStatusCmd(t, r.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	out := buf.String()
	// Default XY style is "labels"; index-modified renders as "staged".
	if !strings.Contains(out, "staged") {
		t.Errorf("expected 'staged' label, got:\n%s", out)
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

func TestFetchDebounceMarker(t *testing.T) {
	dir := t.TempDir()
	gkDir := filepath.Join(dir, "gk")
	marker := fetchMarkerPath(dir)

	if recentlyFetched(dir) {
		t.Fatal("no marker yet → recentlyFetched should be false")
	}

	// Mark a fresh fetch. Verify the debounce window kicks in.
	markFetch(dir)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("markFetch did not create marker under %s: %v", gkDir, err)
	}
	if !recentlyFetched(dir) {
		t.Error("just-written marker should trip the debounce")
	}

	// Backdate the marker past the window — now it should be stale.
	old := time.Now().Add(-statusFetchDebounce - time.Second)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	if recentlyFetched(dir) {
		t.Error("marker older than statusFetchDebounce should not trip")
	}
}

func TestFileKindGlyph(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	cases := []struct {
		path      string
		wantGlyph string
	}{
		{"src/main.go", "●"},
		{"src/main_test.go", "◐"},
		{"src/foo.ts", "●"},
		{"src/foo.test.ts", "◐"},
		{"README.md", "¶"},
		{"docs/intro.rst", "¶"},
		{"config.yaml", "◆"},
		{"settings.toml", "◆"},
		{".env", "◆"},
		{"Makefile", "◆"},
		{"package-lock.json", "⊙"},
		{"go.sum", "⊙"},
		{"dist/gen.js", "↻"},
		{"src/api.pb.go", "↻"},
		{"node_modules/x/index.js", "↻"},
		{"assets/logo.png", "▣"},
		{"unknown.xyz", "·"},
	}
	for _, tc := range cases {
		g, _ := fileKindGlyph(tc.path)
		if g != tc.wantGlyph {
			t.Errorf("fileKindGlyph(%q) = %q, want %q", tc.path, g, tc.wantGlyph)
		}
	}
}

func TestTopDir(t *testing.T) {
	cases := []struct{ in, want string }{
		{"src/api/user.go", "src/"},
		{"main.go", "."},
		{"", "."},
		{"a/b", "a/"},
	}
	for _, tc := range cases {
		if got := topDir(tc.in); got != tc.want {
			t.Errorf("topDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderStatusHeatmap(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	entries := []git.StatusEntry{
		{Path: "src/api/a.go", XY: "M.", Kind: 0},
		{Path: "src/api/b.go", XY: ".M", Kind: 0},
		{Path: "src/ui/c.tsx", XY: ".M", Kind: 0},
		{Path: "README.md", XY: "??", Kind: git.KindUntracked},
	}
	lines := renderStatusHeatmap(entries)
	// Rows bucket by top-level dir: "src/" (3 files) and "." (README) → 2
	// data rows + 1 header line = 3 total.
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (header + 2 dirs), got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "heatmap:") {
		t.Errorf("missing header marker: %q", lines[0])
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"C", "S", "M", "?", "src/", "."} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in heatmap output:\n%s", want, joined)
		}
	}
}

func TestSincePushSuffix(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	// Set up a real upstream: bare remote + push -u. `@{u}` only resolves
	// when the remote-tracking branch was created through a fetch or push,
	// not just via `update-ref`. Setting the configs alone fails with
	// "upstream branch not stored as a remote-tracking branch".
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	cmdInit := exec.Command("git", "init", "-q", "--bare", "-b", "main", bareDir)
	if out, err := cmdInit.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	r.RunGit("remote", "add", "origin", bareDir)
	r.RunGit("push", "-q", "-u", "origin", "main")

	runner := &git.ExecRunner{Dir: r.Dir}
	// No unpushed commits yet — ok=true (upstream known), suffix empty.
	got, ok := sincePushSuffix(context.Background(), runner)
	if !ok {
		t.Errorf("upstream configured → expected ok=true, got ok=false")
	}
	if got != "" {
		t.Errorf("no unpushed → expected empty suffix, got %q", got)
	}
	// Add two unpushed commits.
	r.WriteFile("a.txt", "a\n")
	r.RunGit("add", "a.txt")
	r.RunGit("commit", "-m", "unpushed 1")
	r.WriteFile("b.txt", "b\n")
	r.RunGit("add", "b.txt")
	r.RunGit("commit", "-m", "unpushed 2")

	got, ok = sincePushSuffix(context.Background(), runner)
	if !ok {
		t.Fatalf("expected ok=true after pushing commits, got ok=false")
	}
	if !strings.Contains(got, "since push") {
		t.Errorf("expected 'since push' prefix, got %q", got)
	}
	if !strings.Contains(got, "(2c)") {
		t.Errorf("expected '(2c)' count suffix, got %q", got)
	}
}

// TestSincePushSuffix_UnknownWhenNoUpstream verifies the error-vs-zero
// split: a repo without an upstream returns ok=false (unknown) so the
// caller can render `?` instead of silently claiming up-to-date.
func TestSincePushSuffix_UnknownWhenNoUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t) // fresh repo, no upstream configured
	runner := &git.ExecRunner{Dir: r.Dir}
	_, ok := sincePushSuffix(context.Background(), runner)
	if ok {
		t.Error("no upstream → expected ok=false (unknown), got ok=true")
	}
}

func TestRenderStashSummary(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}

	// No stashes yet → empty output.
	if got := renderStashSummary(context.Background(), runner); got != "" {
		t.Errorf("no stashes → expected empty, got %q", got)
	}

	// Create a stash.
	r.WriteFile("x.txt", "x\n")
	r.RunGit("add", "x.txt")
	r.RunGit("commit", "-m", "seed")
	r.WriteFile("x.txt", "x modified\n")
	r.RunGit("stash", "push", "-m", "wip demo")

	got := renderStashSummary(context.Background(), runner)
	if !strings.Contains(got, "stash:") {
		t.Errorf("expected 'stash:' marker, got %q", got)
	}
	if !strings.Contains(got, "1 entry") {
		t.Errorf("expected '1 entry', got %q", got)
	}
}

func TestBranchDivergence(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	r.CreateBranch("feature/x")
	r.WriteFile("a.txt", "line1\n")
	r.RunGit("add", "a.txt")
	r.RunGit("commit", "-m", "feature commit 1")
	r.WriteFile("a.txt", "line1\nline2\n")
	r.RunGit("add", "a.txt")
	r.RunGit("commit", "-m", "feature commit 2")
	// main stays one commit behind the feature tip.

	runner := &git.ExecRunner{Dir: r.Dir}
	ahead, behind, ok := branchDivergence(context.Background(), runner, "main", "feature/x")
	if !ok {
		t.Fatal("branchDivergence returned ok=false")
	}
	if ahead != 2 || behind != 0 {
		t.Errorf("ahead=%d behind=%d, want 2 / 0", ahead, behind)
	}

	// Push main forward and verify the "behind" branch picks that up.
	r.Checkout("main")
	r.WriteFile("b.txt", "mainonly\n")
	r.RunGit("add", "b.txt")
	r.RunGit("commit", "-m", "main-only commit")
	ahead2, behind2, ok := branchDivergence(context.Background(), runner, "main", "feature/x")
	if !ok {
		t.Fatal("branchDivergence returned ok=false (2)")
	}
	if ahead2 != 2 || behind2 != 1 {
		t.Errorf("ahead=%d behind=%d, want 2 / 1", ahead2, behind2)
	}
}

func TestFastPathDebounced(t *testing.T) {
	repo := t.TempDir()
	dotGit := filepath.Join(repo, ".git")
	if err := os.MkdirAll(dotGit, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("no marker → not debounced", func(t *testing.T) {
		if fastPathDebounced(repo) {
			t.Error("expected false for missing marker")
		}
	})

	t.Run("fresh marker → debounced", func(t *testing.T) {
		markFetch(dotGit)
		if !fastPathDebounced(repo) {
			t.Error("expected true for fresh marker")
		}
	})

	t.Run("stale marker → not debounced", func(t *testing.T) {
		old := time.Now().Add(-statusFetchDebounce - time.Second)
		if err := os.Chtimes(fetchMarkerPath(dotGit), old, old); err != nil {
			t.Fatal(err)
		}
		if fastPathDebounced(repo) {
			t.Error("expected false for stale marker")
		}
	})

	t.Run("worktree layout (.git is a file) → fast path misses", func(t *testing.T) {
		wt := t.TempDir()
		if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere"), 0o644); err != nil {
			t.Fatal(err)
		}
		if fastPathDebounced(wt) {
			t.Error("worktree should fall through to rev-parse path")
		}
	})
}

func TestShouldAutoFetch(t *testing.T) {
	off := &config.Config{Status: config.StatusConfig{AutoFetch: false}}
	on := &config.Config{Status: config.StatusConfig{AutoFetch: true}}

	t.Run("default off — no flag, no config", func(t *testing.T) {
		statusFetch = false
		cmd := &cobra.Command{Use: "status"}
		if shouldAutoFetch(cmd, off) {
			t.Error("expected no fetch by default")
		}
	})

	t.Run("--fetch flag enables", func(t *testing.T) {
		statusFetch = true
		t.Cleanup(func() { statusFetch = false })
		cmd := &cobra.Command{Use: "status"}
		if !shouldAutoFetch(cmd, off) {
			t.Error("--fetch should enable")
		}
	})

	t.Run("config auto_fetch=true enables globally", func(t *testing.T) {
		statusFetch = false
		cmd := &cobra.Command{Use: "status"}
		if !shouldAutoFetch(cmd, on) {
			t.Error("config AutoFetch=true should enable")
		}
	})

	t.Run("nil config treated as off", func(t *testing.T) {
		statusFetch = false
		cmd := &cobra.Command{Use: "status"}
		if shouldAutoFetch(cmd, nil) {
			t.Error("nil config should not enable fetch")
		}
	})
}

func TestResolveStatusVis(t *testing.T) {
	cfg := &config.Config{Status: config.StatusConfig{Vis: []string{"gauge", "bar", "progress", "tree", "staleness"}}}

	t.Run("no flag → config default", func(t *testing.T) {
		cmd := &cobra.Command{Use: "status"}
		cmd.Flags().StringSliceVar(&statusVisFlags, "vis", nil, "")
		got := resolveStatusVis(cmd, cfg)
		if strings.Join(got, ",") != "gauge,bar,progress,tree,staleness" {
			t.Errorf("got %v, want [gauge bar progress tree staleness]", got)
		}
	})

	t.Run("--vis none → nil", func(t *testing.T) {
		cmd := &cobra.Command{Use: "status"}
		cmd.Flags().StringSliceVar(&statusVisFlags, "vis", nil, "")
		_ = cmd.Flags().Set("vis", "none")
		got := resolveStatusVis(cmd, cfg)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("--vis gauge → overrides config", func(t *testing.T) {
		cmd := &cobra.Command{Use: "status"}
		statusVisFlags = nil
		cmd.Flags().StringSliceVar(&statusVisFlags, "vis", nil, "")
		_ = cmd.Flags().Set("vis", "gauge")
		got := resolveStatusVis(cmd, cfg)
		if strings.Join(got, ",") != "gauge" {
			t.Errorf("got %v, want [gauge]", got)
		}
	})

	t.Run("nil config → nil", func(t *testing.T) {
		cmd := &cobra.Command{Use: "status"}
		cmd.Flags().StringSliceVar(&statusVisFlags, "vis", nil, "")
		got := resolveStatusVis(cmd, nil)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
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
	renderStatusTree(buf, entries, nil)
	out := buf.String()
	for _, want := range []string{"src/", "api/", "user.ts", "foo.ts", "README.md", "├─", "└─"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGroupEntriesSeparatesSubmoduleDirtiness(t *testing.T) {
	entries := []git.StatusEntry{
		{Path: "ghostty", XY: ".M", Sub: "S..U", Kind: git.KindSubmodule},
		{Path: "main.go", XY: ".M", Kind: git.KindOrdinary},
	}
	g := groupEntries(entries)
	if len(g.Submodules) != 1 {
		t.Fatalf("Submodules: want 1, got %d", len(g.Submodules))
	}
	if len(g.Modified) != 1 {
		t.Fatalf("Modified: want 1, got %d", len(g.Modified))
	}
	if committableCount(g) != 1 {
		t.Fatalf("committableCount: want 1, got %d", committableCount(g))
	}
}

func TestRenderSubmoduleSection(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	buf := &bytes.Buffer{}
	renderSubmoduleSection(buf, context.Background(), nil, []git.StatusEntry{
		{Path: "ghostty", Sub: "S..U", Kind: git.KindSubmodule},
	}, false, 0)
	out := buf.String()
	for _, want := range []string{"submodules: 1 dirty", "submod", "ghostty", "untracked inside"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderSubmoduleSectionVerboseAction(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	buf := &bytes.Buffer{}
	renderSubmoduleSection(buf, context.Background(), nil, []git.StatusEntry{
		{Path: "ghostty", Sub: "S..U", Kind: git.KindSubmodule},
	}, true, 1)
	if out := buf.String(); !strings.Contains(out, "cd ghostty && gk status") {
		t.Errorf("expected verbose submodule action, got:\n%s", out)
	}
}

func TestRenderEntryStateSubmoduleGitlink(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	e := git.StatusEntry{Path: "ghostty", XY: ".M", Sub: "SC..", Kind: git.KindOrdinary}
	if got := renderEntryState(e, xyStyleLabels); !strings.Contains(got, "submod") {
		t.Fatalf("expected submod state, got %q", got)
	}
	if got := renderEntryDetail(e); !strings.Contains(got, "commit changed") {
		t.Fatalf("expected commit changed detail, got %q", got)
	}
}

func TestRenderEntryStateSplit(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	e := git.StatusEntry{Path: "app.go", XY: "MM", Kind: git.KindOrdinary}
	if got := renderEntryState(e, xyStyleLabels); !strings.Contains(got, "split") {
		t.Fatalf("expected split state, got %q", got)
	}
	if got := renderEntryDetail(e); !strings.Contains(got, "staged + unstaged") {
		t.Fatalf("expected split detail, got %q", got)
	}
}

func TestNextStatusActionUsesGKCommands(t *testing.T) {
	cases := []struct {
		name string
		g    groupedEntries
		st   *git.Status
		want string
	}{
		{"conflicts", groupedEntries{Unmerged: []git.StatusEntry{{Path: "a"}}}, &git.Status{}, "gk resolve"},
		{"committable", groupedEntries{Modified: []git.StatusEntry{{Path: "a"}}}, &git.Status{}, "gk commit --dry-run"},
		{"submodule only", groupedEntries{Submodules: []git.StatusEntry{{Path: "ghostty"}}}, &git.Status{}, "gk status -v"},
		{"behind clean", groupedEntries{}, &git.Status{Behind: 1}, "gk sync"},
		{"ahead clean", groupedEntries{}, &git.Status{Ahead: 1}, "gk push"},
	}
	for _, tc := range cases {
		if got := nextStatusAction(tc.g, tc.st, 0); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStatusExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		g    groupedEntries
		st   *git.Status
		want int
	}{
		{"clean", groupedEntries{}, &git.Status{}, 0},
		{"dirty", groupedEntries{Modified: []git.StatusEntry{{Path: "a"}}}, &git.Status{}, 1},
		{"submodule only", groupedEntries{Submodules: []git.StatusEntry{{Path: "sub"}}}, &git.Status{}, 2},
		{"conflict", groupedEntries{Unmerged: []git.StatusEntry{{Path: "a"}}}, &git.Status{Behind: 1}, 3},
		{"behind", groupedEntries{}, &git.Status{Behind: 1}, 4},
		// Priority guard: dirty beats behind.
		{"dirty and behind", groupedEntries{Modified: []git.StatusEntry{{Path: "a"}}}, &git.Status{Behind: 2}, 1},
		// Priority guard: submodule-only beats behind.
		{"submodule only and behind", groupedEntries{Submodules: []git.StatusEntry{{Path: "sub"}}}, &git.Status{Behind: 1}, 2},
	}
	for _, tc := range cases {
		if got := statusExitCodeFor(tc.g, tc.st); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestSubmoduleDetailSummary(t *testing.T) {
	r := testutil.NewRepo(t)
	sub := filepath.Join(r.Dir, "ghostty")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir submodule dir: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = sub
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init submodule: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = sub
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config email: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = sub
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config name: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(sub, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write tracked: %v", err)
	}
	cmd = exec.Command("git", "add", "tracked.txt")
	cmd.Dir = sub
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "base")
	cmd.Dir = sub
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(sub, "extra.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	got := submoduleDetailSummary(context.Background(), r.Dir, "ghostty")
	for _, want := range []string{"branch", "1 untracked"} {
		if !strings.Contains(got, want) {
			t.Fatalf("submoduleDetailSummary missing %q: %q", want, got)
		}
	}
}

func TestSortStatusEntriesForTopPrioritizesActions(t *testing.T) {
	entries := []git.StatusEntry{
		{Path: "z-untracked", XY: "??", Kind: git.KindUntracked},
		{Path: "a-modified", XY: ".M", Kind: git.KindOrdinary},
		{Path: "m-staged", XY: "M.", Kind: git.KindOrdinary},
		{Path: "b-conflict", XY: "UU", Kind: git.KindUnmerged},
	}
	sortStatusEntriesForTop(entries)
	got := []string{entries[0].Path, entries[1].Path, entries[2].Path, entries[3].Path}
	want := []string{"b-conflict", "m-staged", "a-modified", "z-untracked"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order: want %v, got %v", want, got)
		}
	}
}

func TestRenderStatusJSON(t *testing.T) {
	prevRepo := flagRepo
	prevVerbose := statusVerbose
	t.Cleanup(func() {
		flagRepo = prevRepo
		statusVerbose = prevVerbose
	})
	flagRepo = "/repo"
	statusVerbose = 0
	st := &git.Status{
		Branch:   "main",
		Upstream: "origin/main",
		Entries: []git.StatusEntry{
			{Path: "ghostty", XY: ".M", Sub: "S..U", Kind: git.KindSubmodule},
			{Path: "main.go", XY: ".M", Kind: git.KindOrdinary},
		},
	}
	g := groupEntries(st.Entries)
	buf := &bytes.Buffer{}
	if err := renderStatusJSON(buf, st, g, committableEntries(st.Entries)); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	var out statusJSON
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("json unmarshal: %v\n%s", err, buf.String())
	}
	if out.Counts.Committable != 1 || out.Counts.DirtySubmodules != 1 {
		t.Fatalf("counts: %+v", out.Counts)
	}
	if out.Next != "gk commit --dry-run" {
		t.Fatalf("next: want gk commit --dry-run, got %q", out.Next)
	}
	if len(out.Entries) != 1 || out.Entries[0].Path != "main.go" {
		t.Fatalf("entries: %+v", out.Entries)
	}
	if len(out.Submodules) != 1 || out.Submodules[0].Action != "cd ghostty && gk status" {
		t.Fatalf("submodules: %+v", out.Submodules)
	}
}

func TestWriteChildren_NarrowTTYCompression(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	entries := []git.StatusEntry{
		{Path: "src/api/v2/auth.ts", XY: ".M"},
		{Path: "src/foo.ts", XY: "A."},
	}
	root := buildStatusTree(entries)
	collapseSingletons(root)

	faint := color.New(color.Faint).SprintFunc()

	t.Run("narrow mode uses 2-cell glyphs", func(t *testing.T) {
		buf := &bytes.Buffer{}
		writeChildren(buf, root, "", faint, nil, true, false)
		out := buf.String()
		// Narrow glyphs: ├ and └ without the trailing ─ bar.
		if strings.Contains(out, "├─") || strings.Contains(out, "└─") {
			t.Errorf("narrow mode should not emit 3-cell glyphs, got:\n%s", out)
		}
		if !strings.Contains(out, "├ ") && !strings.Contains(out, "└ ") {
			t.Errorf("narrow mode should emit 2-cell glyphs, got:\n%s", out)
		}
	})

	t.Run("dropBadge suppresses (N) subtree count", func(t *testing.T) {
		buf := &bytes.Buffer{}
		writeChildren(buf, root, "", faint, nil, true, true)
		out := buf.String()
		if strings.Contains(out, "(") && strings.Contains(out, ")") {
			t.Errorf("dropBadge should omit (N) badge, got:\n%s", out)
		}
	})

	t.Run("normal mode keeps 3-cell glyphs and badge", func(t *testing.T) {
		buf := &bytes.Buffer{}
		writeChildren(buf, root, "", faint, nil, false, false)
		out := buf.String()
		if !strings.Contains(out, "├─") && !strings.Contains(out, "└─") {
			t.Errorf("normal mode should emit 3-cell glyphs, got:\n%s", out)
		}
	})
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
		// package-lock/go.sum/Cargo all collapse to ".lock".
		if !strings.Contains(got, ".lock×3") {
			t.Errorf("expected .lock×3 collapse, got %q", got)
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

	t.Run("narrow TTY drops tail tokens with +N more", func(t *testing.T) {
		// Budget=25 fits "types:" + 2-3 tokens at most; remaining tokens
		// collapse into "+N more".
		paths := []string{"a.ts", "a.ts", "a.ts", "b.md", "b.md", "c.go", "d.rs", "e.py"}
		got := renderTypesChipWithWidth(mk(paths...), 25)
		if !strings.Contains(got, "+") || !strings.Contains(got, "more") {
			t.Errorf("expected +N more suffix in narrow output, got %q", got)
		}
		if !strings.Contains(got, "types:") {
			t.Errorf("expected types: prefix, got %q", got)
		}
	})

	t.Run("wide TTY emits all tokens", func(t *testing.T) {
		got := renderTypesChipWithWidth(mk("a.ts", "b.md", "c.go"), 200)
		if strings.Contains(got, "more") {
			t.Errorf("wide TTY should not truncate, got %q", got)
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

	t.Run("empty renders 100% clean", func(t *testing.T) {
		got := renderProgressMeter(groupedEntries{})
		for _, want := range []string{"clean:", "100%", "nothing to do"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
	})

	t.Run("mixed shows verbs", func(t *testing.T) {
		got := renderProgressMeter(mk(1, 3, 5, 1))
		for _, want := range []string{"clean:", "30%", "resolve 1", "stage 5", "commit 3", "add 1"} {
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

	t.Run("empty renders clean placeholder", func(t *testing.T) {
		got := renderDensityBar(groupedEntries{})
		for _, want := range []string{"tree:", "(clean)"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
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
			// R3: behind side uses `▒` (lighter shade) so direction is
			// legible without color for red-green colorblind users.
			mustContain:    []string{"········│▒▒▒·····", "↓3"},
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
	// Default XY style is "labels"; unmerged renders as "conflict".
	if !strings.Contains(out, "conflict") {
		t.Errorf("expected 'conflict' label, got:\n%s", out)
	}
	if !strings.Contains(out, "conflict.txt") {
		t.Errorf("expected 'conflict.txt' in output, got:\n%s", out)
	}
}

func TestVisibleWidth(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"hello", 5},
		// ANSI SGR reset around text — escape sequences must not count.
		{"\x1b[32mok\x1b[0m", 2},
		// Bold cyan prefix like compactBranch wraps.
		{"\x1b[1;36mfeat/foo\x1b[0m", 8},
		// Plain multi-rune UTF-8 (branch name with slash).
		{"feat/my-branch", 14},
		// Lone ESC without '[' is not a CSI sequence; counted as one rune.
		{"\x1bX", 2},
	}
	for _, tc := range cases {
		if got := visibleWidth(tc.s); got != tc.want {
			t.Errorf("visibleWidth(%q) = %d, want %d", tc.s, got, tc.want)
		}
	}
}

func TestCompactBranch(t *testing.T) {
	cases := []struct {
		name     string
		maxWidth int
		want     string
	}{
		// Short names are returned unchanged.
		{"main", 32, "main"},
		{"feat/foo", 8, "feat/foo"},
		// Exact boundary — no truncation.
		{"1234567", 7, "1234567"},
		// One over — truncate with middle ellipsis (head=keep/2, tail=keep-head).
		// keep=9, head=4, tail=5 → "feat" + "…" + "actor"
		{"feature/api-v2-auth-refactor", 10, "feat…actor"},
		// keep=3, head=1, tail=2 → "a" + "…" + "ij"
		{"abcdefghij", 4, "a…ij"},
		// maxWidth <= 0 returns original.
		{"anything", 0, "anything"},
	}
	for _, tc := range cases {
		got := compactBranch(tc.name, tc.maxWidth)
		if got != tc.want {
			t.Errorf("compactBranch(%q, %d) = %q, want %q", tc.name, tc.maxWidth, got, tc.want)
		}
	}
}

func TestCompactUpstreamSuffix(t *testing.T) {
	// Use plain fmt functions so results are deterministic without ANSI state.
	cyan := func(format string, a ...interface{}) string { return fmt.Sprintf(format, a...) }
	faint := func(a ...interface{}) string { return fmt.Sprint(a...) }

	cases := []struct {
		branch   string
		upstream string
		wantSub  string // substring that must appear
		wantNot  string // substring that must NOT appear (empty = skip)
	}{
		// Empty upstream → empty output.
		{"main", "", "", ""},
		// Branch name matches upstream branch → keep full upstream for clarity.
		{"main", "origin/main", "origin/main", ""},
		// Branch differs from upstream branch → show full remote/branch.
		{"feat/local", "origin/release", "origin/release", ""},
		// Upstream without slash → render as-is.
		{"main", "upstream", "upstream", ""},
	}
	for _, tc := range cases {
		got := compactUpstreamSuffix(tc.branch, tc.upstream, cyan, faint)
		if tc.upstream == "" {
			if got != "" {
				t.Errorf("empty upstream: want %q, got %q", "", got)
			}
			continue
		}
		if !strings.Contains(got, tc.wantSub) {
			t.Errorf("compactUpstreamSuffix(%q, %q) = %q, missing %q", tc.branch, tc.upstream, got, tc.wantSub)
		}
		if tc.wantNot != "" && strings.Contains(got, tc.wantNot) {
			t.Errorf("compactUpstreamSuffix(%q, %q) = %q, should NOT contain %q", tc.branch, tc.upstream, got, tc.wantNot)
		}
	}
}

func TestFormatDiffStat(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	cases := []struct {
		name   string
		stats  map[string]diffStat
		path   string
		wantEq string // exact expected result
	}{
		{"nil map", nil, "foo.go", ""},
		{"path not in map", map[string]diffStat{"bar.go": {3, 1}}, "foo.go", ""},
		{"zero counts", map[string]diffStat{"foo.go": {0, 0}}, "foo.go", ""},
		{"only added", map[string]diffStat{"foo.go": {5, 0}}, "foo.go", "  +5"},
		{"only removed", map[string]diffStat{"foo.go": {0, 3}}, "foo.go", "  -3"},
		{"both", map[string]diffStat{"foo.go": {5, 3}}, "foo.go", "  +5 -3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDiffStat(tc.stats, tc.path)
			if got != tc.wantEq {
				t.Errorf("formatDiffStat(%v) = %q, want %q", tc.stats, got, tc.wantEq)
			}
		})
	}
}

func TestXYStyle(t *testing.T) {
	t.Run("labels cover every common XY", func(t *testing.T) {
		cases := map[string]string{
			"??": "new",
			"!!": "ignored",
			".M": "mod",
			".D": "del",
			".R": "ren",
			".T": "typ",
			"M.": "staged",
			"A.": "added",
			"D.": "deleted",
			"R.": "renamed",
			"MM": "split",
			"MD": "split",
			"UU": "conflict",
			"AU": "conflict",
			"UA": "conflict",
			"DD": "conflict",
			"AA": "conflict",
		}
		for xy, want := range cases {
			if got := xyLabel(xy); got != want {
				t.Errorf("xyLabel(%q) = %q, want %q", xy, got, want)
			}
		}
	})

	t.Run("glyphs collapse to 5 categories", func(t *testing.T) {
		cases := map[string]string{
			"??": "+",
			"!!": "#",
			".M": "~",
			".D": "~",
			"M.": "●",
			"A.": "●",
			"MM": "◉",
			"UU": "⚔",
			"DD": "⚔",
			"AA": "⚔",
		}
		for xy, want := range cases {
			if got := xyGlyph(xy); got != want {
				t.Errorf("xyGlyph(%q) = %q, want %q", xy, got, want)
			}
		}
	})

	t.Run("renderXY labels mode pads to 8 cells", func(t *testing.T) {
		color.NoColor = true
		t.Cleanup(func() { color.NoColor = false })
		got := renderXY(".M", xyStyleLabels)
		if got != "mod     " {
			t.Errorf("padded labels: got %q (len %d)", got, len(got))
		}
	})

	t.Run("renderXY raw mode preserves git code", func(t *testing.T) {
		color.NoColor = true
		t.Cleanup(func() { color.NoColor = false })
		if got := renderXY("??", xyStyleRaw); got != "??" {
			t.Errorf("raw mode should preserve code, got %q", got)
		}
	})

	t.Run("renderXY glyphs mode returns single glyph", func(t *testing.T) {
		color.NoColor = true
		t.Cleanup(func() { color.NoColor = false })
		if got := renderXY("??", xyStyleGlyphs); got != "+" {
			t.Errorf("glyphs mode: got %q", got)
		}
	})

	t.Run("normalizeXYStyle defends against bad input", func(t *testing.T) {
		cases := map[string]string{
			"LABELS": xyStyleLabels,
			"glyphs": xyStyleGlyphs,
			"raw":    xyStyleRaw,
			"":       xyStyleLabels,
			"bogus":  xyStyleLabels,
			"  raw ": xyStyleRaw,
		}
		for in, want := range cases {
			if got := normalizeXYStyle(in); got != want {
				t.Errorf("normalizeXYStyle(%q) = %q, want %q", in, got, want)
			}
		}
	})
}

// renderUntrackedRemoteHint surfaces silent divergence on branches without
// a configured @{u} — the mem-mesh-main scenario. The three cases cover
// the full decision matrix: tracked / untracked-divergent / fork.

func TestRenderUntrackedRemoteHint_TrackedSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	_, downstream := setupTrackingDownstream(t)

	got := renderUntrackedRemoteHint(context.Background(), execRunnerFor(downstream), nil, "main")
	if got != "" {
		t.Errorf("hint should stay silent for tracked branch, got: %q", got)
	}
}

func TestRenderUntrackedRemoteHint_UntrackedDivergent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	_, downstream := untrackedDownstream(t)

	got := renderUntrackedRemoteHint(context.Background(), execRunnerFor(downstream), nil, "main")
	if !strings.Contains(got, "origin/main") {
		t.Errorf("hint should reference origin/main, got: %q", got)
	}
	if !strings.Contains(got, "↑0 ↓1") {
		t.Errorf("hint should report ↑0 ↓1, got: %q", got)
	}
	if !strings.Contains(got, "set-upstream-to=origin/main main") {
		t.Errorf("hint should suggest the fix command, got: %q", got)
	}
}

// TestStatus_SiblingUntrackedDivergent reproduces the mem-mesh scenario:
// the user is on `develop` (which tracks origin/develop and is in sync) but
// `main` has no upstream and silently diverges from origin/main. status must
// surface this so the user notices without running doctor explicitly.
func TestStatus_SiblingUntrackedDivergent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")

	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "a\n")
	upstream.Commit("feat: a")
	upstream.RunGit("checkout", "-b", "develop")
	upstream.WriteFile("d.txt", "d\n")
	upstream.Commit("feat: develop")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	// main: snapshot (no upstream).
	downstream.RunGit("reset", "--hard", "origin/main")
	// develop: tracking + in sync.
	downstream.RunGit("checkout", "-b", "develop", "origin/develop")
	downstream.RunGit("branch", "--set-upstream-to=origin/develop", "develop")

	// upstream advances main → origin/main pulls ahead by 1.
	upstream.RunGit("checkout", "main")
	upstream.WriteFile("b.txt", "b\n")
	upstream.Commit("feat: b")
	downstream.RunGit("fetch", "origin")
	// downstream stays on develop; main is left behind in silence.

	cmd, buf := newStatusCmd(t, downstream.Dir)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "untracked: main") {
		t.Errorf("expected sibling-branch hint mentioning 'main', got:\n%s", out)
	}
	if !strings.Contains(out, "↑0 ↓1") {
		t.Errorf("expected ↑0 ↓1 for main, got:\n%s", out)
	}
}

func TestRenderOtherUntrackedHint_Empty(t *testing.T) {
	if got := renderOtherUntrackedHint(nil); got != "" {
		t.Errorf("expected empty for no offenders, got %q", got)
	}
}

func TestRenderOtherUntrackedHint_Multiple(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	items := []untrackedDivergent{
		{Branch: "main", Implicit: "origin/main", Ahead: 0, Behind: 4},
		{Branch: "feat/x", Implicit: "origin/feat/x", Ahead: 2, Behind: 1},
		{Branch: "docs", Implicit: "origin/docs", Ahead: 0, Behind: 3},
	}
	got := renderOtherUntrackedHint(items)
	if !strings.Contains(got, "3 untracked branches diverge") {
		t.Errorf("expected count summary, got: %q", got)
	}
	if !strings.Contains(got, "main ↑0 ↓4") || !strings.Contains(got, "feat/x ↑2 ↓1") {
		t.Errorf("expected first two named, got: %q", got)
	}
	if !strings.Contains(got, "+1 more") {
		t.Errorf("expected '+1 more' collapse, got: %q", got)
	}
}

func TestRenderUntrackedRemoteHint_ForkSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	got := renderUntrackedRemoteHint(context.Background(), execRunnerFor(repo), nil, "main")
	if got != "" {
		t.Errorf("hint should stay silent without a same-named remote ref, got: %q", got)
	}
}

func TestBaseDivergenceHint(t *testing.T) {
	cases := []struct {
		name          string
		ahead, behind int
		dirty         bool
		base          string
		want          string
	}{
		{"in sync", 0, 0, false, "main", ""},
		{"in sync dirty", 0, 0, true, "main", ""},
		{"ahead clean", 3, 0, false, "main", "→ ready to merge into main"},
		{"ahead dirty", 3, 0, true, "main", ""},
		{"behind only", 0, 2, false, "main", "→ behind main: gk sync"},
		{"diverged", 2, 1, false, "main", "→ main moved: gk sync"},
		{"diverged dirty", 2, 1, true, "main", "→ main moved: gk sync"},
		{"non-default base", 4, 0, false, "develop", "→ ready to merge into develop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := baseDivergenceHint(tc.ahead, tc.behind, tc.dirty, tc.base)
			if got != tc.want {
				t.Errorf("baseDivergenceHint(%d, %d, %v, %q) = %q, want %q",
					tc.ahead, tc.behind, tc.dirty, tc.base, got, tc.want)
			}
		})
	}
}

// renderBaseDivergence integration tests — verifies the parent-aware swap
// actually shows up in the status line and that fallback prints the right
// stderr warning.

func TestRenderBaseDivergence_WithParent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/parent")
	repo.WriteFile("p.txt", "p\n")
	repo.RunGit("add", "p.txt")
	repo.RunGit("commit", "-m", "p")
	repo.CreateBranch("feat/sub")
	repo.WriteFile("s.txt", "s\n")
	repo.RunGit("add", "s.txt")
	repo.RunGit("commit", "-m", "s")
	repo.RunGit("config", "branch.feat/sub.gk-parent", "feat/parent")
	t.Chdir(repo.Dir)

	cmd := &cobra.Command{Use: "status"}
	cmd.SetContext(context.Background())
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	got := renderBaseDivergence(cmd, runner, client, &config.Config{BaseBranch: "main"}, "feat/sub", false)
	if !strings.Contains(got, "feat/parent") {
		t.Errorf("expected 'feat/parent' in line, got: %q", got)
	}
	if strings.Contains(got, "main") {
		t.Errorf("parent must replace base — should not contain 'main', got: %q", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("happy path must not write to stderr, got: %q", stderr.String())
	}
}

func TestRenderBaseDivergence_ParentMissingFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/sub")
	repo.WriteFile("s.txt", "s\n")
	repo.RunGit("add", "s.txt")
	repo.RunGit("commit", "-m", "s")
	// Set parent metadata to a non-existent branch.
	repo.RunGit("config", "branch.feat/sub.gk-parent", "feat/never-existed")
	t.Chdir(repo.Dir)

	cmd := &cobra.Command{Use: "status"}
	cmd.SetContext(context.Background())
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	got := renderBaseDivergence(cmd, runner, client, &config.Config{BaseBranch: "main"}, "feat/sub", false)
	if strings.Contains(got, "feat/never-existed") {
		t.Errorf("must not show missing parent in line, got: %q", got)
	}
	if !strings.Contains(got, "main") {
		t.Errorf("must fall back to main, got: %q", got)
	}
	if !strings.Contains(stderr.String(), "feat/never-existed not found") {
		t.Errorf("expected stderr warning about missing parent, got: %q", stderr.String())
	}
}

func TestRenderBaseDivergence_NoParent_ByteEqualBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/sub")
	repo.WriteFile("s.txt", "s\n")
	repo.RunGit("add", "s.txt")
	repo.RunGit("commit", "-m", "s")
	t.Chdir(repo.Dir)

	cmd := &cobra.Command{Use: "status"}
	cmd.SetContext(context.Background())
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	got := renderBaseDivergence(cmd, runner, client, &config.Config{BaseBranch: "main"}, "feat/sub", false)
	if !strings.Contains(got, "main") {
		t.Errorf("no parent → must show base, got: %q", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("no parent must not warn, got stderr: %q", stderr.String())
	}
}
