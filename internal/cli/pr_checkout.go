package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// runPRCheckout backs `gk pr checkout <n>`: fetch the PR head into a local
// branch and switch to it. GitHub publishes refs/pull/<n>/head for every PR
// (fork PRs included), so this needs only git — no API call, no token.
func runPRCheckout(cmd *cobra.Command, args []string) error {
	num, err := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
	if err != nil || num <= 0 {
		return fmt.Errorf("pr checkout: invalid PR number %q", args[0])
	}
	ctx := cmdCtx(cmd)
	runner := &git.ExecRunner{Dir: RepoFlag()}

	remote, _ := cmd.Flags().GetString("remote")
	if remote == "" {
		cfg, _ := config.Load(cmd.Flags())
		if cfg != nil {
			remote = cfg.Remote
		}
		if remote == "" {
			remote = "origin"
		}
	}
	if err := guardRef(remote); err != nil {
		return fmt.Errorf("pr checkout: invalid remote: %w", err)
	}

	local, _ := cmd.Flags().GetString("branch")
	if local == "" {
		local = fmt.Sprintf("pr/%d", num)
	}
	if err := guardRef(local); err != nil {
		return fmt.Errorf("pr checkout: invalid branch name: %w", err)
	}

	msg, err := prCheckoutWith(ctx, runner, remote, local, num)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), msg)
	return nil
}

// prCheckoutWith performs the fetch + switch against an injected runner (so it
// is unit-testable) and returns the success message.
func prCheckoutWith(ctx context.Context, runner git.Runner, remote, local string, num int) (string, error) {
	refspec := fmt.Sprintf("pull/%d/head:%s", num, local)
	if _, stderr, err := runner.Run(ctx, "fetch", remote, refspec); err != nil {
		return "", WithHint(
			fmt.Errorf("pr checkout: fetch %s %s: %s: %w", remote, refspec, strings.TrimSpace(string(stderr)), err),
			fmt.Sprintf("check that %s is a GitHub remote and PR #%d exists", remote, num),
		)
	}
	if _, stderr, err := runner.Run(ctx, "switch", local); err != nil {
		return "", fmt.Errorf("pr checkout: switch %s: %s: %w", local, strings.TrimSpace(string(stderr)), err)
	}
	return fmt.Sprintf("checked out PR #%d as %s (from %s/pull/%d/head)", num, local, remote, num), nil
}
