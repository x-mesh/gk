package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/git"
)

// TrackingMismatch describes a current branch whose upstream refspec
// (branch.<name>.merge = refs/heads/<other>) targets a differently
// named remote branch. Surfaced as a status footer because plain
// `git push` would silently push to <other>, not <branch>.
type TrackingMismatch struct {
	Branch       string
	RemoteBranch string
	Remote       string
}

// IsSet reports whether the struct describes an actual mismatch.
// detectTrackingMismatch returns a zero value for "no signal", so a
// nil-safe predicate keeps the call sites readable.
func (t TrackingMismatch) IsSet() bool {
	return t.Branch != "" && t.RemoteBranch != "" && t.Branch != t.RemoteBranch
}

// detectTrackingMismatch returns a populated TrackingMismatch when the
// local branch's upstream refspec points at a different remote branch
// name. Returns zero when:
//   - the branch is empty
//   - hasUpstream is false (no @{u} configured at all). Callers MUST
//     derive this from the same `git status --porcelain=v2 --branch`
//     parse used elsewhere in runStatusOnce — a separate `git config`
//     probe could race with concurrent edits and is also slower
//   - `branch.<name>.gk-tracking-ok=true` is set (per-branch suppression
//     for triangular workflows / personal forks where the asymmetry is
//     intentional)
//   - the merge refspec is not a refs/heads/* ref (tag refspec, etc.)
//   - the names match
//   - any git config lookup fails (silent, non-fatal — status must
//     still render)
//
// Implementation collapses the three keys we need (gk-tracking-ok,
// merge, remote) into a single `git config --get-regexp` spawn. This
// brings the tracking-detection cost down to one fork+exec per status
// regardless of which keys are set.
func detectTrackingMismatch(ctx context.Context, runner git.Runner, branch, fallbackRemote string, hasUpstream bool) TrackingMismatch {
	if branch == "" || !hasUpstream {
		return TrackingMismatch{}
	}
	pattern := "^branch\\." + regexp.QuoteMeta(branch) + "\\."
	out, _, err := runner.Run(ctx, "config", "--get-regexp", pattern)
	if err != nil {
		// Includes the "no matching keys" exit-1 case — fail open.
		return TrackingMismatch{}
	}

	prefix := "branch." + branch + "."
	var trackingOk, mergeRef, remoteName string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		key, val := line[:sp], strings.TrimSpace(line[sp+1:])
		suffix := strings.TrimPrefix(key, prefix)
		if suffix == key {
			// Defensive: regexp anchored on prefix so this shouldn't
			// hit, but be tolerant of unexpected git output.
			continue
		}
		switch suffix {
		case "gk-tracking-ok":
			trackingOk = val
		case "merge":
			mergeRef = val
		case "remote":
			remoteName = val
		}
	}

	if strings.EqualFold(trackingOk, "true") {
		return TrackingMismatch{}
	}
	const refsHeads = "refs/heads/"
	if !strings.HasPrefix(mergeRef, refsHeads) {
		// Tag refspec, unset, or unusual config — not our concern.
		return TrackingMismatch{}
	}
	remoteBranch := strings.TrimPrefix(mergeRef, refsHeads)
	if remoteBranch == "" || remoteBranch == branch {
		return TrackingMismatch{}
	}
	remote := fallbackRemote
	if remote == "" {
		remote = "origin"
	}
	if remoteName != "" {
		remote = remoteName
	}
	return TrackingMismatch{Branch: branch, RemoteBranch: remoteBranch, Remote: remote}
}

// renderTrackingMismatchFooter produces the multi-line warning + fix
// hint + suppression hint shown when a tracking mismatch is detected.
// Returns "" when t is not actually mismatched.
func renderTrackingMismatchFooter(t TrackingMismatch) string {
	if !t.IsSet() {
		return ""
	}
	yellow := color.New(color.FgYellow).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	header := fmt.Sprintf("%s tracking mismatch: local %s pushes to %s",
		yellow("⚠"),
		cyan("'"+t.Branch+"'"),
		cyan("'"+t.Remote+"/"+t.RemoteBranch+"'"),
	)
	fix := faint(fmt.Sprintf("  fix: git branch --set-upstream-to=%s/%s %s   (or `git push -u %s %s` to migrate)",
		t.Remote, t.Branch, t.Branch,
		t.Remote, t.Branch,
	))
	suppress := faint(fmt.Sprintf("  intentional? git config branch.%s.gk-tracking-ok true", t.Branch))
	return header + "\n" + fix + "\n" + suppress
}
