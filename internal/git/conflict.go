package git

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RebaseConflictInfo describes the state of a paused rebase. It is
// populated from .git/rebase-merge metadata plus a couple of cheap
// porcelain calls. Callers use it to render a richer error than git's
// terse "could not apply" — for example, the list of files that still
// have conflict markers, the commit currently being applied, and how
// far through the rebase we are.
type RebaseConflictInfo struct {
	StoppedSHA  string   // SHA of the commit that stopped the rebase
	StoppedSubj string   // first-line subject of that commit
	Done        int      // picks completed (1-indexed; matches msgnum)
	Total       int      // total picks in the rebase plan
	Unmerged    []string // files still carrying conflict markers (XY=U*)
	Staged      []string // files already staged (auto-merged or resolved)
}

// Remaining returns the number of picks left after the current one,
// or 0 when total is unknown.
func (r *RebaseConflictInfo) Remaining() int {
	if r.Total <= r.Done {
		return 0
	}
	return r.Total - r.Done
}

// RebaseConflictStatus inspects the working tree and .git/ to describe
// a paused rebase. Returns (nil, nil) when no rebase is in progress.
// All git-side errors are tolerated — best-effort population so the
// caller can still print whatever fields it could collect.
func (c *Client) RebaseConflictStatus(ctx context.Context) (*RebaseConflictInfo, error) {
	gitDir, err := resolveGitDir(ctx, c.R)
	if err != nil {
		return nil, err
	}

	// rebase-merge is the modern interactive/merge-strategy rebase.
	// rebase-apply is the legacy am-based rebase used by older git
	// versions or when --apply is forced; the metadata layout differs
	// (next/last vs msgnum/end) but both expose the same idea.
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		dir := filepath.Join(gitDir, name)
		if _, statErr := os.Stat(dir); statErr != nil {
			continue
		}
		info := &RebaseConflictInfo{}
		populateRebaseDir(info, dir, name)
		populateConflictFiles(ctx, c.R, info)
		populateStoppedSubject(ctx, c.R, info)
		return info, nil
	}

	return nil, nil
}

func populateRebaseDir(info *RebaseConflictInfo, dir, kind string) {
	if data, err := os.ReadFile(filepath.Join(dir, "stopped-sha")); err == nil {
		info.StoppedSHA = strings.TrimSpace(string(data))
	}
	if kind == "rebase-merge" {
		info.Done = readIntFile(filepath.Join(dir, "msgnum"))
		info.Total = readIntFile(filepath.Join(dir, "end"))
	} else {
		// rebase-apply uses next/last with the same semantics.
		info.Done = readIntFile(filepath.Join(dir, "next"))
		info.Total = readIntFile(filepath.Join(dir, "last"))
	}
}

func populateConflictFiles(ctx context.Context, r Runner, info *RebaseConflictInfo) {
	// Unmerged paths — files with conflict markers waiting on the user.
	if out, _, err := r.Run(ctx, "diff", "--name-only", "--diff-filter=U"); err == nil {
		info.Unmerged = splitLines(string(out))
	}
	// Auto-merged or already-resolved paths sitting in the index.
	if out, _, err := r.Run(ctx, "diff", "--name-only", "--cached", "--diff-filter=ACMRT"); err == nil {
		info.Staged = splitLines(string(out))
	}
}

func populateStoppedSubject(ctx context.Context, r Runner, info *RebaseConflictInfo) {
	if info.StoppedSHA == "" {
		return
	}
	if out, _, err := r.Run(ctx, "log", "-1", "--format=%s", info.StoppedSHA); err == nil {
		info.StoppedSubj = strings.TrimSpace(string(out))
	}
}

func resolveGitDir(ctx context.Context, r Runner) (string, error) {
	out, _, err := r.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func readIntFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// LatestBackupRef returns the most recent gk backup ref for branch, or
// "" when none exists. Caller can show this so the user knows where to
// jump back to via `git reset --hard <ref>` if they want to bail.
func (c *Client) LatestBackupRef(ctx context.Context, branch string) string {
	if branch == "" {
		return ""
	}
	prefix := BackupRefPrefix + "/" + branch + "/"
	out, _, err := c.R.Run(ctx,
		"for-each-ref", "--sort=-refname", "--count=1",
		"--format=%(refname)", prefix+"*")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
