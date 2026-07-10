package cli

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestRenderRepoTree_Deterministic pins the DoD's "tree generation is
// deterministic (sorted)" requirement: `git ls-files`' own order is index
// order, not a sorted one, and repoTreeDir's children live in a Go map
// (unordered iteration) — renderRepoTree must still produce byte-identical
// output regardless of input order, because it sorts at every level before
// walking.
func TestRenderRepoTree_Deterministic(t *testing.T) {
	paths := []string{
		"cmd/gk/main.go",
		"internal/chat/engine.go",
		"internal/chat/systemprompt.go",
		"internal/config/config.go",
		"README.md",
		"go.mod",
	}
	want := renderRepoTree(buildRepoTree(paths), repoMapMaxDepth, repoMapMaxFiles)

	shuffled := append([]string(nil), paths...)
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		rnd.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got := renderRepoTree(buildRepoTree(shuffled), repoMapMaxDepth, repoMapMaxFiles)
		if got != want {
			t.Fatalf("renderRepoTree not deterministic across input order:\nwant:\n%s\ngot:\n%s", want, got)
		}
	}
}

// TestRenderRepoTree_DepthCap checks a directory nested past maxDepth is
// still named but not expanded — its own contents collapse to a single
// "..." marker instead of being individually listed.
func TestRenderRepoTree_DepthCap(t *testing.T) {
	paths := []string{"a/b/c/d/deep.go", "a/b/shallow.go"}
	out := renderRepoTree(buildRepoTree(paths), 2, repoMapMaxFiles)

	if !strings.Contains(out, "a/\n") && !strings.Contains(out, "a/") {
		t.Errorf("top-level dir missing: %s", out)
	}
	if strings.Contains(out, "deep.go") {
		t.Errorf("file past the depth cap must not be listed individually: %s", out)
	}
	if !strings.Contains(out, "...") {
		t.Errorf("directory collapsed at the depth cap must show an elision marker: %s", out)
	}
	if !strings.Contains(out, "shallow.go") {
		t.Errorf("file within the depth cap must still be listed: %s", out)
	}
}

// TestRenderRepoTree_NoElisionWithinDepthCap checks the "..." marker only
// appears when content is actually being cut off by the depth cap — a
// tree that never reaches the cap must render with no elision marker at
// all.
func TestRenderRepoTree_NoElisionWithinDepthCap(t *testing.T) {
	paths := []string{"a/only.go", "b.go"}
	out := renderRepoTree(buildRepoTree(paths), repoMapMaxDepth, repoMapMaxFiles)
	if strings.Contains(out, "...") {
		t.Errorf("nothing exceeds the depth cap here, want no elision marker: %s", out)
	}
}

// TestRenderRepoTree_FileCap checks the total file count (not per-directory)
// is capped, and a truncation summary line reports how many more files
// exist rather than silently dropping them.
func TestRenderRepoTree_FileCap(t *testing.T) {
	var paths []string
	for i := 0; i < 25; i++ {
		paths = append(paths, fmt.Sprintf("file_%02d.txt", i))
	}
	out := renderRepoTree(buildRepoTree(paths), repoMapMaxDepth, 10)

	shown := strings.Count(out, "file_")
	// The truncation summary line also contains "file" text but not the
	// "file_NN.txt" name pattern, so counting "file_" occurrences only
	// counts actual listed entries.
	if shown != 10 {
		t.Errorf("want exactly 10 file lines rendered under a 10-file cap, got %d:\n%s", shown, out)
	}
	if !strings.Contains(out, "15 more file(s) not shown") {
		t.Errorf("want a truncation summary reporting the 15 remaining files: %s", out)
	}
}

// TestRenderRepoTree_NoTruncationMarkerUnderCap checks the trailing
// "more file(s)" line is entirely absent when nothing was truncated.
func TestRenderRepoTree_NoTruncationMarkerUnderCap(t *testing.T) {
	out := renderRepoTree(buildRepoTree([]string{"a.go", "b.go"}), repoMapMaxDepth, repoMapMaxFiles)
	if strings.Contains(out, "not shown") {
		t.Errorf("no truncation happened, want no truncation marker: %s", out)
	}
}

// TestChatRepoMapString_AutoContextOff pins the DoD's "false/unset leaves
// existing behavior unchanged" contract at the collection layer: with
// ai.chat.auto_context unset (the zero value / default), chatRepoMapString
// must return "" even in a repo with tracked files.
func TestChatRepoMapString_AutoContextOff(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("main.go", "package main\n")
	repo.Commit("init")

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	if cfg.AI.Chat.AutoContext {
		t.Fatal("test premise broken: AutoContext default must be false")
	}

	got := chatRepoMapString(context.Background(), runner, &cfg, nil)
	if got != "" {
		t.Errorf("auto_context off/unset must yield empty repo map, got: %q", got)
	}
}

