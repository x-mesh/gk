package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/x-mesh/gk/internal/cli"
	"github.com/x-mesh/gk/internal/config"
)

// Build-time metadata. The Makefile sets these via -ldflags; a plain
// `go build` falls back to the Go 1.18+ runtime build info for commit
// + date so users always see *something* useful.
var (
	version  = "dev"
	commit   = "none"
	date     = "unknown"
	branch   = "unknown"
	worktree = "unknown"
)

func main() {
	commit, date = vcsFallback(commit, date)
	cli.SetVersionInfo(version, commit, date, branch, worktree)

	// First-run convenience: drop a commented template at
	// ~/.config/gk/config.yaml when it's missing so users have a
	// single, discoverable file to edit. Failures are swallowed —
	// read-only home dirs, sandboxes, CI boxes etc. must not break gk.
	// Opt out with GK_NO_AUTO_CONFIG=1.
	if created, path := config.EnsureGlobalConfig(); created && path != "" {
		fmt.Fprintf(os.Stderr, "gk: created default config at %s (edit to tune gk)\n", path)
	}

	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, cli.FormatError(err))
		os.Exit(1)
	}
}

// vcsFallback fills in commit/date from the Go 1.18+ build-info VCS
// settings when ldflags didn't supply them — so `go build ./cmd/gk`
// alone still produces a recognisable version string.
func vcsFallback(curCommit, curDate string) (string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return curCommit, curDate
	}
	return vcsFallbackFromSettings(curCommit, curDate, info.Settings)
}

// vcsFallbackFromSettings is the pure core of vcsFallback, split out so
// tests can drive it with synthetic []debug.BuildSetting without having
// to fork a real Go toolchain build.
//
// vcs.modified is only honoured when the commit was filled in from
// vcs.revision in this same call — i.e. ldflags did NOT provide a
// commit. When ldflags supplied the commit (release builds via
// goreleaser or `make build`), a transient working-tree change in the
// build sandbox (e.g. `go mod tidy` adjusting go.sum) must not stamp
// the released binary as -dirty.
func vcsFallbackFromSettings(curCommit, curDate string, settings []debug.BuildSetting) (string, string) {
	commitFromVCS := false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			if curCommit == "none" && len(s.Value) >= 7 {
				curCommit = s.Value[:7]
				commitFromVCS = true
			}
		case "vcs.time":
			if curDate == "unknown" && s.Value != "" {
				curDate = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" && commitFromVCS {
				curCommit += "-dirty"
			}
		}
	}
	return curCommit, curDate
}
