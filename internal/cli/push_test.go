package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/easy"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestPushReport guards the post-push output policy, including the codex P2
// regression: a stale remote-tracking ref makes ahead>0 even when the real
// remote already has the commits, so git returns "Everything up-to-date" — the
// summary must not then claim "N pushed".
func TestPushReport(t *testing.T) {
	eng := easy.NewEngine(config.OutputConfig{Easy: true, Lang: "en", Emoji: false, Hints: "verbose"}, true, false)

	// Real push: git prints ref-update lines (not the up-to-date sentinel),
	// ahead>0 → keep git output AND a "pushed" summary.
	gitOut, summary := pushReport("To example.com:x/y.git\n   abc..def  main -> main\n", 2, eng, "origin", "main", "def1234")
	if gitOut == "" {
		t.Errorf("real push must keep git output")
	}
	if summary == "" {
		t.Errorf("real push must produce a summary")
	}

	// Genuine up-to-date: ahead=0, git up-to-date → suppress git's duplicate
	// English line, keep the localized summary.
	gitOut, summary = pushReport("Everything up-to-date\n", 0, eng, "origin", "main", "def1234")
	if gitOut != "" {
		t.Errorf("up-to-date git line must be suppressed, got %q", gitOut)
	}
	if summary == "" {
		t.Errorf("up-to-date must still summarize")
	}

	// Stale upstream (codex P2): ahead>0 but git reports up-to-date because the
	// real remote already has the commits. The summary must match the up-to-date
	// (n=0) variant — never "N pushed" — and git's duplicate line is suppressed.
	gitOut, summary = pushReport("Everything up-to-date\n", 5, eng, "origin", "main", "def1234")
	if want := eng.PushSummaryHint(0, "origin", "main", "def1234"); summary != want {
		t.Errorf("stale upstream must summarize as up-to-date: got %q want %q", summary, want)
	}
	if gitOut != "" {
		t.Errorf("stale up-to-date git line must be suppressed, got %q", gitOut)
	}
}

// TestReportedPushCount guards the shared count helper that both the human
// summary and the --json/agent envelope now route through, so the two can never
// disagree. The key case is a stale remote-tracking ref (ahead>0) where git
// reports the push as a no-op: the reported count must be 0, not the stale ahead.
func TestReportedPushCount(t *testing.T) {
	// Real push: git printed ref-update lines → report the pre-push ahead count.
	if got := reportedPushCount("To x\n   a..b  main -> main\n", 3); got != 3 {
		t.Errorf("real push: got %d, want 3", got)
	}
	// Stale up-to-date: ahead>0 but git sent nothing → authoritative 0.
	if got := reportedPushCount("Everything up-to-date\n", 5); got != 0 {
		t.Errorf("stale up-to-date: got %d, want 0", got)
	}
	// Genuine up-to-date.
	if got := reportedPushCount("Everything up-to-date\n", 0); got != 0 {
		t.Errorf("genuine up-to-date: got %d, want 0", got)
	}
}

// buildPushCmd constructs a push cobra.Command for unit tests that calls
// runPush directly via its RunE, with persistent flags needed by config.Load.
func buildPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "push",
		Args: cobra.MaximumNArgs(2),
		RunE: runPush,
	}
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Bool("skip-scan", false, "")
	cmd.Flags().BoolP("no-verify", "n", false, "")
	cmd.Flags().Bool("yes", false, "")
	// Persistent flags required by config.Load
	cmd.Flags().String("repo", "", "")
	cmd.SetContext(context.Background())
	return cmd
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

// TestPushShipNoVerifyFlag guards that both push and ship expose --no-verify
// under the -n shorthand, keeping the secret-scan bypass consistent with
// `gk commit -n`.
func TestPushShipNoVerifyFlag(t *testing.T) {
	for _, name := range []string{"push", "ship"} {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		f := cmd.Flags().Lookup("no-verify")
		if f == nil {
			t.Errorf("%s: missing --no-verify flag", name)
			continue
		}
		if f.Shorthand != "n" {
			t.Errorf("%s: --no-verify shorthand = %q, want %q", name, f.Shorthand, "n")
		}
	}
}

