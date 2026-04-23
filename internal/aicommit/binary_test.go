package aicommit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectBinary(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{"text", []byte("package main\n\nfunc main() {}\n"), false},
		{"nul-byte", []byte("hello\x00world"), true},
		{"high-entropy", mkBytes(0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08), true},
		{"utf8-korean", []byte("한국어 테스트 파일\n"), false},
		{"empty", []byte{}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, tc.payload, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := DetectBinary(path)
			if err != nil {
				t.Fatalf("DetectBinary: %v", err)
			}
			if got != tc.want {
				t.Errorf("DetectBinary(%s): want %v, got %v", tc.name, tc.want, got)
			}
		})
	}
}

func TestDetectBinaryMissingFile(t *testing.T) {
	got, err := DetectBinary(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing file err: %v", err)
	}
	if got {
		t.Error("missing file: want false, got true")
	}
}

// mkBytes is a tiny helper to build a byte slice inline for test fixtures.
func mkBytes(bs ...byte) []byte { return bs }
