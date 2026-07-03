package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

func TestEntryAgeAndModifiedAt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fresh.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := entryAge(dir, "fresh.txt"); got != "now" {
		t.Errorf("fresh file age = %q, want now", got)
	}

	old := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	if got := entryAge(dir, "old.txt"); got != "3h" {
		t.Errorf("old file age = %q, want 3h", got)
	}

	// Deleted paths: no age, no timestamp — renderers show nothing.
	if got := entryAge(dir, "missing.txt"); got != "" {
		t.Errorf("missing file age = %q, want empty", got)
	}
	if got := entryModifiedAt(dir, "missing.txt"); got != "" {
		t.Errorf("missing file modified_at = %q, want empty", got)
	}

	ts := entryModifiedAt(dir, "old.txt")
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("modified_at %q is not RFC3339: %v", ts, err)
	}
	if d := parsed.Sub(stale); d > time.Second || d < -time.Second {
		t.Errorf("modified_at %v drifts from mtime %v", parsed, stale)
	}
}

func TestRenderStatusTreeShowsAge(t *testing.T) {
	entries := []git.StatusEntry{
		{Kind: git.KindOrdinary, XY: ".M", Path: "a.go"},
		{Kind: git.KindOrdinary, XY: ".M", Path: "b.go"},
	}
	ages := map[string]string{"a.go": "2h"}
	buf := &bytes.Buffer{}
	renderStatusTree(buf, entries, nil, ages)
	out := buf.String()
	if !strings.Contains(out, "· 2h") {
		t.Errorf("tree should show a.go age, got:\n%s", out)
	}
	if strings.Count(out, "·") != 1 {
		t.Errorf("b.go has no age entry and must render without a suffix:\n%s", out)
	}
}
