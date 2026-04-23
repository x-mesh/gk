package cli

import "github.com/spf13/cobra"

var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "AI-assisted workflows",
	Long: `AI-powered helpers for common git tasks.

Subcommands call external AI CLIs (gemini, qwen, kiro-cli) as providers
and never talk to remote LLM APIs directly. Provider selection is
controlled via config (ai.provider) with auto-detection in the order
gemini → qwen → kiro-cli; override with --provider on any subcommand.
`,
}

func init() {
	rootCmd.AddCommand(aiCmd)
}

// AICmd exposes the `gk ai` group so subcommands in other files can
// register themselves. Mirrors the Root()/rootCmd pattern.
func AICmd() *cobra.Command { return aiCmd }
