package forget

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// AnalysisEntry summarises how much repo weight a single forget target
// would shed. Sizes are pre-pack — they reflect the on-the-wire bytes git
// would have to ship, not the compressed pack size, which is closer to
// what users perceive as "repo bloat".
type AnalysisEntry struct {
	Path         string // the target path as the user typed it
	UniqueBlobs  int    // distinct blob OIDs ever stored at Path across all refs
	TotalBytes   int64  // sum of all unique blobs
	LargestBytes int64  // size of the single biggest blob — useful for "what made this commit huge?"
}

// HumanBytes renders n with an SI-ish suffix (KiB, MiB, GiB) and one
// decimal where useful. Kept tiny on purpose — the alternative is pulling
// in dustin/go-humanize for one call site.
func HumanBytes(n int64) string {
	const (
		kib int64 = 1 << 10
		mib int64 = 1 << 20
		gib int64 = 1 << 30
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Analyze walks every commit on every ref and computes per-path blob
// statistics for the supplied targets. Implementation:
//
//  1. `git log --all --pretty=format: --raw --no-renames -- <path>` lists
//     every diff that touched <path>. Each output line carries the pre-
//     and post-image blob OIDs in fields 3 and 4.
//  2. The post-image OIDs are de-duplicated across all output lines and
//     piped through `git cat-file --batch-check='%(objectname) %(objectsize)'`
//     in a single batch so we make exactly one cat-file call per path
//     instead of N.
//
// Empty targets returns an empty slice with no error. Paths that never
// appeared in history yield an entry with zero blobs/bytes — surfacing
// "this path is not in history" is more useful than silently dropping it.
func Analyze(ctx context.Context, r git.Runner, repoDir string, paths []string) ([]AnalysisEntry, error) {
	out := make([]AnalysisEntry, 0, len(paths))
	for _, p := range paths {
		entry, err := analyzeOne(ctx, r, repoDir, p)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

func analyzeOne(ctx context.Context, r git.Runner, repoDir, path string) (AnalysisEntry, error) {
	entry := AnalysisEntry{Path: path}

	stdout, _, err := r.Run(ctx,
		"log", "--all", "--pretty=format:", "--raw", "--no-renames",
		"--", path,
	)
	if err != nil {
		return entry, fmt.Errorf("git log -- %s: %w", path, err)
	}

	// `git log --raw` lines look like:
	//   :100644 100644 abc1234... def5678... M       db/data.sqlite
	// Field 4 (the post-image OID) is the blob we want to count. The
	// pre-image (field 3) is already accounted for by an earlier commit.
	// First-creation diffs use 0000000... as field 3, which we naturally
	// skip.
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(stdout), "\n") {
		if !strings.HasPrefix(line, ":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		oid := fields[3]
		if oid == "" || strings.TrimLeft(oid, "0") == "" {
			// All-zero OID = file deleted in this diff. No blob to count.
			continue
		}
		seen[oid] = struct{}{}
	}

	if len(seen) == 0 {
		return entry, nil
	}

	sizes, err := blobSizes(ctx, repoDir, seen)
	if err != nil {
		return entry, err
	}
	for _, s := range sizes {
		entry.TotalBytes += s
		if s > entry.LargestBytes {
			entry.LargestBytes = s
		}
	}
	entry.UniqueBlobs = len(sizes)
	return entry, nil
}

// blobSizes returns OID → byte-size for the given set in a single
// cat-file --batch-check round-trip. Done as a fresh exec.Cmd rather
// than via git.Runner because Runner is captured-output only and we want
// to stream stdin (the OID list).
func blobSizes(ctx context.Context, repoDir string, oids map[string]struct{}) (map[string]int64, error) {
	if len(oids) == 0 {
		return map[string]int64{}, nil
	}

	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)") //nolint:gosec // user-driven analysis
	cmd.Dir = repoDir

	var stdin bytes.Buffer
	for oid := range oids {
		stdin.WriteString(oid)
		stdin.WriteByte('\n')
	}
	cmd.Stdin = &stdin

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git cat-file: %w\n%s", err, strings.TrimSpace(stdout.String()))
	}

	sizes := make(map[string]int64, len(oids))
	scanner := bufio.NewScanner(&stdout)
	// cat-file batch lines for missing OIDs are "<oid> missing" — skip.
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		size, perr := strconv.ParseInt(fields[2], 10, 64)
		if perr != nil {
			continue
		}
		sizes[fields[0]] = size
	}
	return sizes, nil
}
