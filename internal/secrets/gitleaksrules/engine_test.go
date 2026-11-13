package gitleaksrules

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"runtime"
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

// Gitleaks matches the global path allowlist against the path inside an
// archive as well, so a dependency folder packed into a zip is skipped like
// one in the tree. A "!" in a path that does not follow an archive name is
// part of a file name.
func TestScanFile_ArchivePathAllowlist(t *testing.T) {
	content := "token = \"" + githubPAT + "\"\n"
	for path, want := range map[string]bool{
		"dist/site.zip!node_modules/pkg/index.js":    false,
		"app.jar!BOOT-INF/classes/package-lock.json": false,
		"app.jar!BOOT-INF/classes/application.yml":   true,
		"notes!node_modules/a.txt":                   true,
	} {
		_, ok := find(ScanFile(path, content), "github-pat")
		if ok != want {
			t.Errorf("%s: reported = %v, want %v", path, ok, want)
		}
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

func TestEngine_GenericRuleKeptWhenSpecificRuleMatchesOtherSecretOnSameLine(t *testing.T) {
	e := synth([]jsonRule{
		{ID: "specific", Regex: `tok_[a-z0-9]{8}`},
		{ID: genericRuleID, Regex: `key=([a-z0-9]{8})`},
	}, allowlist{})
	fs := e.scanDiff(addDiff("a", 1, "+tok_abcd1234 key=wxyz9876"))
	if len(fs) != 2 {
		t.Fatalf("got %v, want specific and generic on line 1", kinds(fs))
	}
}

func TestScanDiff_Base64EncodedSecretIsDecoded(t *testing.T) {
	// The decoder skips base64 without a digit or one of "+/-_", which the
	// base64 of awsKey lacks, so this test uses another key.
	encoded := base64.StdEncoding.EncodeToString([]byte("AKIA" + "Z3MMNQW4XPLR7TQB"))
	diff := addDiff("secret.yml", 1, "+kind: Secret", "+data:", "+  AWS_ACCESS_KEY_ID: "+encoded)
	f, ok := find(ScanDiff(diff), "aws-access-token")
	if !ok {
		t.Fatalf("aws-access-token not found in %v", kinds(ScanDiff(diff)))
	}
	if f.FileLine != 3 {
		t.Errorf("FileLine = %d, want 3 (the line of the encoded value)", f.FileLine)
	}
}

func TestScanDiff_DecodedAWSKeyPairsWithDecodedSecret(t *testing.T) {
	key := "AKIA" + "Z3MMNQW4XPLR7TQB"
	secret := "q8Hk2nVd/7Rm" + "Tz4bLw9pXc3Jf6Ys1Ae0Gu5Ko+Ni"
	b64 := base64.StdEncoding.EncodeToString
	diff := addDiff("secret.yml", 1, "+kind: Secret", "+data:",
		"+  AWS_ACCESS_KEY_ID: "+b64([]byte(key)),
		"+  AWS_SECRET_ACCESS_KEY: "+b64([]byte(secret)))
	f, ok := find(ScanDiff(diff), "aws-access-token")
	if !ok || f.Credential == nil {
		t.Fatalf("aws-access-token without credential in %v", kinds(ScanDiff(diff)))
	}
	if got := f.Credential.SecretCandidates(); f.Credential.Value() != key || len(got) != 1 || got[0] != secret {
		t.Errorf("credential = %s with %d candidates, want the decoded secret", f.Credential, len(got))
	}
}

func TestEngine_DecodedPassReportsOnlyMatchesTouchingDecodedText(t *testing.T) {
	e := synth([]jsonRule{{ID: "tok", Regex: `tok_[a-z0-9]{8}`}}, allowlist{})
	encoded := base64.StdEncoding.EncodeToString([]byte("tok_wxyz9876"))
	fs := e.scanDiff(addDiff("a", 1, "+tok_abcd1234", "+v="+encoded))
	if len(fs) != 2 {
		t.Fatalf("got %d findings %v, want the plain one on line 1 and the decoded one on line 2", len(fs), fs)
	}
	if fs[0].FileLine+fs[1].FileLine != 3 {
		t.Errorf("lines = %d, %d, want 1 and 2", fs[0].FileLine, fs[1].FileLine)
	}
}

func TestEngine_DecodedSecretFarIntoLargeHunkIsFound(t *testing.T) {
	e := synth([]jsonRule{{ID: "tok", Regex: `tok_[a-z0-9]{8}`}}, allowlist{})
	lines := []string{"+tok_abcd1234"}
	for i := range 3 * decodeWindowMargin / 32 {
		lines = append(lines, fmt.Sprintf("+filler line %06d padding text", i))
	}
	lines = append(lines, "+v="+base64.StdEncoding.EncodeToString([]byte("tok_wxyz9876")))
	fs := e.scanDiff(addDiff("a", 1, lines...))
	if len(fs) != 2 {
		t.Fatalf("got %d findings %v, want the plain one and the decoded one", len(fs), fs)
	}
	if fs[0].FileLine != 1 || fs[1].FileLine != len(lines) {
		t.Errorf("lines = %d, %d, want 1 and %d", fs[0].FileLine, fs[1].FileLine, len(lines))
	}
}

func TestEngine_LongMatchIntoDecodedTextIsFound(t *testing.T) {
	e := synth([]jsonRule{{ID: "long", Regex: `start[xyz ]+tok_[a-z0-9]{8}`}}, allowlist{})
	prefix := "start " + strings.Repeat("xyz ", decodeWindowMargin/2)
	fs := e.scanDiff(addDiff("a", 1, "+"+prefix+base64.StdEncoding.EncodeToString([]byte("tok_wxyz9876"))))
	if len(fs) != 1 || fs[0].FileLine != 1 {
		t.Fatalf("got %v, want the match that starts %d bytes before the decoded text", fs, len(prefix))
	}
}

func TestWindowable(t *testing.T) {
	cases := map[string]bool{
		`tok_[a-z0-9]{8}`: true,
		`a|b{10}`:         true,
		`\bkey=\S+`:       false,
		`x{4,}`:           false,
		``:                false,
		// The regexp package caps one repeat at 1000, so a match longer
		// than half the margin needs several: 2 x 4000 bytes fit, 3 do not.
		`.{1000}.{1000}`:        true,
		`.{1000}.{1000}.{1000}`: false,
	}
	for pattern, want := range cases {
		if got := windowable(pattern); got != want {
			t.Errorf("windowable(%q) = %v, want %v", pattern, got, want)
		}
	}
}

func TestPassMatches_WindowEdgeFallsBackToWholeText(t *testing.T) {
	cases := []struct {
		name, regex, text string
		window            [2]int
		want              [][]int
	}{
		{"interior match is offset", `tok`, "..tok..tok..", [2]int{4, 12}, [][]int{{7, 10}}},
		{"run cut by the window", `x+`, strings.Repeat("x", 30), [2]int{10, 20}, [][]int{{0, 30}}},
		{"anchor true only at the cut", `^b`, "ab", [2]int{1, 2}, nil},
		{"window covers the text", `b$`, "ab", [2]int{0, 2}, [][]int{{1, 2}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := rule{regex: regexp.MustCompile(c.regex), windowed: true}
			got := passMatches(r, pass{text: c.text, windows: [][2]int{c.window}})
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("passMatches = %v, want %v", got, c.want)
			}
		})
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

// gitleaks skips a finding whose line carries the gitleaks:allow marker.
// Checked against gitleaks 8.30.1 with a two-line file: only the unmarked
// line was reported.
func TestScanDiff_InlineGitleaksAllowSkipsLine(t *testing.T) {
	diff := addDiff("s.txt", 1,
		"+aws = \""+awsKey+"\"",
		"+aws2 = \""+awsKey+"\" // gitleaks:allow")
	fs := ScanDiff(diff)
	if len(fs) != 1 || fs[0].FileLine != 1 {
		t.Fatalf("findings = %+v, want only line 1", fs)
	}
}

func TestScanFile_ReportsPathAndLine(t *testing.T) {
	fs := ScanFile("cfg/app.env", "# creds\nname = x\ntoken = \""+githubPAT+"\"\n")
	f, ok := find(fs, "github-pat")
	if !ok {
		t.Fatalf("github-pat not found in %v", kinds(fs))
	}
	if f.File != "cfg/app.env" || f.FileLine != 3 {
		t.Errorf("finding = %+v, want cfg/app.env:3", f)
	}
}

// Files are scanned concurrently; the output order must still follow the
// diff so reports stay stable between runs.
func TestScanDiff_OutputFollowsDiffOrder(t *testing.T) {
	var b strings.Builder
	var want []string
	for i := range 64 {
		path := fmt.Sprintf("dir/f%02d.txt", i)
		want = append(want, path)
		b.WriteString(addDiff(path, 1, "+token = \""+githubPAT+"\""))
	}
	fs := ScanDiff(b.String())
	var got []string
	for _, f := range fs {
		got = append(got, f.File)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v", got)
	}
}

// A file above splitBytes is spread over several workers by rule slice; the
// findings must match a single-worker scan, including rules late in the list.
func TestScanDiff_LargeFileSplitAcrossWorkersKeepsEveryRule(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(4))
	body := []string{"+aws = \"" + awsKey + "\""}
	for range 4 * splitBytes / 40 {
		body = append(body, "+// filler line that matches no rule at all")
	}
	body = append(body, "+token = \""+githubPAT+"\"")
	diff := addDiff("big.txt", 1, body...)

	files := parseDiff(diff)
	want := get().scanFile(files[0])
	got := ScanDiff(diff)
	if len(want) < 2 {
		t.Fatalf("single-worker scan found %v, want aws and github findings", kinds(want))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("split scan = %+v, want %+v", got, want)
	}
}

func TestScan_AWSKeyCarriesPairedSecret(t *testing.T) {
	secret := "q8Hk2nVd/7Rm" + "Tz4bLw9pXc3Jf6Ys1Ae0Gu5Ko+Ni"
	sameLine := `cfg := aws.Config{AccessKey: "` + awsKey + `", SecretKey: "` + secret + `"}`
	cases := map[string][]secrets.Finding{
		"file same line": ScanFile("main.go", sameLine+"\n"),
		"diff next line": ScanDiff(addDiff("creds", 1, "+aws_access_key_id = "+awsKey, "+aws_secret_access_key = "+secret)),
	}
	for name, fs := range cases {
		f, ok := find(fs, "aws-access-token")
		if !ok || f.Credential == nil {
			t.Fatalf("%s: aws-access-token without credential in %v", name, kinds(fs))
		}
		if f.Credential.Value() != awsKey || len(f.Credential.SecretCandidates()) != 1 || f.Credential.SecretCandidates()[0] != secret {
			t.Errorf("%s: credential = %s with %d candidates", name, f.Credential, len(f.Credential.SecretCandidates()))
		}
	}
	f, ok := find(ScanDiff(addDiff("a.sh", 1, "+export GH="+githubPAT)), "github-pat")
	if !ok {
		t.Fatal("github-pat not found")
	}
	if f.Credential != nil {
		t.Errorf("non-AWS finding carries a credential")
	}
}

func TestScan_TokenFindingsCarryCredential(t *testing.T) {
	bot := "xoxb-" + "123456789012-1234567890123-" + "AbCdEfGhIjKlMnOpQrStUvWx"
	gcp := "AIza" + "SyD3x4mpl3K3yV4lu3F0rT3st1ngPurp0sX"
	hook := "https://hooks.slack.com/services/" + "T00000000/B00000000/" + "XXXXXXXXXXXXXXXXXXXXXXXX"
	fs := ScanFile("app.env", "SLACK="+bot+"\nGOOGLE_KEY="+gcp+"\nHOOK="+hook+"\n")
	for kind, want := range map[string]string{"slack-bot-token": bot, "gcp-api-key": gcp} {
		f, ok := find(fs, kind)
		if !ok {
			t.Fatalf("%s not found in %v", kind, kinds(fs))
		}
		if f.Credential == nil || f.Credential.Value() != want {
			t.Errorf("%s: credential = %v", kind, f.Credential)
		}
	}
	f, ok := find(fs, "slack-webhook-url")
	if !ok {
		t.Fatalf("slack-webhook-url not found in %v", kinds(fs))
	}
	if f.Credential != nil {
		t.Error("webhook URL carries a credential")
	}
}

// slack-legacy-workspace-token caps the tail at 48 characters, so the
// engine's match stops inside a longer token and must be extended.
func TestScan_SlackTokenLongerThanRuleIsExtended(t *testing.T) {
	token := "xoxa-" + "2-" + strings.Repeat("Ab3Cd9", 10)
	f, ok := find(ScanFile("app.env", "SLACK_TOKEN="+token+"\n"), "slack-legacy-workspace-token")
	if !ok {
		t.Fatal("slack-legacy-workspace-token not found")
	}
	if f.Credential == nil || f.Credential.Value() != token {
		t.Errorf("credential = %v, want the %d-character token", f.Credential, len(token))
	}
	refresh := "xoxr-" + strings.Repeat("Ab3Cd9", 4)
	if f, ok := find(ScanFile("app.env", "R="+refresh+"\n"), "slack-legacy-workspace-token"); !ok || f.Credential != nil {
		t.Errorf("xoxr- token: found=%v credential=%v, want found without a credential", ok, f.Credential)
	}
}

// Two tokens on one line can have the same regex match when the rule bounds
// the tail length. Each finding must carry its own token, not the first one.
func TestScan_TruncatedTokensOnOneLineKeepOwnValue(t *testing.T) {
	workspace := "xoxa-" + "2-" + strings.Repeat("Ab3Cd9", 8)
	legacyBot := "xoxb-" + "12345678-" + strings.Repeat("Ab3Cd9", 4) + "Ab"
	cases := []struct {
		name, kind, first, second string
		scan                      func(line string) []secrets.Finding
	}{
		{"workspace token in a file", "slack-legacy-workspace-token", workspace + "Zz1Yy2Xx3Ww4", workspace + "Qq5Rr6Ss7Tt8",
			func(line string) []secrets.Finding { return ScanFile("app.env", line+"\n") }},
		{"workspace token in a diff", "slack-legacy-workspace-token", workspace + "Zz1Yy2Xx3Ww4", workspace + "Qq5Rr6Ss7Tt8",
			func(line string) []secrets.Finding { return ScanDiff(addDiff("app.env", 1, "+"+line)) }},
		{"legacy bot token in a file", "slack-legacy-bot-token", legacyBot + "Zz1Yy2", legacyBot + "Qq5Rr6",
			func(line string) []secrets.Finding { return ScanFile("app.env", line+"\n") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, f := range tc.scan("STAGING=" + tc.first + " PROD=" + tc.second) {
				if f.Kind != tc.kind {
					continue
				}
				if f.Credential == nil {
					t.Fatalf("%s finding without a credential", tc.kind)
				}
				got = append(got, f.Credential.Value())
			}
			if len(got) != 2 || got[0] != tc.first || got[1] != tc.second {
				t.Errorf("credential values = %q, want %q then %q", got, tc.first, tc.second)
			}
		})
	}
}

// A private-key finding carries the PEM as a candidate for OCI pairing. The
// candidate is not verifiable, and the finding itself does not change.
func TestScan_PrivateKeyCarriesCandidate(t *testing.T) {
	// The body lines are not real key material. They are bound to short
	// names first so this file adds no finding of its own to a scan.
	row1 := strings.Repeat("Ab3Cd9Ef", 8)
	row2 := strings.Repeat("Gh4Ij0Kl", 8)
	body := []string{"-----BEGIN " + "RSA PRIVATE KEY-----", row1, row2, "-----END " + "RSA PRIVATE KEY-----"}
	var diffLines []string
	for _, l := range body {
		diffLines = append(diffLines, "+"+l)
	}
	cases := map[string][]secrets.Finding{
		"file": ScanFile("id_rsa", strings.Join(body, "\n")+"\n"),
		"diff": ScanDiff(addDiff("id_rsa", 1, diffLines...)),
	}
	for name, fs := range cases {
		if len(fs) != 1 || fs[0].Kind != "private-key" || fs[0].FileLine != 1 {
			t.Fatalf("%s: findings = %v", name, kinds(fs))
		}
		c := fs[0].Credential
		if c == nil || c.Verifiable() || c.Value() != "" {
			t.Errorf("%s: credential = %v, want a candidate that is not verifiable", name, c)
		}
	}
}
