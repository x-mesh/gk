package gitleaksrules

import (
	"encoding/base64"
	"strings"
	"testing"
)

// hexKey is a 32-byte key in hex, the form of an Ethereum private key. It is
// assembled at runtime so this file does not trip secret scanners.
var hexKey = "0x" + "9f2c71d3a8e4b65017c2d9e3f4a1b6c8" + "5d0e7f3a2b9c4d1e6f8a0b3c5d7e9f12"

func TestMaskLines_UnquotedEnvAssignment(t *testing.T) {
	line := "TEST_DEPLOYER_PRIVATE_KEY=" + hexKey
	got := MaskLines("", []string{line})[0]
	if strings.Contains(got, hexKey[6:]) {
		t.Fatalf("the key value is still in the line: %q", got)
	}
	if !strings.HasPrefix(got, "TEST_DEPLOYER_PRIVATE_KEY=") {
		t.Errorf("the variable name must stay readable: %q", got)
	}
}

func TestMaskLines_LeavesOrdinaryCodeAlone(t *testing.T) {
	lines := []string{
		"const key = process.env.TEST_DEPLOYER_PRIVATE_KEY",
		"func runFind(cmd *cobra.Command, args []string) error {",
		"",
	}
	got := MaskLines("", lines)
	for i := range lines {
		if got[i] != lines[i] {
			t.Errorf("line %d changed: %q -> %q", i, lines[i], got[i])
		}
	}
}

func TestMaskLines_DoesNotChangeTheInput(t *testing.T) {
	line := "token = " + githubPAT
	lines := []string{line}
	MaskLines("", lines)
	if lines[0] != line {
		t.Errorf("the input slice was changed: %q", lines[0])
	}
}

func TestMaskLines_EverySecretOnTheLine(t *testing.T) {
	line := "aws=" + awsKey + " gh=" + githubPAT
	got := MaskLines("", []string{line})[0]
	if strings.Contains(got, awsKey) || strings.Contains(got, githubPAT) {
		t.Fatalf("a secret is still in the line: %q", got)
	}
}

// A scan skips a lock file by its path. A printed line has no such excuse.
func TestMaskLines_IgnoresPathAllowlists(t *testing.T) {
	if fs := ScanDiff(addDiff("package-lock.json", 1, "+token = "+githubPAT)); len(fs) != 0 {
		t.Fatalf("the fixture must be a path the scan skips, got %v", kinds(fs))
	}
	got := MaskLines("", []string{"token = " + githubPAT})[0]
	if strings.Contains(got, githubPAT) {
		t.Fatalf("the secret is still in the line: %q", got)
	}
}

func TestMaskLines_EncodedSecretReplacesTheLine(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("AKIA" + "Z3MMNQW4XPLR7TQB"))
	got := MaskLines("", []string{"data: " + encoded})[0]
	if strings.Contains(got, encoded) {
		t.Fatalf("the encoded secret is still in the line: %q", got)
	}
}

func TestMaskLines_MultiLinePrivateKey(t *testing.T) {
	body := []string{
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj",
		"MzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu",
		"NMoSfm76oqFvAp8Gy0iz5sxjZmSnXyCdPEovGhLa0VzMaQ8s+CLOyS56YyCFGeJZ",
	}
	lines := append([]string{"-----BEGIN " + "PRIVATE KEY-----"}, body...)
	lines = append(lines, "-----END "+"PRIVATE KEY-----")
	got := strings.Join(MaskLines("", lines), "\n")
	for _, l := range body {
		if strings.Contains(got, l) {
			t.Fatalf("a line of the key is still printed: %q", l)
		}
	}
}

// One line of a PEM block, without the header that the private-key rule
// needs.
func TestMaskLines_LoneKeyBodyLine(t *testing.T) {
	line := "  MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj"
	if got := MaskLines("", []string{line})[0]; strings.Contains(got, "MIIEvQIBADANBg") {
		t.Fatalf("the key material is still printed: %q", got)
	}
}