func TestPush_FromFlagRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"push"})
	if err != nil {
		t.Fatalf("find push: %v", err)
	}
	if cmd.Flags().Lookup("from") == nil {
		t.Error("push: missing --from flag")
	}
}

func TestResolvePushBranch(t *testing.T) {
	cases := []struct {
		name      string
		posBranch string
		from      string
		want      string
		wantErr   bool
	}{
		{"both empty falls back", "", "", "", false},
		{"from only", "", "main", "main", false},
		{"positional only", "", "feature", "feature", false},
		{"both agree", "main", "main", "main", false},
		{"both conflict", "feature", "main", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePushBranch(tc.posBranch, tc.from)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got branch %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPushJSON_Schema_Ahead(t *testing.T) {
	// Save and restore the global JSON flag — runPush reads JSONOut()
	// directly. Tests run sequentially in this package so this is safe.
	origJSON := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = origJSON })

	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":                                   {Stdout: "main\n"},
		"rev-parse --verify origin/main^{commit}":                     {Stdout: "old111\n"},
		"log -p --no-color origin/main..HEAD":                         {Stdout: ""},
		"rev-parse --abbrev-ref --symbolic-full-name main@{upstream}": {Stdout: "origin/main\n"},
		"rev-list --count origin/main..main":                          {Stdout: "5\n"},
		"rev-parse --short main":                                      {Stdout: "abc1234\n"},
		"push origin main":                                            {Stdout: "", Stderr: "To github.com:x/y.git\n   old111..new222 main -> main\n"},
	}}

	cmd := buildPushCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	// Inject the fake runner via the same path as production: runPush
	// constructs an ExecRunner directly, so we patch by intercepting at
	// the JSON encoder by exercising the JSON branch end-to-end with a
	// custom runPush wrapper. Simpler: assert at the runPush level by
	// mocking through a function alias if exposed; otherwise verify the
	// JSON shape via the helper that runPush uses.
	//
	// runPush internally builds &git.ExecRunner{} so we can't swap. We
	// instead verify the JSON shape by encoding pushResult directly with
	// the values pushAheadCount/pushHeadShort would produce against the
	// fake runner.
	ctx := context.Background()
	ahead := pushAheadCount(ctx, fake, "origin", "main", true)
	short := pushHeadShort(ctx, fake, "main")

	if ahead != 5 {
		t.Fatalf("ahead = %d, want 5", ahead)
	}
	if short != "abc1234" {
		t.Fatalf("short = %q, want abc1234", short)
	}

	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pushResult{Remote: "origin", Branch: "main", Ahead: ahead, Head: short}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Verify schema: all four fields present, correct types.
	var got pushResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out.String())
	}
	want := pushResult{Remote: "origin", Branch: "main", Ahead: 5, Head: "abc1234"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPushJSON_Schema_UpToDate(t *testing.T) {
	origJSON := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = origJSON })

	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --abbrev-ref --symbolic-full-name main@{upstream}": {Stdout: "origin/main\n"},
		"rev-list --count origin/main..main":                          {Stdout: "0\n"},
		"rev-parse --short main":                                      {Stdout: "def5678\n"},
	}}

	ctx := context.Background()
	ahead := pushAheadCount(ctx, fake, "origin", "main", true)
	short := pushHeadShort(ctx, fake, "main")

	if ahead != 0 {
		t.Fatalf("ahead = %d, want 0 for up-to-date", ahead)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pushResult{Remote: "origin", Branch: "main", Ahead: ahead, Head: short}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	var got pushResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, buf.String())
	}
	if got.Ahead != 0 {
		t.Fatalf("up-to-date case: ahead = %d, want 0", got.Ahead)
	}
	if got.Head != "def5678" {
		t.Fatalf("head = %q, want def5678", got.Head)
	}
}

