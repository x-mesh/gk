package cli

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// maybeCreateRemoteAndRetry handles the "push failed because the GitHub repo
// doesn't exist" case. It returns handled=true only when it took over the
// outcome (created the repo and retried, or deliberately refused after
// recognizing the case) — the caller then returns retryErr verbatim. A
// false handled means "not this case", and the caller prints the original
// git error unchanged.
//
// Creating a GitHub repository is an outward-facing, hard-to-reverse action,
// so it never happens silently: --create-remote is the explicit opt-in that
// works everywhere (agents/CI included), and an interactive terminal is
// prompted. Absent both, it declines with a copy-pasteable `gh` remedy so a
// non-interactive run without the flag never surprises anyone.
func maybeCreateRemoteAndRetry(cmd *cobra.Command, runner git.Runner, remote, branch, stderr string, gitArgs []string) (handled bool, retryErr error) {
	if !isRepoNotFoundErr(stderr) {
		return false, nil
	}
	ctx := cmd.Context()

	// The gh CLI is the only supported creator — without it there is nothing
	// to offer, so fall through to the raw git error.
	if _, err := exec.LookPath("gh"); err != nil {
		return false, nil
	}

	// Derive the create target from the remote's own URL. A URL that doesn't
	// resolve to owner/repo (a plain host, a local path) isn't a GitHub repo
	// we can create — leave it to the raw error.
	url := remoteURL(ctx, runner, remote)
	if url == "" {
		return false, nil
	}
	meta := config.ParseRemoteMeta(url)
	if meta.Owner == "" || meta.Repo == "" {
		return false, nil
	}
	if !isGitHubHost(meta.Host) {
		return false, nil
	}

	createFlag, _ := cmd.Flags().GetBool("create-remote")
	public, _ := cmd.Flags().GetBool("public")
	vis := "private"
	if public {
		vis = "public"
	}
	slug := meta.Owner + "/" + meta.Repo

	// Decide whether to actually create.
	switch {
	case createFlag:
		// Explicit opt-in — proceed (works non-interactively too).
	case promptAllowed():
		fmt.Fprintf(cmd.ErrOrStderr(), "remote %s does not exist on %s.\ncreate it as %s and push? [y/N]: ", slug, meta.Host, vis)
		sc := bufio.NewScanner(cmd.InOrStdin())
		if !sc.Scan() || !isYes(sc.Text()) {
			// User declined — surface the original failure with the manual path.
			fmt.Fprint(cmd.ErrOrStderr(), stderr)
			return true, WithHint(
				fmt.Errorf("push failed: remote repository %s does not exist", slug),
				fmt.Sprintf("create it yourself: gh repo create %s --%s --source . --push", slug, vis),
			)
		}
	default:
		// Non-interactive without the flag: never create outward-facing state
		// silently. Report the case with the exact remedy.
		fmt.Fprint(cmd.ErrOrStderr(), stderr)
		return true, WithRemedy(
			fmt.Errorf("push failed: remote repository %s does not exist", slug),
			fmt.Sprintf("pass --create-remote to create it, or run: gh repo create %s --%s --source . --push", slug, vis),
			errRemedy{Command: fmt.Sprintf("gk push --create-remote%s", publicFlagSuffix(public)), Safety: "safe"},
		)
	}

	// Create the empty repo, then retry the original push against the
	// already-configured origin. `gh repo create <slug> --private` (no
	// --source/--push) avoids fighting the origin remote that init already
	// added — we only need the repo to exist, then git push does the rest.
	if err := ghRepoCreate(ctx, slug, public); err != nil {
		fmt.Fprint(cmd.ErrOrStderr(), stderr)
		return true, fmt.Errorf("gh repo create %s: %w", slug, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "✓ created %s repository %s\n", vis, slug)

	stdout, stderr2, perr := runner.Run(ctx, gitArgs...)
	if perr != nil {
		fmt.Fprint(cmd.ErrOrStderr(), string(stderr2))
		return true, fmt.Errorf("push after creating %s: %w", slug, perr)
	}
	_ = stdout
	if s := strings.TrimSpace(string(stderr2)); s != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), s)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ pushed %s → %s\n", branch, remote)
	return true, nil
}

// isRepoNotFoundErr recognizes the git/gh failure modes that mean "the
// remote repository doesn't exist" across GitHub's SSH and HTTPS transports.
func isRepoNotFoundErr(stderr string) bool {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "repository not found"): // HTTPS
		return true
	case strings.Contains(low, "does not exist"):
		return true
	case strings.Contains(low, "could not read from remote repository") &&
		strings.Contains(low, "please make sure you have the correct access rights"): // SSH, repo-missing
		return true
	default:
		return false
	}
}

// isGitHubHost reports whether gh can create a repo on host. gh targets
// github.com by default and GitHub Enterprise via GH_HOST; gating on a
// "github" hostname keeps us from attempting creation on unrelated hosts
// (gitlab, bitbucket) where the not-found heuristics could also match.
func isGitHubHost(host string) bool {
	h := strings.ToLower(host)
	return h == "github.com" || strings.Contains(h, "github")
}

// ghRepoCreate creates an empty GitHub repository named slug (owner/repo).
// Private unless public is set. No --source/--push: the caller retries the
// existing origin push once the repo exists.
func ghRepoCreate(ctx context.Context, slug string, public bool) error {
	vis := "--private"
	if public {
		vis = "--public"
	}
	c := exec.CommandContext(ctx, "gh", "repo", "create", slug, vis)
	out, err := c.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func isYes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func publicFlagSuffix(public bool) string {
	if public {
		return " --public"
	}
	return ""
}
