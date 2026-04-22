package cli

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed templates/ai/CLAUDE.md
var claudeMDTemplate string

//go:embed templates/ai/AGENTS.md
var agentsMDTemplate string

//go:embed templates/ai/kiro-product.md
var kiroProductTemplate string

//go:embed templates/ai/kiro-tech.md
var kiroTechTemplate string

//go:embed templates/ai/kiro-structure.md
var kiroStructureTemplate string

func init() {
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize gk scaffolding in a repository",
		Long: `gk init scaffolds configuration and context files into a git repository.
Use a subcommand to choose which kind of scaffolding you need.
`,
	}

	initAICmd := &cobra.Command{
		Use:   "ai",
		Short: "Scaffold AI context files (CLAUDE.md, AGENTS.md)",
		Long: `Creates CLAUDE.md and AGENTS.md in the repository root (or --out path)
so that AI coding assistants have immediate project context.

Pass --kiro to also scaffold .kiro/steering/ documents (product.md, tech.md,
structure.md) for Kiro-compatible assistants.

Exits non-zero when a target file already exists; pass --force to overwrite.
`,
		RunE: runInitAI,
	}
	initAICmd.Flags().Bool("force", false, "overwrite existing files")
	initAICmd.Flags().Bool("kiro", false, "also scaffold .kiro/steering/ documents")
	initAICmd.Flags().String("out", "", "write files to this directory instead of repo root")

	initCmd.AddCommand(initAICmd)
	rootCmd.AddCommand(initCmd)
}

// detectProjectType inspects dir for well-known manifest files and returns a
// short language/runtime identifier. The first match wins.
func detectProjectType(dir string) string {
	manifests := []struct {
		file string
		kind string
	}{
		{"go.mod", "go"},
		{"package.json", "node"},
		{"pyproject.toml", "python"},
		{"Cargo.toml", "rust"},
		{"pom.xml", "java"},
	}
	for _, m := range manifests {
		if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
			return m.kind
		}
	}
	return "unknown"
}

// writeScaffoldFile writes content to path. If the file already exists and
// force is false it prints a "skipped" notice and returns nil. Otherwise it
// creates (or overwrites) the file and prints "created".
func writeScaffoldFile(cmd *cobra.Command, path, content string, force bool) error {
	_, statErr := os.Stat(path)
	if statErr == nil && !force {
		fmt.Fprintf(cmd.OutOrStdout(), "skipped: %s\n", path)
		return nil
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("init ai: stat %s: %w", path, statErr)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("init ai: mkdir %s: %w", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("init ai: write %s: %w", path, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "created: %s\n", path)
	return nil
}

func runInitAI(cmd *cobra.Command, _ []string) error {
	outDir, _ := cmd.Flags().GetString("out")
	force, _ := cmd.Flags().GetBool("force")
	kiro, _ := cmd.Flags().GetBool("kiro")

	if outDir == "" {
		outDir = RepoFlag()
	}
	if outDir == "" {
		var err error
		outDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("init ai: cannot determine working directory: %w", err)
		}
	}

	_ = detectProjectType(outDir) // available for future template per-language customisation

	coreFiles := []struct {
		name    string
		content string
	}{
		{"CLAUDE.md", claudeMDTemplate},
		{"AGENTS.md", agentsMDTemplate},
	}
	for _, f := range coreFiles {
		if err := writeScaffoldFile(cmd, filepath.Join(outDir, f.name), f.content, force); err != nil {
			return err
		}
	}

	if kiro {
		kiroFiles := []struct {
			name    string
			content string
		}{
			{filepath.Join(".kiro", "steering", "product.md"), kiroProductTemplate},
			{filepath.Join(".kiro", "steering", "tech.md"), kiroTechTemplate},
			{filepath.Join(".kiro", "steering", "structure.md"), kiroStructureTemplate},
		}
		for _, f := range kiroFiles {
			if err := writeScaffoldFile(cmd, filepath.Join(outDir, f.name), f.content, force); err != nil {
				return err
			}
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "next: edit the scaffolded files with project-specific context, then commit them")
	return nil
}
