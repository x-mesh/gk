package main

import (
	"fmt"
	"os"

	"github.com/x-mesh/gk/internal/cli"
	"github.com/x-mesh/gk/internal/config"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli.SetVersionInfo(version, commit, date)

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
