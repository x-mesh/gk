package diff

import "testing"

func TestFileStatus_String(t *testing.T) {
	tests := []struct {
		status FileStatus
		want   string
	}{
		{StatusModified, "modified"},
		{StatusAdded, "added"},
		{StatusDeleted, "deleted"},
		{StatusRenamed, "renamed"},
		{StatusCopied, "copied"},
		{StatusModeChanged, "mode changed"},
		{FileStatus(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("FileStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestLineKind_String(t *testing.T) {
	tests := []struct {
		kind LineKind
		want string
	}{
		{LineContext, "context"},
		{LineAdded, "added"},
		{LineDeleted, "deleted"},
		{LineKind(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("LineKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}
