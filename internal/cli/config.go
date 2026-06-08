package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func init() {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Read or set gk configuration",
	}
	configCmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print resolved configuration as YAML",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(nil)
			if err != nil {
				return err
			}
			out, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			fmt.Print(string(out))
			return nil
		},
	})
	getCmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Print a single dot-notation config value",
		Args:  cobra.ExactArgs(1),
		RunE:  runConfigGet,
	}
	getCmd.Flags().Bool("source", false, "also print where the value comes from (local/global/default)")
	configCmd.AddCommand(getCmd)
	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value (comments preserved)",
		Long: `Writes one dot-notation key into the global config (or repo-local
.gk.yaml with --local), leaving every other line — comments, ordering,
blank lines — untouched. The target file is created from the documented
template if it does not exist yet.

Examples:
  gk config set ai.commit.model kiro/claude-haiku-4.5
  gk config set --local status.density compact
  gk config set ai.commit.audit true`,
		Args: cobra.ExactArgs(2),
		RunE: runConfigSet,
	}
	setCmd.Flags().Bool("local", false, "write to repo-local .gk.yaml instead of the global config")
	configCmd.AddCommand(setCmd)

	unsetCmd := &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a config value, reverting it to the built-in default",
		Args:  cobra.ExactArgs(1),
		RunE:  runConfigUnset,
	}
	unsetCmd.Flags().Bool("local", false, "operate on repo-local .gk.yaml instead of the global config")
	configCmd.AddCommand(unsetCmd)

	pathCmd := &cobra.Command{
		Use:   "path",
		Short: "Show which config files apply, and whether they exist",
		Args:  cobra.NoArgs,
		RunE:  runConfigPath,
	}
	configCmd.AddCommand(pathCmd)

	editCmd := &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in $EDITOR (creates it if missing)",
		Args:  cobra.NoArgs,
		RunE:  runConfigEdit,
	}
	editCmd.Flags().Bool("local", false, "edit repo-local .gk.yaml instead of the global config")
	configCmd.AddCommand(editCmd)

	rootCmd.AddCommand(configCmd)
}

// runConfigGet handles `gk config get <key> [--source]`.
func runConfigGet(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return err
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return err
	}
	val, ok := lookupDot(m, args[0])
	if !ok {
		return fmt.Errorf("key %q not found", args[0])
	}

	if showSrc, _ := cmd.Flags().GetBool("source"); showSrc {
		root := ""
		if r, terr := gitToplevel(cmd.Context(), &git.ExecRunner{Dir: RepoFlag()}); terr == nil {
			root = r
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%v  (%s)\n", val, config.ValueSource(args[0], root))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), val)
	return nil
}

// runConfigPath handles `gk config path` — lists the global and repo-local
// files in precedence order (later wins) with their existence state.
func runConfigPath(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "설정 파일 (아래일수록 우선):")

	global := config.GlobalConfigPath()
	fmt.Fprintf(out, "  global  %s  %s\n", global, existsTag(global))

	root := ""
	if r, terr := gitToplevel(cmd.Context(), &git.ExecRunner{Dir: RepoFlag()}); terr == nil {
		root = r
	}
	if root == "" {
		fmt.Fprintln(out, "  local   (git 저장소가 아니라 .gk.yaml 미적용)")
		return nil
	}
	local := config.LocalConfigPath(root)
	fmt.Fprintf(out, "  local   %s  %s\n", local, existsTag(local))
	return nil
}

func existsTag(path string) string {
	if path == "" {
		return "(경로 불명)"
	}
	if _, err := os.Stat(path); err == nil {
		return "(있음)"
	}
	return "(없음)"
}

