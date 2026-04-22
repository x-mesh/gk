package gitsafe

import (
	"context"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestParseBackupRef(t *testing.T) {
	tests := []struct {
		ref      string
		wantOK   bool
		wantKind string
		wantBr   string
		wantUnix int64
	}{
		{"refs/gk/undo-backup/main/1700000000", true, "undo", "main", 1700000000},
		{"refs/gk/wipe-backup/feat-x/1700000001", true, "wipe", "feat-x", 1700000001},
		{"refs/gk/timemachine-backup/detached/1700000002", true, "timemachine", "detached", 1700000002},
		// Nested branch segment (rare but spec-compatible — last slash is the unix split).
		{"refs/gk/undo-backup/feat/x/1700000003", true, "undo", "feat/x", 1700000003},
		// Not a backup ref — unexpected kind suffix.
		{"refs/gk/other/main/1700000000", false, "", "", 0},
		// Missing unix segment.
		{"refs/gk/undo-backup/main", false, "", "", 0},
		// Wrong prefix.
		{"refs/heads/main", false, "", "", 0},
		// Empty branch segment.
		{"refs/gk/undo-backup//1700000000", false, "", "", 0},
	}

	for _, tc := range tests {
		t.Run(tc.ref, func(t *testing.T) {
			got, ok := parseBackupRef(tc.ref)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Branch != tc.wantBr {
				t.Errorf("Branch = %q, want %q", got.Branch, tc.wantBr)
			}
			if got.When.Unix() != tc.wantUnix {
				t.Errorf("When.Unix() = %d, want %d", got.When.Unix(), tc.wantUnix)
			}
		})
	}
}

func TestListBackups_EmptyRepo(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")

	refs, err := ListBackups(context.Background(), &git.ExecRunner{Dir: repo.Dir})
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("fresh repo returned %d backups; want 0", len(refs))
	}
}

func TestListBackups_ReturnsNewestFirst(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	// Create three backups with increasing timestamps via the Restorer.
	mkBackup := func(kind string, ts int64) {
		r := NewRestorer(runner, func() time.Time { return time.Unix(ts, 0) }, kind)
		if _, err := r.Backup(ctx, "main"); err != nil {
			t.Fatalf("Backup(%s, %d): %v", kind, ts, err)
		}
	}
	mkBackup("undo", 1700000000)
	mkBackup("wipe", 1700000001)
	mkBackup("timemachine", 1700000002)

	refs, err := ListBackups(ctx, runner)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("got %d backups, want 3 — refs: %+v", len(refs), refs)
	}

	// Newest first: timemachine (1700000002) → wipe → undo.
	wantKinds := []string{"timemachine", "wipe", "undo"}
	for i, want := range wantKinds {
		if refs[i].Kind != want {
			t.Errorf("refs[%d].Kind = %q, want %q", i, refs[i].Kind, want)
		}
	}

	// Each ref's SHA must match the HEAD SHA (all were created at the same HEAD).
	headSHA := repo.RunGit("rev-parse", "HEAD")
	for _, r := range refs {
		if r.SHA != headSHA {
			t.Errorf("ref %q SHA = %q, want HEAD %q", r.Ref, r.SHA, headSHA)
		}
	}
}
