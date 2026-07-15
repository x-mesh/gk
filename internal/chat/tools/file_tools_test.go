package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSliceLines(t *testing.T) {
	cases := []struct {
		name, text string
		start, end int
		want       string
	}{
		{"middle range", "L1\nL2\nL3\nL4\nL5", 2, 4, "L2\nL3\nL4"},
		{"single line", "L1\nL2\nL3\nL4\nL5", 3, 3, "L3"},
		{"start only to end", "L1\nL2\nL3\nL4\nL5", 4, 0, "L4\nL5"},
		{"end past length clamps", "L1\nL2\nL3\nL4\nL5", 3, 99, "L3\nL4\nL5"},
		{"zero start defaults to first", "L1\nL2\nL3\nL4\nL5", 0, 2, "L1\nL2"},
		{"end before start snaps to single", "L1\nL2\nL3\nL4\nL5", 4, 2, "L4"},
		{"start past end returns note", "L1\nL2\nL3\nL4\nL5", 10, 12, "(file has 5 line(s); start_line 10 is past the end)"},
		{"trailing newline is not extra line", "L1\nL2\n", 3, 3, "(file has 2 line(s); start_line 3 is past the end)"},
		{"empty file has zero lines", "", 1, 1, "(file has 0 line(s); start_line 1 is past the end)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readLineRange(strings.NewReader(c.text), c.start, c.end, defaultPerFileCap)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("readLineRange(%d,%d) = %q, want %q", c.start, c.end, got, c.want)
			}
		})
	}
}

func TestReadLineRangeStopsAtEndAndCap(t *testing.T) {
	text := strings.Repeat("line\n", 10_000)
	r := strings.NewReader(text)
	if got, err := readLineRange(r, 2, 2, defaultPerFileCap); err != nil || got != "line" {
		t.Fatalf("bounded line read = %q, %v", got, err)
	}
	if r.Len() == 0 {
		t.Fatal("bounded line read consumed the whole file")
	}

	long := strings.NewReader(strings.Repeat("x", 1<<20))
	got, err := readLineRange(long, 1, 1, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[truncated") || long.Len() == 0 {
		t.Fatalf("capped read did not stop early: len=%d remaining=%d", len(got), long.Len())
	}
	exact, err := readLineRange(strings.NewReader(strings.Repeat("x", 32)+"\n"), 1, 1, 32)
	if err != nil || exact != strings.Repeat("x", 32) {
		t.Fatalf("line-ending delimiter consumed cap: %q, %v", exact, err)
	}
}

// file_read honors start_line/end_line — the field names a caller reaches for
// by reflex — instead of rejecting them as unknown fields.
func TestFileReadLineRange(t *testing.T) {
	sb, root, _ := sandboxFixture(t)
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte("one\ntwo\nthree\nfour\nfive"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &FileTools{Sandbox: sb}

	// A range read returns only those lines.
	out, err := f.fileRead(context.Background(), json.RawMessage(`{"path":"many.txt","start_line":2,"end_line":4}`))
	if err != nil {
		t.Fatalf("fileRead range: %v", err)
	}
	if out != "two\nthree\nfour" {
		t.Errorf("range read = %q, want %q", out, "two\nthree\nfour")
	}

	// No range → whole file (unchanged behavior).
	whole, err := f.fileRead(context.Background(), json.RawMessage(`{"path":"many.txt"}`))
	if err != nil {
		t.Fatalf("fileRead whole: %v", err)
	}
	if whole != "one\ntwo\nthree\nfour\nfive" {
		t.Errorf("whole read = %q", whole)
	}
}
