package git

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCreateBackup_WritesRefWithTimestamp(t *testing.T) {
	fake := &FakeRunner{}
	c := NewClient(fake)

	ref, err := c.CreateBackup(context.Background(), "main", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(ref, "refs/gk/backup/main/") {
		t.Errorf("ref = %q, want prefix refs/gk/backup/main/", ref)
	}
	// Suffix should be parseable as a unix ts.
	suffix := strings.TrimPrefix(ref, "refs/gk/backup/main/")
	if suffix == "" {
		t.Errorf("ref %q missing timestamp suffix", ref)
	}

	// Check the right git command was invoked.
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 git call, got %d", len(fake.Calls))
	}
	got := strings.Join(fake.Calls[0].Args, " ")
	if !strings.HasPrefix(got, "update-ref refs/gk/backup/main/") {
		t.Errorf("call = %q, want update-ref refs/gk/backup/main/...", got)
	}
	if !strings.HasSuffix(got, " abc123") {
		t.Errorf("call = %q, want trailing sha", got)
	}
}

func TestCreateBackup_RejectsEmptyInputs(t *testing.T) {
	c := NewClient(&FakeRunner{})
	if _, err := c.CreateBackup(context.Background(), "", "abc"); err == nil {
		t.Error("expected error for empty branch")
	}
	if _, err := c.CreateBackup(context.Background(), "main", ""); err == nil {
		t.Error("expected error for empty sha")
	}
}

func TestPruneBackups_KeepsRecentRegardlessOfAge(t *testing.T) {
	now := time.Now().Unix()
	day := int64(86400)
	// 5 backups, all >30 days old. keepRecent=2 → 2 newest survive.
	refs := []int64{
		now - 31*day,
		now - 32*day,
		now - 33*day,
		now - 34*day,
		now - 35*day,
	}
	listing := buildRefListing("main", refs)
	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"for-each-ref --format=%(refname) refs/gk/backup/main/*": {Stdout: listing},
		},
	}
	c := NewClient(fake)

	deleted := c.PruneBackups(context.Background(), "main", 30*24*time.Hour, 2)
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}
}

func TestPruneBackups_KeepsFreshOnesEvenBeyondKeep(t *testing.T) {
	now := time.Now().Unix()
	day := int64(86400)
	// Mix: 3 fresh (<30d) + 4 stale (>30d). keepRecent=2 → all 3 fresh survive
	// (cutoff filter), and 2 of stale are kept by keepRecent... wait no:
	// keepRecent applies to the newest 2 overall (which are fresh anyway).
	// So we keep all 3 fresh + 0 stale = 3, delete 4.
	refs := []int64{
		now - 1*day,
		now - 2*day,
		now - 3*day,
		now - 31*day,
		now - 32*day,
		now - 33*day,
		now - 34*day,
	}
	listing := buildRefListing("main", refs)
	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"for-each-ref --format=%(refname) refs/gk/backup/main/*": {Stdout: listing},
		},
	}
	c := NewClient(fake)

	deleted := c.PruneBackups(context.Background(), "main", 30*24*time.Hour, 2)
	if deleted != 4 {
		t.Errorf("deleted = %d, want 4 (only stale beyond keepRecent)", deleted)
	}
}

func TestPruneBackups_NoBackupsNoOp(t *testing.T) {
	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"for-each-ref --format=%(refname) refs/gk/backup/main/*": {Stdout: ""},
		},
	}
	c := NewClient(fake)

	deleted := c.PruneBackups(context.Background(), "main", 30*24*time.Hour, 5)
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}

func TestPruneBackups_IgnoresUnparsableRefs(t *testing.T) {
	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"for-each-ref --format=%(refname) refs/gk/backup/main/*": {
				Stdout: "refs/gk/backup/main/not-a-number\nrefs/gk/backup/main/abc\n",
			},
		},
	}
	c := NewClient(fake)

	deleted := c.PruneBackups(context.Background(), "main", 1*time.Second, 0)
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 (all unparsable)", deleted)
	}
}

func buildRefListing(branch string, timestamps []int64) string {
	var b strings.Builder
	for _, ts := range timestamps {
		fmt.Fprintf(&b, "refs/gk/backup/%s/%d\n", branch, ts)
	}
	return b.String()
}
