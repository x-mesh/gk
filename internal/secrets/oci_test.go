package secrets

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"testing"
)

var (
	ociUser1   = "ocid1.user.oc1.." + strings.Repeat("a", 60)
	ociUser2   = "ocid1.user.oc1.." + strings.Repeat("b", 60)
	ociTenancy = "ocid1.tenancy.oc1.." + strings.Repeat("c", 60)
	ociFP1     = "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"
	ociFP2     = "10:20:30:40:50:60:70:80:90:a0:b0:c0:d0:e0:f0:01"
)

// first returns the nearest profile for fingerprint, which is the one a
// verifier tries first.
func firstOCIProfile(t *testing.T, profiles []OCIProfile, fingerprint string) OCIProfile {
	t.Helper()
	for _, p := range profiles {
		if p.Fingerprint == fingerprint {
			return p
		}
	}
	t.Fatalf("no profile for %s in %+v", fingerprint, profiles)
	return OCIProfile{}
}

func TestFindOCIProfiles_ConfigWithInheritedProfile(t *testing.T) {
	config := strings.Join([]string{
		"[DEFAULT]",
		"user=" + ociUser1,
		"fingerprint=" + ociFP1,
		"key_file=~/.oci/oci_api_key.pem",
		"tenancy=" + ociTenancy,
		"region=ap-seoul-1",
		"",
		"[ADMIN_USER]",
		"user=" + ociUser2,
		"fingerprint=" + strings.ToUpper(ociFP2),
		"key_file=keys/admin_key.pem",
	}, "\n")
	profiles := FindOCIProfiles(config)
	if got, want := firstOCIProfile(t, profiles, ociFP1), (OCIProfile{ociFP1, ociUser1, ociTenancy, "ap-seoul-1"}); got != want {
		t.Errorf("DEFAULT profile = %+v, want %+v", got, want)
	}
	// ADMIN_USER has its own user and inherits tenancy and region. Its
	// fingerprint is upper case in the file and lower case in the profile.
	if got, want := firstOCIProfile(t, profiles, ociFP2), (OCIProfile{ociFP2, ociUser2, ociTenancy, "ap-seoul-1"}); got != want {
		t.Errorf("ADMIN_USER profile = %+v, want %+v", got, want)
	}
	counts := map[string]int{}
	for _, p := range profiles {
		counts[p.Fingerprint]++
	}
	for fp, n := range counts {
		if n > ociMaxProfiles {
			t.Errorf("%s has %d candidates, want at most %d", fp, n, ociMaxProfiles)
		}
	}
}

func TestFindOCIProfiles_OtherLayouts(t *testing.T) {
	want := OCIProfile{ociFP1, ociUser1, ociTenancy, "ap-chuncheon-1"}
	cases := map[string]string{
		"terraform variables": strings.Join([]string{
			`tenancy_ocid     = "` + ociTenancy + `"`,
			`user_ocid        = "` + ociUser1 + `"`,
			`fingerprint      = "` + ociFP1 + `"`,
			`private_key_path = "~/.oci/oci_api_key.pem"`,
			`region           = "ap-chuncheon-1"`,
		}, "\n"),
		"environment file": strings.Join([]string{
			"OCI_CLI_USER=" + ociUser1,
			"OCI_CLI_TENANCY=" + ociTenancy,
			"OCI_CLI_FINGERPRINT=" + ociFP1,
			"OCI_CLI_REGION=ap-chuncheon-1",
		}, "\n"),
		"json": `{"user": "` + ociUser1 + `", "tenancy": "` + ociTenancy + `", "fingerprint": "` + ociFP1 + `", "region": "ap-chuncheon-1"}`,
		"raw diff": strings.Join([]string{
			"diff --git a/.oci/config b/.oci/config",
			"--- /dev/null",
			"+++ b/.oci/config",
			"@@ -0,0 +1,5 @@",
			"+[DEFAULT]",
			"+user=" + ociUser1,
			"+fingerprint=" + ociFP1,
			"+tenancy=" + ociTenancy,
			"+region=ap-chuncheon-1",
		}, "\n"),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if got := firstOCIProfile(t, FindOCIProfiles(text), ociFP1); got != want {
				t.Errorf("profile = %+v, want %+v", got, want)
			}
		})
	}
}

