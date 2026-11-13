package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/policy/rules"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/secrets/gitleaksrules"
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

// An OCI API key is checked with the profile that names its fingerprint.
// The profile and the key are in different files, and after the removal
// commit they are only in different commits of the history. This runs the
// real engine on real git output; only the network call is replaced.
func TestGuardCheck_OCIKeyIsPairedAcrossFilesAndCommits(t *testing.T) {
	newKey := func() *rsa.PrivateKey {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	pemOf := func(key *rsa.PrivateKey) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	}
	key, sshKey := newKey(), newKey()
	fingerprint := secrets.NewOCICredential(key, nil).Value()
	user := "ocid1.user.oc1.." + strings.Repeat("u", 60)
	tenancy := "ocid1.tenancy.oc1.." + strings.Repeat("t", 60)

	repo := testutil.NewRepo(t)
	repo.WriteFile(".oci/config", "[DEFAULT]\nuser="+user+"\nfingerprint="+fingerprint+"\nkey_file=~/.oci/oci_api_key.pem\ntenancy="+tenancy+"\nregion=ap-seoul-1\n")
	repo.Commit("chore: add the oci config")
	repo.WriteFile("keys/oci_api_key.pem", pemOf(key))
	repo.WriteFile("ssh/id_rsa", pemOf(sshKey))
	repo.Commit("chore: add keys")

	var sent []*secrets.Credential
	rule := rules.NewSecretPatternsRule()
	rule.ScanDiff, rule.ScanFile = gitleaksrules.ScanDiff, gitleaksrules.ScanFile
	rule.Verify = func(_ context.Context, creds []*secrets.Credential) map[string]policy.Verification {
		sent = creds
		out := map[string]policy.Verification{}
		for _, c := range creds {
			p := c.OCIProfiles()[0]
			out[c.ID()] = policy.Verification{Provider: c.Provider(), Status: "valid", Account: p.Tenancy, Identity: p.User}
		}
		return out
	}
	evaluate := func(in policy.Input) []policy.Violation {
		t.Helper()
		sent = nil
		in.Runner, in.WorkDir, in.Verify = &git.ExecRunner{Dir: repo.Dir}, repo.Dir, true
		vs, err := rule.Evaluate(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		return vs
	}
	checkPaired := func(name string, vs []policy.Violation) {
		t.Helper()
		want := secrets.OCIProfile{Fingerprint: fingerprint, User: user, Tenancy: tenancy, Region: "ap-seoul-1"}
		if len(sent) != 1 || sent[0].Provider() != secrets.ProviderOCI || sent[0].Value() != fingerprint || sent[0].OCIProfiles()[0] != want {
			t.Fatalf("%s: credentials sent to Verify = %v, want the one OCI key with its profile", name, sent)
		}
		var paired, unpaired int
		for _, v := range vs {
			switch v.File {
			case "keys/oci_api_key.pem":
				paired++
				if v.Verification == nil || v.Verification.Provider != "oci" || v.Verification.Account != tenancy || v.Verification.Identity != user {
					t.Errorf("%s: OCI key verification = %+v", name, v.Verification)
				}
				if !strings.Contains(v.Hint, "OCI console") || v.Severity != policy.SeverityError {
					t.Errorf("%s: OCI key hint = %q, severity %v", name, v.Hint, v.Severity)
				}
			case "ssh/id_rsa":
				unpaired++
				if v.Verification != nil {
					t.Errorf("%s: the SSH key got a verification: %+v", name, v.Verification)
				}
			}
		}
		if paired != 1 || unpaired != 1 {
			t.Errorf("%s: findings for the OCI key = %d and the SSH key = %d, want 1 and 1", name, paired, unpaired)
		}
		if got, want := vs[len(vs)-1].Message, "verify: checked 1 credential(s): OCI 1 valid"; got != want {
			t.Errorf("%s: summary = %q, want %q", name, got, want)
		}
	}

	checkPaired("tracked files", evaluate(policy.Input{}))

	repo.RunGit("rm", "-q", "-r", ".oci", "keys", "ssh")
	repo.Commit("chore: remove the config and the keys")
	checkPaired("history", evaluate(policy.Input{History: true}))

	// The working tree no longer holds them, so the default scope finds nothing to check.
	vs := evaluate(policy.Input{})
	if len(sent) != 0 || !strings.Contains(vs[len(vs)-1].Message, "no AWS, Slack, GCP, or OCI credential") {
		t.Errorf("after removal: sent = %v, summary = %q", sent, vs[len(vs)-1].Message)
	}
}

// A key is often not a plain PEM file: it is indented in a YAML block or a
// heredoc, or it is one line with escaped line breaks in an environment or
// JSON file. The engine must still match it, and the match must bind.
func TestGuardCheck_OCIKeyInIndentedAndEscapedPEM(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	plain := strings.TrimSuffix(string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), "\n")
	fingerprint := secrets.NewOCICredential(key, nil).Value()
	profile := secrets.OCIProfile{
		Fingerprint: fingerprint,
		User:        "ocid1.user.oc1.." + strings.Repeat("u", 60),
		Tenancy:     "ocid1.tenancy.oc1.." + strings.Repeat("t", 60),
		Region:      "ap-seoul-1",
	}
	files := map[string]string{
		"deploy/values.yaml": "oci:\n  private_key: |\n    " + strings.ReplaceAll(plain, "\n", "\n    ") + "\n  region: ap-seoul-1\n",
		"main.tf":            "variable \"private_key\" {\n\tdefault = <<-EOT\n\t\t" + strings.ReplaceAll(plain, "\n", "\n\t\t") + "\n\tEOT\n}\n",
		"deploy.env":         `OCI_CLI_KEY_CONTENT="` + strings.ReplaceAll(plain, "\n", `\n`) + `\n"` + "\n",
		"sa.json":            `{"private_key": "` + strings.ReplaceAll(plain, "\n", `\n`) + `\n", "region": "ap-seoul-1"}` + "\n",
	}
	for path, content := range files {
		t.Run(path, func(t *testing.T) {
			var bound *secrets.Credential
			for _, f := range gitleaksrules.ScanFile(path, content) {
				if f.Kind == "private-key" {
					bound = secrets.BindOCI(f.Credential, []secrets.OCIProfile{profile})
				}
			}
			if bound == nil || bound.Value() != fingerprint || !bound.OCIKey().Equal(key) {
				t.Errorf("the key in %s did not bind to its profile: %v", path, bound)
			}
		})
	}
}
