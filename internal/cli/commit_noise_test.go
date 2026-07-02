package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/aicommit"
)

func TestIsNoisePath(t *testing.T) {
	noise := []string{
		".venv/lib/python3.12/site-packages/x.py",
		"venv/bin/activate",
		"a/b/__pycache__/m.pyc",
		"app.db", "data/store.sqlite", "x.sqlite3",
		".DS_Store", "sub/.DS_Store",
		"node_modules/pkg/index.js",
		".pytest_cache/v/cache",
		// toolchain-owned dot dirs: SwiftPM / Dart
		"app/.build/arm64-apple-macosx/debug/index/store/v5/records/Q2/vm_types.h-2LK",
		".build/debug/MyApp",
		".dart_tool/package_config.json",
		"pkg/.swiftpm/xcode/xcshareddata/x.plist",
	}
	for _, p := range noise {
		if !isNoisePath(p) {
			t.Errorf("expected noise: %q", p)
		}
	}
	clean := []string{
		"main.py", "src/app.go", "README.md",
		"build.gradle", "internal/build/build.go", // 'build' dir is NOT treated as noise (conservative)
		"vendor/lib.go", "dist/index.js", // ambiguous dirs intentionally NOT noise
	}
	for _, p := range clean {
		if isNoisePath(p) {
			t.Errorf("expected NOT noise: %q", p)
		}
	}
}

func TestNoiseGitignorePatterns(t *testing.T) {
	noise := []aicommit.FileChange{
		{Path: ".venv/lib/x.py"},
		{Path: "a/__pycache__/m.pyc"},
		{Path: "app.db"},
		{Path: ".DS_Store"},
		{Path: "node_modules/p/i.js"},
	}
	got := strings.Join(noiseGitignorePatterns(noise), ",")
	for _, want := range []string{".venv/", "__pycache__/", "*.db", ".DS_Store", "node_modules/"} {
		if !strings.Contains(got, want) {
			t.Errorf("patterns %q missing %q", got, want)
		}
	}
}

func TestAppendGitignore(t *testing.T) {
	dir := t.TempDir()
	added, err := appendGitignore(dir, []string{"node_modules/", "*.pyc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Fatalf("first add: want 2, got %v", added)
	}
	// Second call: existing pattern skipped, only the new one added.
	added2, err := appendGitignore(dir, []string{"node_modules/", "*.log"})
	if err != nil {
		t.Fatal(err)
	}
	if len(added2) != 1 || added2[0] != "*.log" {
		t.Fatalf("second add: want [*.log], got %v", added2)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.Count(string(data), "node_modules/") != 1 {
		t.Errorf("node_modules/ should appear once, got:\n%s", data)
	}
}