// runConfigEdit handles `gk config edit [--local]`.
func runConfigEdit(cmd *cobra.Command, _ []string) error {
	local, _ := cmd.Flags().GetBool("local")
	path, _, _, err := configWritePath(cmd, local, false)
	if err != nil {
		return err
	}
	// Ensure the file exists so the editor opens something meaningful: the
	// documented template globally, the self-describing header locally.
	if _, e := os.Stat(path); errors.Is(e, os.ErrNotExist) {
		if local {
			if e2 := os.WriteFile(path, []byte(config.LocalConfigHeader), 0o644); e2 != nil {
				return fmt.Errorf("gk config edit: create %s: %w", path, e2)
			}
		} else if e2 := config.WriteDefaultConfig(path, false); e2 != nil && !errors.Is(e2, config.ErrConfigExists) {
			return e2
		}
	}

	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		for _, cand := range []string{"vim", "vi", "nano"} {
			if _, lerr := exec.LookPath(cand); lerr == nil {
				editor = cand
				break
			}
		}
	}
	if editor == "" {
		return WithHint(
			fmt.Errorf("gk config edit: 편집기를 찾을 수 없습니다"),
			"$EDITOR 환경변수를 설정하거나 직접 파일을 여세요: "+path,
		)
	}

	ed := exec.CommandContext(cmd.Context(), editor, path)
	ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := ed.Run(); err != nil {
		return fmt.Errorf("gk config edit: %s 종료 오류: %w", editor, err)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// runConfigSet handles `gk config set <key> <value> [--local]`.
func runConfigSet(cmd *cobra.Command, args []string) error {
	key, val := args[0], args[1]
	local, _ := cmd.Flags().GetBool("local")

	path, scope, created, err := configWritePath(cmd, local, true)
	if err != nil {
		return err
	}

	written, err := config.SetValue(path, key, val)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrUnknownKey):
			return WithHint(
				fmt.Errorf("gk config set: 알 수 없는 키 %q", key),
				"gk config show 로 설정 가능한 키 목록을 확인하세요",
			)
		case errors.Is(err, config.ErrNotScalar):
			return WithHint(
				fmt.Errorf("gk config set: %q는 목록/맵이라 단일 값을 쓸 수 없습니다", key),
				"gk config edit 로 해당 항목을 직접 편집하세요",
			)
		default:
			return err
		}
	}

	// A freshly created repo-local .gk.yaml gets a self-describing header;
	// SetValue builds the file from an empty root, so we prepend it after.
	if created && local {
		if e := prependLocalHeader(path); e != nil {
			return e
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), successLinef("set", "%s = %s  (%s: %s)", key, written, scope, path))
	return nil
}

// prependLocalHeader inserts the repo-local header comment at the top of path
// unless it is already present. Idempotent.
func prependLocalHeader(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("gk config: read %s: %w", path, err)
	}
	if strings.HasPrefix(string(data), config.LocalConfigHeader) {
		return nil
	}
	if err := os.WriteFile(path, append([]byte(config.LocalConfigHeader), data...), 0o644); err != nil {
		return fmt.Errorf("gk config: write %s: %w", path, err)
	}
	return nil
}

// runConfigUnset handles `gk config unset <key> [--local]`.
func runConfigUnset(cmd *cobra.Command, args []string) error {
	key := args[0]
	local, _ := cmd.Flags().GetBool("local")

	path, scope, _, err := configWritePath(cmd, local, false)
	if err != nil {
		return err
	}

	existed, err := config.UnsetValue(path, key)
	if err != nil {
		return err
	}
	if !existed {
		fmt.Fprintf(cmd.OutOrStdout(), "변경 없음: %s 는 %s 설정에 없습니다 (이미 기본값)\n", key, scope)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), successLinef("unset", "%s  (%s: %s)", key, scope, path))
	return nil
}

// configWritePath resolves which file `set`/`unset` should mutate and, when
// ensure is true, creates it if missing — the documented template for the
// global file, a minimal self-describing header for repo-local .gk.yaml.
func configWritePath(cmd *cobra.Command, local, ensure bool) (path, scope string, created bool, err error) {
	if local {
		runner := &git.ExecRunner{Dir: RepoFlag()}
		root, terr := gitToplevel(cmd.Context(), runner)
		if terr != nil || root == "" {
			return "", "", false, WithHint(
				fmt.Errorf("gk config --local: git 저장소가 아닙니다"),
				"저장소 안에서 실행하거나, --local 없이 글로벌 설정을 변경하세요",
			)
		}
		path = config.LocalConfigPath(root)
		if ensure {
			if _, e := os.Stat(path); errors.Is(e, os.ErrNotExist) {
				// SetValue creates the file from an empty root; the caller
				// prepends the header afterwards.
				created = true
			}
		}
		return path, "local", created, nil
	}

	path = config.GlobalConfigPath()
	if path == "" {
		return "", "", false, fmt.Errorf("gk config: 글로벌 설정 경로를 결정할 수 없습니다 (HOME 미설정?)")
	}
	if ensure {
		if _, e := os.Stat(path); errors.Is(e, os.ErrNotExist) {
			if e2 := config.WriteDefaultConfig(path, false); e2 != nil && !errors.Is(e2, config.ErrConfigExists) {
				return "", "", false, e2
			}
			created = true
		}
	}
	return path, "global", created, nil
}

func lookupDot(m map[string]any, key string) (any, bool) {
	cur := any(m)
	start := 0
	for i := 0; i <= len(key); i++ {
		if i == len(key) || key[i] == '.' {
			seg := key[start:i]
			sub, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			cur, ok = sub[seg]
			if !ok {
				return nil, false
			}
			start = i + 1
		}
	}
	return cur, true
}