// TestChatRepoMapString_AutoContextOn checks the opposite: turning the
// config on in a repo with tracked files produces a non-empty tree
// containing the tracked paths.
func TestChatRepoMapString_AutoContextOn(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("cmd/gk/main.go", "package main\n")
	repo.WriteFile("README.md", "# hi\n")
	repo.Commit("init")

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	cfg.AI.Chat.AutoContext = true

	got := chatRepoMapString(context.Background(), runner, &cfg, nil)
	if got == "" {
		t.Fatal("auto_context on with tracked files must yield a non-empty repo map")
	}
	if !strings.Contains(got, "cmd/") || !strings.Contains(got, "main.go") || !strings.Contains(got, "README.md") {
		t.Errorf("repo map missing expected tracked paths: %s", got)
	}
}

// TestChatRepoMapString_EmptyRepoDegrades covers the DoD's bare/empty-repo
// edge case: a freshly `git init`-ed repo with no commits (unborn HEAD, no
// tracked files) must degrade to "" rather than erroring — `git ls-files`
// simply reports nothing to track yet.
func TestChatRepoMapString_EmptyRepoDegrades(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		r := &git.ExecRunner{Dir: dir}
		if _, _, err := r.Run(context.Background(), args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	runGit("init")

	runner := &git.ExecRunner{Dir: dir}
	cfg := config.Defaults()
	cfg.AI.Chat.AutoContext = true

	got := chatRepoMapString(context.Background(), runner, &cfg, nil)
	if got != "" {
		t.Errorf("empty repo (no tracked files) must degrade to empty string, got: %q", got)
	}
}

// TestChatRepoMapString_NotAGitDirectoryDegrades checks the fully
// degenerate case (no .git at all, so `git ls-files` errors outright)
// degrades the same way instead of propagating the error.
func TestChatRepoMapString_NotAGitDirectoryDegrades(t *testing.T) {
	dir := t.TempDir()
	runner := &git.ExecRunner{Dir: dir}
	cfg := config.Defaults()
	cfg.AI.Chat.AutoContext = true

	got := chatRepoMapString(context.Background(), runner, &cfg, nil)
	if got != "" {
		t.Errorf("non-git directory must degrade to empty string, got: %q", got)
	}
}

// TestChatRepoMapString_NilCfgDegrades guards the defensive nil check —
// callers always pass a real *config.Config today, but the function must
// not panic if that ever changes.
func TestChatRepoMapString_NilCfgDegrades(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("main.go", "package main\n")
	repo.Commit("init")
	runner := &git.ExecRunner{Dir: repo.Dir}

	got := chatRepoMapString(context.Background(), runner, nil, nil)
	if got != "" {
		t.Errorf("nil cfg must degrade to empty string, got: %q", got)
	}
}

// TestChatRepoMapString_DenyPathsFiltered pins the cross-vendor panel's
// top finding: REPO_MAP must not enumerate paths the deny policy hides.
// Redaction scrubs secret VALUES, which does nothing for a tree whose
// leaked datum is the filename itself — so a tracked, denied file must
// be absent from the rendered map even though `git ls-files` reports it.
func TestChatRepoMapString_DenyPathsFiltered(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("main.go", "package main\n")
	repo.WriteFile("secrets/prod.env", "TOKEN=x\n")
	repo.Commit("init")

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	cfg.AI.Chat.AutoContext = true

	got := chatRepoMapString(context.Background(), runner, &cfg, []string{"secrets/**"})
	if got == "" {
		t.Fatal("tracked non-denied files remain, want a non-empty map")
	}
	if strings.Contains(got, "prod.env") {
		t.Errorf("denied file name leaked into REPO_MAP:\n%s", got)
	}
	if strings.Contains(got, "secrets") {
		t.Errorf("denied dir became visible via its only (denied) child:\n%s", got)
	}
	if !strings.Contains(got, "main.go") {
		t.Errorf("non-denied file missing from REPO_MAP:\n%s", got)
	}
}

// TestFilterDeniedPaths covers the filter in isolation, including the
// no-deny fast path and blank-entry skipping.
func TestFilterDeniedPaths(t *testing.T) {
	all := []string{"a.go", "", "secrets/prod.env", "docs/x.md"}
	if got := filterDeniedPaths(all, nil); len(got) != 4 {
		t.Errorf("no deny globs must pass the slice through untouched, got %v", got)
	}
	got := filterDeniedPaths(all, []string{"secrets/**"})
	want := []string{"a.go", "docs/x.md"}
	if len(got) != len(want) {
		t.Fatalf("filterDeniedPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filterDeniedPaths = %v, want %v", got, want)
		}
	}
}