func TestMaskLines_LongNamesAndDigestsAreNotBlobs(t *testing.T) {
	lines := []string{
		"TestFindCommits_ContentOnlyCommitIsNotMissedByMessageSearch",
		"internal/secrets/gitleaksrules/yaml_paths_test_fixture_directory",
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	got := MaskLines("", lines)
	for i := range lines {
		if got[i] != lines[i] {
			t.Errorf("line %d changed: %q -> %q", i, lines[i], got[i])
		}
	}
}

// The Terraform password rule runs only on a .tf or .hcl path, and its value
// is too plain for the generic rule.
func TestMaskLines_PathScopedRuleNeedsThePath(t *testing.T) {
	line := `administrator_login_password = "` + "hunter2pass" + `"`
	if got := MaskLines("main.tf", []string{line})[0]; strings.Contains(got, "hunter2pass") {
		t.Fatalf("the password is still in the line: %q", got)
	}
	if got := MaskLines("", []string{line})[0]; got != line {
		t.Fatalf("the fixture must be a value that only the path-scoped rule finds, got %q", got)
	}
}

// fold splits an encoded value into lines of width characters, indented as in
// a YAML block, after a first line that holds prefix and the first piece.
func fold(prefix, encoded string, width int) []string {
	var lines []string
	for i := 0; i < len(encoded); i += width {
		piece := encoded[i:min(i+width, len(encoded))]
		if i == 0 {
			lines = append(lines, prefix+piece)
			continue
		}
		lines = append(lines, "  "+piece)
	}
	return lines
}

// An encoded secret folded over lines is masked on every line it spans: the
// pieces left on the other lines would join back into the secret.
func TestMaskLines_FoldedEncodedSecret(t *testing.T) {
	secretsToFold := map[string]string{
		"aws key":     "AKIA" + "Z3MMNQW4XPLR7TQB",
		"slack token": "token: xoxb-" + "123456789012-1234567890123-abcdefghijklmnopqrstuvwx",
		"github pat":  "GITHUB_TOKEN=" + githubPAT,
	}
	for name, plain := range secretsToFold {
		encoded := base64.StdEncoding.EncodeToString([]byte(plain))
		for _, width := range []int{1, 2, 3, 8, 16, 30, 64} {
			for _, prefix := range []string{"  ", "data: ", "value: ", "the value is "} {
				block := fold(prefix, encoded, width)
				lines := append([]string{"kind: Secret", "data:"}, block...)
				lines = append(lines, "type: Opaque")
				got := MaskLines("", lines)
				// The pieces left on the lines do not join back into the
				// encoded value, nor into half of it.
				var joined strings.Builder
				for _, l := range got[2 : len(got)-1] {
					joined.WriteString(strings.TrimSpace(strings.TrimPrefix(l, prefix)))
				}
				if strings.Contains(joined.String(), encoded[:len(encoded)/2]) {
					t.Errorf("%s, width %d, prefix %q: the lines join back into the value: %q", name, width, prefix, got)
				}
				// The lines around the block are not touched, and a prefix
				// before a folded value stays. A value on one line replaces
				// the line, as TestMaskLines_EncodedSecretReplacesTheLine
				// covers.
				if got[0] != "kind: Secret" || got[1] != "data:" || got[len(got)-1] != "type: Opaque" {
					t.Errorf("%s, width %d: the lines around the block changed: %q", name, width, got)
				}
				if p := strings.TrimSpace(prefix); p != "" && len(block) > 1 && !strings.HasPrefix(got[2], p) {
					t.Errorf("%s, width %d: the prefix %q is gone: %q", name, width, p, got[2])
				}
			}
		}
	}
}

// A run of base64 lines that decodes to no secret is left alone, and so is a
// line of base64 next to ordinary text.
func TestMaskLines_FoldedBase64WithNoSecret(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("an ordinary config value, nothing secret here. ", 3)))
	lines := append([]string{"data:"}, fold("  ", encoded, 16)...)
	got := MaskLines("", lines)
	for i := range lines {
		if got[i] != lines[i] {
			t.Errorf("line %d changed: %q → %q", i, lines[i], got[i])
		}
	}
}