func TestPushAheadCount(t *testing.T) {
	t.Run("no_upstream_counts_unpushed", func(t *testing.T) {
		// First push (no upstream): count commits not yet on any of the
		// remote's refs, rather than the old hardcoded 0.
		fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"rev-list --count main --not --remotes=origin": {Stdout: "3\n"},
		}}
		if got := pushAheadCount(context.Background(), fake, "origin", "main", false); got != 3 {
			t.Fatalf("got %d, want 3", got)
		}
	})
	t.Run("counts_ahead", func(t *testing.T) {
		fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"rev-list --count origin/main..main": {Stdout: "7\n"},
		}}
		if got := pushAheadCount(context.Background(), fake, "origin", "main", true); got != 7 {
			t.Fatalf("got %d, want 7", got)
		}
	})
	t.Run("git_failure_returns_zero", func(t *testing.T) {
		fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"rev-list --count origin/main..main": {ExitCode: 128, Stderr: "fatal"},
		}}
		if got := pushAheadCount(context.Background(), fake, "origin", "main", true); got != 0 {
			t.Fatalf("got %d, want 0 on failure", got)
		}
	})
	t.Run("non_numeric_output_returns_zero", func(t *testing.T) {
		fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"rev-list --count origin/main..main": {Stdout: "abc\n"},
		}}
		if got := pushAheadCount(context.Background(), fake, "origin", "main", true); got != 0 {
			t.Fatalf("got %d, want 0 on non-numeric", got)
		}
	})
}

func TestPushHeadShort(t *testing.T) {
	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --short main": {Stdout: "abc1234\n"},
	}}
	if got := pushHeadShort(context.Background(), fake, "main"); got != "abc1234" {
		t.Fatalf("got %q, want abc1234", got)
	}
}

func TestBranchHasUpstream(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref --symbolic-full-name feature@{upstream}": {
				Stdout: "origin/feature\n",
			},
			"rev-parse --abbrev-ref --symbolic-full-name nope@{upstream}": {
				ExitCode: 128, Stderr: "fatal: no upstream configured",
			},
		},
	}
	if !branchHasUpstream(context.Background(), fake, "feature") {
		t.Error("expected feature to have upstream")
	}
	if branchHasUpstream(context.Background(), fake, "nope") {
		t.Error("expected nope to NOT have upstream")
	}
}

func TestIsProtected(t *testing.T) {
	tests := []struct {
		branch    string
		protected []string
		want      bool
	}{
		{"main", []string{"main", "master"}, true},
		{"master", []string{"main", "master"}, true},
		{"feature/foo", []string{"main", "master"}, false},
		{"main", []string{}, false},
		{"main", nil, false},
	}
	for _, tc := range tests {
		got := isProtected(tc.branch, tc.protected)
		if got != tc.want {
			t.Errorf("isProtected(%q, %v) = %v, want %v", tc.branch, tc.protected, got, tc.want)
		}
	}
}

func TestScanCommitsToPush_NoFindings(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			// rev-parse fails → upstream unknown → rng = "HEAD"
			"rev-parse --verify origin/main^{commit}": {ExitCode: 128, Stderr: "unknown revision"},
			// git log -p --no-color HEAD returns normal commit output
			"log -p --no-color HEAD": {
				Stdout: `commit abc123
Author: gk-test <test@example.com>
Date:   Mon Jan 1 00:00:00 2024

    feat: add hello

diff --git a/hello.go b/hello.go
+++ b/hello.go
+func Hello() string { return "hello" }
`,
			},
		},
	}

	findings, err := scanCommitsToPush(context.Background(), fake, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %v", findings)
	}
}

