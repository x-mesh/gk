package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/testutil"
)

// newUndoTestCmd builds a fresh cobra.Command with undo flags for testing.
// Output is captured in buf. Context is set to context.Background().
func newUndoTestCmd(buf *bytes.Buffer, flags map[string]string) *cobra.Command {
	cmd := &cobra.Command{Use: "undo"}
	cmd.Flags().Bool("list", false, "")
	cmd.Flags().Int("limit", 20, "")
	cmd.Flags().Bool("yes", false, "")
	cmd.Flags().String("to", "", "")
	cmd.Flags().Bool("hard", false, "")
	cmd.Flags().Bool("soft", false, "")
	if buf != nil {
		cmd.SetOut(buf)
		cmd.SetErr(buf)
	}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetContext(context.Background())
	for k, v := range flags {
		_ = cmd.Flags().Set(k, v)
	}
	return cmd
}

// fixedTime is a deterministic timestamp for backup ref naming in tests.
var fixedTime = time.Unix(1700000000, 0)

func nowFixed() time.Time { return fixedTime }

// ---------------------------------------------------------------------------
// TestUndo_List_EmptyReflog — fresh repo has an initial commit so reflog has
// at least 1 entry; --list prints it without hanging.
// ---------------------------------------------------------------------------

func TestUndo_List_EmptyReflog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"list": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	out := buf.String()
	// Fresh repo has at least the "commit (initial)" entry.
	if out == "no reflog entries available\n" {
		// This is acceptable only if git reflog is truly empty (very unusual).
		t.Log("no reflog entries (initial commit may not create one in isolation env)")
		return
	}
	// Otherwise we expect at least one line.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 1 || lines[0] == "" {
		t.Errorf("expected at least 1 reflog line, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_List_AfterCommits — 3 commits → --list output has 3+ lines with sha
// ---------------------------------------------------------------------------

func TestUndo_List_AfterCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("commit 1")
	repo.WriteFile("b.txt", "b")
	sha2 := repo.Commit("commit 2")
	repo.WriteFile("c.txt", "c")
	sha3 := repo.Commit("commit 3")

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"list": "true", "limit": "20"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	out := buf.String()
	// Output must contain at least 3 lines (one per commit plus initial).
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Errorf("expected at least 3 lines, got %d: %q", len(lines), out)
	}

	// Each SHA short form (first 8 chars) should appear in output.
	for _, sha := range []string{sha1[:8], sha2[:8], sha3[:8]} {
		if !strings.Contains(out, sha) {
			t.Errorf("expected SHA prefix %q in list output, got: %q", sha, out)
		}
	}
}

// ---------------------------------------------------------------------------
// TestUndo_To_ResetsHEAD — 3 commits, --to HEAD~1 → HEAD moves back + backup ref exists
// ---------------------------------------------------------------------------

