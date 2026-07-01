package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/ui"
)

// `gk config setup` is the friendly front door: a short wizard over the handful
// of settings new users actually change (provider, commit model, language, easy
// mode), then one confirmation before writing. Every field can also be supplied
// as a flag, which both powers scripts/CI and lets non-TTY runs work without
// prompting.
func init() {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactive wizard for the most common settings",
		Long: `Walks through provider, commit model, output language, easy mode, and the
log/status display layers (picked from a live preview), then writes them to
the global config (or repo-local .gk.yaml with --local).

An AI connection already present in the config (provider/endpoint/API key)
is shown and kept by default — re-running the wizard never overwrites it
unless you answer "no" to keeping it or pass a provider flag explicitly.

Each answer can be pre-supplied as a flag — handy for scripts and
non-interactive shells, where the wizard applies only the flags given:

  gk config setup --provider anthropic --lang en --yes
  gk config setup --status-vis gauge,tree,local --log-vis cc,safety --yes`,
		Args: cobra.NoArgs,
		RunE: runConfigSetup,
	}
	cmd.Flags().String("provider", "", "AI provider (kiro-api, anthropic, openai, groq, ...)")
	cmd.Flags().String("endpoint", "", "API endpoint URL (custom provider only)")
	cmd.Flags().String("provider-model", "", "model ID for a custom provider")
	cmd.Flags().String("api-key", "", "API key for a custom provider (stored in config)")
	cmd.Flags().String("commit-model", "", "model used only for `gk commit`")
	cmd.Flags().String("commit-lang", "", "language for `gk commit` messages only")
	cmd.Flags().String("lang", "", "output language (ko, en)")
	cmd.Flags().Bool("easy", false, "plain-language output for non-developers")
	cmd.Flags().StringSlice("log-vis", nil, "default `gk log` layers (comma-list: cc,safety,tags-rule,impact,pulse,calendar,hotspots,trailers,lanes)")
	cmd.Flags().Bool("log-graph", false, "draw the topology graph in `gk log` by default")
	cmd.Flags().StringSlice("status-vis", nil, "default `gk status` layers (comma-list: gauge,bar,progress,base,tree,types,staleness,local,since-push,conflict,churn,risk,stash,heatmap)")
	cmd.Flags().String("xy-style", "", "per-entry state column in `gk status`: labels, glyphs, or raw")
	cmd.Flags().Bool("yes", false, "skip the final confirmation")
	cmd.Flags().Bool("local", false, "write to repo-local .gk.yaml instead of the global config")

	// Register under `config`.
	if parent, _, err := rootCmd.Find([]string{"config"}); err == nil && parent != nil && parent.Use == "config" {
		parent.AddCommand(cmd)
	}
}

