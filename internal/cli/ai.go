package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/ui"
)

// vendorSecretPatterns adapts the push scanner's high-signal vendor
// patterns (ghp_/github_pat_, xox, sk-, AWS 40-char secret, PEM, generic
// key=value) for the AI privacy gate. aicommit's own builtins cover only
// five generic keyword shapes — a bare GitHub token on a line without
// "token=" sailed through every AI surface until this wiring (gk chat
// cross-vendor research finding; it applies to ask/do/status/log equally,
// which is why it lives here rather than in the chat path).
var vendorSecretPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(secrets.BuiltinPatterns))
	for _, p := range secrets.BuiltinPatterns {
		out = append(out, p.Regex)
	}
	return out
}()

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

// resolveResponseLang picks the language for *conversational* AI output —
// do / ask / explain / status --ai. These render user-facing prose, so they
// follow output.lang rather than ai.lang. ai.lang governs git *artifacts*
// (commit messages, pr/changelog) and is conventionally left at "en" even by
// users who want a Korean CLI; treating that "en" as the unopinionated default
// (rather than an explicit override) is what lets output.lang=ko still yield
// Korean answers. Precedence:
//
//  1. override        — the --lang flag (always wins)
//  2. aiLang != "en"  — a deliberately chosen non-English AI language
//  3. outputLang      — the CLI's language (the common case)
//  4. aiLang          — only reaches here as "en" when outputLang is unset
//  5. "en"            — final fallback
//
// This mirrors the long-standing statusAssistLang behaviour so every
// conversational command resolves language identically.
func resolveResponseLang(override, aiLang, outputLang string) string {
	if override != "" {
		return override
	}
	if aiLang != "" && aiLang != "en" {
		return aiLang
	}
	if outputLang != "" {
		return outputLang
	}
	if aiLang != "" {
		return aiLang
	}
	return "en"
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
		DenyPaths:      cfg.Commit.DenyPaths,
		SecretPatterns: vendorSecretPatterns,
		MaxSecrets:     max,
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
			fmt.Fprintf(w, "    %s  %s:%d  pattern=%s  sample=%s%s\n",
				r.f.Placeholder, fp, r.f.FileLine, displayPattern(r.f.Pattern), r.f.Original, untalliedNote(r.f))
		}
	}
	if len(noFile) > 0 {
		fmt.Fprintln(w, "  (no source file)")
		for _, r := range noFile {
			fmt.Fprintf(w, "    %s  payload-line=%d  pattern=%s  sample=%s%s\n",
				r.f.Placeholder, r.f.Line, displayPattern(r.f.Pattern), r.f.Original, untalliedNote(r.f))
		}
	}
	// Say which findings actually spent threshold budget. Without this the
	// report reads as "N secrets" while the error names a smaller number, and
	// the user cannot tell which lines they are being asked to look at.
	if counted, skipped := tallySplit(findings); skipped > 0 {
		fmt.Fprintf(w, "  counted %d of %d against the threshold (%d not counted: example/placeholder values and repeats of a value already counted; all findings above are still redacted)\n",
			counted, counted+skipped, skipped)
	}
	fmt.Fprintln(w, stylizeHintLine("hint: review the counted lines first — narrow with --staged-only,"+
		" or pass --skip-privacy for this run (redaction still applies)."+
		" Raising ai.commit.privacy.max_secrets weakens the gate for every future commit"))
}

// untalliedNote marks a finding the threshold ignored, naming why.
func untalliedNote(f aicommit.RedactFinding) string {
	if !f.Untallied {
		return ""
	}
	if f.Reason == "" {
		return "  (not counted)"
	}
	return "  (not counted: " + f.Reason + ")"
}

// tallySplit reports how many secret findings spent threshold budget versus
// how many were exempted. Path findings never count toward the secret
// threshold, so they are excluded from both totals.
func tallySplit(findings []aicommit.RedactFinding) (counted, skipped int) {
	for _, f := range findings {
		if f.Kind != "secret" {
			continue
		}
		if f.Untallied {
			skipped++
			continue
		}
		counted++
	}
	return counted, skipped
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
//
// attr is the attribution footer from aiAttribution ("" to omit it). The
// answer outlives the spinner that named the provider, and a cached answer
// never had one, so without this the reader cannot tell which model wrote
// what they are reading — or that nothing was called at all.
func emitAIAdvice(out io.Writer, title, text, attr string) {
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
	if attr != "" {
		faint := color.New(color.Faint).SprintFunc()
		lines = append(lines, "", faint("— "+attr))
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, ui.RenderSection(title, "", lines, ui.SectionOpts{
		Layout: ui.SectionLayoutBar,
		Color:  ui.SectionInfo,
	}))
}

// aiAttribution renders the credit footer for an AI answer:
// "openai (gpt-4o-mini)", with " · cached" when the text was replayed from
// the on-disk cache rather than generated now. Returns "" for an unknown
// provider so callers can pass it through unconditionally.
func aiAttribution(providerName, model string, cached bool) string {
	if providerName == "" {
		return ""
	}
	s := providerName
	if model != "" && model != "n/a" {
		s += " (" + model + ")"
	}
	if cached {
		s += " · cached"
	}
	return s
}

// providerAttribution builds the footer from a live provider, preferring the
// model the RESULT reports over the one the adapter planned to call: after a
// fallback failover those differ, and the answer must credit whoever actually
// produced it. Pass resultModel="" when the call reported none.
func providerAttribution(p provider.Provider, resultModel string, cached bool) string {
	if p == nil {
		return ""
	}
	model := resultModel
	if model == "" {
		model = providerModel(p)
	}
	return aiAttribution(p.Name(), model, cached)
}

// writeAIJSON marshals v as indented JSON to w. Backs the `--format json`
// outputs so they emit real structured data instead of raw provider prose.
func writeAIJSON(w io.Writer, v any) error {
	return emitAgentResult(w, v)
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

// aiChatHistoryBudget is the approximate token budget `gk chat` trims
// replayed history to (chat.Engine.HistoryBudget). Reads
// ai.chat.history_budget, falling back to 32768 — same
// zero/negative-falls-back-to-default convention as aiChatMaxTokens.
func aiChatHistoryBudget(ai config.AIConfig) int {
	if ai.Chat.HistoryBudget > 0 {
		return ai.Chat.HistoryBudget
	}
	return 32768
}

// aiCallTimeout is the ai.chat.timeout bound as a duration (default 30s).
// Callers hand it to runAIQuery, which derives the bounded context itself
// (see ai_query.go) — that central application replaced the per-call-site
// context helper this used to sit next to.
func aiCallTimeout(ai config.AIConfig) time.Duration {
	return parseDurationOrDefault(ai.Chat.Timeout, 30*time.Second)
}
