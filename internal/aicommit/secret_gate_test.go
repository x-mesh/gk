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

// installFakeGitleaks puts a gitleaks script first on PATH. It reports one
// "custom-rule" finding at line 1 of every file under its working directory
// and appends its arguments and working directory to the returned log file.
func installFakeGitleaks(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = version ] && { echo 8.30.1; exit 0; }\n" +
		"echo \"cwd=$(pwd -P) args=$*\" >> '" + logPath + "'\n" +
		"out=''; code=1\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --report-path) out=$2; shift;; --exit-code) code=$2; shift;; esac; shift; done\n" +
		"json='['; sep=''\n" +
		"for f in $(find . -type f | sed 's|^\\./||' | sort); do json=\"$json$sep{\\\"RuleID\\\":\\\"custom-rule\\\",\\\"File\\\":\\\"$f\\\",\\\"StartLine\\\":1}\"; sep=','; done\n" +
		"printf '%s]' \"$json\" > \"$out\"\n" +
		"[ \"$sep\" = ',' ] && exit \"$code\"; exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "gitleaks"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// repoWithGitleaksConfig returns a repo root holding .gitleaks.toml,
// .gitleaksignore, and a file that is not part of any payload.
func repoWithGitleaksConfig(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".gitleaks.toml":  "[extend]\nuseDefault = true\n",
		".gitleaksignore": "",
		"other.env":       "corp = internal\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// With a gitleaks config, gitleaks scans the payload files themselves, with
// the repo's config and ignore file, wherever the process runs: gk commit
// does not chdir to the repo root.
func TestScanPayloadConfiguredScansPayloadNotCwd(t *testing.T) {
	t.Setenv("GITLEAKS_CONFIG", "")
	t.Setenv("GITLEAKS_CONFIG_TOML", "")
	root := repoWithGitleaksConfig(t)
	t.Chdir(t.TempDir())
	log := installFakeGitleaks(t)
	payload := secrets.PayloadFileHeader("sub/in.env") + "\ncorp = internal\n"
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true, RepoDir: root}, nil)
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "custom-rule" || got[0].File != "sub/in.env" || got[0].Line != 1 {
		t.Fatalf("findings = %+v, want only the payload file sub/in.env", got)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--config " + filepath.Join(root, ".gitleaks.toml"),
		"--gitleaks-ignore-path " + root,
	} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("gitleaks call %q lacks %q", calls, want)
		}
	}
}

// GITLEAKS_CONFIG takes precedence over the repo file in gitleaks itself, so
// gk must not override it with --config.
func TestScanPayloadConfiguredKeepsEnvConfig(t *testing.T) {
	root := repoWithGitleaksConfig(t)
	t.Setenv("GITLEAKS_CONFIG", "/elsewhere/gitleaks.toml")
	log := installFakeGitleaks(t)
	payload := secrets.PayloadFileHeader("in.env") + "\nx\n"
	if _, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true, RepoDir: root}, nil); err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "--config") {
		t.Errorf("gitleaks call %q must leave GITLEAKS_CONFIG in charge", calls)
	}
}

// Without the gitleaks binary the config cannot be honored; the embedded
// ruleset still scans the payload instead of skipping it.
func TestScanPayloadConfigWithoutGitleaksUsesEmbedded(t *testing.T) {
	root := repoWithGitleaksConfig(t)
	t.Setenv("PATH", t.TempDir())
	pat := "ghp_" + "wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx"
	payload := secrets.PayloadFileHeader("cfg/app.env") + "\ntoken = \"" + pat + "\"\n"
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true, RepoDir: root}, nil)
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

// A payload path that escapes the temp directory must not be written there.
func TestScanPayloadConfiguredRejectsEscapingPath(t *testing.T) {
	t.Setenv("GITLEAKS_CONFIG", "")
	t.Setenv("GITLEAKS_CONFIG_TOML", "")
	root := repoWithGitleaksConfig(t)
	installFakeGitleaks(t)
	payload := secrets.PayloadFileHeader("../escape.env") + "\nx\n"
	if _, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true, RepoDir: root}, nil); err == nil {
		t.Fatal("want an error for a payload path outside the repo")
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