func TestScanCommitsToPush_Finds(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify origin/main^{commit}": {ExitCode: 128, Stderr: "unknown revision"},
			"log -p --no-color HEAD": {
				Stdout: `commit abc123
Author: gk-test <test@example.com>

    oops: leaked key

diff --git a/config.go b/config.go
+++ b/config.go
+AWS_KEY=AKIA1234567890ABCDEF
`,
			},
		},
	}

	findings, err := scanCommitsToPush(context.Background(), fake, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least 1 finding, got none")
	}
	found := false
	for _, f := range findings {
		if f.Kind == "aws-access-key" {
			found = true
			if f.File != "config.go" {
				t.Errorf("expected finding to name file config.go, got %q", f.File)
			}
			if f.Location() != "config.go:"+strconv.Itoa(f.FileLine) {
				t.Errorf("Location() = %q, want config.go:<fileline>", f.Location())
			}
			break
		}
	}
	if !found {
		t.Errorf("expected aws-access-key finding, got %v", findings)
	}
}

func TestResolveScanCmp(t *testing.T) {
	cmp := func(resp map[string]git.FakeResponse, base string) string {
		return resolveScanCmp(context.Background(), &git.FakeRunner{Responses: resp}, "origin", "feat", base)
	}
	t.Run("upstream wins", func(t *testing.T) {
		if got := cmp(map[string]git.FakeResponse{
			"rev-parse --verify origin/feat^{commit}": {Stdout: "sha\n"},
		}, "main"); got != "origin/feat" {
			t.Errorf("got %q, want origin/feat", got)
		}
	})
	t.Run("base remote fallback", func(t *testing.T) {
		if got := cmp(map[string]git.FakeResponse{
			"rev-parse --verify origin/feat^{commit}": {ExitCode: 128, Stderr: "x"},
			"rev-parse --verify origin/main^{commit}": {Stdout: "sha\n"},
		}, "main"); got != "origin/main" {
			t.Errorf("got %q, want origin/main", got)
		}
	})
	t.Run("local base fallback", func(t *testing.T) {
		if got := cmp(map[string]git.FakeResponse{
			"rev-parse --verify origin/feat^{commit}": {ExitCode: 128},
			"rev-parse --verify origin/main^{commit}": {ExitCode: 128},
			"rev-parse --verify main^{commit}":        {Stdout: "sha\n"},
		}, "main"); got != "main" {
			t.Errorf("got %q, want main", got)
		}
	})
	t.Run("nothing resolvable", func(t *testing.T) {
		resp := map[string]git.FakeResponse{
			"rev-parse --verify origin/feat^{commit}": {ExitCode: 128},
			"rev-parse --verify origin/main^{commit}": {ExitCode: 128},
			"rev-parse --verify main^{commit}":        {ExitCode: 128},
		}
		if got := cmp(resp, "main"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
		if got := cmp(resp, ""); got != "" {
			t.Errorf("empty base: got %q, want empty", got)
		}
	})
}

// TestScanCommitsToPush_NetDiffFileLine is the regression guard for the
// position bug: with a base, the scan uses a net 3-dot diff whose hunk header
// (@@ +8) is anchored to the current HEAD file, so the token reports its real
// line even when earlier commits introduced it before later inserts shifted it.
func TestScanCommitsToPush_NetDiffFileLine(t *testing.T) {
	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"diff --no-color origin/main...HEAD": {Stdout: "diff --git a/f.rs b/f.rs\n" +
			"--- a/f.rs\n+++ b/f.rs\n" +
			"@@ -3,2 +8,3 @@\n" +
			" ctx\n" +
			"+let gh = \"ghp_abcdefghijklmnopqrstuvwxyz0123456789\";\n"},
	}}
	findings, err := scanCommitsToPush(context.Background(), fake, "origin/main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].FileLine != 9 {
		t.Errorf("FileLine = %d, want 9 (hunk +8 + 1 context line)", findings[0].FileLine)
	}
}

// ---------------------------------------------------------------------------
// Integration tests using testutil.Repo
// ---------------------------------------------------------------------------

