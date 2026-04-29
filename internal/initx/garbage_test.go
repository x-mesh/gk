package initx

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestDetectExistingGarbageEmpty(t *testing.T) {
	dir := t.TempDir()
	// Only a regular Go file — should be clean.
	mustWriteFile(t, filepath.Join(dir, "main.go"), "package main\n")
	got := DetectExistingGarbage(dir)
	if len(got) != 0 {
		t.Errorf("clean tree should yield no garbage, got %+v", got)
	}
}

func TestDetectExistingGarbageFindsPyc(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "src", "foo.pyc"), "")
	mustWriteFile(t, filepath.Join(dir, "src", "deep", "bar.pyc"), "")
	mustWriteFile(t, filepath.Join(dir, "main.go"), "package main\n")

	got := DetectExistingGarbage(dir)
	if len(got) == 0 {
		t.Fatal("expected at least one detection")
	}
	pyc := findPattern(got, "*.pyc")
	if pyc == nil {
		t.Fatalf("expected *.pyc detection, got %+v", got)
		return
	}
	if pyc.Count != 2 {
		t.Errorf("Count: want 2, got %d", pyc.Count)
	}
	if len(pyc.Sample) == 0 {
		t.Error("Sample should not be empty")
	}
}

func TestDetectExistingGarbageFindsPycache(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "internal", "x", "__pycache__", "y.pyc"), "")

	got := DetectExistingGarbage(dir)
	// Both "*.pyc" and "__pycache__/" should plausibly hit, but the
	// first match wins (per implementation). The matter is just that
	// SOMETHING fires.
	if len(got) == 0 {
		t.Fatal("nested __pycache__/y.pyc should be detected")
	}
}

func TestDetectExistingGarbageSkipsBigDirs(t *testing.T) {
	dir := t.TempDir()
	// node_modules should be skipped wholesale.
	mustWriteFile(t, filepath.Join(dir, "node_modules", "pkg", "junk.pyc"), "")
	// vendor too.
	mustWriteFile(t, filepath.Join(dir, "vendor", "x.class"), "")

	got := DetectExistingGarbage(dir)
	if len(got) != 0 {
		t.Errorf("scanner should skip node_modules and vendor, got %+v", got)
	}
}

func TestDetectExistingGarbageSamplesCappedToLimit(t *testing.T) {
	dir := t.TempDir()
	// Create more files than the sample cap.
	for i := 0; i < garbageSampleLimit+5; i++ {
		mustWriteFile(t, filepath.Join(dir, "src", "f"+itoa(i)+".pyc"), "")
	}
	got := DetectExistingGarbage(dir)
	pyc := findPattern(got, "*.pyc")
	if pyc == nil {
		t.Fatal("expected *.pyc detection")
		return
	}
	if len(pyc.Sample) != garbageSampleLimit {
		t.Errorf("Sample length: want %d, got %d", garbageSampleLimit, len(pyc.Sample))
	}
	if pyc.Count != garbageSampleLimit+5 {
		t.Errorf("Count: want %d, got %d", garbageSampleLimit+5, pyc.Count)
	}
}

func TestDetectExistingGarbageEmptyDir(t *testing.T) {
	if got := DetectExistingGarbage(""); got != nil {
		t.Errorf("empty dir should return nil, got %+v", got)
	}
}

func TestMatchArtifactPattern(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"*.pyc", "foo.pyc", true},
		{"*.pyc", "src/foo.pyc", true},
		{"*.pyc", "foo.go", false},
		{"__pycache__/", "internal/__pycache__/x.pyc", true},
		{"__pycache__/", "src/foo.py", false},
		{"coverage/", "coverage/index.html", true},
		{"coverage/", "x/coverage/y.html", true},
		{"*.class", "Main.class", true},
		{"*.class", "Main.classes", false},
	}
	for _, tc := range cases {
		got := matchArtifactPattern(tc.pat, tc.path)
		if got != tc.want {
			t.Errorf("matchArtifactPattern(%q, %q) = %v, want %v", tc.pat, tc.path, got, tc.want)
		}
	}
}

// TestDetectExistingGarbageOrderedByCount verifies the result is sorted
// by Count descending — useful so the CLI shows the worst offender first.
func TestDetectExistingGarbageOrderedByCount(t *testing.T) {
	dir := t.TempDir()
	// 1 .class, 3 .pyc.
	mustWriteFile(t, filepath.Join(dir, "Main.class"), "")
	mustWriteFile(t, filepath.Join(dir, "a.pyc"), "")
	mustWriteFile(t, filepath.Join(dir, "b.pyc"), "")
	mustWriteFile(t, filepath.Join(dir, "c.pyc"), "")

	got := DetectExistingGarbage(dir)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 detections, got %+v", got)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool {
		return got[i].Count > got[j].Count
	}) {
		t.Errorf("detections not sorted by Count desc: %+v", got)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func findPattern(dets []GarbageDetection, pat string) *GarbageDetection {
	for i := range dets {
		if dets[i].Pattern == pat {
			return &dets[i]
		}
	}
	return nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
