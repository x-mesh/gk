package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/update"
)

func init() {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update gk to the latest release",
		Long: `Self-update gk based on how it was installed.

  brew      → forwards to 'brew upgrade x-mesh/tap/gk'
  manual    → downloads the matching archive from the latest release,
              verifies the published sha256, and atomically replaces the
              running binary in place. Escalates with sudo when the
              install dir is not user-writable (e.g. /usr/local/bin).
  go-install → prints the equivalent 'go install ...@latest' command.

Use --check to compare versions without downloading or upgrading anything.`,
		RunE: runUpdate,
	}
	cmd.Flags().Bool("check", false, "report whether a newer release is available without upgrading; exit 0 if up-to-date, 1 if newer")
	cmd.Flags().Bool("force", false, "reinstall even if already on the latest version")
	cmd.Flags().String("to", "", "pin a specific tag (manual installs only); defaults to the latest release")
	rootCmd.AddCommand(cmd)
}

func runUpdate(cmd *cobra.Command, _ []string) error {
	check, _ := cmd.Flags().GetBool("check")
	force, _ := cmd.Flags().GetBool("force")
	pinned, _ := cmd.Flags().GetString("to")

	install, err := update.DetectInstall()
	if err != nil {
		return err
	}

	current := CurrentVersion()

	// HTTP timeout sized for a slow link fetching a small JSON blob; archive
	// downloads use the per-request context which inherits cmd.Context().
	httpClient := &http.Client{Timeout: 30 * time.Second}
	gh := &update.Client{HTTP: httpClient}

	// Resolve the target version. --to skips the API call entirely so users
	// behind a captive portal can still force a known-good version.
	target := pinned
	if target == "" {
		ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		defer cancel()
		target, err = gh.LatestTag(ctx)
		if err != nil {
			return fmt.Errorf("look up latest release: %w", err)
		}
	}

	cmp := update.CompareSemver(current, target)
	uptoDate := cmp >= 0 && !force

	if check {
		return runUpdateCheck(cmd, install, current, target, cmp)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "current: %s\nlatest:  %s\nsource:  %s (%s)\n",
		current, target, install.Source, install.BinaryPath)

	if uptoDate {
		fmt.Fprintln(cmd.OutOrStdout(), "already up-to-date — pass --force to reinstall.")
		return nil
	}

	if DryRun() {
		fmt.Fprintln(cmd.OutOrStdout(), "(dry-run) would upgrade.")
		return nil
	}

	switch install.Source {
	case update.SourceBrew:
		return runBrewUpgrade(cmd)
	case update.SourceGoInstall:
		return printGoInstallHint(cmd)
	case update.SourceManual:
		return runManualUpgrade(cmd, gh, install, target)
	default:
		// SourceUnknown should be impossible after DetectInstall succeeds,
		// but treat it as manual rather than panicking — the worst case is
		// the user gets a download attempt that fails on permissions, which
		// they can recover from.
		return runManualUpgrade(cmd, gh, install, target)
	}
}

func runUpdateCheck(cmd *cobra.Command, install *update.Install, current, target string, cmp int) error {
	out := cmd.OutOrStdout()
	if cmp >= 0 {
		fmt.Fprintf(out, "up-to-date: %s (latest %s, source %s)\n", current, target, install.Source)
		return nil
	}
	fmt.Fprintf(out, "update available: %s\n", update.FormatPlan(current, target))
	// `gk update --check` exits 1 when newer is available so users can
	// `if ! gk update --check; then gk update; fi` in cron and CI.
	os.Exit(1)
	return nil
}

func runBrewUpgrade(cmd *cobra.Command) error {
	if _, err := exec.LookPath("brew"); err != nil {
		return fmt.Errorf("brew not found on PATH; reinstall via the install.sh script or run brew yourself")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "→ brew upgrade x-mesh/tap/gk")
	c := exec.Command("brew", "upgrade", "x-mesh/tap/gk") //nolint:gosec // user-driven self-update
	c.Stdin = os.Stdin
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	return c.Run()
}

func printGoInstallHint(cmd *cobra.Command) error {
	fmt.Fprintln(cmd.OutOrStdout(),
		"detected go-install build; run:\n  go install github.com/x-mesh/gk/cmd/gk@latest")
	return nil
}

func runManualUpgrade(cmd *cobra.Command, gh *update.Client, install *update.Install, tag string) error {
	asset := install.AssetName()
	if asset == "" {
		return fmt.Errorf("unsupported platform: %s/%s", install.OS, install.Arch)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "downloading %s (%s)...\n", asset, tag)
	staged, err := gh.DownloadVerified(cmd.Context(), tag, asset, install.Dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "installing to %s\n", install.BinaryPath)
	if err := update.AtomicReplaceWithSudo(staged, install.BinaryPath); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "updated to %s\n", tag)
	return nil
}
