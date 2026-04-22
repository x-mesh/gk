package gitsafe

import (
	"testing"
	"time"
)

func TestBackupRefName(t *testing.T) {
	ts := time.Unix(1700000000, 0)

	tests := []struct {
		name   string
		kind   string
		branch string
		want   string
	}{
		{"undo plain branch", "undo", "main", "refs/gk/undo-backup/main/1700000000"},
		{"wipe plain branch", "wipe", "main", "refs/gk/wipe-backup/main/1700000000"},
		{"slash collapsed", "undo", "feat/auth/v2", "refs/gk/undo-backup/feat-auth-v2/1700000000"},
		{"detached HEAD", "undo", "", "refs/gk/undo-backup/detached/1700000000"},
		{"wipe detached", "wipe", "", "refs/gk/wipe-backup/detached/1700000000"},
		{"timemachine kind", "timemachine", "main", "refs/gk/timemachine-backup/main/1700000000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BackupRefName(tc.kind, tc.branch, ts)
			if got != tc.want {
				t.Errorf("BackupRefName(%q, %q) = %q, want %q", tc.kind, tc.branch, got, tc.want)
			}
		})
	}
}

func TestSanitizeBranchSegment(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"main", "main"},
		{"", "detached"},
		{"feat/foo", "feat-foo"},
		{"release/v1/rc1", "release-v1-rc1"},
	}

	for _, tc := range tests {
		got := SanitizeBranchSegment(tc.in)
		if got != tc.want {
			t.Errorf("SanitizeBranchSegment(%q) = %q, want %q", tc.in, got, tc.want)
		}
		// Invariant: output never contains "/" and never empty.
		if got == "" {
			t.Errorf("SanitizeBranchSegment(%q) returned empty", tc.in)
		}
		for i := 0; i < len(got); i++ {
			if got[i] == '/' {
				t.Errorf("SanitizeBranchSegment(%q) = %q contains '/'", tc.in, got)
			}
		}
	}
}
