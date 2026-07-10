package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStatusTools(t *testing.T) (*Registry, string) {
	t.Helper()
	runner, sb, root := gitRepoFixture(t)
	g := &GitTools{Runner: runner, Sandbox: sb, DenyGlobs: sb.DenyGlobs}
	r := NewRegistry(nil, 0)
	RegisterGitTools(r, g)
	RegisterStatusTools(r, g)
	return r, root
}

// runGitTest runs one git command directly against the fixture repo for
// test setup (stash, snapshot refs, in-progress markers) — separate from
// the tool-under-test's own Runner.
func runGitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func decodeStatus(t *testing.T, content string) gitStatusOutput {
	t.Helper()
	var out gitStatusOutput
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("decode git_status output: %v\n%s", err, content)
	}
	return out
}

func TestGitStatusClean(t *testing.T) {
	r, _ := newTestStatusTools(t)
	res := dispatch(t, r, "git_status", `{}`)
	if res.IsError {
		t.Fatalf("git_status error: %s", res.Content)
	}
	out := decodeStatus(t, res.Content)
	if !out.Clean || out.Staged || out.Modified || out.Conflict {
		t.Errorf("expected clean status, got %+v", out)
	}
	if out.Branch != "main" || out.Detached {
		t.Errorf("expected branch=main not detached, got %+v", out)
	}
	if len(out.Changed) != 0 || out.UntrackedCount != 0 {
		t.Errorf("expected no changes, got %+v", out)
	}
	if out.InProgress != nil {
		t.Errorf("expected no in-progress op, got %+v", out.InProgress)
	}
}

