package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestCalendarLines(t *testing.T) {
	prevNoColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prevNoColor })

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
		{"feat: x", "feat", "▲"},
		{"fix(auth): y", "fix", "✕"},
		{"refactor!: z", "refactor", "⟳"},
		{"chore(release): v0.4.0", "chore", "⊖"},
		{"docs: update readme", "docs", "¶"},
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

func TestCcColorize(t *testing.T) {
	prevNoColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prevNoColor })

	// With NoColor, the colorize helper still returns the type portion
	// verbatim, so we can assert the output composition deterministically.
	cases := []struct {
		subject, typeName, want string
	}{
		{"feat(status): add", "feat", "feat(status): add"},
		{"fix: bug", "fix", "fix: bug"},
		{"not a match", "feat", "not a match"},
		{"feat: x", "", "feat: x"},
	}
	for _, tc := range cases {
		if got := ccColorize(tc.subject, tc.typeName); got != tc.want {
			t.Errorf("ccColorize(%q, %q) = %q, want %q", tc.subject, tc.typeName, got, tc.want)
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
	prevNoColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prevNoColor })
	if got := renderImpactBar(0, 0, 100); got != "" {
		t.Errorf("expected empty bar for 0 changes, got %q", got)
	}
	bar := renderImpactBar(50, 50, 100)
	if !strings.ContainsAny(bar, "█▏▎▍▌▋▊▉") {
		t.Errorf("expected block glyph, got %q", bar)
	}
}

func TestParseCommitRecords(t *testing.T) {
	// Fields: %H %h %s %an %at %P %b. The 5th is author-time as a unix
	// timestamp (%at); the 6th is the space-separated parent list (%P).
	raw := "sha1fullA\x00sha1A\x00feat: x\x00Alice\x001700000000\x00pa1 pa2\x00body1\x1e\n" +
		"sha2fullB\x00sha2B\x00fix: y\x00Bob\x001700003600\x00\x00body2\x1e\n"
	recs := parseCommitRecords([]byte(raw))
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].short != "sha1A" || recs[0].subject != "feat: x" {
		t.Errorf("rec0 unexpected: %+v", recs[0])
	}
	if recs[1].author != "Bob" {
		t.Errorf("rec1 unexpected author: %+v", recs[1])
	}
	if recs[0].authorTime.Unix() != 1700000000 {
		t.Errorf("rec0 authorTime = %d, want 1700000000", recs[0].authorTime.Unix())
	}
	if recs[1].authorTime.Unix() != 1700003600 {
		t.Errorf("rec1 authorTime = %d, want 1700003600", recs[1].authorTime.Unix())
	}
	if len(recs[0].parents) != 2 || recs[0].parents[0] != "pa1" || recs[0].parents[1] != "pa2" {
		t.Errorf("rec0 parents = %v, want [pa1 pa2]", recs[0].parents)
	}
	if len(recs[1].parents) != 0 {
		t.Errorf("rec1 parents = %v, want empty", recs[1].parents)
	}
}

