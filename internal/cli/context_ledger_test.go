package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContextLedgerPathSeparatesWorktrees(t *testing.T) {
	home := t.TempDir()
	p1 := contextLedgerPath(home, "/repo/a")
	p2 := contextLedgerPath(home, "/repo/b")
	if p1 == p2 {
		t.Fatalf("expected distinct paths for distinct worktrees, both got %q", p1)
	}
	if filepath.Dir(p1) != contextLedgerDir(home) {
		t.Fatalf("path %q not under ledger dir %q", p1, contextLedgerDir(home))
	}
}

func TestContextLedgerPathNormalizesEquivalentPaths(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	withDot := repo + string(filepath.Separator) + "."

	p1 := contextLedgerPath(home, repo)
	p2 := contextLedgerPath(home, withDot)
	if p1 != p2 {
		t.Fatalf("expected equivalent paths to hash identically: %q vs %q", p1, p2)
	}
}

func TestContextLedgerSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	path := contextLedgerPath(home, "/some/worktree")
	want := contextLedgerEntry{
		Schema:   contextLedgerSchema,
		Worktree: normalizeWorktreePath("/some/worktree"),
		SavedAt:  time.Now().UTC().Truncate(time.Second),
		Snapshot: json.RawMessage(`{"branch":"develop","dirty":true}`),
	}

	if err := saveContextLedger(path, want); err != nil {
		t.Fatalf("saveContextLedger: %v", err)
	}

	got, ok := loadContextLedger(path)
	if !ok {
		t.Fatalf("loadContextLedger: expected ok=true after save")
	}
	if got.Worktree != want.Worktree {
		t.Fatalf("worktree mismatch: got %q, want %q", got.Worktree, want.Worktree)
	}
	if !got.SavedAt.Equal(want.SavedAt) {
		t.Fatalf("saved_at mismatch: got %v, want %v", got.SavedAt, want.SavedAt)
	}
	if string(got.Snapshot) != string(want.Snapshot) {
		t.Fatalf("snapshot mismatch: got %s, want %s", got.Snapshot, want.Snapshot)
	}
}

func TestContextLedgerLoadMissingFile(t *testing.T) {
	home := t.TempDir()
	path := contextLedgerPath(home, "/never/saved")
	if _, ok := loadContextLedger(path); ok {
		t.Fatalf("expected ok=false for a file that was never saved")
	}
}

func TestContextLedgerLoadCorruptFile(t *testing.T) {
	home := t.TempDir()
	path := contextLedgerPath(home, "/corrupt/worktree")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, ok := loadContextLedger(path); ok {
		t.Fatalf("expected ok=false for malformed JSON")
	}
}

func TestContextLedgerLoadSchemaMismatch(t *testing.T) {
	home := t.TempDir()
	path := contextLedgerPath(home, "/schema/mismatch")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	future := `{"schema":99,"worktree":"/schema/mismatch","saved_at":"2020-01-01T00:00:00Z","snapshot":{}}`
	if err := os.WriteFile(path, []byte(future), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, ok := loadContextLedger(path); ok {
		t.Fatalf("expected ok=false for an unrecognized schema version")
	}
}

func TestContextLedgerSaveIsAtomic(t *testing.T) {
	home := t.TempDir()
	path := contextLedgerPath(home, "/atomic/worktree")
	entry := contextLedgerEntry{
		Schema:   contextLedgerSchema,
		Worktree: normalizeWorktreePath("/atomic/worktree"),
		SavedAt:  time.Now(),
		Snapshot: json.RawMessage(`{}`),
	}
	if err := saveContextLedger(path, entry); err != nil {
		t.Fatalf("saveContextLedger: %v", err)
	}

	des, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, de := range des {
		if strings.Contains(de.Name(), ".tmp-") {
			t.Fatalf("temp file left behind after save: %s", de.Name())
		}
	}
}

// TestContextLedgerGCExpiresStaleFiles verifies that a single save call
// sweeps a sibling ledger file whose mtime is older than the TTL.
func TestContextLedgerGCExpiresStaleFiles(t *testing.T) {
	home := t.TempDir()
	dir := contextLedgerDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	stalePath := filepath.Join(dir, "stale0000000001.json")
	if err := os.WriteFile(stalePath, []byte(`{"schema":1}`), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	freshPath := contextLedgerPath(home, "/fresh/worktree")
	entry := contextLedgerEntry{
		Schema:   contextLedgerSchema,
		Worktree: normalizeWorktreePath("/fresh/worktree"),
		SavedAt:  time.Now(),
		Snapshot: json.RawMessage(`{}`),
	}
	if err := saveContextLedger(freshPath, entry); err != nil {
		t.Fatalf("saveContextLedger: %v", err)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale ledger file to be GC'd, stat err=%v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("expected freshly saved ledger file to survive: %v", err)
	}
}

// TestContextLedgerGCExpiresFutureMtime verifies a sibling file whose mtime
// is in the future (clock skew) is treated as expired, not preserved.
func TestContextLedgerGCExpiresFutureMtime(t *testing.T) {
	home := t.TempDir()
	dir := contextLedgerDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	futurePath := filepath.Join(dir, "future00000001.json")
	if err := os.WriteFile(futurePath, []byte(`{"schema":1}`), 0o644); err != nil {
		t.Fatalf("write future: %v", err)
	}
	future := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(futurePath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	freshPath := contextLedgerPath(home, "/fresh/worktree2")
	entry := contextLedgerEntry{
		Schema:   contextLedgerSchema,
		Worktree: normalizeWorktreePath("/fresh/worktree2"),
		SavedAt:  time.Now(),
		Snapshot: json.RawMessage(`{}`),
	}
	if err := saveContextLedger(freshPath, entry); err != nil {
		t.Fatalf("saveContextLedger: %v", err)
	}

	if _, err := os.Stat(futurePath); !os.IsNotExist(err) {
		t.Fatalf("expected future-mtime ledger file to be treated as expired, stat err=%v", err)
	}
}

// TestContextLedgerGCKeepsFreshSiblings verifies GC doesn't collaterally
// remove other worktrees' still-fresh snapshots.
func TestContextLedgerGCKeepsFreshSiblings(t *testing.T) {
	home := t.TempDir()
	p1 := contextLedgerPath(home, "/wt/one")
	p2 := contextLedgerPath(home, "/wt/two")
	e1 := contextLedgerEntry{Schema: contextLedgerSchema, Worktree: normalizeWorktreePath("/wt/one"), SavedAt: time.Now(), Snapshot: json.RawMessage(`{}`)}
	e2 := contextLedgerEntry{Schema: contextLedgerSchema, Worktree: normalizeWorktreePath("/wt/two"), SavedAt: time.Now(), Snapshot: json.RawMessage(`{}`)}

	if err := saveContextLedger(p1, e1); err != nil {
		t.Fatalf("save p1: %v", err)
	}
	if err := saveContextLedger(p2, e2); err != nil {
		t.Fatalf("save p2: %v", err)
	}

	if _, ok := loadContextLedger(p1); !ok {
		t.Fatalf("expected p1 to survive a sibling save")
	}
	if _, ok := loadContextLedger(p2); !ok {
		t.Fatalf("expected p2 to be loadable")
	}
}
