package cli

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "push [<remote>] [<branch>]",
		Short: "Guarded push: secret scan + protected-branch confirmation",
		Args:  cobra.MaximumNArgs(2),
		RunE:  runPush,
	}
	cmd.Flags().Bool("force", false, "allow non-fast-forward (uses --force-with-lease)")
	cmd.Flags().Bool("skip-scan", false, "skip the secret-pattern scan")
	cmd.Flags().Bool("yes", false, "skip interactive confirmations (for automation)")
	rootCmd.AddCommand(cmd)
}

func runPush(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()

	force, _ := cmd.Flags().GetBool("force")
	skipScan, _ := cmd.Flags().GetBool("skip-scan")
	yes, _ := cmd.Flags().GetBool("yes")

	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	branch := ""
	switch len(args) {
	case 1:
		remote = args[0]
	case 2:
		remote = args[0]
		branch = args[1]
	}
	if branch == "" {
		b, err := client.CurrentBranch(ctx)
		if err != nil {
			return err
		}
		branch = b
	}

	// Protected branch gate
	if isProtected(branch, cfg.Push.Protected) && force {
		if !cfg.Push.AllowForce {
			if !yes && !ui.IsTerminal() {
				return fmt.Errorf("refusing to force-push to protected branch %q in non-interactive mode", branch)
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "force-pushing to protected branch %q. type the branch name to confirm: ", branch)
				sc := bufio.NewScanner(cmd.InOrStdin())
				if !sc.Scan() {
					return fmt.Errorf("confirmation aborted")
				}
				if strings.TrimSpace(sc.Text()) != branch {
					return fmt.Errorf("confirmation did not match; aborting")
				}
			}
		}
	}

	Dbg("push: remote=%s branch=%s force=%v protected=%v", remote, branch, force, isProtected(branch, cfg.Push.Protected))

	// Secret scan
	if !skipScan {
		findings, err := scanCommitsToPush(ctx, runner, remote, branch)
		if err != nil {
			return fmt.Errorf("secret scan: %w", err)
		}
		Dbg("push: secret-scan — %d finding(s)", len(findings))
		if len(findings) > 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "potential secrets detected:")
			for _, f := range findings {
				fmt.Fprintf(cmd.ErrOrStderr(), "  [%s] line %d: %s\n", f.Kind, f.Line, f.Sample)
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "  use --skip-scan to override (not recommended)")
			return fmt.Errorf("aborting push")
		}
	} else {
		Dbg("push: secret-scan skipped (--skip-scan)")
	}

	// Actual push — auto-set upstream when the branch has none yet.
	hasUpstream := branchHasUpstream(ctx, runner, branch)
	gitArgs := []string{"push"}
	if force {
		gitArgs = append(gitArgs, "--force-with-lease")
	}
	if !hasUpstream {
		gitArgs = append(gitArgs, "--set-upstream")
	}
	gitArgs = append(gitArgs, remote, branch)
	Dbg("push: hasUpstream=%v argv=%v", hasUpstream, gitArgs)
	stdout, stderr, err := runner.Run(ctx, gitArgs...)
	if err != nil {
		fmt.Fprint(cmd.ErrOrStderr(), string(stderr))
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), string(stdout))
	fmt.Fprint(cmd.ErrOrStderr(), string(stderr))
	return nil
}

// branchHasUpstream reports whether <branch>@{upstream} resolves.
// Uses rev-parse; returns false if branch has no configured upstream.
func branchHasUpstream(ctx context.Context, r git.Runner, branch string) bool {
	_, _, err := r.Run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	return err == nil
}

// isProtected reports whether branch is in the protected list.
func isProtected(branch string, list []string) bool {
	for _, p := range list {
		if p == branch {
			return true
		}
	}
	return false
}

// scanCommitsToPush fetches the range "remote/branch..HEAD" diff and scans it.
// If the upstream ref is missing, scans all commits reachable from HEAD.
func scanCommitsToPush(ctx context.Context, r git.Runner, remote, branch string) ([]secrets.Finding, error) {
	ref := remote + "/" + branch
	_, _, err := r.Run(ctx, "rev-parse", "--verify", ref+"^{commit}")
	rng := "HEAD"
	if err == nil {
		rng = ref + "..HEAD"
	}
	stdout, stderr, lerr := r.Run(ctx, "log", "-p", "--no-color", rng)
	if lerr != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), lerr)
	}
	return secrets.Scan(string(stdout), nil), nil
}