func TestShortAge(t *testing.T) {
	cases := []struct {
		offset time.Duration
		want   string
	}{
		{0, "now"}, // 0 == time.Time{} fallback path for IsZero, handled separately; here check <1m path
		{30 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{25 * time.Hour, "1d"},
		{6 * 24 * time.Hour, "6d"},
		{15 * 24 * time.Hour, "2w"},
		{90 * 24 * time.Hour, "3mo"},
		{400 * 24 * time.Hour, "1y"},
	}
	for _, tc := range cases {
		got := shortAge(time.Now().Add(-tc.offset))
		if got != tc.want {
			t.Errorf("shortAge(now - %s) = %q, want %q", tc.offset, got, tc.want)
		}
	}
	if got := shortAge(time.Time{}); got != "now" {
		t.Errorf("shortAge(zero) = %q, want 'now'", got)
	}
}

func TestResolveLogVis(t *testing.T) {
	cfg := &config.Config{Log: config.LogConfig{Vis: []string{"cc", "safety", "tags-rule"}}}

	mkCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "log"}
		for _, name := range logVizNames {
			c.Flags().Bool(name, false, "")
		}
		c.Flags().StringSlice("vis", nil, "")
		c.Flags().String("format", "", "")
		return c
	}

	t.Run("no flags → config default", func(t *testing.T) {
		got := resolveLogVis(mkCmd(), cfg)
		if strings.Join(got, ",") != "cc,safety,tags-rule" {
			t.Errorf("got %v, want [cc safety tags-rule]", got)
		}
	})

	t.Run("individual flag appends to config", func(t *testing.T) {
		c := mkCmd()
		_ = c.Flags().Set("impact", "true")
		got := resolveLogVis(c, cfg)
		if strings.Join(got, ",") != "cc,safety,tags-rule,impact" {
			t.Errorf("got %v, want [cc safety tags-rule impact]", got)
		}
	})

	t.Run("individual flag=false removes from config", func(t *testing.T) {
		c := mkCmd()
		_ = c.Flags().Set("cc", "false")
		got := resolveLogVis(c, cfg)
		if strings.Join(got, ",") != "safety,tags-rule" {
			t.Errorf("got %v, want [safety tags-rule]", got)
		}
	})

	t.Run("--vis replaces config entirely", func(t *testing.T) {
		c := mkCmd()
		_ = c.Flags().Set("vis", "cc,impact")
		got := resolveLogVis(c, cfg)
		if strings.Join(got, ",") != "cc,impact" {
			t.Errorf("got %v, want [cc impact]", got)
		}
	})

	t.Run("--vis none disables everything", func(t *testing.T) {
		c := mkCmd()
		_ = c.Flags().Set("vis", "none")
		got := resolveLogVis(c, cfg)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("--vis none plus --trailers → only trailers", func(t *testing.T) {
		c := mkCmd()
		_ = c.Flags().Set("vis", "none")
		_ = c.Flags().Set("trailers", "true")
		got := resolveLogVis(c, cfg)
		if strings.Join(got, ",") != "trailers" {
			t.Errorf("got %v, want [trailers]", got)
		}
	})

	t.Run("--vis plus individual layers", func(t *testing.T) {
		c := mkCmd()
		_ = c.Flags().Set("vis", "cc,impact")
		_ = c.Flags().Set("trailers", "true")
		got := resolveLogVis(c, cfg)
		if strings.Join(got, ",") != "cc,impact,trailers" {
			t.Errorf("got %v, want [cc impact trailers]", got)
		}
	})

	t.Run("--format alone suppresses config viz", func(t *testing.T) {
		c := mkCmd()
		_ = c.Flags().Set("format", "%H")
		got := resolveLogVis(c, cfg)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("--format plus --cc re-enables cc only", func(t *testing.T) {
		c := mkCmd()
		_ = c.Flags().Set("format", "%H")
		_ = c.Flags().Set("cc", "true")
		got := resolveLogVis(c, cfg)
		if strings.Join(got, ",") != "cc" {
			t.Errorf("got %v, want [cc]", got)
		}
	})
}

func TestPulseLine(t *testing.T) {
	prevNoColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prevNoColor })

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

// TestRebaseSafety covers all four classification branches, including the
// critical pushedKnown=false path that was the subject of the error-vs-zero
// bug fix (commit 93252d5).
func TestRebaseSafety(t *testing.T) {
	const sha = "abc1234"
	cases := []struct {
		name         string
		pushed       map[string]bool
		pushedKnown  bool
		amended      map[string]bool
		amendedKnown bool
		want         rune
	}{
		{
			name:         "amended takes priority over pushed",
			pushed:       map[string]bool{sha: true},
			pushedKnown:  true,
			amended:      map[string]bool{sha: true},
			amendedKnown: true,
			want:         '✎',
		},
		{
			name:         "pushedKnown=false → silent (pre-fix bug: offline showed ◇ on every row)",
			pushed:       nil,
			pushedKnown:  false,
			amended:      nil,
			amendedKnown: false,
			want:         ' ',
		},
		{
			name:         "known pushed → silent",
			pushed:       map[string]bool{sha: true},
			pushedKnown:  true,
			amended:      nil,
			amendedKnown: false,
			want:         ' ',
		},
		{
			name:         "known not-pushed → ◇",
			pushed:       map[string]bool{},
			pushedKnown:  true,
			amended:      nil,
			amendedKnown: false,
			want:         '◇',
		},
		{
			name:         "amendedKnown=false suppresses ✎ even when sha is in map",
			pushed:       map[string]bool{},
			pushedKnown:  true,
			amended:      map[string]bool{sha: true},
			amendedKnown: false,
			want:         '◇',
		},
	}
	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// runner is never dereferenced inside rebaseSafety.
			got := rebaseSafety(ctx, nil, sha, tc.pushed, tc.pushedKnown, tc.amended, tc.amendedKnown)
			if got != tc.want {
				t.Errorf("got %q (%U), want %q (%U)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestCollectPushedShas_NoUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	_, ok := collectPushedShas(context.Background(), runner)
	if ok {
		t.Error("no upstream configured → expected ok=false (unknown), got ok=true")
	}
}

func TestCollectPushedShas_WithUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	r.RunGit("remote", "add", "origin", bareDir)
	r.RunGit("push", "-q", "-u", "origin", "main")

	runner := &git.ExecRunner{Dir: r.Dir}
	m, ok := collectPushedShas(context.Background(), runner)
	if !ok {
		t.Fatal("upstream configured → expected ok=true")
	}
	if len(m) == 0 {
		t.Error("expected at least one pushed SHA in map, got empty")
	}
}

// TestCollectPushedShas_NoUpstreamButRemotes covers the --remotes fallback:
// a branch with NO configured upstream, but whose repo has remote-tracking
// refs, must still resolve a pushed set (ok=true) so `◇ unpushed` works on
// never-pushed local branches. Commits on the remote count as pushed; the
// local-only commit must not.
func TestCollectPushedShas_NoUpstreamButRemotes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	r.RunGit("remote", "add", "origin", bareDir)
	// Push WITHOUT -u: the remote ref exists, but no upstream is tracked.
	r.RunGit("push", "-q", "origin", "main")
	pushedSha := r.RunGit("rev-parse", "HEAD")

	// A local-only branch with a commit that lives on no remote.
	r.CreateBranch("feature")
	r.WriteFile("f.txt", "local")
	localSha := r.Commit("local only")

	// Guard the premise: feature must have no upstream.
	if _, err := r.TryGit("rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		t.Fatal("test setup: feature unexpectedly has an upstream")
	}

	runner := &git.ExecRunner{Dir: r.Dir}
	m, ok := collectPushedShas(context.Background(), runner)
	if !ok {
		t.Fatal("remote refs present → expected ok=true via --remotes fallback")
	}
	if !m[pushedSha] {
		t.Errorf("commit on remote %s should be in the pushed set", pushedSha)
	}
	if m[localSha] {
		t.Errorf("local-only commit %s must NOT be in the pushed set", localSha)
	}
}

func TestRenderPushBoundary(t *testing.T) {
	prevNoColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prevNoColor })

	got := renderPushBoundary(5, 60)
	if !strings.Contains(got, "──┤ ↑ 5 unpushed ├") {
		t.Errorf("missing boundary header in %q", got)
	}
	// Padded to terminal width with trailing dashes.
	if w := utf8.RuneCountInString(got); w != 60 {
		t.Errorf("boundary width = %d runes, want 60: %q", w, got)
	}
}

