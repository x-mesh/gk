package forget

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestAnalyzeCountsUniqueBlobs(t *testing.T) {
	r := testutil.NewRepo(t)
	// Three commits, each rewriting db/data with different content → three
	// unique blobs.
	for i, body := range []string{"v1\n", "v2\n", strings.Repeat("X", 1024)} {
		r.WriteFile("db/data", body)
		r.RunGit("add", ".")
		r.RunGit("commit", "-m", "rev")
		_ = i
	}

	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := Analyze(context.Background(), runner, r.Dir, []string{"db/data"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].UniqueBlobs != 3 {
		t.Errorf("UniqueBlobs = %d, want 3", got[0].UniqueBlobs)
	}
	if got[0].LargestBytes != 1024 {
		t.Errorf("LargestBytes = %d, want 1024", got[0].LargestBytes)
	}
	// v1 (3) + v2 (3) + 1024 = 1030
	if got[0].TotalBytes != 1030 {
		t.Errorf("TotalBytes = %d, want 1030", got[0].TotalBytes)
	}
}

func TestAnalyzePathNotInHistory(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := Analyze(context.Background(), runner, r.Dir, []string{"never-existed.bin"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].UniqueBlobs != 0 || got[0].TotalBytes != 0 {
		t.Errorf("missing path: got %+v, want zero stats", got[0])
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1 << 10, "1.0 KiB"},
		{1500, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{int64(1.5 * float64(1<<30)), "1.5 GiB"},
	}
	for _, tc := range cases {
		if got := HumanBytes(tc.n); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
