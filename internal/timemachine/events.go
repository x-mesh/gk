// Package timemachine assembles the unified event stream that `gk timemachine`
// surfaces — HEAD reflog + per-branch reflogs + refs/gk/*-backup/ +
// (opt-in) stash and dangling commits.
//
// Callers stay in the internal/reflog and internal/gitsafe layers for raw
// access; this package adds the normalization + merge + dedup on top. The
// shape is designed so the eventual bubbletea TUI and the `--no-tui --json`
// NDJSON renderer consume identical structs.
package timemachine

import (
	"context"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/reflog"
)

// EventKind identifies the source pool an Event came from. The values are
// stable and exposed in the `--json` NDJSON output — changing the numeric
// ordering is fine, but the String() form is a user-visible contract.
type EventKind int

const (
	// KindReflog — an entry in HEAD or a branch reflog.
	KindReflog EventKind = iota + 1
	// KindBackup — a gk-managed backup ref under refs/gk/*-backup/.
	KindBackup
	// KindStash — an entry in the stash reflog (refs/stash).
	KindStash
	// KindDangling — a dangling commit surfaced by `git fsck --lost-found`.
	// These are unreachable from any ref and will be pruned by `git gc`
	// unless recovered; timemachine surfaces them so users can rescue.
	KindDangling
)

// String returns the stable lowercase name of the EventKind (used in NDJSON
// output and TUI filter chips).
func (k EventKind) String() string {
	switch k {
	case KindReflog:
		return "reflog"
	case KindBackup:
		return "backup"
	case KindStash:
		return "stash"
	case KindDangling:
		return "dangling"
	default:
		return "unknown"
	}
}

// Event is the unified timeline item shared by every consumer.
//
// Fields are always populated in the same way:
//   - Kind, Ref, OID, When, Subject: always set.
//   - OldOID, Action: reflog-only (empty for backups).
//   - BackupKind, Branch: backup-only (empty for reflog).
//
// Consumers that need raw source details can re-query the underlying
// reflog.Entry / gitsafe.BackupRef — this struct intentionally carries only
// the fields the TUI list pane and NDJSON renderer need.
type Event struct {
	Kind    EventKind
	Ref     string    // "HEAD@{3}" | "refs/gk/undo-backup/main/1700000000"
	OID     string    // full commit SHA
	OldOID  string    // reflog previous tip; empty for backups
	When    time.Time // committer date (reflog) or parsed unix (backup)
	Subject string    // one-line human summary

	// Reflog-only fields.
	Action string // classified action ("commit", "reset", "rebase", ...)

	// Backup-only fields.
	BackupKind string // "undo" | "wipe" | "timemachine"
	Branch     string // branch segment from the backup ref path
}

// FromReflogEntry normalizes a single reflog.Entry into an Event. The Ref
// field is set to the raw reflog ref string (e.g. "HEAD@{3}") and Subject
// falls back to the full Message when Summary is empty.
func FromReflogEntry(e reflog.Entry) Event {
	subject := e.Summary
	if strings.TrimSpace(subject) == "" {
		subject = e.Message
	}
	return Event{
		Kind:    KindReflog,
		Ref:     e.Ref,
		OID:     e.NewSHA,
		OldOID:  e.OldSHA,
		When:    e.When,
		Subject: subject,
		Action:  string(e.Action),
	}
}

// FromBackupRef normalizes a gitsafe.BackupRef into an Event.
func FromBackupRef(b gitsafe.BackupRef) Event {
	// Synthesize a short subject for list-view display.
	subject := b.Kind + "-backup @ " + b.Branch
	return Event{
		Kind:       KindBackup,
		Ref:        b.Ref,
		OID:        b.SHA,
		When:       b.When,
		Subject:    subject,
		BackupKind: b.Kind,
		Branch:     b.Branch,
	}
}

// ReadHEAD returns the HEAD reflog as a slice of Events, newest first.
//
// limit <= 0 is unlimited. On an empty reflog the returned slice is empty
// (not an error) — fresh repos may have zero entries before the first
// commit.
func ReadHEAD(ctx context.Context, r git.Runner, limit int) ([]Event, error) {
	entries, err := reflog.Read(ctx, r, "HEAD", limit)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(entries))
	for _, e := range entries {
		out = append(out, FromReflogEntry(e))
	}
	return out, nil
}
