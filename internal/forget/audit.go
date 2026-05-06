package forget

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// AuditEntry summarises one bucket in a repo-wide history audit.
//
// Buckets are formed by truncating each blob's path to the requested
// depth, so an entry's Path is either a directory prefix (e.g.
// "node_modules") or — when Depth was 0 — an individual file. Unique
// blob counts are within the bucket, so a directory that committed the
// same artefact a hundred times still shows UniqueBlobs=N where N is
// the number of distinct OIDs, not the number of commits that touched
// it.
type AuditEntry struct {
	Path         string
	UniqueBlobs  int
	TotalBytes   int64
	LargestBytes int64
	// InHEAD is true when at least one path under this bucket still
	// exists in the current HEAD tree. Buckets with InHEAD=false are
	// "history only" — already deleted from the working tree but still
	// inflating clones. These are the highest-leverage forget targets.
	InHEAD bool
}

// Audit walks every reachable object on every ref and produces per-
// bucket size/blob statistics. The implementation streams
// `git rev-list --all --objects | git cat-file --batch-check` so that
// even multi-million-object repos do not need to materialise the full
// listing in memory.
//
// `depth` controls bucket granularity:
//   - 0    every blob path is its own bucket (individual files)
//   - 1    top-level directory only (e.g. "node_modules")
//   - N>=2 first N path segments
//
// `top` caps the returned slice; entries are sorted by TotalBytes
// descending so the heaviest contributors come first. Pass 0 for
// "return everything".
func Audit(ctx context.Context, r git.Runner, repoDir string, depth, top int) ([]AuditEntry, error) {
	headPaths, err := headTreePaths(ctx, r)
	if err != nil {
		return nil, err
	}

	type bucket struct {
		oids map[string]int64
		max  int64
	}
	buckets := map[string]*bucket{}

	rows, cleanup, err := streamObjectStats(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	for rows.Scan() {
		// Each line is "<type> <size> <oid> <rest>". %(rest) carries
		// the path that rev-list emitted, which is empty for commits
		// and trees.
		parts := strings.SplitN(rows.Text(), " ", 4)
		if len(parts) < 4 || parts[0] != "blob" {
			continue
		}
		size, perr := strconv.ParseInt(parts[1], 10, 64)
		if perr != nil {
			continue
		}
		oid := parts[2]
		path := parts[3]
		if path == "" {
			continue
		}
		key := bucketKey(path, depth)
		b := buckets[key]
		if b == nil {
			b = &bucket{oids: make(map[string]int64)}
			buckets[key] = b
		}
		// First-write-wins on size: rev-list output is deduped by
		// (oid, path) so a given oid only appears once per bucket
		// already. Storing size keyed by oid still defends against
		// the rare case where two paths in the same bucket share an
		// oid (cp src dst) by counting that blob's bytes only once.
		if _, dup := b.oids[oid]; !dup {
			b.oids[oid] = size
		}
		if size > b.max {
			b.max = size
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan object stream: %w", err)
	}

	out := make([]AuditEntry, 0, len(buckets))
	for path, b := range buckets {
		var total int64
		for _, sz := range b.oids {
			total += sz
		}
		out = append(out, AuditEntry{
			Path:         path,
			UniqueBlobs:  len(b.oids),
			TotalBytes:   total,
			LargestBytes: b.max,
			InHEAD:       prefixHasHEAD(path, depth, headPaths),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalBytes != out[j].TotalBytes {
			return out[i].TotalBytes > out[j].TotalBytes
		}
		return out[i].Path < out[j].Path
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out, nil
}

// bucketKey collapses a path into its bucket prefix. depth<=0 returns
// the path verbatim (file-grain audit). For non-zero depth we keep
// the first N segments and discard the rest, so
// "src/foo/bar/baz.go" with depth=2 collapses to "src/foo".
func bucketKey(path string, depth int) string {
	if depth <= 0 {
		return path
	}
	segments := strings.Split(path, "/")
	if len(segments) <= depth {
		return path
	}
	return strings.Join(segments[:depth], "/")
}

// prefixHasHEAD reports whether any path in the HEAD tree falls under
// the given bucket key. For file-grain (depth=0) we test exact match;
// for directory buckets we test prefix.
func prefixHasHEAD(key string, depth int, head map[string]struct{}) bool {
	if depth <= 0 {
		_, ok := head[key]
		return ok
	}
	prefix := key + "/"
	for p := range head {
		if p == key || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// headTreePaths returns the set of paths in the HEAD tree, used to
// distinguish "history only" buckets from "still on disk" ones.
//
// `git ls-tree -r --name-only HEAD -z` is the cheapest way to get this
// — porcelain `git ls-files` would also work but skips files added
// via add --intent-to-add. ls-tree is faithful to the committed tree.
func headTreePaths(ctx context.Context, r git.Runner) (map[string]struct{}, error) {
	out, _, err := r.Run(ctx, "ls-tree", "-r", "--name-only", "-z", "HEAD")
	if err != nil {
		// Empty repo / no commits: no HEAD tree. Return empty set so
		// every bucket reports InHEAD=false rather than failing the
		// whole audit.
		return map[string]struct{}{}, nil
	}
	set := make(map[string]struct{})
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			set[p] = struct{}{}
		}
	}
	return set, nil
}

// streamObjectStats wires `git rev-list --all --objects | git cat-file
// --batch-check='%(objecttype) %(objectsize) %(objectname) %(rest)'`
// and returns a Scanner over the cat-file stdout plus a cleanup that
// drains both processes. We run them as raw exec.Cmds so the pipe is
// real (zero-copy at the OS level) — going through git.Runner here
// would buffer the rev-list output into memory before spawning
// cat-file, which is exactly what we are trying to avoid for huge
// repos.
func streamObjectStats(ctx context.Context, repoDir string) (*bufio.Scanner, func(), error) {
	revList := exec.CommandContext(ctx, "git", "rev-list", "--all", "--objects") //nolint:gosec // user-driven audit
	revList.Dir = repoDir
	catFile := exec.CommandContext(ctx, "git", "cat-file", //nolint:gosec
		"--batch-check=%(objecttype) %(objectsize) %(objectname) %(rest)")
	catFile.Dir = repoDir

	pipeR, pipeW := io.Pipe()
	revList.Stdout = pipeW
	catFile.Stdin = pipeR

	stdout, err := catFile.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}

	if err := catFile.Start(); err != nil {
		return nil, nil, fmt.Errorf("start cat-file: %w", err)
	}
	if err := revList.Start(); err != nil {
		_ = catFile.Process.Kill()
		return nil, nil, fmt.Errorf("start rev-list: %w", err)
	}

	// Once rev-list finishes, close the pipe so cat-file sees EOF and
	// exits cleanly. Done in a goroutine because cat-file's Scanner
	// runs on the calling goroutine.
	go func() {
		_ = revList.Wait()
		_ = pipeW.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	// cat-file emits one line per input record; default 64 KiB buffer
	// is enough for any realistic blob path.
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<20)

	cleanup := func() {
		_ = pipeR.Close()
		_ = catFile.Wait()
	}
	return scanner, cleanup, nil
}