func runConfigSetup(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	cur, _ := config.Load(cmd.Flags()) // current values seed the prompts
	if cur == nil {
		d := config.Defaults()
		cur = &d
	}

	changes := map[string]string{}
	lists := map[string][]string{} // list-valued keys (log.vis / status.vis)

	// 1. provider. A config that already names a provider is a working API
	// setup the user invested in (endpoint, pasted key) — re-running the
	// wizard must never overwrite it by default. Show what's there and gate
	// the provider questions behind an explicit "no, change it". Provider
	// flags state that intent directly and bypass the gate; non-TTY runs
	// without flags always keep.
	provider := cur.AI.Provider
	keepAPI := false
	if summary := existingAPISummary(cur); len(summary) > 0 && !apiFlagsGiven(cmd) {
		keepAPI = true
		if promptAllowed() {
			fmt.Fprintln(out, "현재 AI 설정:")
			for _, line := range summary {
				fmt.Fprintln(out, "  "+line)
			}
			keep, cerr := ui.ConfirmTUI(ctx, "기존 AI 설정을 유지할까요?",
				"아니오를 선택하면 provider / endpoint / API 키를 다시 묻습니다", true)
			if cerr != nil {
				if errors.Is(cerr, ui.ErrPickerAborted) {
					return errWizardAborted
				}
			} else {
				keepAPI = keep
			}
		}
	}

	if !keepAPI {
		if v, ok, err := wizardValue(cmd, ctx, "provider",
			"AI provider", "kiro-api / anthropic / openai / groq", cur.AI.Provider); err != nil {
			return err
		} else if ok && v != "" {
			changes["ai.provider"] = v
			provider = v
		}

		// 1b. Custom provider (a name outside the built-in set, e.g. kiro-api)
		// needs an endpoint and key. Built-in providers (anthropic/openai/...)
		// skip this — they have known endpoints and read their key from a
		// fixed env var.
		if provider != "" && !isBuiltinProvider(provider) {
			if err := wizardCustomProvider(cmd, ctx, provider, cur, changes); err != nil {
				return err
			}
		}
	}

	// 2. commit-only model. kiro-api pairs with a fast haiku model by default,
	// but we show it as an editable prompt (seeded with that default) rather
	// than setting it silently — the user can accept or change it. Other
	// providers keep the optional yes/no gate.
	if provider == "kiro-api" {
		initial := firstNonEmpty(cur.AI.Commit.Model, "kiro/claude-haiku-4.5")
		if v, ok, err := wizardValue(cmd, ctx, "commit-model",
			"commit 전용 모델", "예: kiro/claude-haiku-4.5", initial); err != nil {
			return err
		} else if ok && v != "" {
			changes["ai.commit.model"] = v
		} else if !ok {
			changes["ai.commit.model"] = initial // non-interactive: apply the default
		}
	} else if v, ok, err := wizardOptional(cmd, ctx, "commit-model",
		"commit 전용 빠른 모델을 지정할까요?",
		"미지정 시 provider 기본 모델을 사용합니다",
		"commit 모델", "예: kiro/claude-haiku-4.5", cur.AI.Commit.Model); err != nil {
		return err
	} else if ok && v != "" {
		changes["ai.commit.model"] = v
	}

	// 3. commit-only language (optional — empty follows ai.lang/output.lang)
	if v, ok, err := wizardOptional(cmd, ctx, "commit-lang",
		"commit 메시지 언어를 따로 지정할까요?",
		"미지정 시 전체 출력 언어(아래 항목)를 따릅니다",
		"commit 언어", "예: en", cur.AI.Commit.Lang); err != nil {
		return err
	} else if ok && v != "" {
		changes["ai.commit.lang"] = v
	}

	// 4. output language
	if v, ok, err := wizardValue(cmd, ctx, "lang",
		"출력 언어", "ko / en", firstNonEmpty(cur.Output.Lang, "ko")); err != nil {
		return err
	} else if ok && v != "" {
		changes["output.lang"] = v
	}

	// 5. easy mode (bool)
	if v, ok, err := wizardBool(cmd, ctx, "easy",
		"쉬운 모드(비개발자용 쉬운 말)를 켤까요?", cur.Output.Easy); err != nil {
		return err
	} else if ok {
		changes["output.easy"] = strconv.FormatBool(v)
	}

	// 6. log/status display layers — picked from a live preview.
	if err := wizardDisplay(cmd, ctx, cur, changes, lists); err != nil {
		return err
	}

	// An answer that matches what the config already says is not a change —
	// drop it so accepting every prompt as-is leaves the file untouched.
	pruneNoopChanges(cur, changes, lists)

	if len(changes) == 0 && len(lists) == 0 {
		fmt.Fprintln(out, "변경할 항목이 없습니다.")
		return nil
	}

	// Resolve the target file and show a summary before writing.
	local, _ := cmd.Flags().GetBool("local")
	path, scope, created, err := configWritePath(cmd, local, true)
	if err != nil {
		return err
	}

	// One display map for the summary: scalars as-is, lists as "[a, b]".
	display := make(map[string]string, len(changes)+len(lists))
	for k, v := range changes {
		if strings.HasSuffix(k, ".api_key") {
			v = maskSecret(v)
		}
		display[k] = v
	}
	for k, v := range lists {
		display[k] = "[" + strings.Join(v, ", ") + "]"
	}

	fmt.Fprintf(out, "\n다음 설정을 %s(%s)에 저장합니다:\n", scope, path)
	for _, k := range sortedKeys(display) {
		fmt.Fprintf(out, "  %s = %s\n", k, display[k])
	}

	if yes, _ := cmd.Flags().GetBool("yes"); !yes && promptAllowed() {
		ok, cerr := ui.ConfirmTUI(ctx, "저장할까요?", "", true)
		if cerr != nil || !ok {
			fmt.Fprintln(out, "취소했습니다.")
			return nil
		}
	}

	for _, k := range sortedKeys(changes) {
		if _, serr := config.SetValue(path, k, changes[k]); serr != nil {
			return fmt.Errorf("gk config setup: %s: %w", k, serr)
		}
	}
	for _, k := range sortedKeys(lists) {
		if _, serr := config.SetList(path, k, lists[k]); serr != nil {
			return fmt.Errorf("gk config setup: %s: %w", k, serr)
		}
	}
	if created && local {
		if e := prependLocalHeader(path); e != nil {
			return e
		}
	}

	fmt.Fprintln(out, successLine("saved", path))

	// An HTTP provider needs an API key in the environment. If the user just
	// set one whose key is missing, point at the exact env var now — so they
	// don't have to run `gk config doctor` to discover the gap.
	if p, ok := changes["ai.provider"]; ok {
		if env, needsKey := providerKeyEnv[p]; needsKey && os.Getenv(env) == "" {
			fmt.Fprintf(out, "→ %s 는 API 키가 필요합니다. 환경변수를 설정하세요: export %s=...\n", p, env)
		}
	}
	return nil
}

