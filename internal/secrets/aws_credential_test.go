package secrets

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
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
			if c.KeyID() != pairKeyID {
				t.Errorf("KeyID = %q", c.KeyID())
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
	f := Finding{Kind: "aws-access-token", AWS: c}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fmt.Sprint(c), fmt.Sprintf("%v", f), fmt.Sprintf("%+v", f), fmt.Sprintf("%#v", f), string(data)} {
		if strings.Contains(out, pairKeyID) || strings.Contains(out, pairSecret) {
			t.Errorf("formatted output leaks a raw value: %s", out)
		}
	}
	if c.MaskedKeyID() != "AKIA...ZAM2" {
		t.Errorf("MaskedKeyID = %q", c.MaskedKeyID())
	}
}
