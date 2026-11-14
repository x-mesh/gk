package secrets

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

// Fixture values are assembled at runtime so this file does not trip secret
// scanners.
var (
	pairKeyID  = "AKIA" + "QYLPMN5HHHFPZAM2"
	pairSecret = "q8Hk2nVd/7Rm" + "Tz4bLw9pXc3Jf6Ys1Ae0Gu5Ko+Ni"
	pairOther  = "Zr4Lm8Qp2Wx" + "6Bn0Ct3Vy7Hs1Jd5Kf9Ga2Ue4Io+P"
)

func TestPairAWS_FindsSecretInCommonLayouts(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		idx   int
	}{
		{"credentials file", []string{"[default]", "aws_access_key_id = " + pairKeyID, "aws_secret_access_key = " + pairSecret}, 1},
		{"env file", []string{"AWS_SECRET_ACCESS_KEY=" + pairSecret, "AWS_ACCESS_KEY_ID=" + pairKeyID}, 1},
		// The generic-api-key finding for this secret is dropped as shadowed
		// by the key ID on the same line, so pairing must read the line.
		{"same line", []string{`cfg := aws.Config{AccessKey: "` + pairKeyID + `", SecretKey: "` + pairSecret + `"}`}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := PairAWS(tc.lines, tc.idx, pairKeyID)
			if c.Value() != pairKeyID {
				t.Errorf("KeyID = %q", c.Value())
			}
			if got := c.SecretCandidates(); !slices.Equal(got, []string{pairSecret}) {
				t.Errorf("SecretCandidates = %q, want the paired secret", got)
			}
		})
	}
}

func TestPairAWS_IgnoresHexAndFarLines(t *testing.T) {
	sha := strings.Repeat("0123456789abcdef", 3)[:40]
	lines := []string{"aws_secret_access_key = " + pairSecret, "", "", "", "", "", "", "commit " + sha, "id = " + pairKeyID}
	c := PairAWS(lines, 8, pairKeyID)
	if got := c.SecretCandidates(); len(got) != 0 {
		t.Errorf("SecretCandidates = %q, want none: the secret is outside the window and the SHA is hex", got)
	}
}

func TestPairAWS_SecretKeywordFirstAndCapped(t *testing.T) {
	lines := []string{
		"a = " + pairOther,
		"id = " + pairKeyID,
		"b = " + strings.ToUpper(pairOther[:20]) + strings.ToLower(pairOther[20:]),
		"c = Ab" + strings.Repeat("x", 38),
		"secret = " + pairSecret,
	}
	got := PairAWS(lines, 1, pairKeyID).SecretCandidates()
	if len(got) != awsMaxSecretCandidates || got[0] != pairSecret {
		t.Errorf("SecretCandidates = %q, want the secret-keyword line first and at most %d", got, awsMaxSecretCandidates)
	}
}

func TestPairAWS_SessionToken(t *testing.T) {
	token := strings.Repeat("FwoGZXIvYXdz", 10)
	lines := []string{"aws_access_key_id = ASIA" + pairKeyID[4:], "aws_secret_access_key = " + pairSecret, "aws_session_token = " + token}
	if got := PairAWS(lines, 0, "ASIA"+pairKeyID[4:]).SessionToken(); got != token {
		t.Errorf("SessionToken = %q", got)
	}
}

func TestAWSCredential_FormatsMasked(t *testing.T) {
	c := NewAWSCredential(pairKeyID, []string{pairSecret}, "tok")
	f := Finding{Kind: "aws-access-token", Credential: c}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fmt.Sprint(c), fmt.Sprintf("%v", f), fmt.Sprintf("%+v", f), fmt.Sprintf("%#v", f), string(data)} {
		if strings.Contains(out, pairKeyID) || strings.Contains(out, pairSecret) {
			t.Errorf("formatted output leaks a raw value: %s", out)
		}
	}
	if c.Masked() != "AKIA...ZAM2" {
		t.Errorf("MaskedKeyID = %q", c.Masked())
	}
}

