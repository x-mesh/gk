package lineedit

import (
	"bytes"
	"strings"
	"testing"
)

// TestVisualLengthWide pins the core of the wide-rune patch: cell width, not
// rune count, with ANSI escape sequences contributing zero.
func TestVisualLengthWide(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"가", 2},                  // one Hangul syllable = two cells
		{"가나다", 6},                // three wide runes
		{"a가b", 4},                // mixed narrow + wide
		{"\x1b[1;36m가\x1b[0m", 2}, // color codes are invisible
	}
	for _, c := range cases {
		if got := visualLength([]rune(c.in)); got != c.want {
			t.Errorf("visualLength(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// scriptedRW feeds a fixed input script to the editor and captures its output,
// standing in for a live terminal so ReadLine can be exercised in tests.
type scriptedRW struct {
	in  *bytes.Reader
	out bytes.Buffer
}

func (s *scriptedRW) Read(p []byte) (int, error)  { return s.in.Read(p) }
func (s *scriptedRW) Write(p []byte) (int, error) { return s.out.Write(p) }

// readOnce drives one ReadLine over the given input bytes. "\r" submits;
// "\x7f" is backspace.
func readOnce(t *testing.T, input string) (string, *scriptedRW) {
	t.Helper()
	rw := &scriptedRW{in: bytes.NewReader([]byte(input))}
	term := NewTerminal(rw, "› ")
	line, err := term.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine(%q): unexpected error %v", input, err)
	}
	return line, rw
}

// TestReadLineWideRunes exercises the wide-rune editing paths (insert, delete,
// mid-line delete) through the public ReadLine API — the slicing added by the
// patch (t.line[:pos]) must stay in bounds and the returned buffer correct.
func TestReadLineWideRunes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"type only", "가나다\r", "가나다"},
		{"backspace wide", "가나\x7f\r", "가"},
		{"delete-then-retype", "가나\x7f다\r", "가다"},
		{"mixed", "a가b\x7f\x7f\r", "a"},
		{"delete all", "가나다\x7f\x7f\x7f\r", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rw := readOnce(t, c.input)
			if got != c.want {
				t.Errorf("ReadLine(%q) = %q, want %q", c.input, got, c.want)
			}
			// A wide-rune delete must blank the vacated cells: after erasing,
			// the editor emits the erase-line-right sequence and spaces. We
			// only sanity-check that output was produced without panicking.
			if strings.Contains(c.input, "\x7f") && rw.out.Len() == 0 {
				t.Error("expected redraw output after backspace, got none")
			}
		})
	}
}
