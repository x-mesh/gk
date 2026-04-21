package main

import (
	"fmt"
	"os"

	"github.com/x-mesh/gk/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli.SetVersionInfo(version, commit, date)
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "gk:", err)
		os.Exit(1)
	}
}
