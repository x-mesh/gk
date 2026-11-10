package rules

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/scan"
	"github.com/x-mesh/gk/internal/secrets"
)

func gitleaksMissing(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error) {
	return nil, scan.ErrGitleaksNotInstalled
}

// stubRunner answers git calls by the joined argument string and records
// every call so tests can assert which diff the rule asked for.
type stubRunner struct {
	out   map[string]string
	fail  map[string]bool
	mu    sync.Mutex
	calls []string
}

func (s *stubRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	key := strings.Join(args, " ")
	s.mu.Lock()
	s.calls = append(s.calls, key)
	s.mu.Unlock()
	if s.fail[key] {
		return nil, nil, errors.New("exit 1")
	}
	return []byte(s.out[key]), nil, nil
}

func builtinRule(diffFinding ...secrets.Finding) *SecretPatternsRule {
	r := NewSecretPatternsRule()
	r.runGitleaks = gitleaksMissing
	r.ScanDiff = func(string) []secrets.Finding { return diffFinding }
	r.ScanFile = func(string, string) []secrets.Finding { return diffFinding }
	return r
}

func severities(v []policy.Violation) (info, errs int) {
	for _, x := range v {
		switch x.Severity {
		case policy.SeverityInfo:
			info++
		case policy.SeverityError:
			errs++
		}
	}
	return
}

func TestSecretPatternsRule_NoGitleaks_StagedScansCachedDiff(t *testing.T) {
	run := &stubRunner{out: map[string]string{"diff --cached --no-color": "DIFF"}}
	r := builtinRule(secrets.Finding{Kind: "aws-access-key", File: "a.env", FileLine: 3, Sample: "AKIA****"})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if info, errs := severities(v); info != 1 || errs != 1 {
		t.Fatalf("info=%d err=%d, want 1/1: %+v", info, errs, v)
	}
	var f policy.Violation
	for _, x := range v {
		if x.Severity == policy.SeverityError {
			f = x
		}
	}
	if f.File != "a.env" || f.Line != 3 || !strings.Contains(f.Message, "aws-access-key") {
		t.Errorf("violation = %+v", f)
	}
	if len(run.calls) != 1 || run.calls[0] != "diff --cached --no-color" {
		t.Errorf("calls = %v", run.calls)
	}
}

func TestSecretPatternsRule_NoGitleaks_PushRangeScansRangeDiff(t *testing.T) {
	run := &stubRunner{out: map[string]string{"diff --no-color origin/main..HEAD": "DIFF"}}
	r := builtinRule(secrets.Finding{Kind: "github-token", File: "x.go", FileLine: 9})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, PushRange: "origin/main..HEAD"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if _, errs := severities(v); errs != 1 {
		t.Fatalf("errs=%d, want 1", errs)
	}
	if len(run.calls) != 1 || run.calls[0] != "diff --no-color origin/main..HEAD" {
		t.Errorf("calls = %v", run.calls)
	}
}

// The history scan diffs every commit reachable from any ref with -U0, as
// gitleaks' git mode does, so both scanners see the same hunks.
func TestSecretPatternsRule_NoGitleaks_DefaultScansHistory(t *testing.T) {
	const logCmd = "log -p -U0 --no-color --no-walk=unsorted c1 c2"
	run := &stubRunner{out: map[string]string{"rev-list --all": "c1\nc2\n", logCmd: "LOG"}}
	r := NewSecretPatternsRule()
	r.runGitleaks = gitleaksMissing
	var scanned string
	r.ScanDiff = func(d string) []secrets.Finding {
		scanned = d
		return []secrets.Finding{{Kind: "private-key", File: "k.pem", FileLine: 1}}
	}
	r.ScanFile = func(string, string) []secrets.Finding { return nil }
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true})
	if scanned != "LOG" {
		t.Errorf("scanned %q, want the output of %q; calls=%v", scanned, logCmd, run.calls)
	}
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if info, errs := severities(v); info != 1 || errs != 1 {
		t.Fatalf("info=%d err=%d, want 1/1", info, errs)
	}
	if !strings.Contains(v[0].Message, "built-in patterns") {
		t.Errorf("notice = %q", v[0].Message)
	}
}