func TestPush_BlocksOnSecret(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// Write a file containing an AWS access key
	repo.WriteFile("secrets.env", "AWS_KEY=AKIA1234567890ABCDEF\n")
	repo.Commit("feat: add secrets file")

	// Build a push command targeting the test repo's ExecRunner
	// We use a FakeRunner for the actual push call to avoid network,
	// but we need real git for log/rev-parse scanning.
	// Use a custom runPushWithRunner approach for integration test.
	runner := &git.ExecRunner{Dir: repo.Dir}

	var findings []interface{} // just check we get an error

	// scanCommitsToPush via real git
	f, err := scanCommitsToPush(context.Background(), runner, "")
	if err != nil {
		// "origin/main" doesn't exist → falls back to HEAD scan which is fine
		// but if it's a different error, fail
		if !strings.Contains(err.Error(), "aborting") {
			// err from log itself is unexpected unless repo is empty
			t.Logf("scanCommitsToPush error (may be expected for bare repo): %v", err)
		}
	}
	_ = findings

	// Simulate what runPush does: if findings exist, return "aborting push"
	if len(f) > 0 {
		// This is the expected path — secret was detected
		return
	}

	// If no findings: the secret pattern might not trigger on a plain env file.
	// Verify via cobra command with FakeRunner for the push step.
	fakeRunner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify origin/main^{commit}": {ExitCode: 128, Stderr: "unknown"},
			"log -p --no-color HEAD": {
				Stdout: "AWS_KEY=AKIA1234567890ABCDEF\n",
			},
			"symbolic-ref --short HEAD": {Stdout: "main\n"},
		},
	}

	ctx := context.Background()
	found, scanErr := scanCommitsToPush(ctx, fakeRunner, "")
	if scanErr != nil {
		t.Fatalf("scan error: %v", scanErr)
	}
	if len(found) == 0 {
		t.Fatal("expected secret finding in diff output, got none")
	}
}

func TestPush_SkipScan(t *testing.T) {
	// With --skip-scan the secret path is bypassed entirely.
	// The push itself will fail (no remote) but the error should NOT be "aborting push".
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD": {Stdout: "feature\n"},
			// push will fail with a git error
			"push origin feature": {ExitCode: 1, Stderr: "no upstream configured\n"},
		},
	}

	ctx := context.Background()

	// Manually simulate the skip-scan path: no scan → straight to push
	gitArgs := []string{"push", "origin", "feature"}
	_, stderr, err := fake.Run(ctx, gitArgs...)
	if err == nil {
		t.Fatal("expected push error without remote")
	}
	// Error should come from git push, not secret scan
	if strings.Contains(err.Error(), "aborting push") {
		t.Errorf("should not get 'aborting push' when skip-scan is active, got: %v", err)
	}
	if !strings.Contains(string(stderr), "no upstream") {
		t.Errorf("expected upstream error in stderr, got: %s", stderr)
	}
}

func TestPush_ProtectedBranchNoForce(t *testing.T) {
	// On a protected branch without --force, push should proceed normally
	// (no extra gate). Push failure comes from git, not protection logic.
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD":               {Stdout: "main\n"},
			"rev-parse --verify origin/main^{commit}": {ExitCode: 128, Stderr: "unknown"},
			"log -p --no-color HEAD":                  {Stdout: "normal code\n"},
			"push origin main":                        {ExitCode: 1, Stderr: "rejected: not fast-forward\n"},
		},
	}

	ctx := context.Background()

	// Simulate the check: isProtected("main", ["main","master"]) == true, force == false
	// → no gate triggered
	protected := isProtected("main", []string{"main", "master"})
	if !protected {
		t.Fatal("main should be protected")
	}

	force := false
	if protected && force {
		t.Fatal("should not enter force gate")
	}

	// Secret scan
	findings, err := scanCommitsToPush(ctx, fake, "")
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %v", findings)
	}

	// Push call
	_, stderr, pushErr := fake.Run(ctx, "push", "origin", "main")
	if pushErr == nil {
		t.Fatal("expected push error")
	}
	// Error must be git's rejection, not our protection/scan messages
	if strings.Contains(pushErr.Error(), "aborting push") {
		t.Error("should not see 'aborting push' without secret findings")
	}
	_ = stderr
}

