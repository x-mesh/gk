package cli

import (
	"errors"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// errorCodeFromError classifies an error into the stable machine-readable
// vocabulary the agent envelope exposes as `error.code`. Classification is
// top-down (message/stderr pattern matching plus the existing predicate
// helpers) rather than retrofitting a code onto each of the ~550 error
// construction sites — the codes agents actually branch on are a handful of
// high-confidence families, and anything else is honestly "unknown".
//
// The vocabulary is append-only: removing or renaming a code is a breaking
// change for agent tooling.
func errorCodeFromError(err error) string {
	if err == nil {
		return ""
	}
	// An explicit code (e.g. from WithBlocked) wins: it lets a localized
	// message still map to a stable code without a brittle prose match.
	if c := codeFrom(err); c != "" {
		return c
	}
	if isNotAGitRepoError(err) {
		return "not-a-repo"
	}
	if isCommitGraphCorruptError(err) {
		return "commit-graph-corrupt"
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		return "conflict"
	}

	msg := strings.ToLower(err.Error())
	var exitErr *git.ExitError
	if errors.As(err, &exitErr) {
		msg += " " + strings.ToLower(exitErr.Stderr)
	}

	switch {
	case strings.Contains(msg, "invalid reference") ||
		strings.Contains(msg, "did not match any") ||
		strings.Contains(msg, "unknown revision"):
		return "branch-not-found"
	case strings.Contains(msg, "diverged"):
		return "diverged"
	case strings.Contains(msg, "conflict"):
		return "conflict"
	case strings.Contains(msg, "uncommitted changes") ||
		strings.Contains(msg, "working tree is dirty"):
		return "dirty-tree"
	case strings.Contains(msg, "in progress"):
		return "in-progress-op"
	case strings.Contains(msg, "already exists") &&
		(strings.Contains(msg, "tag ") || strings.Contains(msg, "refs/tags")):
		return "tag-exists"
	case strings.Contains(msg, "secret"):
		return "secret-found"
	case strings.Contains(msg, "no upstream"):
		return "no-upstream"
	case strings.Contains(msg, "preflight failed"):
		return "preflight-failed"
	case strings.Contains(msg, "requires --dry-run"):
		return "json-needs-dry-run"
	case strings.Contains(msg, "config error") ||
		strings.Contains(msg, "알 수 없는 키") ||
		(strings.Contains(msg, "config") && strings.Contains(msg, "key")):
		return "config-invalid"
	}
	return "unknown"
}
