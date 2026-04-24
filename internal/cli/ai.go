package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
)

var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "AI-assisted workflows",
	Long: `AI-powered helpers for common git tasks.

Subcommands:
  commit      Generate and apply Conventional Commit messages
  pr          Generate a structured PR description
  review      AI-powered code review on staged or range diff
  changelog   Generate a changelog from a range of commits

Provider selection (auto-detect order: nvidia → gemini → qwen → kiro):
  nvidia uses the NVIDIA Chat Completions API via HTTP (default).
  gemini, qwen, kiro-cli shell out to their respective CLI binaries.
  Override with --provider on any subcommand.

Remote providers (Locality=remote) pass through a Privacy Gate that
redacts secrets and deny_paths before the payload leaves the machine.
Use --show-prompt to inspect the redacted payload.

When no explicit --provider is given, a Fallback Chain tries each
available provider in auto-detect order, moving to the next on failure.
`,
}

func init() {
	aiCmd.PersistentFlags().Bool("show-prompt", false, "display the redacted payload sent to the provider")
	rootCmd.AddCommand(aiCmd)
}

// AICmd exposes the `gk ai` group so subcommands in other files can
// register themselves. Mirrors the Root()/rootCmd pattern.
func AICmd() *cobra.Command { return aiCmd }

// ── Privacy Gate helper ──────────────────────────────────────────────

// applyPrivacyGate redacts the payload when the provider is remote.
// Returns the (possibly redacted) payload and any findings. If the
// provider is local, the payload is returned unchanged.
func applyPrivacyGate(prov provider.Provider, payload string, cfg config.AIConfig) (string, []aicommit.RedactFinding, error) {
	if prov.Locality() != provider.LocalityRemote {
		return payload, nil, nil
	}
	return aicommit.Redact(payload, aicommit.PrivacyGateOptions{
		DenyPaths:  cfg.Commit.DenyPaths,
		MaxSecrets: 10,
	})
}

// showPromptIfRequested prints the redacted payload when --show-prompt
// is set. Returns true if the flag was set (caller may still proceed).
func showPromptIfRequested(cmd *cobra.Command, payload string) bool {
	show, _ := cmd.Flags().GetBool("show-prompt")
	if !show {
		return false
	}
	fmt.Fprintln(cmd.OutOrStdout(), "--- show-prompt: redacted payload ---")
	fmt.Fprintln(cmd.OutOrStdout(), payload)
	fmt.Fprintln(cmd.OutOrStdout(), "--- end ---")
	return true
}

// ── Fallback Chain builder ───────────────────────────────────────────

// buildFallbackChain constructs a FallbackChain from available
// providers in auto-detect order. Providers that fail Available()
// (missing binary, missing auth) are excluded from the chain so
// they never waste time on doomed API calls.
func buildFallbackChain(order []string, runner provider.CommandRunner) (*provider.FallbackChain, error) {
	if len(order) == 0 {
		order = []string{"nvidia", "gemini", "qwen", "kiro"}
	}
	if runner == nil {
		runner = provider.ExecRunner{}
	}
	ctx := context.Background()
	var providers []provider.Provider
	for _, name := range order {
		p, err := provider.Build(name, runner)
		if err != nil {
			Dbg("fallback: skip %s: build error: %v", name, err)
			continue
		}
		if err := p.Available(ctx); err != nil {
			Dbg("fallback: skip %s: %v", name, err)
			continue
		}
		providers = append(providers, p)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("no AI providers available — run `gk doctor` for setup hints")
	}
	return &provider.FallbackChain{
		Providers: providers,
		Dbg:       Dbg,
	}, nil
}
