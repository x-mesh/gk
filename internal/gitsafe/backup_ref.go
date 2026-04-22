// Package gitsafe centralizes safety-critical git primitives shared across
// commands that move HEAD or rewrite refs (undo, wipe, timemachine).
//
// At present this package only hosts the backup ref naming helper (SHARED-07).
// See docs/roadmap-v2.md for the full set of planned extractions.
package gitsafe

import (
	"fmt"
	"strings"
	"time"
)

// BackupRefName returns the canonical backup ref path used by every gk command
// that creates a recovery anchor before moving HEAD:
//
//	refs/gk/<kind>-backup/<safeBranch>/<unix>
//
// kind is a short identifier for the originating command ("undo", "wipe",
// "timemachine"). An empty branch (detached HEAD) is rendered as "detached"
// and any "/" characters in the branch name are rewritten to "-" so the
// segment is a single path component.
//
// The output format is part of gk's recovery UX — `gk undo` and `gk wipe`
// print this path to stdout and users copy-paste it into
// `git reset --hard <ref>` to recover. Do not change the format without
// updating the recovery docs in internal/cli/undo.go and wipe.go.
func BackupRefName(kind, branch string, when time.Time) string {
	return fmt.Sprintf("refs/gk/%s-backup/%s/%d", kind, SanitizeBranchSegment(branch), when.Unix())
}

// SanitizeBranchSegment converts a branch name into a safe single-component
// path segment for use inside a ref name. Detached HEAD (empty string) maps
// to "detached"; any "/" is replaced with "-".
func SanitizeBranchSegment(branch string) string {
	if branch == "" {
		return "detached"
	}
	return strings.ReplaceAll(branch, "/", "-")
}
