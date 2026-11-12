package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/testutil"
)

// newGuardFetchFixture returns a clone of origin whose remote-tracking refs
// predate a branch that exists only on origin and carries a secret.
func newGuardFetchFixture(t *testing.T) *testutil.Repo {
	t.Helper()
	origin := testutil.NewRepo(t)
	origin.WriteFile("seed.txt", "seed\n")
	origin.Commit("seed: initial")

	local := testutil.NewRepo(t)
	local.AddRemote("origin", origin.Dir)
	local.RunGit("fetch", "origin")
	local.RunGit("reset", "--hard", "origin/main")

	origin.RunGit("switch", "-c", "leak")
	// Assembled at runtime so this file does not trip secret scanners.
	origin.WriteFile("remote_only.env", "token = \"ghp_"+"wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx\"\n")
	origin.Commit("feat: remote-only secret")
	return local
}

func runGuardCheckCmd(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	prevRepo, prevJSON := flagRepo, flagJSON
	flagRepo, flagJSON = dir, false
	t.Cleanup(func() { flagRepo, flagJSON = prevRepo, prevJSON })

	cmd := &cobra.Command{Use: "check", RunE: runGuardCheck}
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("staged", false, "")
	cmd.Flags().Bool("history", false, "")
	cmd.Flags().Bool("fetch", false, "")
	cmd.SetArgs(append([]string{"--json"}, args...))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return buf.String(), err
}

func TestRunGuardCheck_FetchScansRemoteOnlyBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	local := newGuardFetchFixture(t)

	out, err := runGuardCheckCmd(t, local.Dir)
	if err != nil || strings.Contains(out, "remote_only.env") {
		t.Fatalf("without --fetch the unfetched branch must be out of reach: err=%v\n%s", err, out)
	}

	out, err = runGuardCheckCmd(t, local.Dir, "--fetch")
	if err == nil || !strings.Contains(out, `"file":"remote_only.env"`) {
		t.Fatalf("--fetch must scan the remote-only branch: err=%v\n%s", err, out)
	}
}

// A failed fetch must not pass as a full scan: the local refs are still
// scanned, and a warning names the remote that could not be fetched.
func TestRunGuardCheck_FetchFailureWarnsAndStillScans(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	local := newGuardFetchFixture(t)
	local.WriteFile("local.env", "token = \"ghp_"+"wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx\"\n")
	local.Commit("feat: local secret")
	local.RunGit("remote", "set-url", "origin", local.Dir+"/missing-remote.git")

	out, err := runGuardCheckCmd(t, local.Dir, "--fetch")
	if err == nil {
		t.Fatalf("want a non-zero result:\n%s", out)
	}
	if !strings.Contains(out, `"file":"local.env"`) {
		t.Errorf("local refs must still be scanned:\n%s", out)
	}
	if !strings.Contains(out, `"severity":"warn"`) || !strings.Contains(out, "origin") {
		t.Errorf("want a warning naming the remote that failed:\n%s", out)
	}
}

// testPAT is assembled at runtime so this file does not trip secret scanners.
var testPAT = "ghp_" + "wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx"

// The default scope is the current content of the tracked files: a secret
// removed in a later commit is history-only, and an uncommitted edit counts.
// --staged sees only what is staged.
func TestRunGuardCheck_ScopeDefaultHistoryStaged(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("seed.txt", "seed\n")
	repo.Commit("seed: initial")
	repo.WriteFile("old.env", "token = \""+testPAT+"\"\n")
	repo.Commit("feat: add secret")
	repo.RunGit("rm", "-q", "old.env")
	repo.Commit("fix: remove secret")

	out, err := runGuardCheckCmd(t, repo.Dir)
	if err != nil || strings.Contains(out, "old.env") {
		t.Fatalf("default scope must not report a history-only secret: err=%v\n%s", err, out)
	}
	out, err = runGuardCheckCmd(t, repo.Dir, "--history")
	if err == nil || !strings.Contains(out, `"file":"old.env"`) {
		t.Fatalf("--history must report the removed secret: err=%v\n%s", err, out)
	}

	repo.WriteFile("seed.txt", "seed\ntoken = \""+testPAT+"\"\n")
	out, err = runGuardCheckCmd(t, repo.Dir)
	if err == nil || !strings.Contains(out, `"file":"seed.txt"`) {
		t.Fatalf("default scope must report an uncommitted edit: err=%v\n%s", err, out)
	}
	out, err = runGuardCheckCmd(t, repo.Dir, "--staged")
	if err != nil || strings.Contains(out, "seed.txt") {
		t.Fatalf("--staged must ignore an unstaged edit: err=%v\n%s", err, out)
	}
	repo.RunGit("add", "seed.txt")
	out, err = runGuardCheckCmd(t, repo.Dir, "--staged")
	if err == nil || !strings.Contains(out, `"file":"seed.txt"`) {
		t.Fatalf("--staged must report the staged secret: err=%v\n%s", err, out)
	}
}