func TestCollectRecentlyAmended_FreshRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	// Fresh repo: no amend/rebase entries in reflog → ok=true, empty map.
	m, ok := collectRecentlyAmended(context.Background(), runner)
	if !ok {
		t.Error("valid repo → expected ok=true")
	}
	if len(m) != 0 {
		t.Errorf("no amendments → expected empty map, got %v", m)
	}
}

func TestVisibleCellWidth_StripsCSI(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"hello", 5},
		{"\x1b[31mhello\x1b[0m", 5},
		{"\x1b[1;33mWIP\x1b[0m(scope)", 10},
		{"한글", 4}, // CJK wide cells
		{"", 0},
	}
	for _, c := range cases {
		if got := visibleCellWidth(c.in); got != c.want {
			t.Errorf("visibleCellWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestTrimToVisible_NoOpWhenShort(t *testing.T) {
	in := "\x1b[31mhello\x1b[0m"
	if got := trimToVisible(in, 10); got != in {
		t.Errorf("expected passthrough when fits, got %q", got)
	}
}

func TestTrimToVisible_DisabledOnZero(t *testing.T) {
	in := "any long content with \x1b[31mcolor\x1b[0m"
	if got := trimToVisible(in, 0); got != in {
		t.Errorf("max=0 must be a no-op, got %q", got)
	}
}

func TestTrimToVisible_PreservesEscapes(t *testing.T) {
	// 5 visible cells of red + non-color text. Trim to 6 cells leaves 5
	// for content + 1 for the ellipsis. The leading SGR must survive in
	// the output so the kept prefix still renders red.
	in := "\x1b[31mhello\x1b[0m world"
	got := trimToVisible(in, 6)
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("expected leading SGR preserved, got %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis on truncated output, got %q", got)
	}
	if visibleCellWidth(got) > 6 {
		t.Errorf("trimmed visible width = %d, want ≤ 6", visibleCellWidth(got))
	}
}

func TestTrimToVisible_CJKWideCells(t *testing.T) {
	// 한글 each renders as 2 cells; "한글ABC" = 4 + 3 = 7 cells.
	// Trim to 5: keeps "한글" (4 cells) + ellipsis (1 cell) = 5.
	in := "한글ABC"
	got := trimToVisible(in, 5)
	if visibleCellWidth(got) > 5 {
		t.Errorf("CJK trim overshot: width=%d, output=%q", visibleCellWidth(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis, got %q", got)
	}
}

func TestBoxGlyph(t *testing.T) {
	cases := []struct {
		mask int
		want string
	}{
		{dUp | dDown, gVert},
		{dLeft | dRight, gHoriz},
		{dUp | dDown | dRight, gTeeR},
		{dUp | dDown | dLeft, gTeeL},
		{dDown | dLeft, gCornDL},
		{dDown | dRight, gCornDR},
		{dUp | dLeft, gCornUL},
		{dUp | dRight, gCornUR},
		{dUp | dDown | dLeft | dRight, gCross},
		{0, " "},
	}
	for _, c := range cases {
		if got := boxGlyph(c.mask); got != c.want {
			t.Errorf("boxGlyph(%d) = %q, want %q", c.mask, got, c.want)
		}
	}
}

func TestRenderSelfGraph(t *testing.T) {
	// A single no-ff merge: M has two parents (mainline P, feature F); both
	// reach back to base B. Expect a fork right after M and a join at B.
	recs := []commitRecord{
		{sha: "M", parents: []string{"P", "F"}},
		{sha: "F", parents: []string{"B"}},
		{sha: "P", parents: []string{"B"}},
		{sha: "B"},
	}
	var buf bytes.Buffer
	renderSelfGraph(&buf, recs, false, 0, func(c commitRecord) string { return c.sha }, nil, nil)
	got := buf.String()
	want := "● M\n├─╮\n│ ● F\n● │ P\n● │ B\n╰─╯\n"
	if got != want {
		t.Errorf("renderSelfGraph:\n got: %q\nwant: %q", got, want)
	}
}

func TestVizBodyLines(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	cases := []struct {
		name string
		body string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "\n  \n", nil},
		{"single line", "hello", []string{"  hello"}},
		{"trailing newlines dropped", "hello\n\n", []string{"  hello"}},
		{"interior blank kept", "a\n\nb", []string{"  a", "  ", "  b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := vizBodyLines(commitRecord{body: tc.body})
			if len(got) != len(tc.want) || strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("vizBodyLines(%q) = %#v, want %#v", tc.body, got, tc.want)
			}
		})
	}
}

// TestRenderSelfGraph_Body checks that body lines carry the lane-continuation
// prefix: a single lane (│) under a linear commit, and two lanes (│ │) for a
// commit sitting beside an active sibling lane after a fork.
func TestRenderSelfGraph_Body(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	recs := []commitRecord{
		{sha: "M", parents: []string{"P", "F"}, body: "merge note"},
		{sha: "F", parents: []string{"B"}, body: "feature"},
		{sha: "P", parents: []string{"B"}},
		{sha: "B"},
	}
	var buf bytes.Buffer
	renderSelfGraph(&buf, recs, false, 0, func(c commitRecord) string { return c.sha }, vizBodyLines, nil)
	got := buf.String()
	// M body precedes the fork → single lane. F body sits beside lane P → two.
	want := "● M\n│   merge note\n├─╮\n│ ● F\n│ │   feature\n● │ P\n● │ B\n╰─╯\n"
	if got != want {
		t.Errorf("renderSelfGraph fork+body:\n got: %q\nwant: %q", got, want)
	}
}

// TestLogBehind_TrackingConfigCacheRefMissing: when branch.<name>.remote/merge
// are configured but the remote-tracking ref is absent (pruned / never
// fetched), `gk log --behind` must diagnose the missing cache ref precisely
// instead of claiming no upstream is configured.
func TestLogBehind_TrackingConfigCacheRefMissing(t *testing.T) {
	repo := testutil.NewRepo(t)
	// Hand-write tracking config without ever fetching, so the config is
	// intact but refs/remotes/origin/main does not exist.
	repo.AddRemote("origin", repo.Dir)
	repo.RunGit("config", "branch.main.remote", "origin")
	repo.RunGit("config", "branch.main.merge", "refs/heads/main")

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	cmd := &cobra.Command{Use: "log", RunE: runLog, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().Bool("behind", true, "")
	cmd.Flags().Bool("ahead", false, "")
	cmd.Flags().Bool("fetch", false, "")
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when the remote-tracking ref is missing")
	}
	msg := err.Error()
	if !strings.Contains(msg, "'main' tracks origin/main") ||
		!strings.Contains(msg, "git fetch origin main") {
		t.Errorf("want precise missing-cache-ref hint, got: %v", err)
	}
	if strings.Contains(msg, "no upstream configured") {
		t.Errorf("legacy misdiagnosis still present: %v", err)
	}
}

// TestRenderSelfGraph_BeforeRow checks that beforeRow lines (push boundary,
// tag rules) print verbatim above the matching node row, full width, with no
// lane prefix — restoring the flat path's structure rules in graph mode.
func TestRenderSelfGraph_BeforeRow(t *testing.T) {
	recs := []commitRecord{
		{sha: "C", parents: []string{"B"}},
		{sha: "B", parents: []string{"A"}},
		{sha: "A", parents: []string{"Z"}}, // parent out of view → no lane-end link row
	}
	beforeRow := func(i int, r commitRecord) []string {
		if i == 1 {
			return []string{"--[ boundary ]--"}
		}
		return nil
	}
	var buf bytes.Buffer
	renderSelfGraph(&buf, recs, false, 0, func(c commitRecord) string { return c.sha }, nil, beforeRow)
	got := buf.String()
	want := "● C\n--[ boundary ]--\n● B\n● A\n"
	if got != want {
		t.Errorf("renderSelfGraph beforeRow:\n got: %q\nwant: %q", got, want)
	}
}

// TestResolveGraphFlag: an explicit --graph / --graph=false must beat the
// config default; an untouched flag falls through to config. The old
// `if !graph { graph = cfg }` merge swallowed an explicit false whenever
// config had graph: true, leaving no per-invocation way back to flat view.
func TestResolveGraphFlag(t *testing.T) {
	cases := []struct {
		name    string
		setFlag string // "" = not passed
		cfg     bool
		want    bool
	}{
		{"unset uses config true", "", true, true},
		{"unset uses config false", "", false, false},
		{"explicit true beats config false", "true", false, true},
		{"explicit false beats config true", "false", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("graph", false, "")
			if tc.setFlag != "" {
				if err := cmd.Flags().Set("graph", tc.setFlag); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{Log: config.LogConfig{Graph: tc.cfg}}
			if got := resolveGraphFlag(cmd, cfg); got != tc.want {
				t.Errorf("resolveGraphFlag = %v, want %v", got, tc.want)
			}
		})
	}
}
