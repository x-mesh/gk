package gitsafe

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// BackupRef describes a single gk-managed backup ref as returned by
// ListBackups. The fields are parsed from the ref path
// (refs/gk/<kind>-backup/<branch>/<unix>) and the pointed-at commit SHA.
type BackupRef struct {
	Ref    string    // full ref path, e.g. refs/gk/undo-backup/main/1700000000
	Kind   string    // "undo" | "wipe" | "timemachine"
	Branch string    // branch segment as stored (NOT de-sanitized); empty when "detached"
	When   time.Time // parsed from the unix segment; zero when unparseable
	SHA    string    // commit the ref points at
}

// ListBackups scans refs/gk/*-backup/ and returns every backup ref known to
// this repo, newest first. Empty output is not an error — fresh repos simply
// have no backups yet.
//
// The ref format `refs/gk/<kind>-backup/<branch>/<unix>` is produced by
// BackupRefName; this function inverts that format. Paths that do not match
// are skipped silently (defensive — users can create their own refs under
// refs/gk/ without breaking the lister).
func ListBackups(ctx context.Context, r git.Runner) ([]BackupRef, error) {
	out, stderr, err := r.Run(ctx,
		"for-each-ref",
		"--format=%(refname) %(objectname)",
		"refs/gk/undo-backup/",
		"refs/gk/wipe-backup/",
		"refs/gk/timemachine-backup/",
		"refs/gk/forget-backup/",
	)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), err)
	}

	var refs []BackupRef
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		ref, sha := parts[0], parts[1]
		b, ok := parseBackupRef(ref)
		if !ok {
			continue
		}
		b.SHA = sha
		refs = append(refs, b)
	}

	// Newest first. zero When values sort to the end.
	sort.Slice(refs, func(i, j int) bool {
		switch {
		case refs[i].When.IsZero() && !refs[j].When.IsZero():
			return false
		case !refs[i].When.IsZero() && refs[j].When.IsZero():
			return true
		default:
			return refs[i].When.After(refs[j].When)
		}
	})

	return refs, nil
}

// parseBackupRef extracts (kind, branch, when) from a ref path of the form
// refs/gk/<kind>-backup/<branch>/<unix>. Returns (zero, false) on any shape
// mismatch so callers can skip unknown refs without failing.
func parseBackupRef(ref string) (BackupRef, bool) {
	const prefix = "refs/gk/"
	if !strings.HasPrefix(ref, prefix) {
		return BackupRef{}, false
	}
	rest := ref[len(prefix):]

	// Split off the kind segment: "<kind>-backup/..."
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return BackupRef{}, false
	}
	kindSeg := rest[:slash]
	rest = rest[slash+1:]

	if !strings.HasSuffix(kindSeg, "-backup") {
		return BackupRef{}, false
	}
	kind := strings.TrimSuffix(kindSeg, "-backup")
	if kind == "" {
		return BackupRef{}, false
	}

	// Remaining rest = "<branch>/<unix>" — unix is the last segment.
	lastSlash := strings.LastIndexByte(rest, '/')
	if lastSlash < 0 {
		return BackupRef{}, false
	}
	branch := rest[:lastSlash]
	unixStr := rest[lastSlash+1:]
	if branch == "" || unixStr == "" {
		return BackupRef{}, false
	}

	br := BackupRef{Ref: ref, Kind: kind, Branch: branch}
	if n, err := strconv.ParseInt(unixStr, 10, 64); err == nil {
		br.When = time.Unix(n, 0)
	}
	return br, true
}
