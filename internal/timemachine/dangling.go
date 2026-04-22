package timemachine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// DanglingOptions customize ReadDangling. The zero value is "no cap, no timeout,
// default branch-less HEAD flag". Cap <= 0 disables the cap; any timeout is
// carried in the ctx the caller passes in.
type DanglingOptions struct {
	// Cap limits how many dangling commits are returned. 0 means unlimited.
	// The SYN_v2.1 default is 500 — wired at the CLI layer, not here.
	Cap int
}

// ReadDangling surfaces every commit unreachable from any ref
// (`git fsck --lost-found --no-reflogs`) as a KindDangling event, with the
// commit's committer-date and subject resolved via `git show`.
//
// Performance warning: fsck is O(objects) and can take multiple seconds on
// large repos. Callers (TUI, CLI) should gate this source behind explicit
// opt-in (`--include-dangling`) and should pass a timeout-bearing context.
func ReadDangling(ctx context.Context, r git.Runner, opts DanglingOptions) ([]Event, error) {
	out, stderr, err := r.Run(ctx, "fsck", "--lost-found", "--no-reflogs")
	if err != nil {
		// fsck exits non-zero on integrity issues too — prefer to surface
		// stderr so users see real corruption warnings.
		return nil, fmt.Errorf("git fsck: %s: %w", strings.TrimSpace(string(stderr)), err)
	}

	// Parse lines of the form:
	//     dangling commit <sha>
	//     dangling blob <sha>
	//     dangling tag <sha>
	// Only commits matter here. Tags/blobs stay out of the timeline.
	var shas []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "dangling commit ") {
			continue
		}
		sha := strings.TrimPrefix(line, "dangling commit ")
		if sha == "" {
			continue
		}
		shas = append(shas, sha)
		if opts.Cap > 0 && len(shas) >= opts.Cap {
			break
		}
	}

	events := make([]Event, 0, len(shas))
	for _, sha := range shas {
		ev, err := resolveDanglingEvent(ctx, r, sha)
		if err != nil {
			// Commit may have been pruned between fsck and show — skip.
			continue
		}
		events = append(events, ev)
	}
	return events, nil
}

// resolveDanglingEvent fetches subject + committer-date for a dangling commit
// and returns it as a KindDangling Event. The short SHA is used as Ref
// because dangling commits are, by definition, unnamed.
func resolveDanglingEvent(ctx context.Context, r git.Runner, sha string) (Event, error) {
	out, stderr, err := r.Run(ctx, "show", "--no-patch", "--format=%ct%x00%s", sha)
	if err != nil {
		return Event{}, fmt.Errorf("git show %s: %s: %w",
			sha, strings.TrimSpace(string(stderr)), err)
	}
	line := strings.TrimSpace(string(out))
	parts := strings.SplitN(line, "\x00", 2)
	if len(parts) < 2 {
		return Event{}, fmt.Errorf("unexpected show output for %s: %q", sha, line)
	}
	when := time.Time{}
	if n, perr := strconv.ParseInt(parts[0], 10, 64); perr == nil {
		when = time.Unix(n, 0)
	}
	return Event{
		Kind:    KindDangling,
		Ref:     sha, // dangling commits have no ref name; use the SHA itself
		OID:     sha,
		When:    when,
		Subject: parts[1],
	}, nil
}
