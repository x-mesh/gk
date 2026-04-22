package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// makeInitAICmd returns a *cobra.Command pre-wired with the init ai flags.
func makeInitAICmd(t *testing.T, outDir string, force, kiro bool) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Bool("kiro", false, "")
	cmd.Flags().String("out", "", "")
	if err := cmd.Flags().Set("out", outDir); err != nil {
		t.Fatalf("set --out: %v", err)
	}
	if force {
		if err := cmd.Flags().Set("force", "true"); err != nil {
			t.Fatalf("set --force: %v", err)
		}
	}
	if kiro {
		if err := cmd.Flags().Set("kiro", "true"); err != nil {
			t.Fatalf("set --kiro: %v", err)
		}
	}
	return cmd
}

func TestDetectProjectType(t *testing.T) {
	cases := []struct {
		manifest string
		want     string
	}{
		{"go.mod", "go"},
		{"package.json", "node"},
		{"pyproject.toml", "python"},
		{"Cargo.toml", "rust"},
		{"pom.xml", "java"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.manifest), []byte{}, 0o644); err != nil {
				t.Fatal(err)
			}
			if got := detectProjectType(dir); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		if got := detectProjectType(t.TempDir()); got != "unknown" {
			t.Errorf("got %q, want %q", got, "unknown")
		}
	})
}

func TestInitAI_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	cmd := makeInitAICmd(t, dir, false, false)

	if err := runInitAI(cmd, nil); err != nil {
		t.Fatalf("runInitAI: %v", err)
	}

	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("expected %s to be created: %v", name, err)
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
	// Kiro files must NOT be created without --kiro.
	if _, err := os.Stat(filepath.Join(dir, ".kiro")); !os.IsNotExist(err) {
		t.Error(".kiro/ should not be created without --kiro flag")
	}
}

func TestInitAI_KiroFlag(t *testing.T) {
	dir := t.TempDir()
	cmd := makeInitAICmd(t, dir, false, true)

	if err := runInitAI(cmd, nil); err != nil {
		t.Fatalf("runInitAI: %v", err)
	}

	kiroFiles := []string{
		filepath.Join(".kiro", "steering", "product.md"),
		filepath.Join(".kiro", "steering", "tech.md"),
		filepath.Join(".kiro", "steering", "structure.md"),
	}
	for _, rel := range kiroFiles {
		if _, err := os.Stat(filepath.Join(dir, rel)); os.IsNotExist(err) {
			t.Errorf("expected %s to be created with --kiro", rel)
		}
	}
}

func TestInitAI_SkipsExistingWithoutForce(t *testing.T) {
	dir := t.TempDir()
	original := "original content"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := makeInitAICmd(t, dir, false, false)
	if err := runInitAI(cmd, nil); err != nil {
		t.Fatalf("runInitAI: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if string(data) != original {
		t.Error("CLAUDE.md should not be overwritten without --force")
	}
}

func TestInitAI_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := makeInitAICmd(t, dir, true, false)
	if err := runInitAI(cmd, nil); err != nil {
		t.Fatalf("runInitAI: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if strings.TrimSpace(string(data)) == "old" {
		t.Error("CLAUDE.md should be overwritten with --force")
	}
}

func TestInitAI_TemplateContent(t *testing.T) {
	dir := t.TempDir()
	cmd := makeInitAICmd(t, dir, false, false)

	if err := runInitAI(cmd, nil); err != nil {
		t.Fatalf("runInitAI: %v", err)
	}

	claude, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(string(claude), "# CLAUDE.md") {
		t.Error("CLAUDE.md should contain # CLAUDE.md header")
	}

	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(string(agents), "# AGENTS.md") {
		t.Error("AGENTS.md should contain # AGENTS.md header")
	}
}