// wizardValue returns a required value: the flag when set, else a TTY prompt
// seeded with initial, else (non-TTY, no flag) ok=false to skip the field.
func wizardValue(cmd *cobra.Command, ctx context.Context, flag, title, placeholder, initial string) (string, bool, error) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetString(flag)
		return v, true, nil
	}
	if !promptAllowed() {
		return "", false, nil
	}
	v, err := ui.PromptTextTUI(ctx, title, placeholder, initial)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return "", false, errWizardAborted
		}
		return "", false, nil // non-interactive or other → skip
	}
	return v, true, nil
}

// wizardOptional gates a text prompt behind a yes/no question, so optional
// fields (like the commit model) aren't forced. Flag set → use it directly.
func wizardOptional(cmd *cobra.Command, ctx context.Context, flag, gateTitle, gateDesc, title, placeholder, initial string) (string, bool, error) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetString(flag)
		return v, true, nil
	}
	if !promptAllowed() {
		return "", false, nil
	}
	want, err := ui.ConfirmTUI(ctx, gateTitle, gateDesc, initial != "")
	if err != nil || !want {
		return "", false, nil
	}
	v, err := ui.PromptTextTUI(ctx, title, placeholder, initial)
	if err != nil {
		return "", false, nil
	}
	return v, true, nil
}

// wizardBool returns a bool: the flag when set, else a TTY confirm, else skip.
func wizardBool(cmd *cobra.Command, ctx context.Context, flag, title string, initial bool) (bool, bool, error) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetBool(flag)
		return v, true, nil
	}
	if !promptAllowed() {
		return false, false, nil
	}
	v, err := ui.ConfirmTUI(ctx, title, "", initial)
	if err != nil {
		return false, false, nil
	}
	return v, true, nil
}

// wizardCustomProvider collects the endpoint / model / api_key a custom
// OpenAI-compatible gateway needs, writing them under ai.providers.<name>.*.
// The wire format defaults to openai; the endpoint is asked without a baked-in
// default (the gateway URL is the user's, not ours); the API key is pasted in
// and stored in config (its summary line is masked).
func wizardCustomProvider(cmd *cobra.Command, ctx context.Context, provider string, cur *config.Config, changes map[string]string) error {
	custom, _ := cur.AI.CustomProvider(provider)
	base := "ai.providers." + provider

	// Wire format: default to openai so the OpenAI adapter handles the gateway.
	if custom.Format == "" {
		changes[base+".format"] = "openai"
	}

	// Endpoint — required, no default URL offered.
	if v, ok, err := wizardValue(cmd, ctx, "endpoint",
		provider+" API endpoint URL", "https://<gateway>/v1/chat/completions", custom.Endpoint); err != nil {
		return err
	} else if ok && v != "" {
		changes[base+".endpoint"] = v
	}

	// Model — shown as an editable prompt seeded with a sensible default so the
	// user sees and can change it. kiro-api defaults to the fast haiku model;
	// other custom providers start blank (we can't guess their model id).
	modelInitial := custom.Model
	if modelInitial == "" && provider == "kiro-api" {
		modelInitial = "kiro/claude-haiku-4.5"
	}
	if v, ok, err := wizardValue(cmd, ctx, "provider-model",
		provider+" 모델 ID", "예: kiro/claude-haiku-4.5", modelInitial); err != nil {
		return err
	} else if ok && v != "" {
		changes[base+".model"] = v
	} else if !ok && modelInitial != "" {
		changes[base+".model"] = modelInitial // non-interactive: apply the default
	}

	// API key — pasted in, stored in config (env var is the alternative).
	if v, ok, err := wizardOptional(cmd, ctx, "api-key",
		"API 키를 지금 붙여넣을까요?", "환경변수 대신 config 파일에 저장됩니다",
		provider+" API 키", "sk-...", ""); err != nil {
		return err
	} else if ok && v != "" {
		changes[base+".api_key"] = v
	}
	return nil
}

