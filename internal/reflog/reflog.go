package reflog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// Entry is a single reflog record.
type Entry struct {
	OldSHA   string    // full hash before the action (empty in v0.2.0)
	NewSHA   string    // full hash after
	ShortNew string    // abbreviated new hash (display)
	Action   Action    // classified action type
	Message  string    // raw message (e.g., "reset: moving to HEAD~1")
	Summary  string    // short post-colon part, or full message if no colon
	When     time.Time // committer date (unix timestamp from %at)
	Ref      string    // e.g., "HEAD@{0}" or "refs/heads/main@{0}"
}

const reflogFormat = "--format=%H%x00%h%x00%gD%x00%gs%x00%at%x1e"

// Read runs `git reflog show --format=<raw> <ref>` via the Runner
// and returns parsed entries. limit <= 0 means unlimited.
// If ref is empty, defaults to "HEAD".
func Read(ctx context.Context, r git.Runner, ref string, limit int) ([]Entry, error) {
	if ref == "" {
		ref = "HEAD"
	}

	args := []string{"reflog", "show", reflogFormat, ref}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}

	stdout, _, err := r.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("reflog: git reflog show: %w", err)
	}

	return Parse(stdout)
}

// Parse parses raw git-reflog output produced by the format:
//
//	--format=%H%x00%h%x00%gD%x00%gs%x00%at%x1e
//
// Fields are NUL-delimited (\x00), records are RS-delimited (\x1e).
// Records with fewer than 5 fields are silently skipped.
func Parse(raw []byte) ([]Entry, error) {
	records := strings.Split(string(raw), "\x1e")
	entries := make([]Entry, 0, len(records))

	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}

		fields := strings.SplitN(rec, "\x00", 5)
		if len(fields) < 5 {
			// malformed record: skip silently
			continue
		}

		newSHA := strings.TrimSpace(fields[0])
		shortNew := strings.TrimSpace(fields[1])
		ref := strings.TrimSpace(fields[2])
		message := strings.TrimSpace(fields[3])
		atRaw := strings.TrimSpace(fields[4])

		unixSec, err := strconv.ParseInt(atRaw, 10, 64)
		if err != nil {
			// unparseable timestamp: skip silently
			continue
		}

		summary := message
		if idx := strings.Index(message, ": "); idx != -1 {
			summary = message[idx+2:]
		}

		entries = append(entries, Entry{
			OldSHA:   "",
			NewSHA:   newSHA,
			ShortNew: shortNew,
			Action:   classifyAction(message),
			Message:  message,
			Summary:  summary,
			When:     time.Unix(unixSec, 0),
			Ref:      ref,
		})
	}

	return entries, nil
}
