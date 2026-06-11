package cli

// selfname.go — command references in gk's own guidance follow the name
// the binary was invoked as. `gk` is routinely alias-shadowed in agent
// and user shells (oh-my-zsh maps it to gitk), so an agent that invokes
// `git-kit pull` must receive remedies it can actually run back —
// `git-kit continue`, not `gk continue`. Invoke as gk → suggestions say
// gk; invoke as git-kit (or gk-dev) → suggestions follow suit.
//
// Scope: this applies ONLY to command-suggestion surfaces — hints,
// remedies, advisory blocks, next_actions, resume/abort contracts.
// Documentation surfaces (--help, guide, easy help) keep the canonical
// short name, and data surfaces (log subjects, digest symbols, anything
// quoting repository content) are never rewritten — a commit subject
// that mentions "gk forget" must reach the reader byte-identical.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// invokedNameValue is resolved once at startup; tests override it
// directly (the test binary's argv[0] ends in ".test" and falls back to
// "gk", keeping every existing output assertion stable).
var invokedNameValue = computeInvokedName(os.Args)

func computeInvokedName(args []string) string {
	if len(args) == 0 {
		return "gk"
	}
	base := filepath.Base(args[0])
	base = strings.TrimSuffix(base, ".exe")
	if base == "" || base == "." || base == "/" || strings.HasSuffix(base, ".test") {
		return "gk"
	}
	return base
}

func invokedName() string { return invokedNameValue }

// Two patterns because ANSI styling breaks the predecessor class:
// bold("gk continue") renders as ESC[1mgk, so the styled variant anchors
// on the ANSI sequence itself.
//
// The plain pattern requires a command-position predecessor (start of
// string, whitespace, backtick, paren, quote) instead of a bare \b: a
// word boundary also fires after "/", so a hint containing a path whose
// basename is gk ("cd /work/agentic/gk or ...") would get its directory
// rewritten into a binary name that does not exist on disk.
var (
	selfCmdPlainRE = regexp.MustCompile("(^|[\\s\x60(\"'])gk ([a-z-])")
	selfCmdANSIRE  = regexp.MustCompile(`(\x1b\[[0-9;]*m)gk ([a-z-])`)
)

// selfRewrite rebrands `gk <subcommand>` tokens in a command-suggestion
// string to the invoked name. No-op under the canonical name, so the
// common path costs one string compare.
func selfRewrite(s string) string {
	name := invokedName()
	if name == "gk" || s == "" {
		return s
	}
	s = selfCmdANSIRE.ReplaceAllString(s, "${1}"+name+" ${2}")
	return selfCmdPlainRE.ReplaceAllString(s, "${1}"+name+" ${2}")
}

// selfCmd builds a runnable command reference ("continue" →
// "git-kit continue") for contract fields that are commands by
// definition (resume/abort, next_actions) rather than prose.
func selfCmd(args string) string {
	if args == "" {
		return invokedName()
	}
	return invokedName() + " " + args
}
