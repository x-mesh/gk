package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
)

// configInitCmd은 `gk config init [--force] [--out <path>]`를 처리한다.
// 기존 init_config.go의 로직을 gk config init으로 이동한 것이다.
func init() {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold the default ~/.config/gk/config.yaml",
		Long: `Writes a fully-commented YAML template that documents every supported
field (ai, commit, log, status, branch, clone, worktree, …). Intended as
a starting point — edit to taste, uncomment the lines you care about.

Without --out, the file lands at $XDG_CONFIG_HOME/gk/config.yaml (falls
back to ~/.config/gk/config.yaml). Existing files are never overwritten
unless --force is passed.`,
		RunE: runConfigInit,
	}
	cmd.Flags().Bool("force", false, "overwrite an existing file")
	cmd.Flags().String("out", "", "write to this path instead of the global default")

	// configCmd 아래에 등록
	if parent, _, err := rootCmd.Find([]string{"config"}); err == nil && parent != nil && parent.Use == "config" {
		parent.AddCommand(cmd)
	}
}

// runConfigInit은 전역 config 파일을 생성한다.
// gk config init과 deprecated gk init config 모두에서 호출된다.
func runConfigInit(cmd *cobra.Command, _ []string) error {
	force, _ := cmd.Flags().GetBool("force")
	out, _ := cmd.Flags().GetString("out")

	path := out
	if path == "" {
		path = config.GlobalConfigPath()
	}
	if path == "" {
		return fmt.Errorf("gk config init: cannot determine target path; pass --out")
	}

	err := config.WriteDefaultConfig(path, force)
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "created: %s\n", path)
		return nil
	case errors.Is(err, config.ErrConfigExists):
		fmt.Fprintf(cmd.OutOrStdout(), "skipped: %s (already exists — pass --force to overwrite)\n", path)
		return nil
	default:
		return err
	}
}