// Two files of one diff sit next to each other. A fingerprint must not take
// the OCIDs of the next file.
func TestFindOCIProfiles_WindowStopsAtDiffFileHeader(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/notes.txt b/notes.txt",
		"+++ b/notes.txt",
		"+fingerprint " + ociFP1,
		"diff --git a/.oci/config b/.oci/config",
		"+++ b/.oci/config",
		"+user=" + ociUser1,
		"+tenancy=" + ociTenancy,
		"+region=ap-seoul-1",
	}, "\n")
	if got := FindOCIProfiles(diff); len(got) != 0 {
		t.Errorf("profiles = %+v, want none: the fingerprint and the OCIDs are in different files", got)
	}
}

func TestFindOCIProfiles_RegionNeedsItsKeywordAndShape(t *testing.T) {
	base := "user=" + ociUser1 + "\nfingerprint=" + ociFP1 + "\ntenancy=" + ociTenancy + "\n"
	cases := map[string]struct{ line, want string }{
		"no region":                 {"", ""},
		"hyphenated word elsewhere": {"cache=us-east-1", ""},
		"dots and slashes":          {"region=x.evil.example/", ""},
		"upper case":                {"region=AP-SEOUL-1", ""},
		"host suffix after region":  {"region=ap-seoul-1.evil.example", "ap-seoul-1"},
		"government region":         {"region=us-gov-ashburn-1", "us-gov-ashburn-1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := firstOCIProfile(t, FindOCIProfiles(base+tc.line), ociFP1).Region; got != tc.want {
				t.Errorf("region = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFindOCIProfiles_IgnoresWhatIsNotAProfile(t *testing.T) {
	cases := map[string]string{
		"no OCID in the text":      "fingerprint=" + ociFP1 + "\nregion=ap-seoul-1",
		"placeholder OCIDs":        "user=ocid1.user.oc1..<unique_ID>\ntenancy=ocid1.tenancy.oc1..<unique_ID>\nfingerprint=" + ociFP1,
		"user without a tenancy":   "user=" + ociUser1 + "\nfingerprint=" + ociFP1,
		"OCIDs out of the window":  "user=" + ociUser1 + "\ntenancy=" + ociTenancy + strings.Repeat("\n", ociProfileWindow+1) + "fingerprint=" + ociFP1,
		"SSH MD5 fingerprint":      "user=" + ociUser1 + "\ntenancy=" + ociTenancy + "\nMD5:" + ociFP1,
		"fingerprint that is long": "user=" + ociUser1 + "\ntenancy=" + ociTenancy + "\nfingerprint=" + ociFP1 + ":aa",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if got := FindOCIProfiles(text); len(got) != 0 {
				t.Errorf("profiles = %+v, want none", got)
			}
		})
	}
}

func TestFindOCIProfiles_KeepsNearestCombinationsOnly(t *testing.T) {
	var lines []string
	for i := range 4 {
		lines = append(lines, "user=ocid1.user.oc1.."+strings.Repeat(string(rune('d'+i)), 60))
	}
	lines = append(lines, "user="+ociUser1, "fingerprint="+ociFP1, "tenancy="+ociTenancy, "region=ap-seoul-1")
	got := FindOCIProfiles(strings.Join(lines, "\n"))
	if len(got) != 2 || got[0].User != ociUser1 {
		t.Errorf("profiles = %+v, want the nearest user first and only the two nearest users", got)
	}
}

// The test keys are generated at run time, so no private key is in this file.
var (
	testRSAKey = sync.OnceValue(func() *rsa.PrivateKey {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		return key
	})
	testSmallRSAKey = sync.OnceValue(func() *rsa.PrivateKey {
		key, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			panic(err)
		}
		return key
	})
)

func pemBlock(blockType string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}

