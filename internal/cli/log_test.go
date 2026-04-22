package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

func TestCalendarLines(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	mon := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC) // Monday
	dates := []time.Time{
		mon, mon, mon, // W1 Mon = 3 (peak)
		mon.Add(24 * time.Hour),           // W1 Tue = 1
		mon.Add((7 + 3) * 24 * time.Hour), // W2 Thu
		mon.Add((7 + 3) * 24 * time.Hour), // W2 Thu = 2
	}
	lines := calendarLines(dates)
	if len(lines) != 8 {
		t.Fatalf("expected 8 lines (header + 7 weekdays), got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "W1") || !strings.Contains(lines[0], "W2") {
		t.Errorf("header missing week labels: %q", lines[0])
	}
	// Mon row should have block chars (non-zero bucket)
	if !strings.ContainsAny(lines[1], "░▒▓█") {
		t.Errorf("Mon row should have heat glyph, got %q", lines[1])
	}
}

func TestCcClassify(t *testing.T) {
	cases := []struct {
		subject   string
		wantType  string
		wantGlyph string
	}{
		{"feat: x", "feat", "✨"},
		{"fix(auth): y", "fix", "🐛"},
		{"refactor!: z", "refactor", "♻"},
		{"chore(release): v0.4.0", "chore", "🧹"},
		{"random subject with no prefix", "", ""},
		{"wip hack", "", ""},
	}
	for _, tc := range cases {
		gotT, gotG := ccClassify(tc.subject)
		if gotT != tc.wantType || gotG != tc.wantGlyph {
			t.Errorf("ccClassify(%q) = (%q,%q), want (%q,%q)", tc.subject, gotT, gotG, tc.wantType, tc.wantGlyph)
		}
	}
}

func TestParseTrailers(t *testing.T) {
	body := `This is a commit.

Co-Authored-By: Alice <alice@ex.com>
Reviewed-by: Bob
Signed-off-by: Carol <c@ex.com>
`
	got := parseTrailers(body)
	for _, want := range []string{"+Alice", "review:Bob"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if parseTrailers("no trailers here") != "" {
		t.Error("expected empty for body without trailers")
	}
}

func TestRenderImpactBar(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })
	if got := renderImpactBar(0, 0, 100); got != "" {
		t.Errorf("expected empty bar for 0 changes, got %q", got)
	}
	bar := renderImpactBar(50, 50, 100)
	if !strings.ContainsAny(bar, "█▏▎▍▌▋▊▉") {
		t.Errorf("expected block glyph, got %q", bar)
	}
}

func TestParseCommitRecords(t *testing.T) {
	raw := "sha1fullA\x00sha1A\x00feat: x\x00Alice\x00now\x00body1\x1e\nsha2fullB\x00sha2B\x00fix: y\x00Bob\x001h\x00body2\x1e\n"
	recs := parseCommitRecords([]byte(raw))
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].short != "sha1A" || recs[0].subject != "feat: x" {
		t.Errorf("rec0 unexpected: %+v", recs[0])
	}
	if recs[1].author != "Bob" || recs[1].relDate != "1h" {
		t.Errorf("rec1 unexpected: %+v", recs[1])
	}
}