func TestUndo_To_ResetsHEAD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("commit 1")
	repo.WriteFile("b.txt", "b")
	sha2 := repo.Commit("commit 2")
	repo.WriteFile("c.txt", "c")
	repo.Commit("commit 3")

	// HEAD~1 should be sha2.
	expectedSHA := repo.RunGit("rev-parse", "HEAD~1")
	if expectedSHA != sha2 {
		t.Fatalf("HEAD~1 expected %s, got %s", sha2, expectedSHA)
	}

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"to": "HEAD~1", "yes": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	// HEAD must now be sha2.
	head := repo.RunGit("rev-parse", "HEAD")
	if head != sha2 {
		t.Errorf("HEAD after undo: got %s, want %s", head, sha2)
	}

	// Backup ref must exist under refs/gk/undo-backup/main/<unix>.
	backupRef := gitsafe.BackupRefName("undo", "main", fixedTime)
	backupSHA := repo.RunGit("rev-parse", backupRef)
	if backupSHA == "" {
		t.Errorf("backup ref %q not found", backupRef)
	}

	out := buf.String()
	if !strings.Contains(out, "undone to") {
		t.Errorf("expected 'undone to' in output, got: %q", out)
	}
	if !strings.Contains(out, backupRef) {
		t.Errorf("expected backup ref %q in output, got: %q", backupRef, out)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_To_RefusesDirtyTree — dirty working tree → preflight error
// ---------------------------------------------------------------------------

func TestUndo_To_RefusesDirtyTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("commit 1")

	// Dirty the working tree (modify a tracked file).
	repo.WriteFile("a.txt", "dirty content")

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"to": "HEAD", "yes": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	err := runUndoWith(cmd, deps)
	if err == nil {
		t.Fatal("expected error for dirty working tree, got nil")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("expected 'uncommitted' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_To_RefusesInProgressRebase — rebase conflict state → preflight error
// ---------------------------------------------------------------------------

func TestUndo_To_RefusesInProgressRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// Create a conflict setup: two branches modify the same line.
	repo.WriteFile("file.txt", "original\n")
	repo.Commit("base")

	repo.CreateBranch("feature")
	repo.WriteFile("file.txt", "feature change\n")
	repo.Commit("feature commit")

	repo.Checkout("main")
	repo.WriteFile("file.txt", "main change\n")
	repo.Commit("main commit")

	// Attempt rebase feature onto main — this should conflict.
	repo.Checkout("feature")
	_, rebaseErr := repo.TryGit("rebase", "main")
	if rebaseErr == nil {
		// No conflict (unexpected); skip test.
		t.Skip("expected rebase conflict but got none; skipping")
	}

	// Now repo is in rebase-merge state. Verify git agrees.
	rebaseMergeDir := filepath.Join(repo.GitDir, "rebase-merge")
	if _, err := os.Stat(rebaseMergeDir); os.IsNotExist(err) {
		t.Skip("rebase-merge dir not found; test environment may not support conflict setup")
	}

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"to": "HEAD", "yes": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	err := runUndoWith(cmd, deps)
	if err == nil {
		t.Fatal("expected error for in-progress rebase, got nil")
	}
	if !strings.Contains(err.Error(), "in-progress") {
		t.Errorf("expected 'in-progress' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_Soft_PreservesIndexAndWorktree — soft reset moves HEAD only: the
// index tree (git write-tree) and worktree files are byte-identical before and
// after, staged extras stay staged, and the backup ref points at the old HEAD.
// ---------------------------------------------------------------------------

func TestUndo_Soft_PreservesIndexAndWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("commit 1")
	repo.WriteFile("b.txt", "b")
	sha2 := repo.Commit("commit 2")

	// Stage an extra change — the tree is now dirty, which soft must tolerate
	// (uncommitting with staged work is the whole point of --soft).
	repo.WriteFile("c.txt", "staged extra")
	repo.RunGit("add", "c.txt")

	indexTreeBefore := repo.RunGit("write-tree")

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"to": "HEAD~1", "soft": "true", "yes": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	// HEAD moved back to commit 1.
	if head := repo.RunGit("rev-parse", "HEAD"); head != sha1 {
		t.Errorf("HEAD after undo --soft: got %s, want %s", head, sha1)
	}

	// Index untouched: the tree it would write is identical.
	if indexTreeAfter := repo.RunGit("write-tree"); indexTreeAfter != indexTreeBefore {
		t.Errorf("index tree changed by --soft: got %s, want %s", indexTreeAfter, indexTreeBefore)
	}

	// The staged extra is still staged (now alongside commit 2's changes).
	staged := repo.RunGit("diff", "--cached", "--name-only")
	for _, f := range []string{"b.txt", "c.txt"} {
		if !strings.Contains(staged, f) {
			t.Errorf("expected %s staged after undo --soft, staged set: %q", f, staged)
		}
	}

	// Worktree untouched.
	if got, err := os.ReadFile(filepath.Join(repo.Dir, "b.txt")); err != nil || string(got) != "b" {
		t.Errorf("b.txt worktree content changed: %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(repo.Dir, "c.txt")); err != nil || string(got) != "staged extra" {
		t.Errorf("c.txt worktree content changed: %q, err=%v", got, err)
	}

	// Backup ref recorded the pre-undo HEAD.
	backupRef := gitsafe.BackupRefName("undo", "main", fixedTime)
	if backupSHA := repo.RunGit("rev-parse", backupRef); backupSHA != sha2 {
		t.Errorf("backup ref %s: got %s, want %s", backupRef, backupSHA, sha2)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_SoftHard_MutuallyExclusive — --soft + --hard must error before any
// git activity.
// ---------------------------------------------------------------------------

func TestUndo_SoftHard_MutuallyExclusive(t *testing.T) {
	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"soft": "true", "hard": "true", "to": "HEAD~1"})

	// FakeRunner with no responses: any git call would fail loudly, proving
	// the validation fires first.
	fake := &git.FakeRunner{}
	deps := &undoDeps{
		Runner: fake,
		Client: git.NewClient(fake),
		Picker: nil,
		Now:    nowFixed,
	}

	err := runUndoWith(cmd, deps)
	if err == nil {
		t.Fatal("expected error for --soft --hard, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_Soft_DefaultsToParent — bare --soft (no --to) in a non-interactive
// run (tests have no TTY) resets to HEAD~1.
// ---------------------------------------------------------------------------

func TestUndo_Soft_DefaultsToParent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("commit 1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("commit 2")

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"soft": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	if head := repo.RunGit("rev-parse", "HEAD"); head != sha1 {
		t.Errorf("bare undo --soft should default to HEAD~1: HEAD got %s, want %s", head, sha1)
	}
	if out := buf.String(); !strings.Contains(out, "undone to") {
		t.Errorf("expected 'undone to' in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_Soft_AgentEnvelope — GK_AGENT-mode JSON: {state:"ok", ok:true,
// result:{from,to,backup_ref,mode:"soft"}}; bare --soft still defaults to
// HEAD~1 under agent mode.
// ---------------------------------------------------------------------------

func TestUndo_Soft_AgentEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	prevA, prevJ := flagAgent, flagJSON
	flagAgent, flagJSON = true, true
	t.Cleanup(func() { flagAgent, flagJSON = prevA, prevJ })

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("commit 1")
	repo.WriteFile("b.txt", "b")
	sha2 := repo.Commit("commit 2")

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"soft": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	var env struct {
		Schema int    `json:"schema"`
		State  string `json:"state"`
		OK     bool   `json:"ok"`
		Result struct {
			Schema    int    `json:"schema"`
			Result    string `json:"result"`
			From      string `json:"from"`
			To        string `json:"to"`
			BackupRef string `json:"backup_ref"`
			Mode      string `json:"mode"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\noutput: %s", err, buf.String())
	}
	if env.State != "ok" || !env.OK {
		t.Errorf("envelope state/ok: got %q/%v, want \"ok\"/true", env.State, env.OK)
	}
	if env.Result.Mode != "soft" {
		t.Errorf("result.mode: got %q, want \"soft\"", env.Result.Mode)
	}
	if env.Result.From != sha2 {
		t.Errorf("result.from: got %s, want %s", env.Result.From, sha2)
	}
	if env.Result.To != sha1 {
		t.Errorf("result.to: got %s, want %s", env.Result.To, sha1)
	}
	if env.Result.BackupRef == "" {
		t.Error("result.backup_ref is empty")
	}
	if head := repo.RunGit("rev-parse", "HEAD"); head != sha1 {
		t.Errorf("HEAD after agent-mode undo --soft: got %s, want %s", head, sha1)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_Soft_RootCommitErrors — bare --soft on a root-only commit (HEAD~1
// unresolvable) must fail cleanly: an error naming HEAD~1 with a hint, HEAD
// unmoved, and no backup ref written.
// ---------------------------------------------------------------------------

func TestUndo_Soft_RootCommitErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t) // exactly the initial commit — HEAD is the root
	headBefore := repo.RunGit("rev-parse", "HEAD")

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"soft": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	err := runUndoWith(cmd, deps)
	if err == nil {
		t.Fatal("expected an error: a root-only repo has nothing to uncommit")
	}
	if !strings.Contains(err.Error(), "HEAD~1") {
		t.Errorf("error should name the implicit HEAD~1 target, got: %v", err)
	}
	if hint := HintFrom(err); !strings.Contains(hint, "parent") {
		t.Errorf("error should carry a no-parent hint, got: %q", hint)
	}
	if head := repo.RunGit("rev-parse", "HEAD"); head != headBefore {
		t.Errorf("HEAD must be unmoved: got %s, want %s", head, headBefore)
	}
	if refs := repo.RunGit("for-each-ref", "refs/gk/undo-backup"); strings.TrimSpace(refs) != "" {
		t.Errorf("no backup ref may be written on a failed resolve, got: %q", refs)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_List_AgentEnvelope — --list under --json/GK_AGENT emits a
// structured entries[] envelope, never the human-formatted table.
// ---------------------------------------------------------------------------

func TestUndo_List_AgentEnvelope(t *testing.T) {
	prevA, prevJ := flagAgent, flagJSON
	flagAgent, flagJSON = true, true
	t.Cleanup(func() { flagAgent, flagJSON = prevA, prevJ })

	fakeReflogOutput := "aabbccddee112233445566778899001122334455\x00" +
		"aabbccdd\x00HEAD@{0}\x00commit: initial\x001700000000\x1e"
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"reflog show --format=%H%x00%h%x00%gD%x00%gs%x00%at%x1e HEAD -n 20": {
				Stdout: fakeReflogOutput,
			},
		},
	}

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"list": "true"})
	deps := &undoDeps{Runner: fake, Client: git.NewClient(fake), Picker: nil, Now: nowFixed}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	var env struct {
		State  string `json:"state"`
		OK     bool   `json:"ok"`
		Result struct {
			Schema  int `json:"schema"`
			Entries []struct {
				SHA     string `json:"sha"`
				Action  string `json:"action"`
				Ref     string `json:"ref"`
				Summary string `json:"summary"`
			} `json:"entries"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("--list output is not a JSON envelope: %v\n%s", err, buf.String())
	}
	if env.State != "ok" || !env.OK {
		t.Errorf("envelope state/ok = %q/%v, want ok/true", env.State, env.OK)
	}
	if len(env.Result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1: %s", len(env.Result.Entries), buf.String())
	}
	e := env.Result.Entries[0]
	if e.SHA != "aabbccddee112233445566778899001122334455" || e.Ref != "HEAD@{0}" || e.Action != "commit" {
		t.Errorf("entry = %+v", e)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_EmptyReflog_AgentBlocked — an empty reflog under --json/GK_AGENT is
// a blocked error, not bare prose with a success exit.
// ---------------------------------------------------------------------------

func TestUndo_EmptyReflog_AgentBlocked(t *testing.T) {
	prevA, prevJ := flagAgent, flagJSON
	flagAgent, flagJSON = true, true
	t.Cleanup(func() { flagAgent, flagJSON = prevA, prevJ })

	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"reflog show --format=%H%x00%h%x00%gD%x00%gs%x00%at%x1e HEAD -n 20": {Stdout: ""},
		},
	}

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"soft": "true"})
	deps := &undoDeps{Runner: fake, Client: git.NewClient(fake), Picker: nil, Now: nowFixed}

	err := runUndoWith(cmd, deps)
	if err == nil {
		t.Fatal("empty reflog in JSON mode must surface as an error, not prose + exit 0")
	}
	if !strings.Contains(err.Error(), "no reflog entries") {
		t.Errorf("error = %v, want the empty-reflog message", err)
	}
	if buf.Len() != 0 {
		t.Errorf("no prose may reach stdout in JSON mode, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// TestHumanSince — table-driven unit tests
// ---------------------------------------------------------------------------

func TestHumanSince(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{59 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{90 * time.Minute, "1h ago"},
		{2 * time.Hour, "2h ago"},
		{48 * time.Hour, "2d ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, tc := range tests {
		got := humanSince(tc.d)
		if got != tc.want {
			t.Errorf("humanSince(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestShortSHA — unit tests for SHA truncation
// ---------------------------------------------------------------------------

func TestShortSHA(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc123", "abc123"},                                     // short → unchanged
		{"abcdefgh", "abcdefgh"},                                 // exactly 8 → unchanged
		{"abcdefghi", "abcdefgh"},                                // 9 → first 8
		{"abcdefghijklmnopqrstuvwxyz1234567890abcd", "abcdefgh"}, // full sha → first 8
		{"", ""}, // empty → empty
	}
	for _, tc := range tests {
		got := shortSHA(tc.input)
		if got != tc.want {
			t.Errorf("shortSHA(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestEntriesToPickerItems — key is index string, display contains sha+action
// ---------------------------------------------------------------------------

func TestEntriesToPickerItems(t *testing.T) {
	entries := []interface{}{} // use reflog.Entry via runUndoWith indirectly
	_ = entries

	// Use FakeRunner to get parsed entries without a real repo.
	fakeReflogOutput := "aabbccddee112233445566778899001122334455\x00" +
		"aabbccdd\x00HEAD@{0}\x00commit: initial\x001700000000\x1e"

	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"reflog show --format=%H%x00%h%x00%gD%x00%gs%x00%at%x1e HEAD -n 20": {
				Stdout: fakeReflogOutput,
			},
		},
	}

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"list": "true"})

	deps := &undoDeps{
		Runner: fake,
		Client: git.NewClient(fake),
		Picker: nil,
		Now:    nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "aabbccdd") {
		t.Errorf("expected short sha 'aabbccdd' in list output, got: %q", out)
	}
}
