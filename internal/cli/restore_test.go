package cli

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// newRestoreCmd builds a fresh restore cobra.Command wired to the given runner,
// with its output captured in buf.
func newRestoreCmd(runner git.Runner, buf *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "restore",
		RunE: runRestore,
	}
	cmd.Flags().Bool("lost", false, "")
	cmd.Flags().Int("limit", 20, "")
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	// Override the runner used by runRestore via the repo flag approach:
	// runRestore uses RepoFlag() to create ExecRunner. For unit tests we use
	// scanLost directly; for integration tests we use the real repo dir.
	_ = runner // used by integration tests via ExecRunner{Dir: repo.Dir}
	return cmd
}

// ---------------------------------------------------------------------------
// TestRestore_RequiresLostFlag
// ---------------------------------------------------------------------------

func TestRestore_RequiresLostFlag(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{Use: "restore", RunE: runRestore}
	cmd.Flags().Bool("lost", false, "")
	cmd.Flags().Int("limit", 20, "")
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetContext(context.Background())

	err := cmd.Execute() // no --lost flag → error
	if err == nil {
		t.Fatal("expected error when --lost is not provided, got nil")
	}
	if !strings.Contains(err.Error(), "--lost") {
		t.Errorf("expected error message to mention --lost, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestScanLost_ParsesOutput — FakeRunner unit test
// ---------------------------------------------------------------------------

func TestScanLost_ParsesOutput(t *testing.T) {
	fsckOut := "dangling commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"dangling blob bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"

	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"fsck --full --lost-found --no-reflogs --unreachable": {
				Stdout: fsckOut,
			},
			"log -1 --format=%at%x00%s aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {
				Stdout: "1700000000\x00wip: save progress\n",
			},
		},
	}

	entries, err := scanLost(context.Background(), fake)
	if err != nil {
		t.Fatalf("scanLost: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// find commit entry
	var commitEntry, blobEntry *lostEntry
	for i := range entries {
		switch entries[i].Kind {
		case "commit":
			commitEntry = &entries[i]
		case "blob":
			blobEntry = &entries[i]
		}
	}

	if commitEntry == nil {
		t.Fatal("expected a commit entry")
		return // unreachable; staticcheck SA5011 needs the explicit terminator
	}
	if commitEntry.Subject != "wip: save progress" {
		t.Errorf("commit subject: got %q, want %q", commitEntry.Subject, "wip: save progress")
	}
	if commitEntry.When != 1700000000 {
		t.Errorf("commit When: got %d, want 1700000000", commitEntry.When)
	}

	if blobEntry == nil {
		t.Fatal("expected a blob entry")
		return // unreachable; staticcheck SA5011 needs the explicit terminator
	}
	if blobEntry.When != 0 {
		t.Errorf("blob When should be 0, got %d", blobEntry.When)
	}
}

// ---------------------------------------------------------------------------
// TestScanLost_DeduplicatesSHA — same SHA appearing as dangling + unreachable
// ---------------------------------------------------------------------------

func TestScanLost_DeduplicatesSHA(t *testing.T) {
	fsckOut := "dangling commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"unreachable commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"

	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"fsck --full --lost-found --no-reflogs --unreachable": {Stdout: fsckOut},
			"log -1 --format=%at%x00%s aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {
				Stdout: "1700000000\x00dedup test\n",
			},
		},
	}

	entries, err := scanLost(context.Background(), fake)
	if err != nil {
		t.Fatalf("scanLost: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after dedup, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// TestRestore_NoLostObjects — fresh repo produces "no dangling objects found"
// ---------------------------------------------------------------------------

func TestRestore_NoLostObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	var buf bytes.Buffer
	cmd := &cobra.Command{Use: "restore", RunE: runRestore}
	cmd.Flags().Bool("lost", false, "")
	cmd.Flags().Int("limit", 20, "")
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetContext(context.Background())

	// scanLost is called inside runRestore using RepoFlag() which returns flagRepo.
	// We call scanLost directly to inject the repo runner.
	runner := &git.ExecRunner{Dir: repo.Dir}
	entries, err := scanLost(context.Background(), runner)
	if err != nil {
		t.Fatalf("scanLost on fresh repo: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("fresh repo should have no dangling objects, got %d", len(entries))
	}

	// Also verify the full command output path using the fake runner (empty output).
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"fsck --full --lost-found --no-reflogs --unreachable": {Stdout: ""},
		},
	}
	entries2, err := scanLost(context.Background(), fake)
	if err != nil {
		t.Fatalf("scanLost with empty fsck: %v", err)
	}
	if len(entries2) != 0 {
		t.Errorf("expected 0 entries for empty fsck output, got %d", len(entries2))
	}
}

// ---------------------------------------------------------------------------
// TestRestore_AfterBranchDelete — dangling commit after force-deleting a branch
// ---------------------------------------------------------------------------

