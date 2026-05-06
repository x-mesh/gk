package forget

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// BackupRefPrefix groups all forget-related backup refs under a single
// reflog namespace so users can list them with one for-each-ref query
// and clean them up with `git update-ref -d` once the rewrite is
// confirmed-good.
const BackupRefPrefix = "refs/gk/forget-backup"

// Backup captures the state of every branch and tag immediately before a
// forget rewrite, recorded both as a sibling backup ref and as a flat
// text manifest under .git/gk/ for offline rollback.
//
// The dual format is deliberate:
//
//   - Backup refs (refs/gk/forget-backup/<unix>/<original>) survive a
//     normal `git gc`, are visible in `gk timemachine list`, and let
//     users rollback a single branch with `git update-ref` without
//     touching the rest of the repo.
//   - The manifest .git/gk/forget-backup-<unix>.txt survives even an
//     accidental `git update-ref -d refs/gk/forget-backup/...` and is
//     trivial to apply with `git update-ref --stdin`.
type Backup struct {
	Stamp     int64 // unix seconds — also embedded in ref namespace and manifest filename
	Refs      []RefSnapshot
	Manifest  string // absolute path to the text manifest
	RefPrefix string // refs/gk/forget-backup/<stamp>
}

// RefSnapshot is one (refname, sha) pair captured pre-rewrite. We do not
// chase tag objects to commits — annotated tags should round-trip as-is,
// and a lightweight tag is just a ref pointing at a commit either way.
type RefSnapshot struct {
	Name string // e.g. "refs/heads/main"
	SHA  string
}

// CreateBackup snapshots refs/heads, refs/tags, and HEAD into both a ref
// namespace and a text manifest. Returns the populated Backup struct so
// the caller can surface paths in CLI output.
//
// gitDir is the absolute path to the repo's .git directory; the manifest
// lands at <gitDir>/gk/forget-backup-<unix>.txt.
func CreateBackup(ctx context.Context, r git.Runner, gitDir string, now time.Time) (*Backup, error) {
	stamp := now.Unix()
	prefix := fmt.Sprintf("%s/%d", BackupRefPrefix, stamp)

	stdout, _, err := r.Run(ctx,
		"for-each-ref",
		"--format=%(refname) %(objectname)",
		"refs/heads",
		"refs/tags",
	)
	if err != nil {
		return nil, fmt.Errorf("for-each-ref: %w", err)
	}

	var snaps []RefSnapshot
	for _, line := range strings.Split(string(stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		snaps = append(snaps, RefSnapshot{Name: fields[0], SHA: fields[1]})
	}

	// HEAD is captured separately as a regular ref since it can reference
	// a detached commit that is not under refs/heads.
	if head, _, err := r.Run(ctx, "rev-parse", "HEAD"); err == nil {
		sha := strings.TrimSpace(string(head))
		if sha != "" {
			snaps = append(snaps, RefSnapshot{Name: "HEAD", SHA: sha})
		}
	}

	// Write the parallel backup ref namespace. We deliberately keep the
	// original refname intact so users see "refs/gk/forget-backup/<ts>/refs/heads/main"
	// — long, but trivially mappable to the original.
	for _, s := range snaps {
		if s.Name == "HEAD" {
			// Skip HEAD here; it lives in the manifest but not the ref
			// namespace (a HEAD-named ref under refs/gk/... is confusing).
			continue
		}
		ref := prefix + "/" + s.Name
		if _, _, err := r.Run(ctx, "update-ref", ref, s.SHA); err != nil {
			return nil, fmt.Errorf("write backup ref %s: %w", ref, err)
		}
	}

	manifest := filepath.Join(gitDir, "gk", fmt.Sprintf("forget-backup-%d.txt", stamp))
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		return nil, fmt.Errorf("create gk manifest dir: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("# gk forget backup — apply with `git update-ref --stdin`-compatible script:\n")
	sb.WriteString("#   while read ref sha; do git update-ref \"$ref\" \"$sha\"; done < this-file\n")
	for _, s := range snaps {
		sb.WriteString(s.Name)
		sb.WriteByte(' ')
		sb.WriteString(s.SHA)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(manifest, []byte(sb.String()), 0o644); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	return &Backup{
		Stamp:     stamp,
		Refs:      snaps,
		Manifest:  manifest,
		RefPrefix: prefix,
	}, nil
}
