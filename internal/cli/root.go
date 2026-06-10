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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/easy"
	"github.com/x-mesh/gk/internal/git"
)

var (
	// rawVersion is the raw build-time version string (e.g. "v0.29.1" or "dev")
	// captured separately from the formatted rootCmd.Version so that
	// `gk update` can compare it against the latest release tag without
	// re-parsing the formatted "vX.Y.Z (commit ..., built ...)" string.
	rawVersion = "dev"

	// Persistent flags populated during root init.
	flagRepo    string
	flagVerbose bool
	flagDryRun  bool
	flagJSON    bool
	flagNoColor bool
	flagDebug   bool
	flagEasy    bool
	flagNoEasy  bool
	flagAgent   bool

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
	rootCmd.PersistentFlags().BoolVar(&flagEasy, "easy", false, "enable Easy Mode for this invocation")
	rootCmd.PersistentFlags().BoolVar(&flagNoEasy, "no-easy", false, "disable Easy Mode even if config/env enables it")
	// GK_DEBUG=1 env var is honored so users can opt in without every
	// subcommand needing to pass -d by hand.
	if v := os.Getenv("GK_DEBUG"); v == "1" || v == "true" {
		flagDebug = true
	}
	// GK_AGENT=1 turns on agent mode for the whole process: every command
	// that supports JSON emits the {ok, result, error} envelope, and
	// failures carry machine-readable code+remedies. One `export GK_AGENT=1`
	// in an agent's instructions replaces per-call --json flags — flag
	// omission can't silently fall back to prose. Implies --json; an
	// explicit --json=false still wins because cobra parses flags after
	// this init runs.
	if v := os.Getenv("GK_AGENT"); v == "1" || v == "true" {
		flagAgent = true
		flagJSON = true
	}
	// Install subprocess hooks once per process invocation before the
	// selected subcommand runs. Flag parsing happens before PreRun, so
	// by the time this fires flagDebug reflects the -d / GK_DEBUG state.
	// Easy Mode initialisation is deferred to the first EasyEngine()
	// call — config.Load forks `git rev-parse` and `git config` under
	// the hood, and hot-path commands like `prompt-info` that never
	// touch the engine should not pay for that.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		installDebugHooks()
		return nil
	}
}

// Root returns the cobra root command. Used by subcommands in other files
// via init() to attach themselves: cli.Root().AddCommand(...)
func Root() *cobra.Command { return rootCmd }

// versionFields captures the build-time version metadata SetVersionInfo
// received, so Execute can re-render the root help header under the actual
// invocation name (gk / git-kit) instead of the "gk" baked in at startup.
type versionFields struct {
	set                           bool
	version, commit, date, suffix string
}

var verMeta versionFields

// renderRootLong builds the root --help header: a version line under the
// given invocation name, followed by the static description. Shared by
// SetVersionInfo (default "gk") and Execute (real invocation name) so the
// two never drift.
func renderRootLong(name, version, commit, date, suffix string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s%s)\n\n%s", name, version, commit, date, suffix, rootLongDesc)
}

// SetVersionInfo wires build-time version metadata for `gk --version` output
// and prepends the version line to the root help page. branch + worktree
// are surfaced when known so users can tell which checkout produced the
// binary they're running — invaluable when juggling multiple gk worktrees.
//
// The Long header is rendered under the canonical `gk` here; Execute rewrites
// it to the real invocation name (e.g. `git-kit`) once argv[0] is known.
func SetVersionInfo(v, c, d, b, w string) {
	suffix := buildSuffix(b, w)
	rawVersion = v
	verMeta = versionFields{set: true, version: v, commit: c, date: d, suffix: suffix}
	rootCmd.Version = fmt.Sprintf("%s (commit %s, built %s%s)", v, c, d, suffix)
	rootCmd.Long = renderRootLong("gk", v, c, d, suffix)
}

