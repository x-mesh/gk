package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/aicommit"
)

// unformattedGo is valid Go that gofmt will rewrite (bad indentation +
// missing alignment), so `gofmt -l` lists it.
const unformattedGo = "package main\nfunc main() {\nx := 1\n_ = x\n}\n"

// writeGoModRepo creates a temp dir with a go.mod and the given files
// (path → content relative to the repo root), returning the repo root.
func writeGoModRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func fc(path string) aicommit.FileChange {
	return aicommit.FileChange{Path: path, Status: "modified"}
}

func TestGuardGofmt_ReportsUnformatted(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}
	root := writeGoModRepo(t, map[string]string{"bad.go": unformattedGo})
	var out bytes.Buffer
	guardGofmt(context.Background(), &out, root, []aicommit.FileChange{fc(filepath.Join(root, "bad.go"))})

	got := out.String()
	if !strings.Contains(got, "NOTE") {
		t.Errorf("expected a NOTE advisory block, got:\n%s", got)
	}
	if !strings.Contains(got, "bad.go") {
		t.Errorf("expected the unformatted file in the note, got:\n%s", got)
	}
	if !strings.Contains(got, "gofmt -w") {
		t.Errorf("expected a `gofmt -w` fix hint, got:\n%s", got)
	}
}

func TestGuardGofmt_CleanIsSilent(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}
	// gofmt-clean content (formatted by gofmt itself would be a no-op).
	clean := "package main\n\nfunc main() {\n\tx := 1\n\t_ = x\n}\n"
	root := writeGoModRepo(t, map[string]string{"good.go": clean})
	var out bytes.Buffer
	guardGofmt(context.Background(), &out, root, []aicommit.FileChange{fc(filepath.Join(root, "good.go"))})

	if out.Len() != 0 {
		t.Errorf("expected no output for formatted file, got:\n%s", out.String())
	}
}

func TestGuardGofmt_NoGoModSkips(t *testing.T) {
	// Repo with an unformatted .go file but NO go.mod → guard is silent.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(unformattedGo), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	guardGofmt(context.Background(), &out, dir, []aicommit.FileChange{fc(filepath.Join(dir, "bad.go"))})

	if out.Len() != 0 {
		t.Errorf("expected no output without go.mod, got:\n%s", out.String())
	}
}

func TestGuardGofmt_NoGofmtBinarySkips(t *testing.T) {
	root := writeGoModRepo(t, map[string]string{"bad.go": unformattedGo})
	// Empty PATH → exec.LookPath("gofmt") fails → guard self-skips.
	t.Setenv("PATH", "")
	var out bytes.Buffer
	guardGofmt(context.Background(), &out, root, []aicommit.FileChange{fc(filepath.Join(root, "bad.go"))})

	if out.Len() != 0 {
		t.Errorf("expected no output when gofmt is unavailable, got:\n%s", out.String())
	}
}

func TestGuardGofmt_ExcludesGenerated(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}
	// All three are unformatted but generated → none should be reported.
	root := writeGoModRepo(t, map[string]string{
		"api_gen.go":           unformattedGo,
		"msg.pb.go":            unformattedGo,
		"zz_generated.deep.go": unformattedGo,
	})
	var out bytes.Buffer
	guardGofmt(context.Background(), &out, root, []aicommit.FileChange{
		fc(filepath.Join(root, "api_gen.go")),
		fc(filepath.Join(root, "msg.pb.go")),
		fc(filepath.Join(root, "zz_generated.deep.go")),
	})

	if out.Len() != 0 {
		t.Errorf("expected generated files to be excluded, got:\n%s", out.String())
	}
}

func TestGuardGofmt_ExcludesDeleted(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}
	root := writeGoModRepo(t, nil) // go.mod only; gone.go never written
	var out bytes.Buffer
	guardGofmt(context.Background(), &out, root, []aicommit.FileChange{
		{Path: filepath.Join(root, "gone.go"), Status: "deleted"},
	})

	if out.Len() != 0 {
		t.Errorf("expected deleted files to be skipped, got:\n%s", out.String())
	}
}

func TestIsGeneratedGoFile(t *testing.T) {
	generated := []string{"x.pb.go", "api_gen.go", "zz_generated.deepcopy.go", "sub/dir/foo_gen.go"}
	for _, p := range generated {
		if !isGeneratedGoFile(p) {
			t.Errorf("expected generated: %q", p)
		}
	}
	plain := []string{"main.go", "switch_test.go", "gen.go", "generator.go", "pbgo.go"}
	for _, p := range plain {
		if isGeneratedGoFile(p) {
			t.Errorf("expected NOT generated: %q", p)
		}
	}
}