func pkcs1PEM(key *rsa.PrivateKey) string {
	return pemBlock("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
}

func pkcs8PEM(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pemBlock("PRIVATE KEY", der)
}

func profileFor(key *rsa.PrivateKey) OCIProfile {
	return OCIProfile{Fingerprint: ociFingerprint(&key.PublicKey), User: ociUser1, Tenancy: ociTenancy, Region: "ap-seoul-1"}
}

// The expected value is the output of
// `openssl rsa -pubout -outform DER | openssl md5 -c` for the same key. The
// PEM below is the public half only.
func TestOCIFingerprint_MatchesOpenSSL(t *testing.T) {
	const publicPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAzdV+pWxIDlumG4FbPRYH
iE32sN+ygkIT4/mIVI+/9LZFg/FAU9t1Wg2d8Cx9V3JnHd/6m4GYoTR/BTQKKg+e
EzXtkTMMeEO2py0OzftaTYykXvsU4LOStPy6OZ+yHXbGTF+gvsONG1BpuZiDyPLQ
VXmYLrwHdXaJDZb+VEcacLDCdCHjE9mZgIe5SXRVAVpvJ6omPy4p5VBUlRmDbAMI
cuoi3pD8O/NDWXyYw679XbYUWFiSdeOHuX8HYrc5+b55JEtYI7BHqz5W3vEqUVpL
DtdnXLycr8gwAcvNgkNnjC8JNCs7KuxOdk21zPnPJ1fjDZtmGelNV/6vJ7IQRLOT
wQIDAQAB
-----END PUBLIC KEY-----`
	block, _ := pem.Decode([]byte(publicPEM))
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ociFingerprint(parsed.(*rsa.PublicKey)), "f3:90:fa:0a:5c:59:ff:7d:2f:ad:77:ad:7f:fd:b6:42"; got != want {
		t.Errorf("fingerprint = %s, want %s", got, want)
	}
}

func TestBindOCI_ReadsCommonPEMForms(t *testing.T) {
	key := testRSAKey()
	plain := strings.TrimSuffix(pkcs1PEM(key), "\n")
	forms := map[string]string{
		"PKCS#1":                  pkcs1PEM(key),
		"PKCS#8":                  pkcs8PEM(t, key),
		"no trailing newline":     plain,
		"CRLF":                    strings.ReplaceAll(plain, "\n", "\r\n"),
		"indented block":          strings.ReplaceAll(plain, "\n", "\n    "),
		"tab-indented block":      strings.ReplaceAll(plain, "\n", "\n\t\t"),
		"escaped newlines":        strings.ReplaceAll(plain, "\n", `\n`),
		"escaped CRLF":            strings.ReplaceAll(plain, "\n", `\r\n`),
		"escaped with a tail":     strings.ReplaceAll(plain, "\n", `\n`) + `\n`,
		"PKCS#8 escaped newlines": strings.ReplaceAll(strings.TrimSuffix(pkcs8PEM(t, key), "\n"), "\n", `\n`),
	}
	want := profileFor(key)
	for name, text := range forms {
		t.Run(name, func(t *testing.T) {
			c := BindOCI(KeyCandidate(text), []OCIProfile{want})
			if c == nil {
				t.Fatal("BindOCI = nil, want an OCI credential")
			}
			if c.Provider() != ProviderOCI || !c.Verifiable() || c.Value() != want.Fingerprint {
				t.Errorf("credential = %v with value %q", c, c.Value())
			}
			if c.OCIKey() == nil || !c.OCIKey().Equal(key) {
				t.Error("credential does not hold the parsed key")
			}
			if got := c.OCIProfiles(); len(got) != 1 || got[0] != want {
				t.Errorf("profiles = %+v", got)
			}
		})
	}
}

func TestBindOCI_SkipsKeysItCannotUse(t *testing.T) {
	key := testRSAKey()
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plain := pkcs1PEM(key)
	encrypted := string(pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00112233445566778899AABBCCDDEEFF"},
		Bytes:   []byte(strings.Repeat("ciphertext-not-der", 60)),
	}))
	small := testSmallRSAKey()
	cases := map[string]struct {
		text     string
		profiles []OCIProfile
	}{
		"EC key":                   {pkcs8PEM(t, ec), []OCIProfile{profileFor(key)}},
		"OpenSSH key":              {pemBlock("OPENSSH PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)), []OCIProfile{profileFor(key)}},
		"encrypted PKCS#1":         {encrypted, []OCIProfile{profileFor(key)}},
		"encrypted PKCS#8":         {pemBlock("ENCRYPTED PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)), []OCIProfile{profileFor(key)}},
		"truncated body":           {plain[:len(plain)/2] + "\n-----END RSA PRIVATE KEY-----\n", []OCIProfile{profileFor(key)}},
		"placeholder body":         {"-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat(".", 80) + "\n-----END RSA PRIVATE KEY-----", []OCIProfile{profileFor(key)}},
		"1024-bit key":             {pkcs1PEM(small), []OCIProfile{profileFor(small)}},
		"no profile names the key": {plain, []OCIProfile{{Fingerprint: ociFP1, User: ociUser1, Tenancy: ociTenancy}}},
		"no profile at all":        {plain, nil},
		"not a PEM block":          {"just text", []OCIProfile{profileFor(key)}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if c := BindOCI(KeyCandidate(tc.text), tc.profiles); c != nil {
				t.Errorf("BindOCI = %v, want nil", c)
			}
		})
	}
	if BindOCI(nil, []OCIProfile{profileFor(key)}) != nil {
		t.Error("BindOCI(nil) != nil")
	}
}

func TestKeyCandidate_SizeLimit(t *testing.T) {
	if KeyCandidate(strings.Repeat("A", ociMaxPEMBytes)) == nil {
		t.Error("a block at the limit must be kept")
	}
	if KeyCandidate(strings.Repeat("A", ociMaxPEMBytes+1)) != nil {
		t.Error("a block over the limit must be dropped")
	}
	if c := KeyCandidate(pkcs1PEM(testRSAKey())); c == nil || c.Verifiable() {
		t.Errorf("candidate = %v, want a credential that is not verifiable", c)
	}
}

func TestBindOCI_KeepsDistinctMatchingProfiles(t *testing.T) {
	key := testRSAKey()
	match := profileFor(key)
	var profiles []OCIProfile
	profiles = append(profiles, OCIProfile{Fingerprint: ociFP1, User: ociUser2, Tenancy: ociTenancy}, match, match)
	for i := range 4 {
		p := match
		p.Region = fmt.Sprintf("ap-seoul-%d", i+2)
		profiles = append(profiles, p)
	}
	got := BindOCI(KeyCandidate(pkcs1PEM(key)), profiles).OCIProfiles()
	if len(got) != ociMaxProfiles || got[0] != match || got[1].Region != "ap-seoul-2" || got[2].Region != "ap-seoul-3" {
		t.Errorf("profiles = %+v, want the first %d distinct matches in order", got, ociMaxProfiles)
	}
}

func TestOCICredential_NeverFormatsThePEM(t *testing.T) {
	key := testRSAKey()
	text := pkcs1PEM(key)
	body := strings.Split(text, "\n")[1]
	candidate := KeyCandidate(text)
	bound := BindOCI(candidate, []OCIProfile{profileFor(key)})
	for name, c := range map[string]*Credential{"candidate": candidate, "bound": bound} {
		f := Finding{Kind: "private-key", Credential: c}
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range []string{fmt.Sprint(c), fmt.Sprintf("%v", f), fmt.Sprintf("%+v", f), fmt.Sprintf("%#v", f), string(data), c.Value(), c.ID(), c.Masked()} {
			if strings.Contains(out, body) || strings.Contains(out, "PRIVATE KEY") {
				t.Errorf("%s: output holds key material: %.60s", name, out)
			}
		}
	}
	if candidate.Value() != "" {
		t.Errorf("candidate value = %q, want empty", candidate.Value())
	}
}
