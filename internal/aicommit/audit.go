package aicommit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// AuditEntry is one line in .git/gk-ai-commit/audit.jsonl.
//
// The file is append-only; a new entry is written per applied commit
// (so a 3-group run produces 3 entries sharing a RunID). Consumers
// can group by RunID to reconstruct a single `gk commit` execution.
type AuditEntry struct {
	TS         time.Time `json:"ts"`
	RunID      string    `json:"run_id"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model,omitempty"`
	CommitSha  string    `json:"commit_sha,omitempty"`
	GroupType  string    `json:"group_type"`
	GroupScope string    `json:"group_scope,omitempty"`
	Files      []string  `json:"files"`
	Subject    string    `json:"subject"`
	Tokens     int       `json:"tokens,omitempty"`
	Attempts   int       `json:"attempts,omitempty"`
	DryRun     bool      `json:"dry_run,omitempty"`
	BackupRef  string    `json:"backup_ref,omitempty"`
}

// AuditWriter opens (or creates) the audit jsonl inside the repo's
// .git dir. Closing is the caller's responsibility.
//
// The file lives in .git/gk-ai-commit/audit.jsonl so it is not
// committed. Rotation is the user's problem for v1 — we only append.
type AuditWriter struct {
	f *os.File
}

// OpenAuditLog returns an AuditWriter writing into gitDir, which is
// normally the output of `git rev-parse --git-dir`. Creates any
// missing directories.
func OpenAuditLog(gitDir string) (*AuditWriter, error) {
	if gitDir == "" {
		return nil, fmt.Errorf("aicommit: audit: empty git dir")
	}
	dir := filepath.Join(gitDir, "gk-ai-commit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("aicommit: audit mkdir: %w", err)
	}
	path := filepath.Join(dir, "audit.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("aicommit: audit open: %w", err)
	}
	return &AuditWriter{f: f}, nil
}

// Write serializes entry as one JSON line, terminated by '\n'.
func (a *AuditWriter) Write(entry AuditEntry) error {
	return writeAuditEntry(a.f, entry)
}

// Close flushes and closes the underlying file.
func (a *AuditWriter) Close() error {
	if a == nil || a.f == nil {
		return nil
	}
	return a.f.Close()
}

// writeAuditEntry is split out for tests that want to write to an
// in-memory buffer instead of a real file.
func writeAuditEntry(w io.Writer, entry AuditEntry) error {
	if entry.TS.IsZero() {
		entry.TS = time.Now().UTC()
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("aicommit: audit marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("aicommit: audit write: %w", err)
	}
	return nil
}
