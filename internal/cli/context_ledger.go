package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// contextLedgerSchema is the on-disk schema version for ledger snapshots.
// loadContextLedger rejects any other value rather than guessing at a
// migration, since a mismatched schema means a future or incompatible build
// wrote the file.
const contextLedgerSchema = 1

// contextLedgerTTL bounds how long a worktree's snapshot survives without a
// fresh save. `gk context --delta` only ever needs the latest snapshot per
// worktree, and without a bound this directory would repeat what already
// happened to ~/.gk once before: 986MB of stale worktree metadata with
// nothing to age it out. TTL cleanup is therefore a launch condition for
// this feature, not a later optimization.
const contextLedgerTTL = 7 * 24 * time.Hour

// contextLedgerEntry is the snapshot stored for one worktree.
type contextLedgerEntry struct {
	Schema   int             `json:"schema"`
	Worktree string          `json:"worktree"`
	SavedAt  time.Time       `json:"saved_at"`
	Snapshot json.RawMessage `json:"snapshot"`
}

// contextLedgerDir is where per-worktree ledger files live. It is global
// (like sessionaudit's history path), not per-repo, since a worktree hash
// already identifies which checkout a ledger belongs to.
func contextLedgerDir(home string) string {
	return filepath.Join(home, ".gk", "context-ledger")
}

// contextLedgerPath returns the ledger file for a worktree, named by a
// truncated sha256 of its normalized path so the same worktree always maps
// to the same file regardless of how the caller spelled it (relative path,
// trailing slash, "./" segments).
func contextLedgerPath(home, worktree string) string {
	norm := normalizeWorktreePath(worktree)
	sum := sha256.Sum256([]byte(norm))
	name := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(contextLedgerDir(home), name+".json")
}

// normalizeWorktreePath canonicalizes a worktree path (absolute + cleaned).
// Callers building a contextLedgerEntry should normalize through this same
// function before setting entry.Worktree, so the stored field always
// matches the path that produced the file name.
func normalizeWorktreePath(worktree string) string {
	if abs, err := filepath.Abs(worktree); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(worktree)
}

// saveContextLedger writes entry to path as the current snapshot for its
// worktree. The write is atomic (temp file in the same directory, then
// rename) so a crash or a concurrent load never observes a partial file.
//
// Every save also sweeps the directory for expired siblings (lazy GC — no
// separate background process needed). Expiry is judged by each sibling
// file's mtime rather than by parsing its saved_at field: mtime is a stat()
// away with no read or JSON decode, and that precision is more than enough
// for a threshold measured in days. A file whose mtime is in the future
// (clock set back after it was written) is treated as already expired
// instead of trusted, so a misbehaving clock can't pin a stale file forever.
func saveContextLedger(path string, entry contextLedgerEntry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}

	gcContextLedgerDir(dir)
	return nil
}

// loadContextLedger reads and decodes the ledger snapshot at path. A missing
// file, an unreadable file, malformed JSON, or a schema this build doesn't
// recognize all fall back to (zero value, false) instead of an error — "no
// usable snapshot yet" is the normal cold-start case for a caller, not a
// failure worth surfacing.
func loadContextLedger(path string) (contextLedgerEntry, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return contextLedgerEntry{}, false
	}
	var entry contextLedgerEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return contextLedgerEntry{}, false
	}
	if entry.Schema != contextLedgerSchema {
		return contextLedgerEntry{}, false
	}
	return entry, true
}

// gcContextLedgerDir removes ledger files in dir whose mtime is past
// contextLedgerTTL, or in the future. Errors are swallowed throughout: GC is
// best-effort housekeeping riding along on a save, not something a save
// should ever fail over.
func gcContextLedgerDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, de := range entries {
		if de.IsDir() || filepath.Ext(de.Name()) != ".json" {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		age := now.Sub(info.ModTime())
		if age < 0 || age > contextLedgerTTL {
			_ = os.Remove(filepath.Join(dir, de.Name()))
		}
	}
}
