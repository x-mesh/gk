package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/x-mesh/gk/internal/ui"
)

// A gk subshell carries a control file so that gk run inside it can hand
// the next worktree to the gk that owns the shell instead of nesting
// another shell. The owner starts bash, zsh or fish with a bootstrap that
// defines a gk function after the user's own startup files. The function
// runs the real binary and, when the call left something in the control
// file, ends the shell with builtin exit. The owner then reads the file:
//
//	empty      the user typed exit: return to the parent shell
//	/abs/path  hop: start the next subshell in that worktree
//	back       the picker's [←] back row: return to the parent shell
//
// GK_WT_CTL names the file and is set only when a bootstrap exists for the
// shell. It is exported, so a shell started inside the subshell (bash, a
// tmux pane) inherits it without the function that would end the shell.
// The function therefore marks each of its own calls with GK_WT_FN, and gk
// hands off only on such a call. Any other gk nests as before.
const (
	envWTCtl    = "GK_WT_CTL"
	envWTFn     = "GK_WT_FN"
	envWTParent = "GK_WT_PARENT_PWD"
	ctlBack     = "back"

	worktreeKeyBack = "__gk_back__"
)

type subshellFamily int

const (
	shellOther subshellFamily = iota
	shellBash
	shellZsh
	shellFish
)

// subshellFamilyOf classifies a shell by the basename of its path.
func subshellFamilyOf(sh string) subshellFamily {
	switch filepath.Base(sh) {
	case "bash":
		return shellBash
	case "zsh":
		return shellZsh
	case "fish":
		return shellFish
	}
	return shellOther
}

// subshellBootstrap is how to start a shell so that it defines the gk
// function after the user's own startup files.
type subshellBootstrap struct {
	Args []string // appended to the shell command line
	Env  []string // appended to the environment
}

var subshellFuncName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// subshellFunctionNames returns the names the bootstrap defines: gk, plus
// the name this binary was invoked as (git-kit, gk-dev) when it differs.
func subshellFunctionNames(argv0 string) []string {
	names := []string{"gk"}
	if base := filepath.Base(argv0); base != "gk" && subshellFuncName.MatchString(base) {
		names = append(names, base)
	}
	return names
}

// subshellFunctionSh renders the gk function for bash and zsh. An alias
// of the same name would win over the function, so it is removed first,
// and the binary is called by absolute path so no alias can re-enter.
func subshellFunctionSh(exe string, names []string) string {
	var b strings.Builder
	b.WriteString("unalias " + strings.Join(names, " ") + " 2>/dev/null\n")
	for _, n := range names {
		fmt.Fprintf(&b, `%s() {
  GK_WT_FN=1 %s "$@"
  local rc=$?
  if [ -n "$%s" ] && [ -s "$%s" ]; then
    builtin exit 0
  fi
  return $rc
}
`, n, shellQuote(exe), envWTCtl, envWTCtl)
	}
	return b.String()
}

// subshellFunctionFish renders the gk function for fish, where an alias is
// itself a function and this definition replaces it.
func subshellFunctionFish(exe string, names []string) string {
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, `function %s --wraps %s
    set -lx GK_WT_FN 1
    %s $argv
    set -l rc $status
    if test -n "$%s"; and test -s "$%s"
        builtin exit 0
    end
    return $rc
end
`, n, n, shellQuote(exe), envWTCtl, envWTCtl)
	}
	return b.String()
}

