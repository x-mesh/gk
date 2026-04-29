package git

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BackupRefPrefix is the namespace under which gk stores history-rewriting
// safety nets. Each ref is `refs/gk/backup/<branch>/<unix-ts>` and points at
// the pre-rewrite tip of <branch>. Recovery: `git reset --hard <ref>` or
// `git branch <name> <ref>`.
const BackupRefPrefix = "refs/gk/backup"

// CreateBackup writes a backup ref pointing to sha for branch and returns
// the ref name. The timestamp is encoded into the ref so multiple backups
// of the same branch don't collide.
func (c *Client) CreateBackup(ctx context.Context, branch, sha string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("create backup: empty branch")
	}
	if sha == "" {
		return "", fmt.Errorf("create backup: empty sha")
	}
	ref := fmt.Sprintf("%s/%s/%d", BackupRefPrefix, branch, time.Now().Unix())
	if _, _, err := c.R.Run(ctx, "update-ref", ref, sha); err != nil {
		return "", fmt.Errorf("create backup ref %q: %w", ref, err)
	}
	return ref, nil
}

// PruneBackups deletes backup refs for branch older than maxAge, while
// preserving the keepRecent newest ones regardless of age. Returns the
// number of refs deleted. Errors during enumeration or deletion are
// swallowed — pruning is best-effort and must never block a pull.
func (c *Client) PruneBackups(ctx context.Context, branch string, maxAge time.Duration, keepRecent int) int {
	if branch == "" {
		return 0
	}
	prefix := BackupRefPrefix + "/" + branch + "/"
	out, _, err := c.R.Run(ctx, "for-each-ref", "--format=%(refname)", prefix+"*")
	if err != nil {
		return 0
	}

	type entry struct {
		ref string
		ts  int64
	}
	var entries []entry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		suffix := strings.TrimPrefix(line, prefix)
		ts, perr := strconv.ParseInt(suffix, 10, 64)
		if perr != nil {
			continue
		}
		entries = append(entries, entry{ref: line, ts: ts})
	}
	if len(entries) == 0 {
		return 0
	}

	// Newest first.
	sort.Slice(entries, func(i, j int) bool { return entries[i].ts > entries[j].ts })

	cutoff := time.Now().Add(-maxAge).Unix()
	deleted := 0
	for i, e := range entries {
		if i < keepRecent {
			continue
		}
		if e.ts >= cutoff {
			continue
		}
		if _, _, derr := c.R.Run(ctx, "update-ref", "-d", e.ref); derr == nil {
			deleted++
		}
	}
	return deleted
}
