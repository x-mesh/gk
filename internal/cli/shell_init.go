package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// envCdFile names the file the gk shell function from gk shell-init passes
// on each call. A command that would open a worktree subshell writes the
// worktree path there instead, and the function cds the calling shell
// into it after gk exits, since a child process cannot change its
// parent's directory.
const envCdFile = "GK_CD_FILE"

func init() {
	cmd := &cobra.Command{
		Use:       "shell-init <zsh|bash|fish>",
		Short:     "Print a shell function that lets gk wt cd your shell into a worktree",
		ValidArgs: []string{"zsh", "bash", "fish"},
		Args:      cobra.ExactArgs(1),
		Long: `Print a shell function named gk for your shell's startup file.

Without it, gk wt and gk switch open a subshell in the chosen worktree,
and you return with exit. With it, they cd the shell you typed them in,
so there is no subshell to leave: cd - goes back, and gk wt moves on.

  # ~/.zshrc
  eval "$(gk shell-init zsh)"

  # ~/.bashrc
  eval "$(gk shell-init bash)"

  # ~/.config/fish/config.fish
  gk shell-init fish | source

The function runs the gk binary for every gk command and changes
directory only when a command picked a worktree. An alias with the
same name would take precedence over the function, so the zsh and bash
scripts remove it. Use --name for another function name, and --bin when
the binary on PATH has a different name than the function.`,
		RunE: runShellInit,
	}
	cmd.Flags().String("name", "gk", "name of the shell function to define")
	cmd.Flags().String("bin", "", "gk binary the function runs (default: the name this command was run as)")
	rootCmd.AddCommand(cmd)
}

func runShellInit(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	bin, _ := cmd.Flags().GetString("bin")
	if bin == "" {
		bin = filepath.Base(os.Args[0])
	}
	script, err := shellInitScript(args[0], name, bin)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), script)
	return err
}

// shellInitScript renders the gk function for shell. name is the function
// to define and bin the command it runs; both must be plain command names.
func shellInitScript(shell, name, bin string) (string, error) {
	for _, n := range []string{name, bin} {
		if !subshellFuncName.MatchString(n) {
			return "", fmt.Errorf("invalid command name %q: use letters, digits, '.', '_' or '-'", n)
		}
	}
	switch shell {
	case "zsh", "bash":
		return strings.NewReplacer("__NAME__", name, "__BIN__", bin, "__ENV__", envCdFile).Replace(shellInitPOSIX), nil
	case "fish":
		return strings.NewReplacer("__NAME__", name, "__BIN__", bin, "__ENV__", envCdFile).Replace(shellInitFish), nil
	}
	return "", fmt.Errorf("unsupported shell %q: use zsh, bash or fish", shell)
}

// The function keyword keeps zsh from expanding an alias of the same name
// inside the definition itself, which name() { ... } would do.
const shellInitPOSIX = `# gk shell integration: gk wt and gk switch cd this shell into a worktree.
unalias __NAME__ 2>/dev/null
function __NAME__ {
  local __gk_cd __gk_rc
  __gk_cd=$(command mktemp "${TMPDIR:-/tmp}/gk-cd.XXXXXX") || { command __BIN__ "$@"; return; }
  __ENV__="$__gk_cd" command __BIN__ "$@"
  __gk_rc=$?
  if [ -s "$__gk_cd" ]; then
    builtin cd -- "$(<"$__gk_cd")" || __gk_rc=$?
  fi
  command rm -f -- "$__gk_cd"
  return $__gk_rc
}
`

const shellInitFish = `# gk shell integration: gk wt and gk switch cd this shell into a worktree.
function __NAME__ --wraps __BIN__
    set -l tmp /tmp
    set -q TMPDIR; and set tmp $TMPDIR
    set -l cdfile (command mktemp "$tmp/gk-cd.XXXXXX")
    or begin
        command __BIN__ $argv
        return
    end
    __ENV__=$cdfile command __BIN__ $argv
    set -l rc $status
    if test -s $cdfile
        builtin cd (cat $cdfile); or set rc $status
    end
    command rm -f -- $cdfile
    return $rc
end
`