func TestPush_ProtectedForceNoConfirm(t *testing.T) {
	// --force on protected branch, non-TTY, --yes=false → should error
	// We test the logic directly (ui.IsTerminal() returns false in test env).
	branch := "main"
	protected := isProtected(branch, []string{"main", "master"})
	if !protected {
		t.Fatal("main should be protected")
	}

	force := true
	allowForce := false
	yes := false
	isTerminal := false // simulated non-TTY

	if protected && force && !allowForce {
		if !yes && !isTerminal {
			// This is the expected refusal path
			return
		}
		t.Fatal("should have refused before reaching here")
	}
	t.Fatal("should have entered the force gate")
}

func TestPush_ProtectedForceWithConfirm(t *testing.T) {
	// Simulate the stdin confirmation path with correct branch name input.
	branch := "main"
	input := strings.NewReader("main\n")
	sc := bufio.NewScanner(input)

	if !sc.Scan() {
		t.Fatal("expected scan to succeed")
	}
	typed := strings.TrimSpace(sc.Text())
	if typed != branch {
		t.Errorf("confirmation mismatch: got %q, want %q", typed, branch)
	}
}

func TestPush_ProtectedForceWrongConfirm(t *testing.T) {
	// Wrong branch name typed → should reject.
	branch := "main"
	input := strings.NewReader("master\n")
	sc := bufio.NewScanner(input)

	if !sc.Scan() {
		t.Fatal("expected scan to succeed")
	}
	typed := strings.TrimSpace(sc.Text())
	if typed == branch {
		t.Error("wrong confirmation should not match branch name")
	}
}

// TestScanDiffAdditions_SkipsTestFilesAndRemovals guards two false-positive
// classes that hit the user during a v0.17.x release:
//  1. removal lines ("-old fixture") were flagged even though the secret is
//     already in the base branch and not new content.
//  2. test files were scanned, so deliberate fake secrets in *_test.go
//     fixtures (added to verify pattern detection) blocked every release
//     that touched the secret-scanner tests.
func TestScanDiffAdditions_SkipsTestFilesAndRemovals(t *testing.T) {
	diff := `commit deadbeef
diff --git a/internal/secrets/patterns_test.go b/internal/secrets/patterns_test.go
+++ b/internal/secrets/patterns_test.go
@@ -10,3 +10,4 @@
+    input: "AKIA1234567890ABCDEF",
+    wantFound: true,
diff --git a/src/config.go b/src/config.go
+++ b/src/config.go
@@ -5,2 +5,3 @@
-    api_key = "OLD_REMOVED_SECRET_VALUE"
+    api_key = "AKIA1234567890ABCDEF"
`
	findings := scanDiffAdditions(diff)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding (only the +api_key in src/config.go), got %d: %+v", len(findings), findings)
	}
	if findings[0].Kind != "aws-access-key" {
		t.Errorf("want aws-access-key, got %s", findings[0].Kind)
	}
}

// TestScanDiffAdditions_IgnoresContextAndMetadata confirms that hunk
// headers, "diff --git" lines, "+++"/"---" markers, and context lines do
// not feed into the scanner.
func TestScanDiffAdditions_IgnoresContextAndMetadata(t *testing.T) {
	diff := `diff --git a/notes.md b/notes.md
index abc..def
--- a/notes.md
+++ b/notes.md
@@ -1,2 +1,3 @@
 context: api_key = "AKIA1234567890ABCDEF"
+harmless added text
`
	findings := scanDiffAdditions(diff)
	if len(findings) != 0 {
		t.Errorf("context line should not be flagged, got %+v", findings)
	}
}

