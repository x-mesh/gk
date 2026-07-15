package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSliceLines(t *testing.T) {
	text := "L1\nL2\nL3\nL4\nL5"
	cases := []struct {
		name       string
		start, end int
		want       string
	}{
		{"middle range", 2, 4, "L2\nL3\nL4"},
		{"single line", 3, 3, "L3"},
		{"start only to end", 4, 0, "L4\nL5"},
		{"end past length clamps", 3, 99, "L3\nL4\nL5"},
		{"zero start defaults to first", 0, 2, "L1\nL2"},
		{"end before start snaps to single", 4, 2, "L4"},
		{"start past end returns note", 10, 12, "(file has 5 line(s); start_line 10 is past the end)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sliceLines(text, c.start, c.end); got != c.want {
				t.Errorf("sliceLines(%d,%d) = %q, want %q", c.start, c.end, got, c.want)
			}
		})
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
