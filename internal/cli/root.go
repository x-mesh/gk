// Package cli wires all gk subcommands into a cobra tree.
//
// Each subcommand lives in its own file (pull.go, log.go, status.go, ...)
// and registers itself with the root command via an init() function that
// appends to rootCmd.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

var (
	// Persistent flags populated during root init.
	flagRepo    string
	flagVerbose bool
	flagDryRun  bool
	flagJSON    bool
	flagNoColor bool
	flagDebug   bool

	// debugStart is captured the first time Dbg() fires so every
	// subsequent log line carries an elapsed-since-start offset, which
	// makes the time distribution between stages easy to eyeball.
	debugStart     time.Time
	debugStartOnce sync.Once

	rootCmd = &cobra.Command{
		Use:           "gk",
		Short:         "gk — git helper",
		Long:          rootLongDesc,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       "", // set by SetVersionInfo
	}
)

// rootLongDesc is the human-readable description shown by `gk --help`.
// SetVersionInfo prepends a `gk vX.Y.Z (...)` header at startup.
const rootLongDesc = "A lightweight Go git helper for daily pull / log / status / branch workflows."

func init() {
	rootCmd.PersistentFlags().StringVar(&flagRepo, "repo", "", "path to git repo (default: cwd)")
	rootCmd.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	rootCmd.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "print actions without executing")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output where supported")
	rootCmd.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "disable color output")
	rootCmd.PersistentFlags().BoolVarP(&flagDebug, "debug", "d", false, "emit diagnostic logs (subprocess invocations, retry reasons, timings) to stderr")
	// GK_DEBUG=1 env var is honored so users can opt in without every
	// subcommand needing to pass -d by hand.
	if v := os.Getenv("GK_DEBUG"); v == "1" || v == "true" {
		flagDebug = true
	}
	// Install subprocess hooks once per process invocation before the
	// selected subcommand runs. Flag parsing happens before PreRun, so
	// by the time this fires flagDebug reflects the -d / GK_DEBUG state.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		installDebugHooks()
		return nil
	}
}

// Root returns the cobra root command. Used by subcommands in other files
// via init() to attach themselves: cli.Root().AddCommand(...)
func Root() *cobra.Command { return rootCmd }

// SetVersionInfo wires build-time version metadata for `gk --version` output
// and prepends the version line to the root help page. branch + worktree
// are surfaced when known so users can tell which checkout produced the
// binary they're running — invaluable when juggling multiple gk worktrees.
func SetVersionInfo(v, c, d, b, w string) {
	suffix := buildSuffix(b, w)
	rootCmd.Version = fmt.Sprintf("%s (commit %s, built %s%s)", v, c, d, suffix)
	rootCmd.Long = fmt.Sprintf("gk %s (commit %s, built %s%s)\n\n%s", v, c, d, suffix, rootLongDesc)
}

func buildSuffix(branch, worktree string) string {
	parts := []string{}
	if branch != "" && branch != "unknown" {
		parts = append(parts, "branch "+branch)
	}
	if worktree != "" && worktree != "unknown" {
		parts = append(parts, "worktree "+worktree)
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

// Execute runs the root command. Returns the error so main.go can set exit code.
func Execute() error { return rootCmd.Execute() }

// Persistent flag accessors for subcommand files.
func RepoFlag() string  { return flagRepo }
func Verbose() bool     { return flagVerbose }
func DryRun() bool      { return flagDryRun }
func JSONOut() bool     { return flagJSON }
func NoColorFlag() bool { return flagNoColor }
func Debug() bool       { return flagDebug }

// debugWriter is the sink for Dbg lines. Production uses os.Stderr;
// tests override it via SetDebugWriter to assert on emitted log lines.
var debugWriter io.Writer = os.Stderr

// SetDebugWriter overrides the destination for Dbg output. Returns the
// previous writer so callers can restore on cleanup. Safe to call from
// tests; not intended for runtime use.
func SetDebugWriter(w io.Writer) io.Writer {
	prev := debugWriter
	debugWriter = w
	return prev
}

// Dbg emits a diagnostic line to stderr (or SetDebugWriter target) when
// --debug / -d / GK_DEBUG=1 is active. No-op otherwise — so it is safe
// to pepper hot paths with Dbg calls without any runtime cost in the
// common case.
//
// Each line is prefixed with an elapsed-since-first-call duration so a
// user can eyeball "which stage spent the time", and the entire line
// (prefix + body) is rendered in dim gray so debug output visually
// separates from the command's real output even when the two are
// interleaved on stderr:
//
//	[debug +0.003s] ai commit: provider=gemini
//	[debug +0.042s] ai commit: classify ok — 3 groups
//	[debug +2.815s] ai commit: compose(1/3) attempt=1 type=feat scope=api
func Dbg(format string, args ...interface{}) {
	if !flagDebug {
		return
	}
	debugStartOnce.Do(func() { debugStart = time.Now() })
	elapsed := time.Since(debugStart).Seconds()
	line := fmt.Sprintf("[debug +%6.3fs] ", elapsed) + fmt.Sprintf(format, args...)
	// Whole line faint so it recedes next to real command output.
	fmt.Fprintln(debugWriter, color.New(color.Faint).Sprint(line))
}

// installDebugHooks wires subprocess-level debug logging into the git
// and AI provider runners when --debug / -d is active. Called from the
// root PersistentPreRunE so every subcommand gets the coverage without
// individual opt-in.
//
// Both hooks are package-level vars in their respective runners; they
// are nil in production (no overhead) unless this function installs a
// closure. Since Dbg is a no-op when flagDebug is false, it would be
// safe to always install — but wiring only under the flag keeps the
// normal path completely allocation-free.
func installDebugHooks() {
	if !flagDebug {
		return
	}
	git.ExecHook = func(args []string, dur time.Duration, err error) {
		status := "ok"
		if err != nil {
			// Truncate noisy stderrs so a 10KB git error doesn't
			// flood the debug log — the full message still goes
			// through the real return path.
			msg := err.Error()
			if len(msg) > 120 {
				msg = msg[:120] + "…"
			}
			status = "err=" + msg
		}
		Dbg("git %s  (%s, %s)", strings.Join(args, " "), dur.Round(time.Millisecond), status)
	}
	provider.ExecHook = func(name string, args []string, dur time.Duration, err error) {
		status := "ok"
		if err != nil {
			msg := err.Error()
			if len(msg) > 120 {
				msg = msg[:120] + "…"
			}
			status = "err=" + msg
		}
		Dbg("exec %s %s  (%s, %s)", name, strings.Join(debugProviderArgs(args), " "), dur.Round(time.Millisecond), status)
	}
}

func debugProviderArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if len(arg) > 160 || strings.Contains(arg, "\n") {
			out = append(out, fmt.Sprintf("<%d-char prompt>", len(arg)))
			continue
		}
		out = append(out, arg)
	}
	return out
}
