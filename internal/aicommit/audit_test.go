package aicommit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAuditEntryShape(t *testing.T) {
	buf := &bytes.Buffer{}
	entry := AuditEntry{
		TS:        time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
		RunID:     "run-xyz",
		Provider:  "gemini",
		Model:     "gemini-3-flash-preview",
		CommitSha: "1234567",
		GroupType: "feat",
		Files:     []string{"a.go", "b.go"},
		Subject:   "add ai pipeline",
		Tokens:    128,
		Attempts:  1,
	}
	if err := writeAuditEntry(buf, entry); err != nil {
		t.Fatalf("writeAuditEntry: %v", err)
	}
	line := strings.TrimRight(buf.String(), "\n")
	var got AuditEntry
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("json round-trip: %v (raw=%q)", err, line)
	}
	if got.RunID != "run-xyz" || got.CommitSha != "1234567" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Error("audit entries must end with a newline (jsonl invariant)")
	}
}

func TestWriteAuditEntryFillsTimestamp(t *testing.T) {
	buf := &bytes.Buffer{}
	if err := writeAuditEntry(buf, AuditEntry{RunID: "x"}); err != nil {
		t.Fatalf("writeAuditEntry: %v", err)
	}
	var got AuditEntry
	_ = json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &got)
	if got.TS.IsZero() {
		t.Error("TS should be auto-filled")
	}
}

func TestOpenAuditLogCreatesDirAndAppends(t *testing.T) {
	gitDir := t.TempDir()
	w, err := OpenAuditLog(gitDir)
	if err != nil {
		t.Fatalf("OpenAuditLog: %v", err)
	}
	if err := w.Write(AuditEntry{RunID: "1", Subject: "first"}); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and append — ensures O_APPEND flag worked.
	w2, err := OpenAuditLog(gitDir)
	if err != nil {
		t.Fatalf("OpenAuditLog reopen: %v", err)
	}
	if err := w2.Write(AuditEntry{RunID: "2", Subject: "second"}); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	_ = w2.Close()

	data, err := os.ReadFile(filepath.Join(gitDir, "gk-ai-commit", "audit.jsonl"))
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d (%q)", len(lines), string(data))
	}
	for _, line := range lines {
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("parse %q: %v", line, err)
		}
	}
}