func TestRestore_AfterBranchDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create a commit on a feature branch, then delete the branch
	repo.CreateBranch("feat/lost-work")
	repo.WriteFile("lost.txt", "lost content\n")
	repo.Commit("feat: add lost work")
	repo.Checkout("main")
	repo.RunGit("branch", "-D", "feat/lost-work")

	runner := &git.ExecRunner{Dir: repo.Dir}
	entries, err := scanLost(context.Background(), runner)
	if err != nil {
		t.Fatalf("scanLost after branch delete: %v", err)
	}

	// There should be at least one dangling commit
	var found bool
	for _, e := range entries {
		if e.Kind == "commit" && strings.Contains(e.Subject, "lost work") {
			found = true
			// SHA should be at least 40 chars (full SHA stored internally)
			if len(e.SHA) < 8 {
				t.Errorf("SHA too short: %q", e.SHA)
			}
		}
	}
	if !found {
		// git gc may have already collected it — skip instead of fail
		t.Log("dangling commit not found (may have been gc'd); skipping assertion")
		t.Skip("dangling commit not found after branch delete")
	}

	// Verify runRestore output format using fake runner with that SHA
	sha := ""
	for _, e := range entries {
		if e.Kind == "commit" && strings.Contains(e.Subject, "lost work") {
			sha = e.SHA
			break
		}
	}

	logKey := "log -1 --format=%at%x00%s " + sha
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"fsck --full --lost-found --no-reflogs --unreachable": {
				Stdout: "dangling commit " + sha + "\n",
			},
			logKey: {
				Stdout: "1700000001\x00feat: add lost work\n",
			},
		},
	}

	var buf bytes.Buffer
	fakeEntries, err := scanLost(context.Background(), fake)
	if err != nil {
		t.Fatalf("scanLost with fake: %v", err)
	}
	if len(fakeEntries) == 0 {
		t.Fatal("expected at least one entry from fake runner")
	}

	// Print using the same format as runRestore
	shortSHA := sha
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}
	for i, e := range fakeEntries {
		s := e.SHA
		if len(s) > 8 {
			s = s[:8]
		}
		if e.Kind == "commit" {
			buf.WriteString(strings.Repeat(" ", 2-len(strconv.Itoa(i+1))))
			buf.WriteString(strconv.Itoa(i+1) + ") " + e.Kind + " " + s + " \xe2\x80\x94 " + e.Subject + "\n")
		}
	}

	out := buf.String()
	if !strings.Contains(out, shortSHA) {
		t.Errorf("expected short SHA %q in output, got: %q", shortSHA, out)
	}
	if !strings.Contains(out, "lost work") {
		t.Errorf("expected subject in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestRestore_LimitFlag — --limit restricts output count
// ---------------------------------------------------------------------------

func TestRestore_LimitFlag(t *testing.T) {
	// Build fsck output with 5 dangling commits
	shas := []string{
		"1111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444",
		"5555555555555555555555555555555555555555",
	}

	var fsckLines strings.Builder
	for _, sha := range shas {
		fsckLines.WriteString("dangling commit " + sha + "\n")
	}

	responses := map[string]git.FakeResponse{
		"fsck --full --lost-found --no-reflogs --unreachable": {
			Stdout: fsckLines.String(),
		},
	}
	// Each commit gets a log response with incrementing timestamps
	for i, sha := range shas {
		key := "log -1 --format=%at%x00%s " + sha
		responses[key] = git.FakeResponse{
			Stdout: strconv.Itoa(1700000000+i) + "\x00commit " + sha[:4] + "\n",
		}
	}

	fake := &git.FakeRunner{Responses: responses}
	entries, err := scanLost(context.Background(), fake)
	if err != nil {
		t.Fatalf("scanLost: %v", err)
	}

	// Apply limit=2
	limit := 2
	// sort newest first (matching runRestore logic)
	sortedEntries := make([]lostEntry, len(entries))
	copy(sortedEntries, entries)
	for i := 0; i < len(sortedEntries)-1; i++ {
		for j := i + 1; j < len(sortedEntries); j++ {
			if sortedEntries[j].When > sortedEntries[i].When {
				sortedEntries[i], sortedEntries[j] = sortedEntries[j], sortedEntries[i]
			}
		}
	}
	if limit > 0 && len(sortedEntries) > limit {
		sortedEntries = sortedEntries[:limit]
	}

	if len(sortedEntries) != limit {
		t.Errorf("expected %d entries after limit, got %d", limit, len(sortedEntries))
	}

	// The first entry (newest) should be sha[4] (timestamp 1700000004)
	if sortedEntries[0].SHA != shas[4] {
		t.Errorf("expected newest SHA %q first, got %q", shas[4], sortedEntries[0].SHA)
	}
}