// CurrentVersion returns the raw build-time version string set by
// SetVersionInfo (e.g. "v0.29.1"). Returns "dev" when running an unreleased
// build. Stripped of the formatted "(commit ..., built ...)" suffix so
// callers can pass it to semver comparisons directly.
func CurrentVersion() string { return rawVersion }

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
func Execute() error {
	// Render help/usage under whichever name actually invoked us, so the
	// command tree reads correctly whether the user typed `gk`, `git-kit`,
	// or `git kit` (git execs the `git-kit` binary for the latter). Without
	// this, someone calling `git kit push --help` would see `gk push` in the
	// usage line — confusing, since `gk` may be the very alias they're avoiding.
	name := invocationName()
	rootCmd.Use = name
	// The version header SetVersionInfo baked in at startup is "gk"-prefixed;
	// re-render it under the real invocation name so `git-kit --help` reads
	// "git-kit vX …", not "gk vX …". Only when we were reached by another
	// name — keeping the canonical `gk` path identical to before.
	if name != "gk" && verMeta.set {
		rootCmd.Long = renderRootLong(name, verMeta.version, verMeta.commit, verMeta.date, verMeta.suffix)
	}
	// Wire Easy-Mode help after every subcommand has registered, so the
	// plain-Korean descriptions cover the whole command tree.
	installEasyHelp(rootCmd)
	return rootCmd.Execute()
}

// invocationName derives the root command name shown in help/usage from
// argv[0]; see resolveInvocationName for the policy.
func invocationName() string { return resolveInvocationName(os.Args[0]) }

// resolveInvocationName maps argv[0] to the name gk renders itself under.
// Only the names we actually ship as are honoured — `git-kit` (the
// git-subcommand-friendly alias, also reached via `git kit`), `gk-dev`
// (the Makefile dev build), and the canonical `gk`. Anything else (most
// importantly the `*.test` binary that drives the CLI under `go test`)
// falls back to "gk" so help output stays stable and the cobra tests
// don't have to special-case argv[0].
func resolveInvocationName(arg0 string) string {
	base := strings.TrimSuffix(filepath.Base(arg0), ".exe")
	switch base {
	case "gk", "git-kit", "gk-dev":
		return base
	default:
		return "gk"
	}
}

// Persistent flag accessors for subcommand files.
func RepoFlag() string  { return flagRepo }
func Verbose() bool     { return flagVerbose }
func DryRun() bool      { return flagDryRun }
func JSONOut() bool     { return flagJSON }
func NoColorFlag() bool { return flagNoColor }
func Debug() bool       { return flagDebug }
func EasyFlag() bool    { return flagEasy }
func AgentOut() bool    { return flagAgent }
func NoEasyFlag() bool  { return flagNoEasy }

// debugWriter is the sink for Dbg lines. Production uses os.Stderr;
// tests override it via SetDebugWriter to assert on emitted log lines.
var debugWriter io.Writer = os.Stderr

// easyEngine is the package-level Easy Mode engine, lazily initialised
// on the first EasyEngine() call. Deferring construction skips config
// loading (which forks two git subprocesses for layered config + gk.*
// keys) for commands that never read the engine.
var (
	easyEngine     *easy.Engine
	easyEngineOnce sync.Once
)

// EasyEngine returns the package-level Easy Mode engine, building it
// on first access. The construction path mirrors what PersistentPreRunE
// used to do eagerly: load layered config, fall back to defaults on
// error, then wire the debug fn when --debug is on. Tests that invoke
// command RunE directly (bypassing the cobra Execute path) still get a
// usable engine the moment they call EasyEngine().
func EasyEngine() *easy.Engine {
	easyEngineOnce.Do(func() {
		cfg, _ := config.Load(nil)
		out := config.Defaults().Output
		if cfg != nil {
			out = cfg.Output
		}
		easyEngine = easy.NewEngine(out, flagEasy, flagNoEasy)
		if flagDebug {
			easyEngine.SetDebugFn(Dbg)
		}
	})
	return easyEngine
}

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
	provider.HTTPHook = func(brand, model string, dur time.Duration, err error) {
		status := "ok"
		if err != nil {
			msg := err.Error()
			if len(msg) > 120 {
				msg = msg[:120] + "…"
			}
			status = "err=" + msg
		}
		Dbg("ai %s model=%s  (%s, %s)", brand, model, dur.Round(time.Millisecond), status)
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
