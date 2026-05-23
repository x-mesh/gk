package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// aiAutoOrder is the canonical provider auto-detect order, shared by the
// factory's AutoOrder default and buildFallbackChain so a single command
// and a fallback chain probe providers in the same sequence.
var aiAutoOrder = []string{"anthropic", "openai", "nvidia", "groq", "gemini", "qwen", "kiro"}

func init() {
	rootCmd.PersistentFlags().Bool("show-prompt", false, "display the redacted payload sent to the provider")
	rootCmd.PersistentFlags().Bool("skip-privacy", false, "skip privacy gate abort threshold (redaction still applied)")
}

// ── Remote-policy gate ───────────────────────────────────────────────

// ensureRemoteAllowed enforces the local-only policy across EVERY AI entry
// point, not just `gk commit`. When the resolved provider is remote and
// ai.commit.allow_remote is false, the caller must refuse rather than
// upload the payload to a vendor. This mirrors aicommit.Preflight's check
// (which previously guarded commit alone) so the policy is consistent
// whether the user runs commit, pr, review, changelog, ask, explain, do,
// status --ai, or merge --ai.
//
// allow_remote lives under ai.commit for historical reasons but is treated
// as the repo-wide remote policy. A nil provider passes (nothing to send).
func ensureRemoteAllowed(prov provider.Provider, cfg config.AIConfig) error {
	if prov == nil {
		return nil
	}
	if prov.Locality() == provider.LocalityRemote && !cfg.Commit.AllowRemote {
		return fmt.Errorf("provider %q is remote; set ai.commit.allow_remote=true to opt in", prov.Name())
	}
	return nil
}

// ── Privacy Gate helper ──────────────────────────────────────────────

// applyPrivacyGate redacts the payload when the provider is remote.
// Returns the (possibly redacted) payload and any findings. If the
// provider is local, the payload is returned unchanged.
//
// MaxSecrets falls back to ai.commit.privacy.max_secrets (default 10).
// A negative value disables the threshold so callers can audit findings
// without aborting. When cmd has --skip-privacy set, the abort threshold
// is disabled but redaction is still applied so the LLM never sees raw
// secrets.
func applyPrivacyGate(cmd *cobra.Command, prov provider.Provider, payload string, cfg config.AIConfig) (string, []aicommit.RedactFinding, error) {
	if prov.Locality() != provider.LocalityRemote {
		return payload, nil, nil
	}
	max := cfg.Commit.Privacy.MaxSecrets
	if max == 0 {
		max = 10
	}
	if cmd != nil {
		if skip, _ := cmd.Flags().GetBool("skip-privacy"); skip {
			max = -1
		}
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
		" raise ai.commit.privacy.max_secrets in .gk.yaml,"+
		" or pass --skip-privacy to bypass the threshold (redaction still applies)")
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
		order = aiAutoOrder
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

// ── AI response cache (shared) ───────────────────────────────────────
//
// A small content-addressed cache under .git/gk-ai-cache/<kind>/<key>.
// Callers derive a key from the deterministic inputs (diff, range, lang,
// provider) so an unchanged input reuses the previous answer and a changed
// input misses naturally. Living inside .git keeps it out of the work tree
// and untracked. All failures are silent — caching is an optimization, not
// a correctness guarantee.

// aiCacheKey derives a stable 16-hex-char key from the given parts.
func aiCacheKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// aiCacheDir resolves .git/gk-ai-cache/<kind>, creating nothing. ok=false
// when the git dir cannot be located (not a repo).
func aiCacheDir(ctx context.Context, runner git.Runner, kind string) (string, bool) {
	if runner == nil {
		return "", false
	}
	out, _, err := runner.Run(ctx, "rev-parse", "--git-path", "gk-ai-cache")
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		base := runnerDir(runner)
		if base == "" {
			base = RepoFlag()
		}
		p = filepath.Join(base, p)
	}
	return filepath.Join(p, kind), true
}

func readAICache(ctx context.Context, runner git.Runner, kind, key string) (string, bool) {
	dir, ok := aiCacheDir(ctx, runner, kind)
	if !ok {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(dir, key))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func writeAICache(ctx context.Context, runner git.Runner, kind, key, text string) {
	dir, ok := aiCacheDir(ctx, runner, kind)
	if !ok {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp := filepath.Join(dir, key+".tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(dir, key))
}

// emitAIAdvice renders a free-text AI answer as a titled section, appending
// a caution when the model mentioned a hard-to-undo command. Shared by the
// terminal-read AI surfaces (status, ask, explain) so they get consistent
// chrome and the same post-hoc safety guard. Paste-oriented outputs (pr,
// changelog) deliberately stay raw — section chrome would pollute content
// the user copies elsewhere.
func emitAIAdvice(out io.Writer, title, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	lines := strings.Split(text, "\n")
	if danger := flagDangerousMentions(text); len(danger) > 0 {
		lines = append(lines, "",
			"⚠ mentions hard-to-undo commands: "+strings.Join(danger, ", ")+
				" — verify before running.")
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, ui.RenderSection(title, "", lines, ui.SectionOpts{
		Layout: ui.SectionLayoutBar,
		Color:  ui.SectionInfo,
	}))
}

// writeAIJSON marshals v as indented JSON to w. Backs the `--format json`
// outputs so they emit real structured data instead of raw provider prose.
func writeAIJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// aiChatMaxTokens is the advisory response cap for the chat-style AI
// commands (pr, review, changelog, ask, explain, do). Reads
// ai.chat.max_tokens, falling back to 4096.
func aiChatMaxTokens(ai config.AIConfig) int {
	if ai.Chat.MaxTokens > 0 {
		return ai.Chat.MaxTokens
	}
	return 4096
}

// aiCallContext bounds a single AI call so a command never hangs on a slow
// provider (including a long fallback chain). Derives the deadline from
// ai.chat.timeout (default 30s). The caller must defer the returned cancel.
func aiCallContext(ctx context.Context, ai config.AIConfig) (context.Context, context.CancelFunc) {
	d := parseDurationOrDefault(ai.Chat.Timeout, 30*time.Second)
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}