func TestPulseLine(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = false })

	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	dates := []time.Time{
		now.Add(-6 * 24 * time.Hour),
		now.Add(-5 * 24 * time.Hour),
		now.Add(-5 * 24 * time.Hour),
		now.Add(-5 * 24 * time.Hour),
		now.Add(-2 * 24 * time.Hour),
		now,
	}

	t.Run("basic", func(t *testing.T) {
		got := pulseLine(dates, "1w")
		for _, want := range []string{"pulse", "1w", "6 commits", "peak"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
		// Zero bucket and non-zero block glyph must both appear.
		if !strings.Contains(got, "·") {
			t.Errorf("expected '·' for zero bucket, got %q", got)
		}
		if !strings.ContainsAny(got, "▁▂▃▄▅▆▇█") {
			t.Errorf("expected block glyph for non-zero bucket, got %q", got)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if got := pulseLine(nil, "1d"); got != "" {
			t.Errorf("empty dates should produce empty pulse, got %q", got)
		}
	})

	t.Run("derives label when since blank", func(t *testing.T) {
		got := pulseLine(dates, "")
		if !strings.Contains(got, "7d") {
			t.Errorf("expected derived 7d label, got %q", got)
		}
	})
}

// TestNormalizeSince verifies the short-form since parser.
func TestNormalizeSince(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"1w", "1.weeks.ago"},
		{"3d", "3.days.ago"},
		{"12h", "12.hours.ago"},
		{"last monday", "last monday"},
		{"", ""},
		{"2m", "2.minutes.ago"},
		{"5s", "5.seconds.ago"},
		{"1M", "1.months.ago"},
		{"2y", "2.years.ago"},
	}
	for _, tc := range cases {
		got := normalizeSince(tc.input)
		if got != tc.want {
			t.Errorf("normalizeSince(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestParseJSONLog verifies that raw git log output is parsed into LogEntry slices correctly.
func TestParseJSONLog(t *testing.T) {
	// Build a raw byte slice with 2 records separated by \x1e, fields by \x00.
	raw := "" +
		"abc123def456abc123def456abc123def456abc1" + "\x00" +
		"abc123d" + "\x00" +
		"Alice" + "\x00" +
		"alice@example.com" + "\x00" +
		"2024-01-01T00:00:00+00:00" + "\x00" +
		"First commit" + "\x00" +
		"" + "\x1e" +
		"def456abc123def456abc123def456abc123def4" + "\x00" +
		"def456a" + "\x00" +
		"Bob" + "\x00" +
		"bob@example.com" + "\x00" +
		"2024-01-02T00:00:00+00:00" + "\x00" +
		"Second commit" + "\x00" +
		"some body text" + "\x1e"

	entries := parseJSONLog([]byte(raw))

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	e0 := entries[0]
	if e0.SHA != "abc123def456abc123def456abc123def456abc1" {
		t.Errorf("entry[0].SHA = %q", e0.SHA)
	}
	if e0.ShortSHA != "abc123d" {
		t.Errorf("entry[0].ShortSHA = %q", e0.ShortSHA)
	}
	if e0.Author != "Alice" {
		t.Errorf("entry[0].Author = %q", e0.Author)
	}
	if e0.Email != "alice@example.com" {
		t.Errorf("entry[0].Email = %q", e0.Email)
	}
	if e0.Subject != "First commit" {
		t.Errorf("entry[0].Subject = %q", e0.Subject)
	}
	if e0.Body != "" {
		t.Errorf("entry[0].Body = %q, want empty", e0.Body)
	}

	e1 := entries[1]
	if e1.Author != "Bob" {
		t.Errorf("entry[1].Author = %q", e1.Author)
	}
	if e1.Subject != "Second commit" {
		t.Errorf("entry[1].Subject = %q", e1.Subject)
	}
	if e1.Body != "some body text" {
		t.Errorf("entry[1].Body = %q", e1.Body)
	}
}

// buildLogCmd returns a fresh cobra command tree rooted at a test root,
// with the log subcommand attached. We wire it manually to avoid touching
// the package-level rootCmd and its persistent flags.
func buildLogCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	// Use a minimal root so we can set --repo without side effects.
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	logCmd := &cobra.Command{
		Use:     "log [revisions] [-- <path>...]",
		Aliases: []string{"slog"},
		Short:   "Show short, colorful commit log",
		RunE:    runLog,
	}
	logCmd.Flags().String("since", "", "show commits since this time")
	logCmd.Flags().String("format", "%H", "git pretty-format string")
	logCmd.Flags().Bool("graph", false, "include topology graph")
	logCmd.Flags().IntP("limit", "n", 0, "max number of commits")

	testRoot.AddCommand(logCmd)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	// Set --repo and --no-color via args prepended.
	allArgs := append([]string{"--repo", repoDir, "--no-color", "log"}, extraArgs...)
	testRoot.SetArgs(allArgs)

	return testRoot, buf
}

// TestLogIntegration_PlainOutput verifies that runLog produces one line per commit
// when given 3 commits in a fresh repo.
func TestLogIntegration_PlainOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "alpha")
	sha1 := repo.Commit("commit one")
	repo.WriteFile("b.txt", "beta")
	sha2 := repo.Commit("commit two")
	repo.WriteFile("c.txt", "gamma")
	sha3 := repo.Commit("commit three")

	root, buf := buildLogCmd(repo.Dir, "--format=%H")
	if err := root.Execute(); err != nil {
		t.Fatalf("runLog error: %v", err)
	}

	out := buf.String()
	for _, sha := range []string{sha1, sha2, sha3} {
		if !strings.Contains(out, sha) {
			t.Errorf("output missing sha %s\nfull output:\n%s", sha, out)
		}
	}
}

// TestLogIntegration_LimitFlag verifies that -n 1 returns exactly one commit line.
func TestLogIntegration_LimitFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "alpha")
	repo.Commit("commit one")
	repo.WriteFile("b.txt", "beta")
	repo.Commit("commit two")
	repo.WriteFile("c.txt", "gamma")
	repo.Commit("commit three")

	root, buf := buildLogCmd(repo.Dir, "--format=%H", "-n", "1")
	if err := root.Execute(); err != nil {
		t.Fatalf("runLog error: %v", err)
	}

	out := strings.TrimSpace(buf.String())
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d:\n%s", len(lines), out)
	}
}

// TestLogIntegration_JSONOutput verifies --json produces a valid JSON array with 3 elements.
func TestLogIntegration_JSONOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "alpha")
	repo.Commit("commit one")
	repo.WriteFile("b.txt", "beta")
	repo.Commit("commit two")
	repo.WriteFile("c.txt", "gamma")
	sha3 := repo.Commit("commit three")

	// Build command with --json flag before the subcommand.
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
	testRoot.SetArgs([]string{"--repo", repo.Dir, "--json", "log"})

	if err := testRoot.Execute(); err != nil {
		t.Fatalf("runLog --json error: %v", err)
	}

	var entries []LogEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("json.Unmarshal failed: %v\nraw output:\n%s", err, buf.String())
	}

	// NewRepo creates 1 initial commit + 3 test commits = 4 total.
	if len(entries) != 4 {
		t.Errorf("expected 4 entries (1 initial + 3 test), got %d", len(entries))
	}

	// The most recent commit should match sha3.
	if len(entries) > 0 && entries[0].SHA != sha3 {
		t.Errorf("entries[0].SHA = %q, want %q", entries[0].SHA, sha3)
	}
}