func TestGitStatusDirtyAndDeniedFiltering(t *testing.T) {
	r, root := newTestStatusTools(t)
	if err := os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package a\n\nfunc Hello() string { return \"hi again\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"),
		[]byte("API_SECRET=another-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := dispatch(t, r, "git_status", `{}`)
	if res.IsError {
		t.Fatalf("git_status error: %s", res.Content)
	}
	out := decodeStatus(t, res.Content)
	if out.Clean {
		t.Errorf("expected dirty status, got clean: %+v", out)
	}
	if !out.Modified {
		t.Errorf("expected modified=true, got %+v", out)
	}
	if out.UntrackedCount != 1 {
		t.Errorf("expected untracked_count=1, got %d", out.UntrackedCount)
	}
	foundAGo, foundEnv := false, false
	for _, e := range out.Changed {
		if e.Path == "a.go" {
			foundAGo = true
		}
		if strings.Contains(e.Path, ".env") {
			foundEnv = true
		}
	}
	if !foundAGo {
		t.Errorf("expected a.go in changed list: %+v", out.Changed)
	}
	if foundEnv {
		t.Errorf("denied .env must not appear in changed list: %+v", out.Changed)
	}
	if strings.Contains(res.Content, "another-secret") {
		t.Errorf("git_status must never leak file content: %s", res.Content)
	}
}

func TestGitStatusStash(t *testing.T) {
	r, root := newTestStatusTools(t)
	if err := os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package a\n\nfunc Hello() string { return \"stash me\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "stash", "push", "-m", "wip: testing stash")

	res := dispatch(t, r, "git_status", `{}`)
	if res.IsError {
		t.Fatalf("git_status error: %s", res.Content)
	}
	out := decodeStatus(t, res.Content)
	if len(out.Stash) != 1 {
		t.Fatalf("expected 1 stash entry, got %+v", out.Stash)
	}
	if out.Stash[0].Index != 0 {
		t.Errorf("expected stash index 0, got %d", out.Stash[0].Index)
	}
	if !strings.Contains(out.Stash[0].Subject, "wip: testing stash") {
		t.Errorf("expected stash subject to carry the message, got %q", out.Stash[0].Subject)
	}
	// Stashing should have returned the tree to clean.
	if !out.Clean {
		t.Errorf("expected clean tree after stash push, got %+v", out)
	}
}

func TestGitStatusInProgressMerge(t *testing.T) {
	r, root := newTestStatusTools(t)
	head := strings.TrimSpace(runGitTest(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, ".git", "MERGE_HEAD"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := dispatch(t, r, "git_status", `{}`)
	if res.IsError {
		t.Fatalf("git_status error: %s", res.Content)
	}
	out := decodeStatus(t, res.Content)
	if out.InProgress == nil || out.InProgress.Kind != "merge" {
		t.Errorf("expected in_progress.kind=merge, got %+v", out.InProgress)
	}
}

func TestGitStatusInProgressRebase(t *testing.T) {
	r, root := newTestStatusTools(t)
	rebaseDir := filepath.Join(root, ".git", "rebase-merge")
	if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(rebaseDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("onto", "deadbeef\n")
	write("msgnum", "2\n")
	write("end", "5\n")

	res := dispatch(t, r, "git_status", `{}`)
	if res.IsError {
		t.Fatalf("git_status error: %s", res.Content)
	}
	out := decodeStatus(t, res.Content)
	if out.InProgress == nil || out.InProgress.Kind != "rebase-merge" {
		t.Fatalf("expected in_progress.kind=rebase-merge, got %+v", out.InProgress)
	}
	if out.InProgress.Step != 2 || out.InProgress.Total != 5 || out.InProgress.Onto != "deadbeef" {
		t.Errorf("expected step=2 total=5 onto=deadbeef, got %+v", out.InProgress)
	}
}

func TestGitStatusRejectUnknownFields(t *testing.T) {
	r, _ := newTestStatusTools(t)
	if res := dispatch(t, r, "git_status", `{"exec":"rm -rf /"}`); !res.IsError {
		t.Error("unknown git_status field must be rejected")
	}
}

func TestGitSnapshotListEmpty(t *testing.T) {
	r, _ := newTestStatusTools(t)
	res := dispatch(t, r, "git_snapshot_list", `{}`)
	if res.IsError {
		t.Fatalf("git_snapshot_list error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "no snapshots") {
		t.Errorf("expected 'no snapshots' message, got: %s", res.Content)
	}
}

// snapshotRef creates a minimal refs/wip/<branch> entry pointing at HEAD —
// enough to exercise list/diff without depending on internal/cli's own
// snapshot-tree machinery (which this package deliberately cannot import).
func snapshotRef(t *testing.T, root, branch, note string) {
	t.Helper()
	head := strings.TrimSpace(runGitTest(t, root, "rev-parse", "HEAD"))
	runGitTest(t, root, "update-ref", "--create-reflog", "-m", "gk snapshot: "+note,
		"refs/wip/"+branch, head)
}

func TestGitSnapshotListEntries(t *testing.T) {
	r, root := newTestStatusTools(t)
	snapshotRef(t, root, "main", "before refactor")

	res := dispatch(t, r, "git_snapshot_list", `{}`)
	if res.IsError {
		t.Fatalf("git_snapshot_list error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "@{0}") {
		t.Errorf("expected @{0} selector, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "before refactor") {
		t.Errorf("expected snapshot note, got: %s", res.Content)
	}
}

func TestGitSnapshotDiff(t *testing.T) {
	r, root := newTestStatusTools(t)
	snapshotRef(t, root, "main", "baseline")

	if err := os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package a\n\nfunc Hello() string { return \"changed after snapshot\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"),
		[]byte("API_SECRET=snapshot-diff-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := dispatch(t, r, "git_snapshot_diff", `{"index":0}`)
	if res.IsError {
		t.Fatalf("git_snapshot_diff error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("expected a.go in digest, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "snapshot-diff-secret") {
		t.Errorf("digest leaked denied content: %s", res.Content)
	}
	if !strings.Contains(res.Content, "withheld by deny_paths") {
		t.Errorf("expected withheld note for denied .env, got: %s", res.Content)
	}

	raw := dispatch(t, r, "git_snapshot_diff", `{"index":0,"raw":true}`)
	if raw.IsError {
		t.Fatalf("git_snapshot_diff raw error: %s", raw.Content)
	}
	if !strings.Contains(raw.Content, "changed after snapshot") {
		t.Errorf("expected raw patch content for a.go, got: %s", raw.Content)
	}
	if strings.Contains(raw.Content, "snapshot-diff-secret") {
		t.Errorf("raw diff leaked denied .env content: %s", raw.Content)
	}
}

func TestGitSnapshotDiffMissingIndex(t *testing.T) {
	r, root := newTestStatusTools(t)
	snapshotRef(t, root, "main", "only one")

	res := dispatch(t, r, "git_snapshot_diff", `{"index":5}`)
	if !res.IsError {
		t.Error("expected error for out-of-range snapshot index")
	}
}

func TestGitSnapshotDiffNegativeIndexRejected(t *testing.T) {
	r, root := newTestStatusTools(t)
	snapshotRef(t, root, "main", "only one")

	res := dispatch(t, r, "git_snapshot_diff", `{"index":-1}`)
	if !res.IsError {
		t.Error("expected error for negative snapshot index")
	}
}

func TestGitSnapshotToolsRejectUnknownFields(t *testing.T) {
	r, _ := newTestStatusTools(t)
	if res := dispatch(t, r, "git_snapshot_list", `{"exec":"x"}`); !res.IsError {
		t.Error("unknown git_snapshot_list field must be rejected")
	}
	if res := dispatch(t, r, "git_snapshot_diff", `{"exec":"x"}`); !res.IsError {
		t.Error("unknown git_snapshot_diff field must be rejected")
	}
}

// TestGitStatusFlagsRespectDeny pins the panel finding that git_status's
// clean/staged/modified flags were computed from the UNFILTERED porcelain:
// a repo whose only change is a denied path must report clean, otherwise
// `clean:false` is a working oracle for "the denied file changed" even
// though changed[] correctly withholds the name.
func TestGitStatusFlagsRespectDeny(t *testing.T) {
	entries := []gitStatusEntry{{Code: " M", Path: "secrets/prod.env"}}
	g := &GitTools{DenyGlobs: []string{"secrets/**"}}

	filtered := g.filterDeniedEntries(entries)
	if len(filtered) != 0 {
		t.Fatalf("denied entry must be filtered, got %v", filtered)
	}
	flags := flagsFromEntries(filtered)
	if !flags.Clean() {
		t.Errorf("flags derived from filtered entries must be clean, got %+v", flags)
	}
}

// TestGitStatusUntrackedOnlyNotClean pins the panel finding that
// git_status reported clean:true for a working tree that had ONLY
// untracked files — a self-contradiction next to untracked_count>0 that
// would make a model believe there was nothing to look at. git.DirtyFlags
// itself is untouched (untracked ≠ dirty is correct for gk's other
// callers); the fix lives in this tool's own Clean computation.
func TestGitStatusUntrackedOnlyNotClean(t *testing.T) {
	r, root := newTestStatusTools(t)
	for _, name := range []string{"new1.txt", "new2.txt", "new3.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res := dispatch(t, r, "git_status", `{}`)
	if res.IsError {
		t.Fatalf("git_status error: %s", res.Content)
	}
	out := decodeStatus(t, res.Content)
	if out.UntrackedCount != 3 {
		t.Fatalf("expected untracked_count=3, got %d", out.UntrackedCount)
	}
	if out.Clean {
		t.Errorf("untracked-only tree must not report clean:true (self-contradicts untracked_count=%d)", out.UntrackedCount)
	}
	// Only `clean`'s meaning changes here — the narrower tracked-file
	// flags must still read false, since nothing tracked changed.
	if out.Modified || out.Staged || out.Conflict {
		t.Errorf("untracked files must not flip the tracked-file flags: %+v", out)
	}
}

// snapshotCommitWithWorkingTree replicates gk snapshot's non-destructive
// capture (internal/cli/snapshot.go's snapshotTree/commitSnapshotTree —
// throwaway index, `add -A`, write-tree, commit-tree parented on HEAD)
// using raw git plumbing, so this test can build a snapshot that captured
// an UNTRACKED file without importing internal/cli (which would create the
// import cycle snapshotRefPrefix's comment already explains).
func snapshotCommitWithWorkingTree(t *testing.T, root, branch, note string) {
	t.Helper()
	head := strings.TrimSpace(runGitTest(t, root, "rev-parse", "HEAD"))

	tmp, err := os.CreateTemp(t.TempDir(), "gk-test-snapshot-index-")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	if err := os.Remove(tmpPath); err != nil {
		t.Fatal(err)
	}

	runIndexed := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_INDEX_FILE="+tmpPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	runIndexed("add", "-A")
	tree := strings.TrimSpace(runIndexed("write-tree"))

	commit := strings.TrimSpace(runGitTest(t, root, "commit-tree", tree, "-p", head, "-m", "gk snapshot: "+note))
	runGitTest(t, root, "update-ref", "--create-reflog", "-m", "gk snapshot: "+note,
		"refs/wip/"+branch, commit)
}

// TestGitSnapshotDiffUntrackedFileUnchanged pins the panel finding that
// git_snapshot_diff diverged from `gk snapshot diff`'s contract for files
// that were UNTRACKED at snapshot time. Before the fix, git_snapshot_diff
// ran a plain `git diff <sha>` (single ref): that form compares <sha>
// against the CURRENT INDEX, and an untracked file is absent from the real
// index even though the snapshot's tree captured it — so the file rendered
// as "deleted" even though it still sits on disk, byte-for-byte unchanged.
func TestGitSnapshotDiffUntrackedFileUnchanged(t *testing.T) {
	r, root := newTestStatusTools(t)

	// notes.txt is written to disk but never `git add`ed — it is untracked
	// both when the snapshot is taken and afterward.
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("scratch notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshotCommitWithWorkingTree(t, root, "main", "before touching notes")

	res := dispatch(t, r, "git_snapshot_diff", `{"index":0,"raw":true}`)
	if res.IsError {
		t.Fatalf("git_snapshot_diff error: %s", res.Content)
	}
	if strings.Contains(res.Content, "notes.txt") {
		t.Errorf("untracked-but-unchanged file must not appear in the snapshot diff, got: %s", res.Content)
	}
}

// TestFlagsFromEntriesMatchesParser checks the reconstruction path agrees
// with the canonical porcelain parser on the XY codes that matter.
func TestFlagsFromEntriesMatchesParser(t *testing.T) {
	cases := []struct {
		name                       string
		entries                    []gitStatusEntry
		staged, modified, conflict bool
	}{
		{"empty", nil, false, false, false},
		{"staged only", []gitStatusEntry{{Code: "M ", Path: "a"}}, true, false, false},
		{"worktree only", []gitStatusEntry{{Code: " M", Path: "a"}}, false, true, false},
		{"conflict UU", []gitStatusEntry{{Code: "UU", Path: "a"}}, false, false, true},
		{"conflict AA", []gitStatusEntry{{Code: "AA", Path: "a"}}, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := flagsFromEntries(tc.entries)
			if got.Staged != tc.staged || got.Modified != tc.modified || got.Conflict != tc.conflict {
				t.Errorf("flagsFromEntries = %+v, want staged=%v modified=%v conflict=%v",
					got, tc.staged, tc.modified, tc.conflict)
			}
		})
	}
}

// TestSnapshotNowTreeNoDeniedBlob pins the v2 panel's high finding: the
// throwaway-index `add -A` that git_snapshot_diff builds must not write a
// denied file's content into the object DB. A read-only tool persisting
// .env's blob is a contract break even though filterAndDigest hides it
// from the diff text.
func TestSnapshotNowTreeNoDeniedBlob(t *testing.T) {
	runner, sb, root := gitRepoFixture(t)
	g := &GitTools{Runner: runner, Sandbox: sb, DenyGlobs: sb.DenyGlobs} // deny: .env

	// Give .env a brand-new content whose blob would be uniquely findable.
	unique := "API_SECRET=UNIQUE-NEEDLE-9f3a\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(unique), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := g.snapshotNowTree(context.Background()); err != nil {
		t.Fatalf("snapshotNowTree: %v", err)
	}

	// The unique .env content must NOT exist as a blob in the object DB.
	h := hashObjectStdin(t, root, unique)
	out := tryCatFile(t, root, h)
	if out {
		t.Errorf("denied file .env was written as blob %s to the object DB", h)
	}
}

func hashObjectStdin(t *testing.T, root, content string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "--stdin")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(content)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hash-object: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// tryCatFile reports whether the object exists in the DB.
func tryCatFile(t *testing.T, root, sha string) bool {
	t.Helper()
	cmd := exec.Command("git", "cat-file", "-e", sha)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	return cmd.Run() == nil
}

// TestGitStatusUntrackedDeniedNotCounted pins the v2 finding that an
// untracked deny_paths file still bumped untracked_count and flipped
// clean:false — an existence oracle for a hidden path. A repo whose only
// untracked file is denied must report clean:true, untracked_count:0.
func TestGitStatusUntrackedDeniedNotCounted(t *testing.T) {
	runner, _, root := gitRepoFixture(t) // fixture leaves a clean tree
	sb, err := NewSandbox(root, []string{"secrets/**"})
	if err != nil {
		t.Fatal(err)
	}
	g := &GitTools{Runner: runner, Sandbox: sb, DenyGlobs: sb.DenyGlobs}
	r := NewRegistry(nil, 0)
	RegisterGitTools(r, g)
	RegisterStatusTools(r, g)

	// The ONLY working-tree change is a new untracked, denied file.
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets", "prod.env"), []byte("TOKEN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := decodeStatus(t, dispatch(t, r, "git_status", `{}`).Content)
	if out.UntrackedCount != 0 {
		t.Errorf("denied untracked file must not be counted, got untracked_count=%d", out.UntrackedCount)
	}
	if !out.Clean {
		t.Errorf("a tree whose only change is a denied untracked file must read clean, got %+v", out)
	}
	for _, e := range out.Changed {
		if strings.Contains(e.Path, "secrets") {
			t.Errorf("denied path leaked into changed[]: %+v", e)
		}
	}
}

// TestGitStatusRenameFromDeniedWithheld pins the 3rd-panel finding: a
// staged rename whose SOURCE is a denied path but whose destination is not
// must be withheld from changed[] — otherwise the R code on the visible
// destination is an oracle that a deny-listed file was renamed. The -z
// switch broke the old " -> " split, so the origin field is now what
// filterDeniedEntries checks.
func TestGitStatusRenameFromDeniedWithheld(t *testing.T) {
	runner, _, root := gitRepoFixture(t) // clean tree, tracked a.go/.env
	sb, err := NewSandbox(root, []string{"secrets/**"})
	if err != nil {
		t.Fatal(err)
	}
	g := &GitTools{Runner: runner, Sandbox: sb, DenyGlobs: sb.DenyGlobs}
	r := NewRegistry(nil, 0)
	RegisterGitTools(r, g)
	RegisterStatusTools(r, g)

	// Commit a tracked file under the denied path, then rename it out.
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets", "old.txt"), []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "secrets/old.txt")
	runGitTest(t, root, "commit", "-qm", "add secret")
	// Staged rename: secrets/old.txt -> visible.txt (source denied, dest not).
	runGitTest(t, root, "mv", "secrets/old.txt", "visible.txt")

	out := decodeStatus(t, dispatch(t, r, "git_status", `{}`).Content)
	for _, e := range out.Changed {
		if strings.Contains(e.Path, "visible.txt") || strings.Contains(e.Path, "secrets") {
			t.Errorf("rename from a denied source leaked into changed[]: %+v", e)
		}
	}
}
