package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// fakeGitleaks puts a shell script named gitleaks first on PATH. body runs
// with $report bound to the --report-path argument and $code to --exit-code.
func fakeGitleaks(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$1\" = version ] && { echo 8.30.1; exit 0; }\n" +
		"report=''; code=1\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --report-path) report=$2; shift;; --exit-code) code=$2; shift;; esac; shift; done\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gitleaks"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// gitleaks exits 1 both on a fatal setup error and, by default, on findings.
// 8.30.1 on macOS rejected --report-path /dev/stdout that way, and gk used to
// read the empty output as "no findings".
func TestRunGitleaks_FatalErrorIsNotZeroFindings(t *testing.T) {
	fakeGitleaks(t, `echo 'FTL Report path is not writable' >&2; exit 1`)
	findings, err := RunGitleaks(context.Background(), GitleaksOptions{WorkDir: t.TempDir()})
	if err == nil {
		t.Fatalf("want error, got %d findings and nil error", len(findings))
	}
	if !strings.Contains(err.Error(), "Report path is not writable") {
		t.Errorf("err = %v, want the gitleaks stderr line", err)
	}
}

func TestRunGitleaks_FindingsReadFromReportFile(t *testing.T) {
	fakeGitleaks(t, `echo '[{"RuleID":"github-pat","File":"a.go","StartLine":3}]' > "$report"; exit "$code"`)
	findings, err := RunGitleaks(context.Background(), GitleaksOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RunGitleaks: %v", err)
	}
	if len(findings) != 1 || findings[0].RuleID != "github-pat" || findings[0].StartLine != 3 {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestRunGitleaks_CleanRun(t *testing.T) {
	fakeGitleaks(t, `echo '[]' > "$report"; exit 0`)
	findings, err := RunGitleaks(context.Background(), GitleaksOptions{WorkDir: t.TempDir()})
	if err != nil || len(findings) != 0 {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

func TestRunGitleaks_LeakExitWithoutReportIsError(t *testing.T) {
	fakeGitleaks(t, `exit "$code"`)
	if _, err := RunGitleaks(context.Background(), GitleaksOptions{WorkDir: t.TempDir()}); err == nil {
		t.Fatal("want error when gitleaks reports leaks but writes no report")
	}
}
