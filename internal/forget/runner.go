package forget

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// ErrFilterRepoNotInstalled is returned when `git filter-repo` is not on PATH.
// gk does not bundle filter-repo and does not fall back to the deprecated
// `git filter-branch` — that tool has subtle bugs around ref updates and
// the upstream documentation actively recommends against using it.
var ErrFilterRepoNotInstalled = fmt.Errorf(
	"git filter-repo not found on PATH; install via: brew install git-filter-repo  (or: pip install git-filter-repo)",
)

// EnsureFilterRepo verifies that filter-repo is callable. Done as a
// dedicated step rather than letting the rewrite fail mid-flight so the
// preflight phase can abort cleanly with an actionable error message.
func EnsureFilterRepo() error {
	// filter-repo is invoked as `git filter-repo`, but the actual binary
	// is `git-filter-repo` on PATH. LookPath the binary directly so we
	// fail fast with a dedicated install hint instead of relying on
	// git's "not a git command" error.
	if _, err := exec.LookPath("git-filter-repo"); err != nil {
		return ErrFilterRepoNotInstalled
	}
	return nil
}

// CapturedRemote holds the URL of an origin remote that we capture before
// running filter-repo and re-attach afterwards. filter-repo deliberately
// detaches origin to make accidental force-pushes harder; gk re-attaches
// it because the next thing the user wants to do is `git push --force`,
// which is impossible without a remote.
type CapturedRemote struct {
	Name string // typically "origin" — empty when no remote was configured
	URL  string
}

// CaptureOrigin reads the origin URL so RestoreOrigin can re-add it later.
// Missing origin is not an error; users who never had one (e.g. forking a
// local repo) shouldn't be blocked.
func CaptureOrigin(ctx context.Context, r git.Runner) (*CapturedRemote, error) {
	stdout, _, err := r.Run(ctx, "remote", "get-url", "origin")
	if err != nil {
		// `git remote get-url` exits non-zero when origin is missing —
		// treat that as "nothing to capture" rather than propagating.
		return &CapturedRemote{}, nil
	}
	url := strings.TrimSpace(string(stdout))
	if url == "" {
		return &CapturedRemote{}, nil
	}
	return &CapturedRemote{Name: "origin", URL: url}, nil
}

// RestoreOrigin re-adds the captured remote. No-op when CapturedRemote is
// empty. Idempotent: tries `git remote add` first and falls back to
// `git remote set-url` if a remote with the same name already exists
// (filter-repo's removal is not always complete).
func RestoreOrigin(ctx context.Context, r git.Runner, remote *CapturedRemote) error {
	if remote == nil || remote.Name == "" || remote.URL == "" {
		return nil
	}
	if _, _, err := r.Run(ctx, "remote", "add", remote.Name, remote.URL); err == nil {
		return nil
	}
	if _, _, err := r.Run(ctx, "remote", "set-url", remote.Name, remote.URL); err != nil {
		return fmt.Errorf("restore remote %s: %w", remote.Name, err)
	}
	return nil
}

// RunFilterRepo invokes `git filter-repo --invert-paths --path X --path Y
// --force` against the working repo. We always pass --force because gk
// has already gated the flow with its own confirmation; filter-repo's
// internal "fresh clone" check would just produce a confusing error
// after the user already typed `yes`.
//
// Stdout/stderr are streamed to the provided runner so the user sees
// filter-repo's progress output verbatim.
func RunFilterRepo(ctx context.Context, repoDir string, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("no paths to forget")
	}
	args := []string{"filter-repo", "--invert-paths", "--force"}
	for _, p := range paths {
		args = append(args, "--path", p)
	}
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // user-driven history rewrite
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git filter-repo: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
