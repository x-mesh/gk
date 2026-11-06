package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// contextFetchTimeout bounds each remote fetch under `gk context --fetch`.
// An orientation call that hangs on a slow or unreachable remote costs the
// agent more than the stale counts it was trying to avoid.
const contextFetchTimeout = 15 * time.Second

// contextFetchJSON reports one remote fetched by `gk context --fetch`. A failed
// fetch does not fail the call: the counts are then as of the last successful
// fetch, and ok:false with the error is how the caller can tell.
type contextFetchJSON struct {
	Remote string `json:"remote"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// contextFetchRemotes lists the remotes whose tracking refs feed the context
// counts: the base remote (base.behind_remote / ahead_remote) and, when it
// differs, the remote of the current branch's upstream (ahead / behind).
// Remotes that are not configured are dropped.
func contextFetchRemotes(ctx context.Context, runner git.Runner, cfg *config.Config, branch string) []string {
	var candidates []string
	base := "origin"
	if cfg != nil && strings.TrimSpace(cfg.Remote) != "" {
		base = strings.TrimSpace(cfg.Remote)
	}
	candidates = append(candidates, base)
	if branch != "" {
		if out, _, err := runner.Run(ctx, "config", "--get", "branch."+branch+".remote"); err == nil {
			if r := strings.TrimSpace(string(out)); r != "" && r != "." && r != base {
				candidates = append(candidates, r)
			}
		}
	}
	var remotes []string
	for _, r := range candidates {
		if _, _, err := runner.Run(ctx, "config", "--get", "remote."+r+".url"); err == nil {
			remotes = append(remotes, r)
		}
	}
	return remotes
}

// runContextFetch fetches each remote with prompts disabled, so a credential
// prompt fails the fetch instead of blocking the call.
func runContextFetch(ctx context.Context, runner *git.ExecRunner, remotes []string) []contextFetchJSON {
	fr := &git.ExecRunner{
		Dir:      runner.Dir,
		ExtraEnv: append(append([]string{}, runner.ExtraEnv...), "SSH_ASKPASS=", "GCM_INTERACTIVE=never"),
	}
	results := make([]contextFetchJSON, 0, len(remotes))
	for _, remote := range remotes {
		res := contextFetchJSON{Remote: remote, OK: true}
		tctx, cancel := context.WithTimeout(ctx, contextFetchTimeout)
		_, stderr, err := fr.Run(tctx, "fetch", "--quiet", remote)
		if err != nil {
			res.OK = false
			switch {
			case errors.Is(tctx.Err(), context.DeadlineExceeded):
				res.Error = fmt.Sprintf("timed out after %s", contextFetchTimeout)
			case strings.TrimSpace(string(stderr)) != "":
				res.Error = strings.TrimSpace(string(stderr))
			default:
				res.Error = err.Error()
			}
		}
		cancel()
		results = append(results, res)
	}
	return results
}
