package scan

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// sampleGitleaksReport is a real-shape gitleaks v8 report with two findings.
// Kept inline (not in testdata/) so the test file is self-contained.
const sampleGitleaksReport = `[
  {
    "Description": "AWS Access Key",
    "StartLine": 5,
    "EndLine": 5,
    "StartColumn": 14,
    "EndColumn": 33,
    "Match": "AKIAIOSFODNN7EXAMPLE",
    "Secret": "AKIAIOSFODNN7EXAMPLE",
    "File": "config/secrets.yml",
    "Commit": "abc123",
    "Entropy": 4.125,
    "Author": "Jane Doe",
    "Email": "jane@example.com",
    "Date": "2025-01-01T00:00:00Z",
    "Message": "add secrets",
    "Tags": [],
    "RuleID": "aws-access-key-id"
  },
  {
    "Description": "Generic API Key",
    "StartLine": 12,
    "EndLine": 12,
    "Match": "api_key=\"abc123xyz\"",
    "Secret": "abc123xyz",
    "File": "src/client.go",
    "Commit": "def456",
    "Entropy": 3.8,
    "RuleID": "generic-api-key"
  }
]`

func TestParseGitleaksFindings_TwoFindings(t *testing.T) {
	findings, err := ParseGitleaksFindings([]byte(sampleGitleaksReport))
	if err != nil {
		t.Fatalf("ParseGitleaksFindings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(findings))
	}

	f := findings[0]
	if f.RuleID != "aws-access-key-id" {
		t.Errorf("finding[0].RuleID = %q", f.RuleID)
	}
	if f.File != "config/secrets.yml" {
		t.Errorf("finding[0].File = %q", f.File)
	}
	if f.StartLine != 5 {
		t.Errorf("finding[0].StartLine = %d", f.StartLine)
	}
	if f.Entropy < 4.0 || f.Entropy > 4.2 {
		t.Errorf("finding[0].Entropy = %v, want ~4.125", f.Entropy)
	}
	if f.Match != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("finding[0].Match = %q", f.Match)
	}

	// Second finding is minimal but still parsed.
	if findings[1].RuleID != "generic-api-key" {
		t.Errorf("finding[1].RuleID = %q", findings[1].RuleID)
	}
	if findings[1].Secret != "abc123xyz" {
		t.Errorf("finding[1].Secret = %q", findings[1].Secret)
	}
}

func TestParseGitleaksFindings_EmptyVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty string", ""},
		{"whitespace", "   \n\t"},
		{"literal null", "null"},
		{"empty array", "[]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := ParseGitleaksFindings([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(findings) != 0 {
				t.Errorf("got %d findings, want 0: %+v", len(findings), findings)
			}
		})
	}
}

func TestParseGitleaksFindings_MalformedJSON(t *testing.T) {
	_, err := ParseGitleaksFindings([]byte("{not json}"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "parse gitleaks findings") {
		t.Errorf("error lacks context prefix: %v", err)
	}
}

func TestInterpretGitleaksExit_Success(t *testing.T) {
	findings, err := interpretGitleaksExit([]byte("[]"), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(findings) != 0 {
		t.Errorf("got %d findings, want 0", len(findings))
	}
}

func TestRunGitleaks_MissingBinary_ReturnsSentinel(t *testing.T) {
	// Only runs when gitleaks is NOT installed — otherwise we can't guarantee
	// the sentinel path executes.
	if _, _, ok := FindGitleaks(); ok {
		t.Skip("gitleaks is installed; sentinel path cannot be exercised here")
	}
	_, err := RunGitleaks(context.Background(), GitleaksOptions{})
	if err == nil {
		t.Fatal("expected ErrGitleaksNotInstalled when binary absent, got nil")
	}
	if !errors.Is(err, ErrGitleaksNotInstalled) {
		t.Errorf("err = %v, want ErrGitleaksNotInstalled", err)
	}
}

func TestFindGitleaks_GracefullyHandlesMissing(t *testing.T) {
	// We can't guarantee gitleaks is installed on every dev machine, so the
	// test asserts the contract for both cases: ok=true → path non-empty,
	// ok=false → both path and version empty.
	path, version, ok := FindGitleaks()
	if ok {
		if path == "" {
			t.Errorf("ok=true but path empty")
		}
		// version may still be empty if the binary is a wrapper shim; just
		// assert it doesn't leak whitespace/newlines.
		if version != strings.TrimSpace(version) {
			t.Errorf("version not trimmed: %q", version)
		}
	} else {
		if path != "" || version != "" {
			t.Errorf("ok=false but fields non-empty: path=%q version=%q", path, version)
		}
	}
}
