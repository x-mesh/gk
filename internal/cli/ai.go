package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

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
//
// MaxSecrets falls back to ai.commit.privacy.max_secrets (default 10).
// A negative value disables the threshold so callers can audit findings
// without aborting.
func applyPrivacyGate(prov provider.Provider, payload string, cfg config.AIConfig) (string, []aicommit.RedactFinding, error) {
	if prov.Locality() != provider.LocalityRemote {
		return payload, nil, nil
	}
	max := cfg.Commit.Privacy.MaxSecrets
	if max == 0 {
		max = 10
	}
	return aicommit.Redact(payload, aicommit.PrivacyGateOptions{
		DenyPaths:  cfg.Commit.DenyPaths,
		MaxSecrets: max,
	})
}

// renderPrivacyFindings writes a per-finding report to w grouped by file
// so the user can see exactly where each match came from. Used when the
// gate aborts; safe to call on success too.
func renderPrivacyFindings(w io.Writer, findings []aicommit.RedactFinding) {
	if len(findings) == 0 {
		return
	}
	type row struct {
		f aicommit.RedactFinding
	}
	byFile := map[string][]row{}
	var noFile []row
	for _, f := range findings {
		if f.File == "" {
			noFile = append(noFile, row{f})
			continue
		}
		byFile[f.File] = append(byFile[f.File], row{f})
	}
	files := make([]string, 0, len(byFile))
	for k := range byFile {
		files = append(files, k)
	}
	sort.Strings(files)

	fmt.Fprintln(w, "privacy gate findings:")
	for _, fp := range files {
		rows := byFile[fp]
		fmt.Fprintf(w, "  %s\n", fp)
		for _, r := range rows {
			fmt.Fprintf(w, "    %s  %s:%d  pattern=%s  sample=%s\n",
				r.f.Placeholder, fp, r.f.FileLine, displayPattern(r.f.Pattern), r.f.Original)
		}
	}
	if len(noFile) > 0 {
		fmt.Fprintln(w, "  (no source file)")
		for _, r := range noFile {
			fmt.Fprintf(w, "    %s  payload-line=%d  pattern=%s  sample=%s\n",
				r.f.Placeholder, r.f.Line, displayPattern(r.f.Pattern), r.f.Original)
		}
	}
	fmt.Fprintln(w, "hint: edit the offending lines, narrow with --staged-only,"+
		" or raise ai.commit.privacy.max_secrets in .gk.yaml")
}

func displayPattern(p string) string {
	if p == "" {
		return "?"
	}
	return strings.ReplaceAll(p, "_", "-")
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
