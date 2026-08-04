package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// runJSONLog executes `gk log --json [extra...]` against repo and returns the
// parsed entries plus the raw output for failure messages.
func runJSONLog(t *testing.T, repo *testutil.Repo, extra ...string) ([]LogEntry, string) {
	t.Helper()
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repo.Dir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	logCmd := &cobra.Command{
		Use:  "log [revisions] [-- <path>...]",
		RunE: runLog,
	}
	logCmd.Flags().String("since", "", "show commits since this time")
	logCmd.Flags().String("format", "", "git pretty-format string")
	logCmd.Flags().Bool("graph", false, "include topology graph")
	logCmd.Flags().IntP("limit", "n", 0, "max number of commits")
	testRoot.AddCommand(logCmd)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs(append([]string{"--repo", repo.Dir, "--json", "log"}, extra...))

	if err := testRoot.Execute(); err != nil {
		t.Fatalf("gk log --json %v error: %v\noutput:\n%s", extra, err, buf.String())
	}
	var entries []LogEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("log --json is not valid JSON: %v\nraw:\n%s", err, buf.String())
	}
	return entries, buf.String()
}

// TestLogJSON_ContractAgainstGit is the golden round-trip for the log --json
// machine contract: EVERY field of EVERY record is compared against what git
// itself reports for the same commit.
//
// This exists because a hand-built fixture cannot hold this contract. The
// field-shift that shipped in v0.135.0 parsed cleanly and returned wrong
// values; the count check and the first-record check both stayed green,
// because record 0 was correct and only records 1..n were shifted — and the
// unit fixture reproduced the format string rather than what `git log -z`
// actually writes. The only authority on that byte stream is git, so git is
// what this test asks.
func TestLogJSON_ContractAgainstGit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}

	repo := testutil.NewRepo(t)
	// The shapes that have actually bitten, in one history: a multi-line body
	// (SplitN must keep it whole), a unicode subject (no byte-length trap), and
	// an empty body last (the -z trailing-terminator trap that dropped the
	// oldest record when trimmed wrong).
	repo.WriteFile("a.txt", "alpha")
	repo.RunGit("add", ".")
	repo.RunGit("commit", "-m", "feat: multi-line body", "-m", "first body line\n\nthird body line")
	repo.WriteFile("b.txt", "beta")
	repo.RunGit("add", ".")
	repo.RunGit("commit", "-m", "fix(로그): 한글 제목 — unicode subject")
	repo.WriteFile("c.txt", "gamma")
	repo.RunGit("add", ".")
	repo.RunGit("commit", "-m", "docs: empty body at HEAD")

	entries, raw := runJSONLog(t, repo)

	wantCount, err := strconv.Atoi(repo.RunGit("rev-list", "--count", "HEAD"))
	if err != nil {
		t.Fatalf("rev-list --count: %v", err)
	}
	if len(entries) != wantCount {
		t.Fatalf("entries = %d, git rev-list --count = %d\nraw:\n%s", len(entries), wantCount, raw)
	}

	for i, e := range entries {
		// Address each commit positionally, independent of the parse under test.
		ref := fmt.Sprintf("HEAD~%d", i)
		get := func(format string) string {
			t.Helper()
			return repo.RunGit("show", "-s", "--format="+format, ref)
		}
		if want := get("%H"); e.SHA != want {
			t.Errorf("[%d].sha = %q, git says %q", i, e.SHA, want)
		}
		if want := get("%h"); e.ShortSHA != want {
			t.Errorf("[%d].short_sha = %q, git says %q", i, e.ShortSHA, want)
		}
		if want := get("%an"); e.Author != want {
			t.Errorf("[%d].author = %q, git says %q", i, e.Author, want)
		}
		if want := get("%ae"); e.Email != want {
			t.Errorf("[%d].email = %q, git says %q", i, e.Email, want)
		}
		if want := get("%aI"); e.Date != want {
			t.Errorf("[%d].date = %q, git says %q", i, e.Date, want)
		}
		if want := get("%s"); e.Subject != want {
			t.Errorf("[%d].subject = %q, git says %q", i, e.Subject, want)
		}
		// Body may differ by trailing newlines between %b spellings; inner
		// newlines are the contract (SplitN must not cut a multi-line body).
		if want := get("%b"); strings.TrimRight(e.Body, "\n") != strings.TrimRight(want, "\n") {
			t.Errorf("[%d].body = %q, git says %q", i, e.Body, want)
		}
	}
}

// The positional-revisions form is part of the machine contract too — the
// session audit now answers `git log A..B` with `gk log A..B`, so the range
// spelling has to keep returning exactly the commits git says are in range.
func TestLogJSON_ContractRangeForm(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}

	repo := testutil.NewRepo(t)
	for i := 0; i < 3; i++ {
		repo.WriteFile(fmt.Sprintf("f%d.txt", i), strconv.Itoa(i))
		repo.RunGit("add", ".")
		repo.RunGit("commit", "-m", fmt.Sprintf("commit %d", i))
	}

	entries, raw := runJSONLog(t, repo, "HEAD~2..HEAD")
	if len(entries) != 2 {
		t.Fatalf("HEAD~2..HEAD returned %d entries, want 2\nraw:\n%s", len(entries), raw)
	}
	if want := repo.RunGit("rev-parse", "HEAD"); entries[0].SHA != want {
		t.Errorf("range [0].sha = %q, git says %q", entries[0].SHA, want)
	}
	if want := repo.RunGit("rev-parse", "HEAD~1"); entries[1].SHA != want {
		t.Errorf("range [1].sha = %q, git says %q", entries[1].SHA, want)
	}
}