// isBuiltinProvider reports whether name is one of gk's built-in adapters,
// which have known endpoints and read their key from a fixed env var.
func isBuiltinProvider(name string) bool {
	switch name {
	case "anthropic", "claude", "openai", "nvidia", "groq", "gemini", "qwen", "kiro", "kiro-cli":
		return true
	}
	return false
}

// maskSecret hides all but the first few characters of a secret for display.
func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + strings.Repeat("*", 6)
}

var errWizardAborted = errors.New("gk config setup: 취소되었습니다")

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// existingAPISummary describes the AI connection already present in the
// loaded config — the lines shown above the "keep it?" gate. Empty when no
// provider is configured (nothing to preserve). The API key is masked.
func existingAPISummary(cur *config.Config) []string {
	p := cur.AI.Provider
	if p == "" {
		return nil
	}
	lines := []string{"provider: " + p}
	if custom, ok := cur.AI.CustomProvider(p); ok {
		if custom.Endpoint != "" {
			lines = append(lines, "endpoint: "+custom.Endpoint)
		}
		if custom.Model != "" {
			lines = append(lines, "model:    "+custom.Model)
		}
		if custom.APIKey != "" {
			lines = append(lines, "api_key:  "+maskSecret(custom.APIKey)+" (설정됨)")
		}
	}
	return lines
}

// apiFlagsGiven reports whether any provider-connection flag was passed —
// an explicit ask to change the API settings, which bypasses the keep gate.
func apiFlagsGiven(cmd *cobra.Command) bool {
	for _, f := range []string{"provider", "endpoint", "provider-model", "api-key"} {
		if cmd.Flags().Changed(f) {
			return true
		}
	}
	return false
}

// pruneNoopChanges drops every pending change whose value matches what the
// loaded config already resolves to. Accepting each prompt as-is must leave
// the file byte-for-byte untouched — that, plus the keep gate, is the
// "re-running setup never rewrites your config" guarantee.
func pruneNoopChanges(cur *config.Config, changes map[string]string, lists map[string][]string) {
	for k, v := range changes {
		if curV, known := setupCurrentValue(cur, k); known && curV == v {
			delete(changes, k)
		}
	}
	for k, v := range lists {
		var curList []string
		switch k {
		case "log.vis":
			curList = cur.Log.Vis
		case "status.vis":
			curList = cur.Status.Vis
		default:
			continue
		}
		if sameStringSet(curList, v) {
			delete(lists, k)
		}
	}
}

// setupCurrentValue resolves the current effective value of a wizard-managed
// scalar key. known=false for keys the wizard doesn't track (those are kept).
func setupCurrentValue(cur *config.Config, key string) (string, bool) {
	switch key {
	case "ai.provider":
		return cur.AI.Provider, true
	case "ai.commit.model":
		return cur.AI.Commit.Model, true
	case "ai.commit.lang":
		return cur.AI.Commit.Lang, true
	case "output.lang":
		return cur.Output.Lang, true
	case "output.easy":
		return strconv.FormatBool(cur.Output.Easy), true
	case "log.graph":
		return strconv.FormatBool(cur.Log.Graph), true
	case "status.xy_style":
		return cur.Status.XYStyle, true
	}
	// ai.providers.<name>.<field> — compare against the named entry.
	if rest, ok := strings.CutPrefix(key, "ai.providers."); ok {
		name, field, ok := strings.Cut(rest, ".")
		if !ok {
			return "", false
		}
		custom, _ := cur.AI.CustomProvider(name)
		switch field {
		case "format":
			return custom.Format, true
		case "endpoint":
			return custom.Endpoint, true
		case "model":
			return custom.Model, true
		case "api_key":
			return custom.APIKey, true
		}
	}
	return "", false
}

// sameStringSet reports whether a and b hold the same elements, order-blind —
// reordering layers the user didn't change is not a change worth writing.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		if !set[x] {
			return false
		}
	}
	return true
}