func TestSecretPatternsRule_NoGitleaks_UnbornHeadReportsNoticeOnly(t *testing.T) {
	run := &stubRunner{out: map[string]string{"rev-list --all": ""}}
	r := builtinRule(secrets.Finding{Kind: "aws-access-key"})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if info, errs := severities(v); info != 1 || errs != 0 {
		t.Fatalf("info=%d err=%d, want 1/0", info, errs)
	}
	for _, c := range run.calls {
		if strings.HasPrefix(c, "log ") {
			t.Errorf("log must not run on an unborn HEAD: %v", run.calls)
		}
	}
}

// The history is diffed in parallel chunks of commits. Every commit must be
// diffed exactly once, and findings must keep rev-list order.
func TestSecretPatternsRule_HistoryChunksKeepOrderAndCoverEveryCommit(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(4))
	var shas []string
	for i := range 3*historyChunkMin + 5 {
		shas = append(shas, fmt.Sprintf("c%03d", i))
	}
	run := &stubRunner{out: map[string]string{"rev-list --all": strings.Join(shas, "\n")}}
	const prefix = "log -p -U0 --no-color --no-walk=unsorted "
	for lo := 0; lo < len(shas); lo += historyChunkMin {
		chunk := shas[lo:min(lo+historyChunkMin, len(shas))]
		run.out[prefix+strings.Join(chunk, " ")] = chunk[0]
	}
	r := builtinRule()
	r.ScanDiff = func(d string) []secrets.Finding { return []secrets.Finding{{Kind: "k", File: d}} }
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	var got []string
	for _, x := range v[1:] {
		got = append(got, x.File)
	}
	if want := "c000 c032 c064 c096"; strings.Join(got, " ") != want {
		t.Fatalf("finding order = %v, want %s; calls=%v", got, want, run.calls)
	}
	diffed := map[string]int{}
	for _, c := range run.calls {
		if rest, ok := strings.CutPrefix(c, prefix); ok {
			for _, sha := range strings.Fields(rest) {
				diffed[sha]++
			}
		}
	}
	for _, sha := range shas {
		if diffed[sha] != 1 {
			t.Errorf("commit %s diffed %d times", sha, diffed[sha])
		}
	}
}

func TestSecretPatternsRule_HistoryChunkFailureSurfaces(t *testing.T) {
	run := &stubRunner{
		out:  map[string]string{"rev-list --all": "c1\n"},
		fail: map[string]bool{"log -p -U0 --no-color --no-walk=unsorted c1": true},
	}
	if _, err := builtinRule().Evaluate(context.Background(), policy.Input{Runner: run, History: true}); err == nil {
		t.Fatal("want error when a history chunk fails")
	}
}

func TestSecretPatternsRule_NoGitleaks_GitFailureSurfaces(t *testing.T) {
	run := &stubRunner{fail: map[string]bool{"diff --cached --no-color": true}}
	r := builtinRule()
	if _, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true}); err == nil {
		t.Fatal("want error when the diff command fails")
	}
}

func TestSecretPatternsRule_NoGitleaks_NilRunnerKeepsNoOpNotice(t *testing.T) {
	r := builtinRule(secrets.Finding{Kind: "aws-access-key"})
	v, err := r.Evaluate(context.Background(), policy.Input{})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(v) != 1 || !strings.Contains(v[0].Message, "no-op") {
		t.Fatalf("violations = %+v", v)
	}
}

func gitleaksConfigDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSecretPatternsRule_GitleaksConfig_RunsGitleaks(t *testing.T) {
	for _, name := range []string{".gitleaks.toml", ".gitleaksignore"} {
		t.Run(name, func(t *testing.T) {
			run := &stubRunner{}
			r := NewSecretPatternsRule()
			r.runGitleaks = func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error) {
				return []scan.GitleaksFinding{{RuleID: "r", File: "f", StartLine: 1}}, nil
			}
			r.ScanDiff = func(string) []secrets.Finding { t.Fatal("built-in scan must not run"); return nil }
			v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: gitleaksConfigDir(t, name)})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if len(v) != 1 || len(run.calls) != 0 {
				t.Fatalf("violations=%+v calls=%v", v, run.calls)
			}
		})
	}
}