// writeSubshellBootstrap writes the startup files for fam into dir and
// returns the shell arguments and environment that make the shell read
// them. The files run the user's own startup files first, so prompts,
// aliases and completion stay as they are.
func writeSubshellBootstrap(dir string, fam subshellFamily, fn string, origZDotDir string) (subshellBootstrap, error) {
	write := func(name, body string) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, []byte(body), 0o600)
	}
	switch fam {
	case shellBash:
		// --rcfile replaces ~/.bashrc only; /etc/bash.bashrc is still read.
		rc, err := write("bashrc", `[ -f "$HOME/.bashrc" ] && . "$HOME/.bashrc"
`+fn)
		if err != nil {
			return subshellBootstrap{}, err
		}
		return subshellBootstrap{Args: []string{"--rcfile", rc}}, nil
	case shellZsh:
		// zsh looks up $ZDOTDIR before each startup file. The temporary
		// .zshenv restores the user's ZDOTDIR, runs their .zshenv (which
		// may move ZDOTDIR, as XDG setups do), records the result and
		// points ZDOTDIR back here so the temporary .zshrc runs next. That
		// .zshrc restores ZDOTDIR before it runs the user's .zshrc, so
		// compinit and history land in the user's directory, not in dir.
		// /etc/zshrc runs between the two files while ZDOTDIR is still dir,
		// and macOS's sets HISTFILE=${ZDOTDIR:-$HOME}/.zsh_history there, so
		// a HISTFILE inside dir is moved to the user's directory. zsh reads
		// the history file after all startup files, so this covers both
		// loading and saving, and a HISTFILE the user's .zshrc sets wins.
		qdir := shellQuote(dir)
		if _, err := write(".zshenv", `if [ -n "$GK_ZDOTDIR_ORIG" ]; then ZDOTDIR="$GK_ZDOTDIR_ORIG"; else unset ZDOTDIR; fi
[ -f "${ZDOTDIR:-$HOME}/.zshenv" ] && . "${ZDOTDIR:-$HOME}/.zshenv"
GK_ZDOTDIR_ORIG="$ZDOTDIR"
ZDOTDIR=`+qdir+`
`); err != nil {
			return subshellBootstrap{}, err
		}
		if _, err := write(".zshrc", `if [ -n "$GK_ZDOTDIR_ORIG" ]; then ZDOTDIR="$GK_ZDOTDIR_ORIG"; else unset ZDOTDIR; fi
unset GK_ZDOTDIR_ORIG
[[ $HISTFILE == `+qdir+`/* ]] && HISTFILE="${ZDOTDIR:-$HOME}/${HISTFILE:t}"
[ -f "${ZDOTDIR:-$HOME}/.zshrc" ] && . "${ZDOTDIR:-$HOME}/.zshrc"
`+fn); err != nil {
			return subshellBootstrap{}, err
		}
		return subshellBootstrap{Env: []string{"ZDOTDIR=" + dir, "GK_ZDOTDIR_ORIG=" + origZDotDir}}, nil
	case shellFish:
		// -C runs after config.fish, so the function is defined last.
		init, err := write("init.fish", fn)
		if err != nil {
			return subshellBootstrap{}, err
		}
		return subshellBootstrap{Args: []string{"-C", "source " + shellQuote(init)}}, nil
	}
	return subshellBootstrap{}, fmt.Errorf("no subshell bootstrap for %v", fam)
}

// prepareSubshellBootstrap writes the bootstrap for this binary into dir.
func prepareSubshellBootstrap(dir string, fam subshellFamily) (subshellBootstrap, error) {
	exe, err := os.Executable()
	if err != nil {
		return subshellBootstrap{}, err
	}
	names := subshellFunctionNames(os.Args[0])
	fn := subshellFunctionSh(exe, names)
	if fam == shellFish {
		fn = subshellFunctionFish(exe, names)
	}
	return writeSubshellBootstrap(dir, fam, fn, os.Getenv("ZDOTDIR"))
}

type ctlAction int

const (
	ctlExit   ctlAction = iota // nothing written: the user left the shell
	ctlHop                     // an absolute worktree path to enter next
	ctlReturn                  // the [←] back row
)

// parseSubshellCtl interprets what gk inside the subshell left in the
// control file.
func parseSubshellCtl(content string) (ctlAction, string) {
	s := strings.TrimSpace(content)
	switch {
	case s == "":
		return ctlExit, ""
	case s == ctlBack:
		return ctlReturn, ""
	case filepath.IsAbs(s):
		return ctlHop, s
	}
	return ctlExit, ""
}

// inHopSubshell reports whether this gk was called by the gk function of a
// bootstrapped subshell, the only caller that ends the shell after a write.
func inHopSubshell() bool {
	return os.Getenv(envWTCtl) != "" && os.Getenv(envWTFn) == "1"
}

// writeSubshellCtl hands content to the gk that owns this subshell. It
// reports false when this process is not inside a bootstrapped subshell.
// A write that fails is treated the same way: the owner is gone or its
// directory was removed, so the caller falls back to a nested shell.
func writeSubshellCtl(content string) bool {
	if !inHopSubshell() {
		return false
	}
	return os.WriteFile(os.Getenv(envWTCtl), []byte(content), 0o600) == nil
}

// worktreeBackRow is the picker row that returns to the parent shell. It
// exists only inside a bootstrapped subshell, where the owner can act on it.
func worktreeBackRow(faint func(a ...interface{}) string) (ui.PickerItem, bool) {
	if !inHopSubshell() {
		return ui.PickerItem{}, false
	}
	label := "[←] back"
	if parent := os.Getenv(envWTParent); parent != "" {
		label += " to " + parent
	}
	return ui.PickerItem{Display: faint(label), Cells: []string{label, "", "", ""}, Key: worktreeKeyBack}, true
}
