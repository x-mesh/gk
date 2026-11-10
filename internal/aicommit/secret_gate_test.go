package aicommit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/scan"
	"github.com/x-mesh/gk/internal/secrets"
)

type fakeGitleaks struct {
	findings []scan.GitleaksFinding
	err      error
}

func (f fakeGitleaks) Run(context.Context) ([]scan.GitleaksFinding, error) {
	return f.findings, f.err
}

func TestScanPayloadBuiltinOnly(t *testing.T) {
	payload := "api_key: \"AKIA1234567890ABCDEF\"\n"
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{}, fakeGitleaks{})
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("findings: %+v", got)
	}
	if got[0].Source != "builtin" || got[0].Kind != "aws-access-key" {
		t.Errorf("finding: %+v", got[0])
	}
}

func TestScanPayloadAllowKindsSuppresses(t *testing.T) {
	payload := "AKIA1234567890ABCDEF\n"
	got, err := ScanPayload(context.Background(), payload,
		SecretGateOptions{AllowKinds: []string{"aws-access-key"}}, fakeGitleaks{})
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("AllowKinds should suppress aws-access-key, got %+v", got)
	}
}

func TestScanPayloadGitleaksAugments(t *testing.T) {
	payload := "\n" // no builtin hit
	gl := fakeGitleaks{findings: []scan.GitleaksFinding{{
		RuleID:    "stripe-key",
		StartLine: 7,
		File:      "config.yaml",
		Match:     "sk_live_***REDACTED***",
	}}}
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true}, gl)
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("findings: %+v", got)
	}
	if got[0].Source != "gitleaks" || got[0].Kind != "stripe-key" {
		t.Errorf("finding: %+v", got[0])
	}
	if got[0].File != "config.yaml" || got[0].Line != 7 {
		t.Errorf("finding location: %+v", got[0])
	}
}

func TestScanPayloadGitleaksMissingIsSilent(t *testing.T) {
	gl := fakeGitleaks{err: scan.ErrGitleaksNotInstalled}
	got, err := ScanPayload(context.Background(), "hello world\n",
		SecretGateOptions{RunGitleaks: true}, gl)
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("findings: %+v", got)
	}
}

func TestScanPayloadGitleaksRealErrorPropagates(t *testing.T) {
	gl := fakeGitleaks{err: errors.New("gitleaks: i/o timeout")}
	_, err := ScanPayload(context.Background(), "x\n",
		SecretGateOptions{RunGitleaks: true}, gl)
	if err == nil {
		t.Fatal("want wrapped error")
	}
}

// TestScanPayloadMarkdownHeadingNotMistakenForFile guards the regression
// where a markdown H3 inside scanned content (e.g. "### 첫 호출" in a
// README) was treated as a file boundary, surfacing as a phantom
// filename in the rendered finding output. The payload header is now
// sentinel-based so any `### ...` content stays content.
func TestScanPayloadMarkdownHeadingNotMistakenForFile(t *testing.T) {
	payload := secrets.PayloadFileHeader("README.md") + "\n" +
		"# 가이드\n" +
		"\n" +
		"### 첫 호출\n" +
		"잘못된 파싱 회귀 케이스\n" +
		"AKIA1234567890ABCDEF\n"
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{}, fakeGitleaks{})
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("findings: %+v", got)
	}
	if got[0].File != "README.md" {
		t.Errorf("file: want README.md, got %q (markdown H3 leaked in)", got[0].File)
	}
}

// The default gitleaks-ruleset scan covers only the payload. It used to run
// `gitleaks dir` over the whole working directory, which reported files the
// commit does not touch (here: this package's own test fixtures).
func TestScanPayloadDefaultScansPayloadOnly(t *testing.T) {
	pat := "ghp_" + "wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx"
	payload := secrets.PayloadFileHeader("cfg/app.env") + "\n# creds\nname = x\ntoken = \"" + pat + "\"\n"
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true}, nil)
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	var fromRuleset []SecretFinding
	for _, f := range got {
		if f.Source == "gitleaks" {
			fromRuleset = append(fromRuleset, f)
		}
	}
	if len(fromRuleset) != 1 {
		t.Fatalf("gitleaks-ruleset findings = %+v, want exactly the payload secret", fromRuleset)
	}
	f := fromRuleset[0]
	if f.Kind != "github-pat" || f.File != "cfg/app.env" || f.Line != 3 {
		t.Errorf("finding = %+v, want github-pat at cfg/app.env:3", f)
	}
}

// installFakeGitleaks puts a gitleaks script first on PATH that writes
// report as its JSON report and exits with the findings code it was given.
func installFakeGitleaks(t *testing.T, report string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$1\" = version ] && { echo 8.30.1; exit 0; }\n" +
		"out=''; code=1\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --report-path) out=$2; shift;; --exit-code) code=$2; shift;; esac; shift; done\n" +
		"printf '%s' '" + report + "' > \"$out\"; exit \"$code\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gitleaks"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func chdirWithGitleaksConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitleaks.toml"), []byte("[extend]\nuseDefault = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

// A repo with a gitleaks config gets gitleaks, so its custom rules and
// .gitleaksignore apply; findings outside the payload are dropped.
func TestScanPayloadDefaultUsesGitleaksWhenRepoHasConfig(t *testing.T) {
	chdirWithGitleaksConfig(t)
	installFakeGitleaks(t, `[{"RuleID":"custom-rule","File":"in.env","StartLine":2},{"RuleID":"custom-rule","File":"other.env","StartLine":1}]`)
	payload := secrets.PayloadFileHeader("in.env") + "\nname = x\ncorp = internal\n"
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true}, nil)
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "custom-rule" || got[0].File != "in.env" || got[0].Line != 2 {
		t.Fatalf("findings = %+v, want only the custom rule hit in in.env", got)
	}
}

// Without the gitleaks binary the config cannot be honored; the embedded
// ruleset still scans the payload instead of skipping it.
func TestScanPayloadConfigWithoutGitleaksUsesEmbedded(t *testing.T) {
	chdirWithGitleaksConfig(t)
	t.Setenv("PATH", t.TempDir())
	pat := "ghp_" + "wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx"
	payload := secrets.PayloadFileHeader("cfg/app.env") + "\ntoken = \"" + pat + "\"\n"
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true}, nil)
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	var kinds []string
	for _, f := range got {
		kinds = append(kinds, f.Source+":"+f.Kind)
	}
	if !strings.Contains(strings.Join(kinds, ","), "gitleaks:github-pat") {
		t.Fatalf("findings = %v, want the embedded github-pat hit", kinds)
	}
}

func TestScanPayloadEmbeddedHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	payload := secrets.PayloadFileHeader("a.txt") + "\nhello\n"
	if _, err := ScanPayload(ctx, payload, SecretGateOptions{RunGitleaks: true}, nil); err == nil {
		t.Fatal("a cancelled scan must not pass as clean")
	}
}