func TestHooks_PreCommitScansStagedChanges(t *testing.T) {
	for _, h := range knownHooks() {
		if h.Name == "pre-commit" && h.Script != "exec gk guard check --staged" {
			t.Errorf("pre-commit script = %q; a full scan would block every commit on old secrets", h.Script)
		}
	}
}

// `git remote` failing is not the same as having no remote: the scan did not
// cover the remotes, so it must warn instead of noting a normal state.
func TestGuardFetch_RemoteListFailureWarns(t *testing.T) {
	vs := guardFetch(context.Background(), &git.ExecRunner{Dir: filepath.Join(t.TempDir(), "missing")})
	if len(vs) != 1 || vs[0].Severity != policy.SeverityWarn || !strings.Contains(vs[0].Message, "could not list remotes") {
		t.Fatalf("violations = %+v, want one warning about listing remotes", vs)
	}
}

// The flag groups live on the real command built in init(), so check that
// command, not a copy.
func TestGuardCheck_ScopeFlagsAreExclusive(t *testing.T) {
	check, _, err := rootCmd.Find([]string{"guard", "check"})
	if err != nil {
		t.Fatalf("find guard check: %v", err)
	}
	for _, args := range [][]string{{"--staged", "--history"}, {"--staged", "--fetch"}, {"--staged", "--verify"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Cleanup(func() {
				for _, name := range []string{"staged", "history", "fetch", "verify"} {
					f := check.Flags().Lookup(name)
					_ = f.Value.Set(f.DefValue)
					f.Changed = false
				}
			})
			if err := check.ParseFlags(args); err != nil {
				t.Fatalf("parse %v: %v", args, err)
			}
			if err := check.ValidateFlagGroups(); err == nil {
				t.Errorf("%v must be rejected", args)
			}
		})
	}
}

func TestGuardCheck_VerificationOutput(t *testing.T) {
	vs := []policy.Violation{
		{RuleID: "secret_patterns", Severity: policy.SeverityError, File: "a", Line: 1, Message: "m",
			Verification: &policy.Verification{Status: "valid", Account: "123456789012", ARN: "arn:aws:iam::123456789012:user/ci"}},
		{RuleID: "secret_patterns", Severity: policy.SeverityError, File: "b", Line: 2, Message: "m",
			Verification: &policy.Verification{Status: "rejected", Code: "InvalidClientTokenId"}},
		{RuleID: "secret_patterns", Severity: policy.SeverityError, File: "c", Line: 3, Message: "m"},
		{RuleID: "secret_patterns", Severity: policy.SeverityError, File: "d", Line: 4, Message: "m",
			Verification: &policy.Verification{Provider: "slack", Status: "valid", Account: "T0123", AccountName: "Acme, Inc", URL: "https://acme.slack.com/", Identity: "U0456", IdentityName: "deploy bot"}},
		{RuleID: "secret_patterns", Severity: policy.SeverityError, File: "e", Line: 5, Message: "m",
			Verification: &policy.Verification{Provider: "gcp", Status: "valid", Account: "projects/123", Code: "API_KEY_HTTP_REFERRER_BLOCKED"}},
	}
	var human bytes.Buffer
	printViolationsHuman(&human, vs)
	for _, want := range []string{
		"verify: valid (account 123456789012, arn:aws:iam::123456789012:user/ci)",
		"verify: rejected (InvalidClientTokenId)",
		`verify: valid (account T0123 "Acme, Inc" https://acme.slack.com/, user U0456 "deploy bot")`,
		"verify: valid (account projects/123, API_KEY_HTTP_REFERRER_BLOCKED)",
	} {
		if !strings.Contains(human.String(), want) {
			t.Errorf("human output lacks %q:\n%s", want, human.String())
		}
	}
	if strings.Count(human.String(), "verify:") != 4 {
		t.Errorf("unverified finding got a verify line:\n%s", human.String())
	}

	var nd bytes.Buffer
	if err := printViolationsNDJSON(&nd, vs); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(nd.String()), "\n")
	if !strings.Contains(lines[0], `"verification":{"status":"valid","account":"123456789012","arn":"arn:aws:iam::123456789012:user/ci"}`) ||
		!strings.Contains(lines[1], `"verification":{"status":"rejected","code":"InvalidClientTokenId"}`) ||
		strings.Contains(lines[2], "verification") ||
		!strings.Contains(lines[3], `"verification":{"provider":"slack","status":"valid","account":"T0123","identity":"U0456","account_name":"Acme, Inc","url":"https://acme.slack.com/","identity_name":"deploy bot"}`) {
		t.Errorf("NDJSON =\n%s", nd.String())
	}
}
