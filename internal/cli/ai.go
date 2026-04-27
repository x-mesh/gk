package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
)

func init() {
	rootCmd.PersistentFlags().Bool("show-prompt", false, "display the redacted payload sent to the provider")
}

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