func TestSecretPatternsRule_GitleaksConfigEnv_RunsGitleaks(t *testing.T) {
	t.Setenv("GITLEAKS_CONFIG", "/elsewhere/gitleaks.toml")
	called := false
	r := NewSecretPatternsRule()
	r.runGitleaks = func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error) {
		called = true
		return nil, nil
	}
	r.ScanDiff = func(string) []secrets.Finding { return nil }
	if _, err := r.Evaluate(context.Background(), policy.Input{Runner: &stubRunner{}, WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !called {
		t.Fatal("gitleaks must run when GITLEAKS_CONFIG is set")
	}
}

// Without a gitleaks config the embedded engine scans even when gitleaks is
// installed: it reports the same findings and runs faster.
func TestSecretPatternsRule_NoConfig_PrefersEmbeddedOverGitleaks(t *testing.T) {
	run := &stubRunner{out: map[string]string{"diff --cached --no-color": "DIFF"}}
	r := NewSecretPatternsRule()
	r.runGitleaks = func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error) {
		t.Fatal("gitleaks must not run without a gitleaks config")
		return nil, nil
	}
	r.ScanDiff = func(string) []secrets.Finding { return []secrets.Finding{{Kind: "k", File: "f", FileLine: 2}} }
	r.ScanFile = func(string, string) []secrets.Finding { return nil }
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if info, errs := severities(v); info != 1 || errs != 1 {
		t.Fatalf("info=%d err=%d, want 1/1", info, errs)
	}
	if strings.Contains(v[0].Message, "not installed") {
		t.Errorf("notice must not claim gitleaks is missing: %q", v[0].Message)
	}
}

func TestSecretPatternsRule_ConfigButNoGitleaks_FallsBackWithNotice(t *testing.T) {
	run := &stubRunner{out: map[string]string{"diff --cached --no-color": "DIFF"}}
	r := builtinRule(secrets.Finding{Kind: "k", File: "f", FileLine: 2})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true, WorkDir: gitleaksConfigDir(t, ".gitleaks.toml")})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(v) != 2 || !strings.Contains(v[0].Message, "gitleaks not installed") {
		t.Fatalf("violations = %+v", v)
	}
}

func TestSecretPatternsRule_Name(t *testing.T) {
	r := NewSecretPatternsRule()
	if r.Name() != "secret_patterns" {
		t.Errorf("Name() = %q, want secret_patterns", r.Name())
	}
}

func TestSecretPatternsRule_ImplementsPolicyRule(t *testing.T) {
	var _ policy.Rule = (*SecretPatternsRule)(nil)
}

