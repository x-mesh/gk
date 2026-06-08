package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// `gk config doctor` validates the config files rather than the git environment
// (that's `gk doctor`): it flags unknown keys (typos — `set` blocks them, but a
// hand-edit doesn't) and a provider configured without its API key.
func init() {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check config files for unknown keys and missing provider keys",
		Args:  cobra.NoArgs,
		RunE:  runConfigDoctor,
	}
	if parent, _, err := rootCmd.Find([]string{"config"}); err == nil && parent != nil && parent.Use == "config" {
		parent.AddCommand(cmd)
	}
}

// providerKeyEnv maps an HTTP provider name to the env var holding its API key.
// CLI providers (kiro, gemini, qwen) authenticate themselves and are omitted.
var providerKeyEnv = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
	"groq":      "GROQ_API_KEY",
	"nvidia":    "NVIDIA_API_KEY",
}

func runConfigDoctor(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	issues := 0

	// 1. Unknown keys in each config file.
	for _, f := range configFilesInScope(cmd) {
		if f.path == "" {
			continue
		}
		unknown, err := config.UnknownKeys(f.path)
		if err != nil {
			fmt.Fprintf(out, "✗ %s: %v\n", f.scope, err)
			issues++
			continue
		}
		for _, k := range unknown {
			fmt.Fprintf(out, "✗ 알 수 없는 키: %s  (%s: %s)\n", k, f.scope, f.path)
			issues++
		}
	}

	// 2. Provider configured but its API key env is missing.
	cfg, err := config.Load(cmd.Flags())
	if err == nil && cfg != nil {
		if env, ok := providerKeyEnv[cfg.AI.Provider]; ok && os.Getenv(env) == "" {
			fmt.Fprintf(out, "✗ provider %q 설정됨, 그러나 %s 환경변수가 없습니다\n", cfg.AI.Provider, env)
			issues++
		}
	}

	if issues == 0 {
		fmt.Fprintln(out, successLine("ok", "설정에 문제가 없습니다"))
		return nil
	}
	return fmt.Errorf("gk config doctor: 문제 %d건 발견 — 위 항목을 확인하세요", issues)
}

type scopedConfigFile struct {
	scope string
	path  string
}

// configFilesInScope returns the global config and, when inside a repo, the
// repo-local .gk.yaml — the two files config doctor inspects.
func configFilesInScope(cmd *cobra.Command) []scopedConfigFile {
	files := []scopedConfigFile{{scope: "global", path: config.GlobalConfigPath()}}
	if root, terr := gitToplevel(cmd.Context(), &git.ExecRunner{Dir: RepoFlag()}); terr == nil && root != "" {
		files = append(files, scopedConfigFile{scope: "local", path: config.LocalConfigPath(root)})
	}
	return files
}
