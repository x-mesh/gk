package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

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
		Long: `Walks through provider, commit model, output language, and easy mode,
then writes them to the global config (or repo-local .gk.yaml with --local).

Each answer can be pre-supplied as a flag — handy for scripts and
non-interactive shells, where the wizard applies only the flags given:

  gk config setup --provider anthropic --lang en --yes`,
		Args: cobra.NoArgs,
		RunE: runConfigSetup,
	}
	cmd.Flags().String("provider", "", "AI provider (kiro-api, anthropic, openai, groq, ...)")
	cmd.Flags().String("commit-model", "", "model used only for `gk commit`")
	cmd.Flags().String("commit-lang", "", "language for `gk commit` messages only")
	cmd.Flags().String("lang", "", "output language (ko, en)")
	cmd.Flags().Bool("easy", false, "plain-language output for non-developers")
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

	// 1. provider
	if v, ok, err := wizardValue(cmd, ctx, "provider",
		"AI provider", "kiro-api / anthropic / openai / groq", cur.AI.Provider); err != nil {
		return err
	} else if ok && v != "" {
		changes["ai.provider"] = v
	}

	// 2. commit-only model (optional — empty keeps the provider default)
	if v, ok, err := wizardOptional(cmd, ctx, "commit-model",
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

	if len(changes) == 0 {
		fmt.Fprintln(out, "변경할 항목이 없습니다.")
		return nil
	}

	// Resolve the target file and show a summary before writing.
	local, _ := cmd.Flags().GetBool("local")
	path, scope, created, err := configWritePath(cmd, local, true)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\n다음 설정을 %s(%s)에 저장합니다:\n", scope, path)
	for _, k := range sortedKeys(changes) {
		fmt.Fprintf(out, "  %s = %s\n", k, changes[k])
	}

	if yes, _ := cmd.Flags().GetBool("yes"); !yes && ui.IsTerminal() {
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
	if !ui.IsTerminal() {
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
	if !ui.IsTerminal() {
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
	if !ui.IsTerminal() {
		return false, false, nil
	}
	v, err := ui.ConfirmTUI(ctx, title, "", initial)
	if err != nil {
		return false, false, nil
	}
	return v, true, nil
}

var errWizardAborted = errors.New("gk config setup: 취소되었습니다")

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
