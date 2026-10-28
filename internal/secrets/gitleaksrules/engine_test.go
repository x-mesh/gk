package gitleaksrules

import (
	"fmt"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/secrets"
)

func addDiff(path string, startLine int, body ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%[1]s b/%[1]s\n--- a/%[1]s\n+++ b/%[1]s\n@@ -1,1 +%[2]d,%[3]d @@\n", path, startLine, len(body))
	for _, l := range body {
		b.WriteString(l + "\n")
	}
	return b.String()
}

func kinds(fs []secrets.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func find(fs []secrets.Finding, kind string) (secrets.Finding, bool) {
	for _, f := range fs {
		if f.Kind == kind {
			return f, true
		}
	}
	return secrets.Finding{}, false
}

// Fixture secrets are assembled at runtime so this file itself does not trip
// secret scanners.
var (
	awsKey    = "AKIA" + "QYLPMN5HHHFPZAM2"
	githubPAT = "ghp_" + "wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx"
)

func TestRulesetLoads(t *testing.T) {
	if !strings.HasPrefix(Version(), "v8.") {
		t.Errorf("Version() = %q", Version())
	}
	if RuleCount() < 200 {
		t.Errorf("RuleCount() = %d, want >= 200", RuleCount())
	}
}

func TestScanDiff_AWSKeyReportsFileAndLine(t *testing.T) {
	diff := addDiff("config/app.env", 10, "+# creds", "+aws_access_key_id = "+awsKey)
	fs := ScanDiff(diff)
	f, ok := find(fs, "aws-access-token")
	if !ok {
		t.Fatalf("aws-access-token not found in %v", kinds(fs))
	}
	if f.File != "config/app.env" || f.FileLine != 11 {
		t.Errorf("location = %s:%d, want config/app.env:11", f.File, f.FileLine)
	}
	if strings.Contains(f.Sample, awsKey) {
		t.Errorf("sample leaks the secret: %q", f.Sample)
	}
}

func TestScanDiff_GitHubPAT(t *testing.T) {
	fs := ScanDiff(addDiff("a.sh", 1, "+export GH="+githubPAT))
	if _, ok := find(fs, "github-pat"); !ok {
		t.Fatalf("github-pat not found in %v", kinds(fs))
	}
}

func TestScanDiff_MultiLinePrivateKey(t *testing.T) {
	body := "+-----BEGIN RSA PRIVATE KEY-----\n" +
		"+MIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun\n" +
		"+VTLw7onLRnrq0/IzW7yWR7QkrmBL7jTKEn5u+qKhbwKfBstIs+bMY2Zkp18gnTxK\n" +
		"+-----END RSA PRIVATE KEY-----"
	fs := ScanDiff(addDiff("id_rsa", 1, strings.Split(body, "\n")...))
	f, ok := find(fs, "private-key")
	if !ok {
		t.Fatalf("private-key not found in %v", kinds(fs))
	}
	if f.FileLine != 1 {
		t.Errorf("FileLine = %d, want 1", f.FileLine)
	}
}

func TestScanDiff_RuleAllowlistSkipsExampleKey(t *testing.T) {
	fs := ScanDiff(addDiff("docs.md", 1, "+key = AKIAIOSFODNN7EXAMPLE"))
	if _, ok := find(fs, "aws-access-token"); ok {
		t.Errorf("EXAMPLE key must be allowlisted: %v", kinds(fs))
	}
}

func TestScanDiff_GlobalPathAllowlistSkipsLockfile(t *testing.T) {
	fs := ScanDiff(addDiff("package-lock.json", 1, "+\"token\": \""+githubPAT+"\""))
	if len(fs) != 0 {
		t.Errorf("lockfile must be skipped, got %v", kinds(fs))
	}
}

func TestScanDiff_LowEntropyTokenSkipped(t *testing.T) {
	fs := ScanDiff(addDiff("a.sh", 1, "+export GH=ghp_"+strings.Repeat("a", 36)))
	if _, ok := find(fs, "github-pat"); ok {
		t.Errorf("monotonous token must fail the entropy check: %v", kinds(fs))
	}
}

func TestScanDiff_PathOnlyRuleMatchesBinaryFile(t *testing.T) {
	diff := "diff --git a/certs/client.p12 b/certs/client.p12\nnew file mode 100644\nBinary files /dev/null and b/certs/client.p12 differ\n"
	fs := ScanDiff(diff)
	f, ok := find(fs, "pkcs12-file")
	if !ok {
		t.Fatalf("pkcs12-file not found in %v", kinds(fs))
	}
	if f.File != "certs/client.p12" || f.FileLine != 0 {
		t.Errorf("location = %s:%d, want certs/client.p12:0", f.File, f.FileLine)
	}
}

func TestScanDiff_RemovedLinesAndDeletedFilesAreNotScanned(t *testing.T) {
	removed := "diff --git a/a.sh b/a.sh\n--- a/a.sh\n+++ b/a.sh\n@@ -1,2 +1,1 @@\n-export GH=" + githubPAT + "\n keep\n"
	deleted := "diff --git a/old.p12 b/old.p12\ndeleted file mode 100644\n--- a/old.p12\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-x\n"
	if fs := ScanDiff(removed + deleted); len(fs) != 0 {
		t.Errorf("got %v, want none", kinds(fs))
	}
}

func TestScanDiff_LineNumberCountsContextLines(t *testing.T) {
	diff := "diff --git a/a.sh b/a.sh\n--- a/a.sh\n+++ b/a.sh\n@@ -4,3 +20,4 @@\n ctx\n ctx\n+export GH=" + githubPAT + "\n ctx\n"
	f, ok := find(ScanDiff(diff), "github-pat")
	if !ok {
		t.Fatal("github-pat not found")
	}
	if f.FileLine != 22 {
		t.Errorf("FileLine = %d, want 22", f.FileLine)
	}
}

func TestScanDiff_GitLogPatchesAreScannedPerCommit(t *testing.T) {
	log := "commit abc\nAuthor: t\n\n    add key\n\n" + addDiff("a.env", 1, "+k="+awsKey) +
		"\ncommit def\nAuthor: t\n\n    add other\n\n" + addDiff("b.sh", 1, "+export GH="+githubPAT)
	fs := ScanDiff(log)
	if _, ok := find(fs, "aws-access-token"); !ok {
		t.Errorf("first commit missed: %v", kinds(fs))
	}
	if _, ok := find(fs, "github-pat"); !ok {
		t.Errorf("second commit missed: %v", kinds(fs))
	}
}

func synth(rules []jsonRule, global allowlist) *engine {
	return compile(ruleset{Source: source{GitleaksVersion: "vtest"}, Allowlist: global, Rules: rules})
}

func TestEngine_KeywordPrefilterGatesRegex(t *testing.T) {
	e := synth([]jsonRule{{ID: "r", Regex: `[0-9]{6}`, Keywords: []string{"ZZKey"}}}, allowlist{})
	if fs := e.scanDiff(addDiff("a", 1, "+value 123456")); len(fs) != 0 {
		t.Errorf("no keyword, got %v", kinds(fs))
	}
	if fs := e.scanDiff(addDiff("a", 1, "+zzkey 123456")); len(fs) != 1 {
		t.Errorf("keyword is case-insensitive, got %v", kinds(fs))
	}
}

func TestEngine_GlobalRegexAndStopwordAllowlist(t *testing.T) {
	e := synth([]jsonRule{{ID: "r", Regex: `tok_([a-z0-9]{8})`}}, allowlist{
		Regexes:   []string{`^deadbeef$`},
		Stopwords: []string{"Fakeword"},
	})
	if fs := e.scanDiff(addDiff("a", 1, "+tok_deadbeef")); len(fs) != 0 {
		t.Errorf("global regex must allow, got %v", kinds(fs))
	}
	if fs := e.scanDiff(addDiff("a", 1, "+tok_fakeword")); len(fs) != 0 {
		t.Errorf("global stopword must allow, got %v", kinds(fs))
	}
	if fs := e.scanDiff(addDiff("a", 1, "+tok_realvalu")); len(fs) != 1 {
		t.Errorf("real value must be reported, got %v", kinds(fs))
	}
}

func TestEngine_RuleAllowlistConditionAndRegexTarget(t *testing.T) {
	andRule := jsonRule{ID: "and", Regex: `key=([a-z0-9]{8})`, Allowlists: []jsonRuleAllow{{
		Condition: "AND", Paths: []string{`\.md$`}, Regexes: []string{`^allowme1$`},
	}}}
	e := synth([]jsonRule{andRule}, allowlist{})
	if fs := e.scanDiff(addDiff("a.md", 1, "+key=allowme1")); len(fs) != 0 {
		t.Errorf("both criteria hold, want allowed, got %v", kinds(fs))
	}
	if fs := e.scanDiff(addDiff("a.go", 1, "+key=allowme1")); len(fs) != 1 {
		t.Errorf("only the regex holds, AND must not allow, got %v", kinds(fs))
	}

	orRule := jsonRule{ID: "or", Regex: `key=([a-z0-9]{8})`, Allowlists: []jsonRuleAllow{{
		Paths: []string{`\.md$`}, Regexes: []string{`^nomatch$`},
	}}}
	e = synth([]jsonRule{orRule}, allowlist{})
	if fs := e.scanDiff(addDiff("a.md", 1, "+key=whatever1")); len(fs) != 0 {
		t.Errorf("OR with one criterion true must allow, got %v", kinds(fs))
	}

	lineRule := jsonRule{ID: "line", Regex: `key=([a-z0-9]{8})`, Allowlists: []jsonRuleAllow{{
		RegexTarget: "line", Regexes: []string{`# noqa$`},
	}}}
	e = synth([]jsonRule{lineRule}, allowlist{})
	if fs := e.scanDiff(addDiff("a", 1, "+key=whatever1 # noqa")); len(fs) != 0 {
		t.Errorf("line target must allow, got %v", kinds(fs))
	}
	if fs := e.scanDiff(addDiff("a", 1, "+key=whatever1")); len(fs) != 1 {
		t.Errorf("line without marker must be reported, got %v", kinds(fs))
	}

	matchRule := jsonRule{ID: "match", Regex: `key=([a-z0-9]{8})`, Allowlists: []jsonRuleAllow{{
		RegexTarget: "match", Regexes: []string{`^key=whatever$`},
	}}}
	e = synth([]jsonRule{matchRule}, allowlist{})
	if fs := e.scanDiff(addDiff("a", 1, "+key=whatever1")); len(fs) != 0 {
		t.Errorf("match target must allow, got %v", kinds(fs))
	}
}

func TestEngine_SecretGroupSelectsEntropyInput(t *testing.T) {
	r := jsonRule{ID: "g", Regex: `(low)-([a-z0-9]{12})`, SecretGroup: 2, Entropy: 3.0}
	e := synth([]jsonRule{r}, allowlist{})
	if fs := e.scanDiff(addDiff("a", 1, "+low-aaaaaaaaaaaa")); len(fs) != 0 {
		t.Errorf("group 2 is low entropy, got %v", kinds(fs))
	}
	if fs := e.scanDiff(addDiff("a", 1, "+low-q8x2m5k9w1zt")); len(fs) != 1 {
		t.Errorf("group 2 is high entropy, got %v", kinds(fs))
	}
}

func TestEngine_GenericRuleDroppedWhenSpecificRuleMatchesSameLine(t *testing.T) {
	e := synth([]jsonRule{
		{ID: "specific", Regex: `tok_[a-z0-9]{8}`},
		{ID: genericRuleID, Regex: `key=([a-z0-9]{8})`},
	}, allowlist{})
	fs := e.scanDiff(addDiff("a", 1, "+key=abcd1234 tok_abcd1234", "+key=lonely99"))
	if len(fs) != 2 {
		t.Fatalf("got %v, want specific@1 and generic@2", kinds(fs))
	}
	if f, _ := find(fs, genericRuleID); f.FileLine != 2 {
		t.Errorf("surviving generic finding is at line %d, want 2", f.FileLine)
	}
}

func TestScanDiff_QuotedPathsAreDecoded(t *testing.T) {
	cases := []struct{ header, want string }{
		{`"b/quote\"d.env"`, `quote"d.env`},
		{`"b/back\\slash.env"`, `back\slash.env`},
		{`"b/tab\there.env"`, "tab\there.env"},
		{`"b/\355\225\234.env"`, "한.env"},
	}
	for _, c := range cases {
		diff := "diff --git \"a/x\" " + c.header + "\n--- /dev/null\n+++ " + c.header + "\n@@ -0,0 +1,1 @@\n+export GH=" + githubPAT + "\n"
		f, ok := find(ScanDiff(diff), "github-pat")
		if !ok {
			t.Errorf("%s: secret in a quoted path was skipped", c.header)
			continue
		}
		if f.File != c.want {
			t.Errorf("%s: File = %q, want %q", c.header, f.File, c.want)
		}
	}
}

func TestScanDiff_PathWithSpaceDropsTrailingTab(t *testing.T) {
	diff := "diff --git a/my cert.p12 b/my cert.p12\nnew file mode 100644\n--- /dev/null\n+++ b/my cert.p12\t\n@@ -0,0 +1,1 @@\n+x\n"
	f, ok := find(ScanDiff(diff), "pkcs12-file")
	if !ok {
		t.Fatal("pkcs12-file must match a path with a space")
	}
	if f.File != "my cert.p12" {
		t.Errorf("File = %q, want %q", f.File, "my cert.p12")
	}
}

func TestScanDiff_QuotedPathInHeaderOnlyForBinaryFile(t *testing.T) {
	diff := "diff --git \"a/c\\\"x.p12\" \"b/c\\\"x.p12\"\nnew file mode 100644\nBinary files /dev/null and \"b/c\\\"x.p12\" differ\n"
	f, ok := find(ScanDiff(diff), "pkcs12-file")
	if !ok {
		t.Fatal("pkcs12-file must match a quoted name that only the diff header carries")
	}
	if f.File != `c"x.p12` {
		t.Errorf("File = %q", f.File)
	}
}