func TestSecretPatternsRule_NoGitleaks_EmitsInfoViolation(t *testing.T) {
	r := NewSecretPatternsRule()
	r.runGitleaks = gitleaksMissing
	v, err := r.Evaluate(context.Background(), policy.Input{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("got %d violations, want 1", len(v))
	}
	if v[0].Severity != policy.SeverityInfo {
		t.Errorf("Severity = %v, want Info", v[0].Severity)
	}
	if !strings.Contains(v[0].Message, "gitleaks not installed") {
		t.Errorf("Message = %q", v[0].Message)
	}
	if !strings.Contains(v[0].Hint, "brew install gitleaks") {
		t.Errorf("Hint = %q", v[0].Hint)
	}
}

// TestSecretPatternsRule_FindingsMap_ConvertFromSampleStruct exercises the
// GitleaksFinding → policy.Violation mapping by constructing the findings
// directly (no subprocess). Uses the same field shape that
// scan.ParseGitleaksFindings yields.
func TestSecretPatternsRule_FindingsMap_ConvertFromSampleStruct(t *testing.T) {
	findings := []scan.GitleaksFinding{
		{
			RuleID:      "aws-access-key-id",
			Description: "AWS Access Key",
			File:        "config/secrets.yml",
			StartLine:   5,
		},
		{
			RuleID:      "generic-api-key",
			Description: "", // trigger fallback message path
			File:        "src/client.go",
			StartLine:   12,
		},
	}

	violations := convertFindings(findings)
	if len(violations) != 2 {
		t.Fatalf("got %d violations, want 2", len(violations))
	}

	first := violations[0]
	if first.RuleID != "secret_patterns" {
		t.Errorf("RuleID = %q, want secret_patterns", first.RuleID)
	}
	if first.Severity != policy.SeverityError {
		t.Errorf("Severity = %v, want Error", first.Severity)
	}
	if first.File != "config/secrets.yml" || first.Line != 5 {
		t.Errorf("file/line = %q:%d, want config/secrets.yml:5", first.File, first.Line)
	}
	if !strings.Contains(first.Message, "AWS Access Key") ||
		!strings.Contains(first.Message, "aws-access-key-id") {
		t.Errorf("Message = %q", first.Message)
	}

	// Fallback message path when Description is empty.
	if !strings.Contains(violations[1].Message, "secret matched rule generic-api-key") {
		t.Errorf("fallback Message = %q", violations[1].Message)
	}
}

func TestConvertFindings_Empty(t *testing.T) {
	v := convertFindings(nil)
	if len(v) != 0 {
		t.Errorf("got %d violations for nil input, want 0", len(v))
	}
}

func TestSecretPatternsRule_NoGitleaks_NoticeUsesScannerLabel(t *testing.T) {
	run := &stubRunner{out: map[string]string{"diff --cached --no-color": "DIFF"}}
	r := builtinRule()
	r.ScannerLabel = func() string { return "the embedded ruleset vX" }
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(v) != 1 || !strings.Contains(v[0].Message, "scanned staged changes with the embedded ruleset vX") {
		t.Fatalf("violations = %+v", v)
	}
}

// The default scope reads the working-tree content of every tracked file:
// uncommitted edits count, files missing from the working tree, symlinks,
// and submodules are skipped, and a binary file is matched by path only.
func TestSecretPatternsRule_DefaultScansTrackedFiles(t *testing.T) {
	root := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cfg/app.env", []byte("token = x\n"))
	write("cert.p12", []byte{0x30, 0x00, 0x82})
	write("z.txt", []byte("z\n"))
	if err := os.Symlink("cfg/app.env", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	run := &stubRunner{out: map[string]string{
		"rev-parse --show-toplevel":     root + "\n",
		"ls-files -z --full-name -- :/": "cfg/app.env\x00cert.p12\x00gone.txt\x00link\x00z.txt\x00",
	}}
	var mu sync.Mutex
	scanned := map[string]string{}
	r := builtinRule()
	r.ScanFile = func(path, content string) []secrets.Finding {
		mu.Lock()
		scanned[path] = content
		mu.Unlock()
		return []secrets.Finding{{Kind: "k", File: path}}
	}
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	want := map[string]string{"cfg/app.env": "token = x\n", "cert.p12": "", "z.txt": "z\n"}
	if fmt.Sprint(scanned) != fmt.Sprint(want) {
		t.Errorf("scanned = %q, want %q", scanned, want)
	}
	var order []string
	for _, x := range v[1:] {
		order = append(order, x.File)
	}
	if strings.Join(order, ",") != "cfg/app.env,cert.p12,z.txt" {
		t.Errorf("finding order = %v, want ls-files order", order)
	}
	if !strings.Contains(v[0].Message, "scanned tracked files") {
		t.Errorf("notice = %q", v[0].Message)
	}
}

func TestSecretPatternsRule_GitleaksModeFollowsScope(t *testing.T) {
	cases := []struct {
		name string
		in   policy.Input
		mode string
		args string
	}{
		{"default", policy.Input{}, "dir", "--x"},
		{"staged", policy.Input{Staged: true}, "git", "--staged --x"},
		{"push", policy.Input{PushRange: "a..b"}, "git", "--log-opts=a..b --x"},
		{"history", policy.Input{History: true}, "git", "--x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewSecretPatternsRule()
			r.GitleaksOpts.ExtraArgs = []string{"--x"}
			var got scan.GitleaksOptions
			r.runGitleaks = func(_ context.Context, o scan.GitleaksOptions) ([]scan.GitleaksFinding, error) {
				got = o
				return nil, nil
			}
			c.in.WorkDir = gitleaksConfigDir(t, ".gitleaks.toml")
			if _, err := r.Evaluate(context.Background(), c.in); err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got.Mode != c.mode || strings.Join(got.ExtraArgs, " ") != c.args || !got.Redact {
				t.Errorf("gitleaks opts = %+v, want mode %s args %q redact", got, c.mode, c.args)
			}
		})
	}
}
