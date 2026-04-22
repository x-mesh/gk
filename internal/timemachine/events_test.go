package timemachine

import (
	"context"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/reflog"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestEventKind_String(t *testing.T) {
	cases := []struct {
		k    EventKind
		want string
	}{
		{KindReflog, "reflog"},
		{KindBackup, "backup"},
		{KindStash, "stash"},
		{KindDangling, "dangling"},
		{EventKind(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.k.String(); got != tc.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tc.k, got, tc.want)
		}
	}
}

func TestFromReflogEntry(t *testing.T) {
	when := time.Unix(1700000000, 0)
	e := reflog.Entry{
		OldSHA:  "old-sha",
		NewSHA:  "new-sha",
		Ref:     "HEAD@{3}",
		Action:  reflog.ActionReset,
		Message: "reset: moving to HEAD~1",
		Summary: "moving to HEAD~1",
		When:    when,
	}

	ev := FromReflogEntry(e)

	if ev.Kind != KindReflog {
		t.Errorf("Kind = %v, want KindReflog", ev.Kind)
	}
	if ev.OID != "new-sha" {
		t.Errorf("OID = %q, want new-sha", ev.OID)
	}
	if ev.OldOID != "old-sha" {
		t.Errorf("OldOID = %q, want old-sha", ev.OldOID)
	}
	if ev.Ref != "HEAD@{3}" {
		t.Errorf("Ref = %q", ev.Ref)
	}
	if ev.Subject != "moving to HEAD~1" {
		t.Errorf("Subject = %q, want summary form", ev.Subject)
	}
	if ev.Action != string(reflog.ActionReset) {
		t.Errorf("Action = %q", ev.Action)
	}
	if !ev.When.Equal(when) {
		t.Errorf("When = %v, want %v", ev.When, when)
	}
	// Backup-only fields must stay empty for reflog-sourced events.
	if ev.BackupKind != "" || ev.Branch != "" {
		t.Errorf("backup fields populated on reflog event: %+v", ev)
	}
}

func TestFromReflogEntry_FallsBackToMessageWhenSummaryEmpty(t *testing.T) {
	e := reflog.Entry{
		NewSHA:  "sha",
		Ref:     "HEAD@{0}",
		Message: "commit: initial",
		Summary: "",
	}
	ev := FromReflogEntry(e)
	if ev.Subject != "commit: initial" {
		t.Errorf("Subject = %q, want fallback to Message", ev.Subject)
	}
}

func TestFromBackupRef(t *testing.T) {
	when := time.Unix(1700000000, 0)
	b := gitsafe.BackupRef{
		Ref:    "refs/gk/undo-backup/main/1700000000",
		Kind:   "undo",
		Branch: "main",
		When:   when,
		SHA:    "abc123",
	}

	ev := FromBackupRef(b)

	if ev.Kind != KindBackup {
		t.Errorf("Kind = %v, want KindBackup", ev.Kind)
	}
	if ev.OID != "abc123" {
		t.Errorf("OID = %q", ev.OID)
	}
	if ev.BackupKind != "undo" {
		t.Errorf("BackupKind = %q", ev.BackupKind)
	}
	if ev.Branch != "main" {
		t.Errorf("Branch = %q", ev.Branch)
	}
	if ev.Subject == "" {
		t.Errorf("Subject should be synthesized, got empty")
	}
	// Reflog-only fields must stay empty for backup-sourced events.
	if ev.OldOID != "" || ev.Action != "" {
		t.Errorf("reflog fields populated on backup event: %+v", ev)
	}
}

func TestReadHEAD_NonEmptyRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	evs, err := ReadHEAD(context.Background(), &git.ExecRunner{Dir: repo.Dir}, 10)
	if err != nil {
		t.Fatalf("ReadHEAD: %v", err)
	}
	if len(evs) < 2 {
		t.Fatalf("expected >=2 reflog events, got %d", len(evs))
	}
	// All must be Kind=Reflog and have non-empty OID.
	for i, ev := range evs {
		if ev.Kind != KindReflog {
			t.Errorf("ev[%d].Kind = %v, want KindReflog", i, ev.Kind)
		}
		if ev.OID == "" {
			t.Errorf("ev[%d].OID empty", i)
		}
	}
}
