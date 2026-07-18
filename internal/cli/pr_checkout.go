package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
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

// prCheckoutTarget resolves the fetch source and local branch for a PR selected
// from a multi-repository search. Same-repository PRs keep using the configured
// remote. Cross-repository PRs use a URL rewritten from that remote so SSH host
// aliases and usernames continue to work; HTTPS userinfo is dropped so secrets
// never enter git argv and the configured credential helper handles auth.
func prCheckoutTarget(ctx context.Context, runner git.Runner, cfg config.Config, owner, repo string, num int) (string, string, error) {
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	raw := remoteURL(ctx, runner, remote)
	if raw == "" {
		return "", "", fmt.Errorf("pr checkout: no %s remote to derive the selected repository", remote)
	}
	meta := config.ParseRemoteMeta(raw)
	if meta.Owner == "" || meta.Repo == "" {
		return "", "", fmt.Errorf("pr checkout: could not derive a repository from %s URL %q", remote, remoteDisplayName(raw))
	}
	if strings.EqualFold(meta.Owner, owner) && strings.EqualFold(meta.Repo, repo) {
		return remote, fmt.Sprintf("pr/%d", num), nil
	}

	source, err := rewriteRemoteRepo(raw, owner, repo)
	if err != nil {
		return "", "", fmt.Errorf("pr checkout: selected repository %s/%s: %w", owner, repo, err)
	}
	local := fmt.Sprintf("pr/%s/%s/%d", owner, repo, num)
	if err := guardRef(local); err != nil {
		return "", "", fmt.Errorf("pr checkout: invalid branch name: %w", err)
	}
	return source, local, nil
}

// rewriteRemoteRepo preserves the transport and host portion of a configured
// remote while replacing only owner/repo. Both URL and SCP-style remotes are
// supported (for example https://github.com/a/b.git and git@work:a/b.git).
func rewriteRemoteRepo(raw, owner, repo string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "", fmt.Errorf("unsupported remote URL")
		}
		u.Path = "/" + owner + "/" + repo + ".git"
		u.RawPath = ""
		u.RawQuery = ""
		u.Fragment = ""
		if u.User != nil {
			if u.Scheme == "ssh" {
				u.User = url.User(u.User.Username()) // preserve SSH user, never a password
			} else {
				u.User = nil // HTTPS authentication belongs in the credential helper
			}
		}
		return u.String(), nil
	}

	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return "", fmt.Errorf("unsupported remote URL")
	}
	colonRel := strings.Index(raw[at+1:], ":")
	if colonRel < 1 {
		return "", fmt.Errorf("unsupported remote URL")
	}
	colon := at + 1 + colonRel
	return raw[:colon+1] + owner + "/" + repo + ".git", nil
}

// remoteDisplayName removes URL credentials and query data before a fetch
// source is included in user-facing output. The original source is still passed
// unchanged to git so configured authentication keeps working.
func remoteDisplayName(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "remote URL"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

var remoteURLInText = regexp.MustCompile(`(?i)(?:https?|ssh)://[^\s'\"<>]+`)

func sanitizeGitRemoteText(text, remote string) string {
	if text == "" {
		return ""
	}
	if remote != "" {
		text = strings.ReplaceAll(text, remote, remoteDisplayName(remote))
	}
	return remoteURLInText.ReplaceAllStringFunc(text, remoteDisplayName)
}

func sanitizeGitRemoteError(err error, remote string) error {
	if err == nil {
		return nil
	}
	var exitErr *git.ExitError
	if errors.As(err, &exitErr) {
		args := make([]string, len(exitErr.Args))
		for i, arg := range exitErr.Args {
			args[i] = sanitizeGitRemoteText(arg, remote)
		}
		return &git.ExitError{
			Code:   exitErr.Code,
			Args:   args,
			Stderr: sanitizeGitRemoteText(exitErr.Stderr, remote),
		}
	}
	return errors.New(sanitizeGitRemoteText(err.Error(), remote))
}

// prCheckoutWith performs the fetch + switch against an injected runner (so it
// is unit-testable) and returns the success message.
func prCheckoutWith(ctx context.Context, runner git.Runner, remote, local string, num int) (string, error) {
	refspec := fmt.Sprintf("pull/%d/head:%s", num, local)
	displayRemote := remoteDisplayName(remote)
	if _, stderr, err := runner.Run(ctx, "fetch", remote, refspec); err != nil {
		safeStderr := strings.TrimSpace(sanitizeGitRemoteText(string(stderr), remote))
		safeErr := sanitizeGitRemoteError(err, remote)
		return "", WithHint(
			fmt.Errorf("pr checkout: fetch %s %s: %s: %w", displayRemote, refspec, safeStderr, safeErr),
			fmt.Sprintf("check that %s is a GitHub remote and PR #%d exists", displayRemote, num),
		)
	}
	if _, stderr, err := runner.Run(ctx, "switch", local); err != nil {
		return "", fmt.Errorf("pr checkout: switch %s: %s: %w", local, strings.TrimSpace(string(stderr)), err)
	}
	return fmt.Sprintf("checked out PR #%d as %s (from %s/pull/%d/head)", num, local, displayRemote, num), nil
}