func TestTokenCredential_ExtendsTruncatedMatch(t *testing.T) {
	tail := strings.Repeat("aB3", 20)
	token := "xoxa-" + "2-" + tail
	// slack-legacy-workspace-token caps the tail at 48 characters, so its
	// match stops inside this 60-character tail.
	match := token[:len("xoxa-2-")+48]
	line := `SLACK_TOKEN="` + token + `".`
	c := TokenCredential("slack-legacy-workspace-token", line, strings.Index(line, match), match)
	if c == nil || c.Value() != token || c.Provider() != ProviderSlack {
		t.Fatalf("credential = %v, want the full token", c)
	}
	gcp := "AIza" + strings.Repeat("Ab_-9", 7)
	if c := TokenCredential("gcp-api-key", "key: "+gcp+";", len("key: "), gcp); c == nil || c.Value() != gcp || c.Provider() != ProviderGCP {
		t.Errorf("gcp credential = %v", c)
	}
}

func TestTokenCredential_SkipsUncheckableRules(t *testing.T) {
	for _, rule := range []string{"slack-webhook-url", "slack-config-refresh-token", "github-pat", "aws-access-token"} {
		if c := TokenCredential(rule, "x", 0, "x"); c != nil {
			t.Errorf("%s: credential = %v, want nil", rule, c)
		}
	}
}

func TestCredential_IDSeparatesProviders(t *testing.T) {
	a := NewTokenCredential(ProviderSlack, "k", "v1")
	b := NewTokenCredential(ProviderGCP, "k", "v1")
	if a.ID() == b.ID() {
		t.Error("same value under two providers shares one ID")
	}
}

func TestTokenCredential_ExtendsFromItsOwnColumn(t *testing.T) {
	match := "xoxa-" + "2-" + strings.Repeat("aB3", 16)
	first, second := match+"FIRSTtail", match+"SECONDtail"
	line := "A=" + first + " B=" + second
	col := strings.LastIndex(line, match)
	if c := TokenCredential("slack-legacy-workspace-token", line, col, match); c == nil || c.Value() != second {
		t.Errorf("second occurrence: credential = %v, want the second token", c)
	}
	if c := TokenCredential("slack-legacy-workspace-token", line, 2, match); c == nil || c.Value() != first {
		t.Errorf("first occurrence: credential = %v, want the first token", c)
	}
	// A column that does not hold the match must not pick another token.
	for _, bad := range []int{-1, 0, len(line), len(line) + 5} {
		if c := TokenCredential("slack-legacy-workspace-token", line, bad, match); c == nil || c.Value() != match {
			t.Errorf("col %d: credential = %v, want the match unextended", bad, c)
		}
	}
}

// A credential and an OCI profile outlive the text they were found in. A
// value that shares the text's memory keeps all of it alive, which ran a
// long history out of memory.
func TestScannerValues_DoNotShareTheScannedText(t *testing.T) {
	text := strings.Join([]string{
		"aws_access_key_id = " + pairKeyID,
		"aws_session_token = " + strings.Repeat("Ab3/", 30),
		"aws_secret_access_key = " + pairSecret,
		"user=" + ociUser1,
		"fingerprint=" + ociFP1,
		"tenancy=" + ociTenancy,
		"region=ap-seoul-1",
	}, "\n")
	lines := strings.Split(text, "\n")
	aws := PairAWS(lines, 0, strings.TrimPrefix(lines[0], "aws_access_key_id = "))
	values := map[string]string{"AWS key ID": aws.value, "AWS session token": aws.token}
	for i, s := range aws.secrets {
		values[fmt.Sprint("AWS secret ", i)] = s
	}
	values["PEM"] = KeyCandidate(text[:len(text)/2]).pem
	bot := "xoxb-" + "123456789012-1234567890123-" + "AbCdEfGhIjKlMnOpQrStUvWx"
	line := text + "\nSLACK=" + bot
	values["token"] = TokenCredential("slack-bot-token", line, len(line)-len(bot), bot).value
	profiles := FindOCIProfiles(text)
	if len(profiles) != 1 {
		t.Fatalf("profiles = %+v, want 1", profiles)
	}
	p := profiles[0]
	for name, s := range map[string]string{"OCI fingerprint": p.Fingerprint, "OCI user": p.User, "OCI tenancy": p.Tenancy, "OCI region": p.Region} {
		values[name] = s
	}
	for name, s := range values {
		if s == "" {
			t.Errorf("%s is empty", name)
		}
		if sharesMemory(s, text) || sharesMemory(s, line) {
			t.Errorf("%s shares the memory of the scanned text", name)
		}
	}
}

func sharesMemory(s, text string) bool {
	if s == "" || text == "" {
		return false
	}
	p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	lo := uintptr(unsafe.Pointer(unsafe.StringData(text)))
	return p >= lo && p < lo+uintptr(len(text))
}