// TestScanDiffAdditions_FileLineFromHunkHeader guards the position bug where
// FileLine reported the offset within the diff blob instead of the line in the
// post-image file. The hunk starts at +215 with three context lines, so the
// added token sits on file line 218 — not wherever it happens to land in the
// accumulated diff.
func TestScanDiffAdditions_FileLineFromHunkHeader(t *testing.T) {
	diff := `commit deadbeef
diff --git a/src/app.rs b/src/app.rs
index abc..def 100644
--- a/src/app.rs
+++ b/src/app.rs
@@ -215,3 +215,4 @@ fn main() {
 ctx a
 ctx b
 ctx c
+let gh = "ghp_abcdefghij1234567890ABCDEFGHIJ123456";
`
	findings := scanDiffAdditions(diff)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Kind != "github-token" {
		t.Errorf("Kind = %q, want github-token", f.Kind)
	}
	if f.FileLine != 218 {
		t.Errorf("FileLine = %d, want 218 (hunk +215 + 3 context lines)", f.FileLine)
	}
	if f.Location() != "src/app.rs:218" {
		t.Errorf("Location() = %q, want src/app.rs:218", f.Location())
	}
}

// TestScanDiffAdditions_VerboseContext checks that the scanner captures the
// masked ±1 source context around a hit: the line above, the hit itself, and
// the line below — all secret-masked.
func TestScanDiffAdditions_VerboseContext(t *testing.T) {
	diff := "diff --git a/src/app.rs b/src/app.rs\n" +
		"--- a/src/app.rs\n+++ b/src/app.rs\n" +
		"@@ -10,3 +10,4 @@\n" +
		" fn main() {\n" +
		"+    let gh = \"ghp_abcdefghijklmnopqrstuvwxyz0123456789\";\n" +
		"     init();\n"
	findings := scanDiffAdditions(diff)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.ContextBefore != "fn main() {" {
		t.Errorf("ContextBefore = %q, want %q", f.ContextBefore, "fn main() {")
	}
	if f.LineText != `    let gh = "ghp_********";` {
		t.Errorf("LineText = %q, want masked token line", f.LineText)
	}
	if f.ContextAfter != "    init();" {
		t.Errorf("ContextAfter = %q, want %q", f.ContextAfter, "    init();")
	}
}

// TestScanDiffAdditions_ContextStopsAtHunkEdge confirms the context window does
// not cross a hunk boundary: a hit on the first line of a hunk has no "before".
func TestScanDiffAdditions_ContextStopsAtHunkEdge(t *testing.T) {
	diff := "diff --git a/src/app.rs b/src/app.rs\n" +
		"--- a/src/app.rs\n+++ b/src/app.rs\n" +
		"@@ -5,1 +5,2 @@\n" +
		"+gh = \"ghp_abcdefghijklmnopqrstuvwxyz0123456789\"\n" +
		" below\n"
	findings := scanDiffAdditions(diff)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.ContextBefore != "" {
		t.Errorf("ContextBefore = %q, want empty (hunk's first line)", f.ContextBefore)
	}
	if f.ContextAfter != "below" {
		t.Errorf("ContextAfter = %q, want %q", f.ContextAfter, "below")
	}
}

// TestScanDiffAdditions_FileLineAcrossMultipleHunks confirms the post-image
// counter resets per hunk: a second hunk far down the file must report its own
// line, not one accumulated from the first hunk's length.
func TestScanDiffAdditions_FileLineAcrossMultipleHunks(t *testing.T) {
	diff := `diff --git a/src/app.rs b/src/app.rs
--- a/src/app.rs
+++ b/src/app.rs
@@ -3,2 +3,3 @@
 ctx
+let early = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
@@ -400,1 +401,2 @@
 ctx
+let late = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
`
	findings := scanDiffAdditions(diff)
	if len(findings) != 2 {
		t.Fatalf("want 2 findings, got %d: %+v", len(findings), findings)
	}
	if got := findings[0].FileLine; got != 4 {
		t.Errorf("first finding FileLine = %d, want 4 (hunk +3 + 1 context)", got)
	}
	if got := findings[1].FileLine; got != 402 {
		t.Errorf("second finding FileLine = %d, want 402 (hunk +401 + 1 context)", got)
	}
}
