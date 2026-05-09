package cli

import (
	"context"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

func configuredRemotes(ctx context.Context, runner git.Runner) ([]string, bool) {
	if runner == nil {
		return nil, false
	}
	out, _, err := runner.Run(ctx, "remote")
	if err != nil {
		return nil, false
	}
	var remotes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			remotes = append(remotes, line)
		}
	}
	return remotes, true
}

func hasRemote(remotes []string, remote string) bool {
	for _, r := range remotes {
		if r == remote {
			return true
		}
	}
	return false
}

func remoteURL(ctx context.Context, runner git.Runner, remote string) string {
	if runner == nil || strings.TrimSpace(remote) == "" {
		return ""
	}
	out, _, err := runner.Run(ctx, "remote", "get-url", remote)
	if err != nil {
		return ""
	}
	return stripControlChars(strings.TrimSpace(string(out)))
}
