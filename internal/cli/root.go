// Package cli wires all gk subcommands into a cobra tree.
//
// Each subcommand lives in its own file (pull.go, log.go, status.go, ...)
// and registers itself with the root command via an init() function that
// appends to rootCmd.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// Persistent flags populated during root init.
	flagRepo    string
	flagVerbose bool
	flagDryRun  bool
	flagJSON    bool
	flagNoColor bool

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
}

// Root returns the cobra root command. Used by subcommands in other files
// via init() to attach themselves: cli.Root().AddCommand(...)
func Root() *cobra.Command { return rootCmd }

// SetVersionInfo wires build-time version metadata for `gk --version` output
// and prepends the version line to the root help page.
func SetVersionInfo(v, c, d string) {
	rootCmd.Version = fmt.Sprintf("%s (commit %s, built %s)", v, c, d)
	rootCmd.Long = fmt.Sprintf("gk %s (commit %s, built %s)\n\n%s", v, c, d, rootLongDesc)
}

// Execute runs the root command. Returns the error so main.go can set exit code.
func Execute() error { return rootCmd.Execute() }

// Persistent flag accessors for subcommand files.
func RepoFlag() string  { return flagRepo }
func Verbose() bool     { return flagVerbose }
func DryRun() bool      { return flagDryRun }
func JSONOut() bool     { return flagJSON }
func NoColorFlag() bool { return flagNoColor }
