package rules

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	blobs map[string]string
	mu    sync.Mutex
	calls []string
	// reads counts the blobs `cat-file --batch` printed.
	reads map[string]int
	// batches lists the blob ids of each `cat-file --batch` call.
	batches [][]string
	// dies makes `cat-file --batch` fail when it comes to this blob, after
	// it printed the blobs before it.
	dies string
	// waits makes `cat-file --batch` wait before it prints this blob until
	// its context ends, and then fail, as git does when gk cancels it.
	waits string
}

// patch is cmd with the pathspecs that leave archives out of a patch.
func patch(cmd string) string { return cmd + " " + strings.Join(archivePatchExclude, " ") }

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

// Stream answers `cat-file --batch-check` and `cat-file --batch` from blobs,
// the way git prints them, and a blob it does not hold as missing.
func (s *stubRunner) Stream(ctx context.Context, stdin io.Reader, w io.Writer, args ...string) error {
	key := strings.Join(args, " ")
	s.mu.Lock()
	s.calls = append(s.calls, key)
	s.mu.Unlock()
	if s.fail[key] {
		return errors.New("exit 128")
	}
	ids, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if key == "cat-file --batch" {
		s.mu.Lock()
		s.batches = append(s.batches, strings.Fields(string(ids)))
		s.mu.Unlock()
	}
	for _, id := range strings.Fields(string(ids)) {
		if key == "cat-file --batch" && id == s.dies {
			return errors.New("exit 128")
		}
		if key == "cat-file --batch" && id == s.waits {
			// The limit keeps a scan that never cancels from hanging the
			// test; the blob it then prints fails the test.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Second):
			}
		}
		data, ok := s.blobs[id]
		if !ok {
			fmt.Fprintf(w, "%s missing\n", id)
			continue
		}
		fmt.Fprintf(w, "%s blob %d\n", id, len(data))
		if key == "cat-file --batch" {
			s.mu.Lock()
			if s.reads == nil {
				s.reads = map[string]int{}
			}
			s.reads[id]++
			s.mu.Unlock()
			fmt.Fprintf(w, "%s\n", data)
		}
	}
	return nil
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
	run := &stubRunner{out: map[string]string{patch("diff --cached --no-color"): "DIFF"}}
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
	if want := patch("diff --cached --no-color") + ",diff --cached --raw -z --no-abbrev --no-renames"; strings.Join(run.calls, ",") != want {
		t.Errorf("calls = %v", run.calls)
	}
}

func TestSecretPatternsRule_NoGitleaks_PushRangeScansRangeDiff(t *testing.T) {
	run := &stubRunner{out: map[string]string{patch("diff --no-color origin/main..HEAD"): "DIFF"}}
	r := builtinRule(secrets.Finding{Kind: "github-token", File: "x.go", FileLine: 9})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, PushRange: "origin/main..HEAD"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if _, errs := severities(v); errs != 1 {
		t.Fatalf("errs=%d, want 1", errs)
	}
	if want := patch("diff --no-color origin/main..HEAD") + ",diff --raw -z --no-abbrev --no-renames origin/main..HEAD"; strings.Join(run.calls, ",") != want {
		t.Errorf("calls = %v", run.calls)
	}
}

// The history scan diffs every commit reachable from any ref with -U0, as
// gitleaks' git mode does, so both scanners see the same hunks.
func TestSecretPatternsRule_NoGitleaks_DefaultScansHistory(t *testing.T) {
	logCmd := patch("log -p -U0 --no-color --no-walk=unsorted c1 c2")
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
		run.out[patch(prefix+strings.Join(chunk, " "))] = chunk[0]
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
		fail: map[string]bool{patch("log -p -U0 --no-color --no-walk=unsorted c1"): true},
	}
	if _, err := builtinRule().Evaluate(context.Background(), policy.Input{Runner: run, History: true}); err == nil {
		t.Fatal("want error when a history chunk fails")
	}
}

func TestSecretPatternsRule_NoGitleaks_GitFailureSurfaces(t *testing.T) {
	run := &stubRunner{fail: map[string]bool{patch("diff --cached --no-color"): true}}
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
	for _, env := range []string{"GITLEAKS_CONFIG", "GITLEAKS_CONFIG_TOML"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("GITLEAKS_CONFIG", "")
			t.Setenv("GITLEAKS_CONFIG_TOML", "")
			t.Setenv(env, "/elsewhere/gitleaks.toml")
			called := false
			r := builtinRule()
			r.runGitleaks = func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error) {
				called = true
				return nil, nil
			}
			if _, err := r.Evaluate(context.Background(), policy.Input{Runner: &stubRunner{}, WorkDir: t.TempDir()}); err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if !called {
				t.Fatalf("gitleaks must run when %s is set", env)
			}
		})
	}
}

// Without a gitleaks config the embedded engine scans even when gitleaks is
// installed: it reports the same findings and runs faster.
func TestSecretPatternsRule_NoConfig_PrefersEmbeddedOverGitleaks(t *testing.T) {
	run := &stubRunner{out: map[string]string{patch("diff --cached --no-color"): "DIFF"}}
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
	run := &stubRunner{out: map[string]string{patch("diff --cached --no-color"): "DIFF"}}
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
	run := &stubRunner{out: map[string]string{patch("diff --cached --no-color"): "DIFF"}}
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

// A tracked file the scan cannot stat is an error, not a deleted file: a
// skipped file would read as clean.
func TestSecretPatternsRule_UnreadableTrackedFileFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "a.env"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	run := &stubRunner{out: map[string]string{
		"rev-parse --show-toplevel":     root + "\n",
		"ls-files -z --full-name -- :/": "locked/a.env\x00",
	}}
	if _, err := builtinRule().Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir()}); err == nil {
		t.Fatal("want an error for a tracked file that cannot be read")
	}
}

// A repo root that starts or ends with a space must keep it, or no tracked
// file is found.
func TestSecretPatternsRule_TrackedFilesRootWithSpaces(t *testing.T) {
	for _, name := range []string{"repo ", " repo", " repo "} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "a.env"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			run := &stubRunner{out: map[string]string{
				"rev-parse --show-toplevel":     root + "\n",
				"ls-files -z --full-name -- :/": "a.env\x00",
			}}
			var scanned []string
			r := builtinRule()
			r.ScanFile = func(path, _ string) []secrets.Finding {
				scanned = append(scanned, path)
				return nil
			}
			if _, err := r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir()}); err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if len(scanned) != 1 {
				t.Fatalf("scanned = %v, want a.env", scanned)
			}
		})
	}
}

func TestSecretPatternsRule_VerifyAnnotatesAWSFindings(t *testing.T) {
	live := secrets.NewAWSCredential("AKIA"+"LIVE000000000000", []string{"s"}, "")
	dead := secrets.NewAWSCredential("AKIA"+"DEAD000000000000", []string{"s"}, "")
	r := builtinRule(
		secrets.Finding{Kind: "aws-access-token", File: "a", FileLine: 1, Credential: live},
		secrets.Finding{Kind: "aws-access-token", File: "b", FileLine: 2, Credential: live},
		secrets.Finding{Kind: "aws-access-token", File: "c", FileLine: 3, Credential: dead},
		secrets.Finding{Kind: "generic-api-key", File: "c", FileLine: 4},
	)
	var calls int
	var got []*secrets.Credential
	r.Verify = func(_ context.Context, creds []*secrets.Credential) map[string]policy.Verification {
		calls++
		got = creds
		return map[string]policy.Verification{
			live.ID(): {Provider: "aws", Status: "valid", Account: "123456789012"},
			dead.ID(): {Provider: "aws", Status: "rejected", Code: "InvalidClientTokenId"},
		}
	}
	run := &stubRunner{out: map[string]string{patch("diff --cached --no-color"): "x"}}

	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir(), Staged: true})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("VerifyAWS ran without Input.Verify")
	}
	for _, x := range v {
		if x.Verification != nil {
			t.Errorf("unverified scan annotated %s", x.File)
		}
	}

	v, err = r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir(), Staged: true, Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(got) != 3 {
		t.Fatalf("VerifyAWS calls = %d with %d credentials, want 1 call with 3", calls, len(got))
	}
	byFile := map[string]policy.Violation{}
	for _, x := range v {
		if x.File != "" {
			byFile[x.File+strconv.Itoa(x.Line)] = x
		}
	}
	for _, k := range []string{"a1", "b2"} {
		x := byFile[k]
		if x.Verification == nil || x.Verification.Status != "valid" || !strings.Contains(x.Hint, "live") {
			t.Errorf("%s: %+v", k, x)
		}
		if x.Severity != policy.SeverityError {
			t.Errorf("%s: severity changed to %v", k, x.Severity)
		}
	}
	if x := byFile["c3"]; x.Verification == nil || x.Verification.Status != "rejected" || x.Severity != policy.SeverityError {
		t.Errorf("rejected finding = %+v, want annotated and still an error", x)
	}
	if byFile["c4"].Verification != nil {
		t.Errorf("generic finding annotated")
	}
	summary := v[len(v)-1]
	if summary.Severity != policy.SeverityInfo || !strings.Contains(summary.Message, "checked 2 credential(s)") || !strings.Contains(summary.Message, "AWS 1 valid, 1 rejected") {
		t.Errorf("summary = %q", summary.Message)
	}
}

func TestSecretPatternsRule_VerifyWithGitleaksSaysNothingVerified(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitleaks.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	r := builtinRule()
	r.runGitleaks = func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error) { return nil, nil }
	r.Verify = func(context.Context, []*secrets.Credential) map[string]policy.Verification {
		t.Fatal("Verify called on the gitleaks path")
		return nil
	}
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: &stubRunner{}, WorkDir: dir, Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || !strings.Contains(v[0].Message, "nothing was verified") {
		t.Errorf("violations = %+v", v)
	}
}

func TestSecretPatternsRule_VerifyHintAndSummaryPerProvider(t *testing.T) {
	slack := secrets.NewTokenCredential(secrets.ProviderSlack, "slack-bot-token", "xoxb-1")
	gcp := secrets.NewTokenCredential(secrets.ProviderGCP, "gcp-api-key", "AIza-1")
	r := builtinRule(
		secrets.Finding{Kind: "slack-bot-token", File: "a", FileLine: 1, Credential: slack},
		secrets.Finding{Kind: "gcp-api-key", File: "b", FileLine: 1, Credential: gcp},
	)
	r.Verify = func(context.Context, []*secrets.Credential) map[string]policy.Verification {
		return map[string]policy.Verification{
			slack.ID(): {Provider: secrets.ProviderSlack, Status: "valid", Account: "T1", Identity: "U1"},
			gcp.ID():   {Provider: secrets.ProviderGCP, Status: "rejected", Code: "API_KEY_INVALID"},
		}
	}
	run := &stubRunner{out: map[string]string{patch("diff --cached --no-color"): "x"}}
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir(), Staged: true, Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v[1].Hint, "revoke") {
		t.Errorf("slack hint = %q", v[1].Hint)
	}
	if strings.Contains(v[2].Hint, "live") {
		t.Errorf("rejected gcp finding got the live hint: %q", v[2].Hint)
	}
	summary := v[len(v)-1]
	if summary.Message != "verify: checked 2 credential(s): Slack 1 valid; GCP 1 rejected" || summary.Hint != "" {
		t.Errorf("summary = %q hint %q", summary.Message, summary.Hint)
	}
}

// A private-key finding carries a candidate for OCI pairing. With no profile
// that names it, such as for an SSH or TLS key, it must not reach Verify.
func TestSecretPatternsRule_UnpairedPrivateKeyIsNotVerified(t *testing.T) {
	r := builtinRule(secrets.Finding{
		Kind: "private-key", File: "ssh/id_rsa", FileLine: 1,
		Credential: secrets.KeyCandidate("-----BEGIN " + "OPENSSH PRIVATE KEY-----\nabc\n-----END " + "OPENSSH PRIVATE KEY-----"),
	})
	r.Verify = func(context.Context, []*secrets.Credential) map[string]policy.Verification {
		t.Fatal("Verify called for a private key that no OCI profile names")
		return nil
	}
	run := &stubRunner{out: map[string]string{patch("diff --cached --no-color"): "x"}}
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir(), Staged: true, Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 || v[1].File != "ssh/id_rsa" || v[1].Verification != nil || v[1].Severity != policy.SeverityError {
		t.Fatalf("violations = %+v, want the finding unchanged and without a verification", v)
	}
	if got, want := v[2].Message, "verify: no AWS, Slack, GCP, or OCI credential to check"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// Profiles matter only to verification, so a scan without it must not pay
// for the search.
func TestSecretPatternsRule_OCIProfilesAreCollectedOnlyForVerify(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := &stubRunner{out: map[string]string{
		patch("diff --cached --no-color"):                       "staged diff",
		patch("diff --no-color main..feat"):                     "range diff",
		"rev-parse --show-toplevel":                             root + "\n",
		"ls-files -z --full-name -- :/":                         "a.txt\x00b.txt\x00",
		"rev-list --all":                                        "c1\nc2\n",
		patch("log -p -U0 --no-color --no-walk=unsorted c1 c2"): "history diff",
	}}
	var mu sync.Mutex
	var seen []string
	r := builtinRule()
	r.findOCIProfiles = func(text string) []secrets.OCIProfile {
		mu.Lock()
		seen = append(seen, text)
		mu.Unlock()
		return nil
	}
	r.Verify = func(context.Context, []*secrets.Credential) map[string]policy.Verification { return nil }
	cases := []struct {
		name string
		in   policy.Input
		want []string
	}{
		{"staged without verify", policy.Input{Staged: true}, nil},
		{"tracked files without verify", policy.Input{}, nil},
		{"history without verify", policy.Input{History: true}, nil},
		{"push range without verify", policy.Input{PushRange: "main..feat"}, nil},
		{"staged", policy.Input{Staged: true, Verify: true}, []string{"staged diff"}},
		{"push range", policy.Input{PushRange: "main..feat", Verify: true}, []string{"range diff"}},
		{"tracked files", policy.Input{Verify: true}, []string{"a.txt\n", "b.txt\n"}},
		{"history", policy.Input{History: true, Verify: true}, []string{"history diff"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			tc.in.Runner, tc.in.WorkDir = run, t.TempDir()
			if _, err := r.Evaluate(context.Background(), tc.in); err != nil {
				t.Fatal(err)
			}
			slices.Sort(seen)
			if !slices.Equal(seen, tc.want) {
				t.Errorf("profile search saw %q, want %q", seen, tc.want)
			}
		})
	}
}

func TestVerifySummary_RejectedHintsNameTheirProvider(t *testing.T) {
	cases := map[string]struct {
		verified map[string]policy.Verification
		hint     []string
	}{
		"OCI rejected": {map[string]policy.Verification{"a": {Provider: secrets.ProviderOCI, Status: "rejected"}}, []string{"OCI rejected", "not subscribed"}},
		"AWS and OCI rejected": {map[string]policy.Verification{
			"a": {Provider: secrets.ProviderAWS, Status: "rejected"}, "b": {Provider: secrets.ProviderOCI, Status: "rejected"},
		}, []string{"AWS rejected", "OCI rejected"}},
		"OCI unknown": {map[string]policy.Verification{"a": {Provider: secrets.ProviderOCI, Status: "unknown"}}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			hint := verifySummary("secret_patterns", tc.verified).Hint
			if len(tc.hint) == 0 && hint != "" {
				t.Errorf("hint = %q, want none", hint)
			}
			for _, want := range tc.hint {
				if !strings.Contains(hint, want) {
					t.Errorf("hint = %q, want it to mention %q", hint, want)
				}
			}
		})
	}
}

// Most findings carry no credential. Verification must leave them as they
// are: reported, and without a verification.
func TestSecretPatternsRule_VerifyWithFindingWithoutCredential(t *testing.T) {
	r := builtinRule(secrets.Finding{Kind: "generic-api-key", File: "a", FileLine: 1})
	r.Verify = func(context.Context, []*secrets.Credential) map[string]policy.Verification {
		t.Fatal("Verify called with no credential to check")
		return nil
	}
	run := &stubRunner{out: map[string]string{patch("diff --cached --no-color"): "x"}}
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir(), Staged: true, Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 || v[1].File != "a" || v[1].Verification != nil || v[1].Severity != policy.SeverityError {
		t.Errorf("violations = %+v, want the finding reported without a verification", v)
	}
}

func makeZip(t *testing.T, files ...string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < len(files); i += 2 {
		w, err := zw.Create(files[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// rawEntry is one record of `git diff --raw -z --no-abbrev`.
func rawEntry(status, mode, blob string, paths ...string) string {
	old := strings.Repeat("0", 40)
	return ":100644 " + mode + " " + old + " " + blob + " " + status + "\x00" + strings.Join(paths, "\x00") + "\x00"
}

func blobID(n int) string { return fmt.Sprintf("%040x", n) }

// archiveStub serves the size and content of each blob in blobs.
func archiveStub(run *stubRunner, blobs map[string]string) {
	if run.blobs == nil {
		run.blobs = map[string]string{}
	}
	maps.Copy(run.blobs, blobs)
}

// recordFiles makes ScanFile record each path and content it is given and
// report a finding for every file named application.yml.
func recordFiles(r *SecretPatternsRule) *[]string {
	var mu sync.Mutex
	var scanned []string
	r.ScanFile = func(path, content string) []secrets.Finding {
		mu.Lock()
		scanned = append(scanned, path+"="+content)
		mu.Unlock()
		if strings.HasSuffix(path, "application.yml") {
			return []secrets.Finding{{Kind: "k", File: path, FileLine: 1}}
		}
		return nil
	}
	return &scanned
}

const rawHistoryCmd = "log --raw -z --no-abbrev --no-renames --format= --no-walk=unsorted c1 c2"

// A patch shows an archive only as "Binary files differ", so the history
// scan reads each archive blob that a commit adds or changes and scans the
// files in it. A blob that several commits carry is read once and scanned
// once per path, and a deleted file, a symlink, a submodule, and a file that
// is no archive are not read.
func TestSecretPatternsRule_HistoryScansEachArchiveBlobOnce(t *testing.T) {
	jar := makeZip(t, "BOOT-INF/classes/application.yml", "a: 1\n", "App.class", "\xca\xfe\x00")
	renamed := makeZip(t, "BOOT-INF/classes/application.yml", "a: 2\n")
	moved := makeZip(t, "application.yml", "a: 3\n")
	zero := strings.Repeat("0", 40)
	run := &stubRunner{out: map[string]string{
		"rev-list --all": "c1\nc2\n",
		rawHistoryCmd: rawEntry("A", "100644", blobID(1), "was/app-1.0.jar") +
			":100644 000000 " + blobID(1) + " " + zero + " D\x00was/app-1.0.jar\x00" +
			rawEntry("A", "100644", blobID(2), "was/app-1.1.jar") +
			rawEntry("M", "100755", blobID(1), "copy/app.jar") +
			rawEntry("R100", "100644", blobID(3), "old.jar", "moved/new.JAR") +
			rawEntry("A", "100644", blobID(4), "lib.so") +
			rawEntry("A", "120000", blobID(5), "link.jar") +
			rawEntry("A", "160000", blobID(6), "sub.jar"),
	}}
	archiveStub(run, map[string]string{blobID(1): jar, blobID(2): renamed, blobID(3): moved})
	r := builtinRule()
	scanned := recordFiles(r)
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	slices.Sort(*scanned)
	want := []string{
		"copy/app.jar!App.class=",
		"copy/app.jar!BOOT-INF/classes/application.yml=a: 1\n",
		"moved/new.JAR!application.yml=a: 3\n",
		"was/app-1.0.jar!App.class=",
		"was/app-1.0.jar!BOOT-INF/classes/application.yml=a: 1\n",
		"was/app-1.1.jar!BOOT-INF/classes/application.yml=a: 2\n",
	}
	if !slices.Equal(*scanned, want) {
		t.Errorf("scanned = %q, want %q", *scanned, want)
	}
	var files []string
	for _, x := range v[1:] {
		files = append(files, x.File)
	}
	if got := strings.Join(files, ","); got != "was/app-1.0.jar!BOOT-INF/classes/application.yml,was/app-1.1.jar!BOOT-INF/classes/application.yml,copy/app.jar!BOOT-INF/classes/application.yml,moved/new.JAR!application.yml" {
		t.Errorf("findings = %s, want listing order", got)
	}
	if fmt.Sprint(run.reads) != fmt.Sprint(map[string]int{blobID(1): 1, blobID(2): 1, blobID(3): 1}) {
		t.Errorf("blob reads = %v", run.reads)
	}
	if !strings.HasSuffix(v[0].Message, "including the files in 4 archive(s)") {
		t.Errorf("notice = %q", v[0].Message)
	}
}

// A text file at an archive path is scanned as text, because the patches
// leave out archive paths, even when it starts as a zip file does. A binary
// file that is no zip is still reported, and a zip file is still opened,
// also behind a launch script without a NUL byte in its first 8000 bytes.
func TestSecretPatternsRule_TextAtAnArchivePathIsScannedAsText(t *testing.T) {
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 123\n"
	run := &stubRunner{out: map[string]string{
		"rev-list --all": "c1\nc2\n",
		rawHistoryCmd: rawEntry("A", "100644", blobID(1), "notes.zip") +
			rawEntry("A", "100644", blobID(2), "bin.zip") +
			rawEntry("A", "100644", blobID(3), "lfs.jar") +
			rawEntry("A", "100644", blobID(4), "app.jar") +
			rawEntry("A", "100644", blobID(5), "pk.zip") +
			rawEntry("A", "100644", blobID(6), "boot.jar"),
	}}
	script := "#!/bin/sh\n" + strings.Repeat("# launch\n", 1000)
	archiveStub(run, map[string]string{
		blobID(1): "password: x\n",
		blobID(2): "no zip\x00",
		blobID(3): pointer,
		blobID(4): makeZip(t, "application.yml", "a: 1\n"),
		blobID(5): "PK password: x\n",
		blobID(6): script + makeZip(t, "application.yml", "b: 2\n"),
	})
	r := builtinRule()
	scanned := recordFiles(r)
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	got := slices.Sorted(slices.Values(*scanned))
	want := []string{"app.jar!application.yml=a: 1\n", "boot.jar!application.yml=b: 2\n", "lfs.jar=" + pointer, "notes.zip=password: x\n", "pk.zip=PK password: x\n"}
	if !slices.Equal(got, want) {
		t.Errorf("scanned = %q, want %q", got, want)
	}
	var notices []string
	for _, x := range v[1:] {
		if x.Severity == policy.SeverityInfo {
			notices = append(notices, x.File+": "+x.Message)
		}
	}
	if want := []string{"bin.zip: archive not scanned: zip: not a valid zip file"}; !slices.Equal(notices, want) {
		t.Errorf("notices = %q, want %q", notices, want)
	}
}

// An archive the scan could not open, or opened only in part, is reported
// with an informational notice instead of reading as clean, and it does not
// fail the check: a blob deep in the history stays there.
func TestSecretPatternsRule_UnreadArchivesAreReported(t *testing.T) {
	partly := makeZip(t, "application.yml", "a: 1\n", "big.json", strings.Repeat("x", 200))
	run := &stubRunner{out: map[string]string{
		"rev-list --all": "c1\nc2\n",
		rawHistoryCmd: rawEntry("A", "100644", blobID(1), "huge.war") +
			rawEntry("A", "100644", blobID(2), "notes.zip") +
			rawEntry("A", "100644", blobID(3), "app.jar") +
			rawEntry("A", "100644", blobID(4), "lfs.jar"),
	}}
	archiveStub(run, map[string]string{blobID(2): "not a zip file\x00"})
	// Git LFS keeps the archive outside the repository; its pointer is no
	// archive that could not be read.
	archiveStub(run, map[string]string{blobID(4): "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 123\n"})
	archiveStub(run, map[string]string{blobID(1): strings.Repeat("x", 3<<20), blobID(3): partly})
	r := builtinRule()
	r.archiveLimit = &archiveLimit{bytes: 1 << 20, zip: secrets.ZipLimits{Entries: 10, EntryBytes: 100, TotalBytes: 1 << 20}}
	recordFiles(r)
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	var notices []string
	for _, x := range v[1:] {
		if x.Severity == policy.SeverityInfo {
			notices = append(notices, x.File+": "+x.Message)
		}
	}
	want := []string{
		"huge.war: archive not scanned: 3 MiB, more than the limit of 1 MiB",
		"notes.zip: archive not scanned: zip: not a valid zip file",
		"app.jar: archive partly scanned: 1 file(s) in it could not be read",
	}
	if !slices.Equal(notices, want) {
		t.Errorf("notices = %q, want %q", notices, want)
	}
	if _, errs := severities(v); errs != 1 {
		t.Errorf("errs = %d, want the finding in app.jar only", errs)
	}
	if run.reads[blobID(1)] != 0 {
		t.Error("an archive over the size limit must not be read")
	}
	if !strings.HasSuffix(v[0].Message, "including the files in 1 archive(s)") {
		t.Errorf("notice = %q", v[0].Message)
	}
}

// A git failure while reading an archive is a failed scan, like a failed
// history chunk, and so is a blob that git cannot find.
func TestSecretPatternsRule_ArchiveReadFailureSurfaces(t *testing.T) {
	jar := makeZip(t, "application.yml", "a: 1\n")
	cases := map[string]*stubRunner{
		"batch-check fails": {fail: map[string]bool{"cat-file --batch-check": true}, blobs: map[string]string{blobID(1): jar}},
		"batch fails":       {fail: map[string]bool{"cat-file --batch": true}, blobs: map[string]string{blobID(1): jar}},
		"blob missing":      {},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			run.out = map[string]string{"rev-list --all": "c1\nc2\n", rawHistoryCmd: rawEntry("A", "100644", blobID(1), "app.jar")}
			if _, err := builtinRule().Evaluate(context.Background(), policy.Input{Runner: run, History: true}); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestSecretPatternsRule_DiffScopesScanArchives(t *testing.T) {
	jar := makeZip(t, "application.yml", "a: 1\n")
	listing := rawEntry("A", "100644", blobID(7), "app.jar")
	cases := map[string]struct {
		in  policy.Input
		cmd string
	}{
		"staged":     {policy.Input{Staged: true}, "diff --cached --raw -z --no-abbrev --no-renames"},
		"push range": {policy.Input{PushRange: "main..feat"}, "diff --raw -z --no-abbrev --no-renames main..feat"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run := &stubRunner{out: map[string]string{tc.cmd: listing}}
			archiveStub(run, map[string]string{blobID(7): jar})
			r := builtinRule()
			r.ScanDiff = func(string) []secrets.Finding { return nil }
			recordFiles(r)
			tc.in.Runner = run
			v, err := r.Evaluate(context.Background(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if len(v) != 2 || v[1].File != "app.jar!application.yml" {
				t.Errorf("violations = %+v", v)
			}
		})
	}
}

func TestSecretPatternsRule_TrackedArchiveIsOpened(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.jar"), []byte(makeZip(t, "application.yml", "a: 1\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &stubRunner{out: map[string]string{
		"rev-parse --show-toplevel":     root + "\n",
		"ls-files -z --full-name -- :/": "app.jar\x00",
	}}
	r := builtinRule()
	scanned := recordFiles(r)
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app.jar=", "app.jar!application.yml=a: 1\n"}; !slices.Equal(*scanned, want) {
		t.Errorf("scanned = %q, want %q", *scanned, want)
	}
	if len(v) != 2 || v[1].File != "app.jar!application.yml" {
		t.Errorf("violations = %+v", v)
	}
}

// The archives of a scan add profile slots after the diff's, so an OCI key
// in one place still pairs with a profile in the other.
func TestSecretPatternsRule_ArchiveOCIProfilesKeepDiffProfiles(t *testing.T) {
	run := &stubRunner{out: map[string]string{
		"rev-list --all": "c1\nc2\n",
		patch("log -p -U0 --no-color --no-walk=unsorted c1 c2"): "history diff",
		rawHistoryCmd: rawEntry("A", "100644", blobID(1), "app.jar"),
	}}
	archiveStub(run, map[string]string{blobID(1): makeZip(t, "oci.conf", "profile in jar")})
	r := builtinRule()
	r.findOCIProfiles = func(text string) []secrets.OCIProfile {
		return []secrets.OCIProfile{{Fingerprint: text}}
	}
	oci := &ociProfileSink{find: r.findOCIProfiles}
	if _, _, err := r.scanHistory(context.Background(), run, oci, 1); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range oci.profiles() {
		got = append(got, p.Fingerprint)
	}
	if want := []string{"history diff", "profile in jar"}; !slices.Equal(got, want) {
		t.Errorf("profiles = %q, want %q", got, want)
	}
}

// runOnly is a Runner that cannot stream.
type runOnly struct{ *stubRunner }

func (r runOnly) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	return r.stubRunner.Run(ctx, args...)
}

// Without a way to stream the blobs the archives are not read, and the scan
// fails instead of reporting them clean.
func TestSecretPatternsRule_ArchivesNeedAStreamingRunner(t *testing.T) {
	run := runOnly{&stubRunner{out: map[string]string{"rev-list --all": "c1\nc2\n", rawHistoryCmd: rawEntry("A", "100644", blobID(1), "app.jar")}}}
	if _, err := builtinRule().Evaluate(context.Background(), policy.Input{Runner: run, History: true}); err == nil {
		t.Fatal("want an error")
	}
}

// A path allowlist can skip an archive at one path, so a blob committed at
// several paths is scanned under each of them, from one read.
func TestSecretPatternsRule_ArchiveBlobIsScannedAtEachPath(t *testing.T) {
	run := &stubRunner{out: map[string]string{
		"rev-list --all": "c1\nc2\n",
		rawHistoryCmd: rawEntry("A", "100644", blobID(1), "node_modules/pkg/app.jar") +
			rawEntry("A", "100644", blobID(1), "lib/app.jar") +
			rawEntry("M", "100644", blobID(1), "lib/app.jar"),
	}}
	archiveStub(run, map[string]string{blobID(1): makeZip(t, "application.yml", "a: 1\n")})
	r := builtinRule()
	scanned := recordFiles(r)
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(*scanned)
	if want := []string{"lib/app.jar!application.yml=a: 1\n", "node_modules/pkg/app.jar!application.yml=a: 1\n"}; !slices.Equal(*scanned, want) {
		t.Errorf("scanned = %q, want %q", *scanned, want)
	}
	if run.reads[blobID(1)] != 1 {
		t.Errorf("blob read %d times, want once", run.reads[blobID(1)])
	}
	if _, errs := severities(v); errs != 2 {
		t.Errorf("errs = %d, want one finding per path", errs)
	}
}

func TestSplitRuns(t *testing.T) {
	cases := []struct {
		sizes []int64
		n     int
		want  []int
	}{
		{nil, 2, nil},
		{[]int64{5}, 2, []int{1}},
		{[]int64{1, 1, 1, 1}, 1, []int{4}},
		{[]int64{1, 1, 1, 1}, 2, []int{2, 4}},
		{[]int64{10, 20, 30, 40}, 2, []int{3, 4}},
		{[]int64{100, 1, 1}, 2, []int{1, 3}},
		{[]int64{1, 1, 100}, 2, []int{3}},
		{[]int64{0, 0, 0, 0}, 3, []int{1, 2, 4}},
		{[]int64{5, 5}, 4, []int{1, 2}},
	}
	for _, c := range cases {
		if got := splitRuns(c.sizes, c.n); !slices.Equal(got, c.want) {
			t.Errorf("splitRuns(%v, %d) = %v, want %v", c.sizes, c.n, got, c.want)
		}
	}
}

// Each reader takes a contiguous run of the listing, which keeps the versions
// of a jar that share delta bases in one git process. The findings and their
// order do not depend on the number of readers.
func TestSecretPatternsRule_ArchiveReadersSplitTheListing(t *testing.T) {
	listing := ""
	var order []string
	blobs := map[string]string{}
	for i := 1; i <= 4; i++ {
		listing += rawEntry("A", "100644", blobID(i), fmt.Sprintf("was/app-1.%d.jar", i))
		order = append(order, blobID(i))
		blobs[blobID(i)] = makeZip(t, "BOOT-INF/classes/application.yml", fmt.Sprintf("v: %d\n", i))
	}
	findings := func(readers int) ([]string, [][]string) {
		t.Helper()
		run := &stubRunner{out: map[string]string{"rev-list --all": "c1\nc2\n", rawHistoryCmd: listing}}
		archiveStub(run, blobs)
		r := builtinRule()
		recordFiles(r)
		v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true, ArchiveReaders: readers})
		if err != nil {
			t.Fatalf("readers=%d: %v", readers, err)
		}
		var files []string
		for _, x := range v[1:] {
			files = append(files, x.File)
		}
		// The readers run at once, so their calls arrive in any order.
		slices.SortFunc(run.batches, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
		return files, run.batches
	}
	one, batches := findings(1)
	if len(batches) != 1 || len(batches[0]) != 4 {
		t.Fatalf("one reader: batches = %v", batches)
	}
	for _, readers := range []int{0, 2, 3} {
		got, batches := findings(readers)
		if !slices.Equal(got, one) {
			t.Errorf("readers=%d: findings %v, want %v", readers, got, one)
		}
		want := readers
		if readers == 0 {
			want = DefaultArchiveReaders
		}
		if len(batches) != want || !slices.Equal(slices.Concat(batches...), order) {
			t.Errorf("readers=%d: batches = %v, want %d contiguous runs of the listing", readers, batches, want)
		}
	}
}

// A git process that dies in the middle of its run fails the scan, while the
// other readers finish.
func TestSecretPatternsRule_ArchiveReaderFailureFailsTheScan(t *testing.T) {
	listing := ""
	// The first reader gets blobs 1 and 2 and waits before blob 2; the
	// second gets blobs 3 and 4 and fails at blob 3.
	run := &stubRunner{out: map[string]string{"rev-list --all": "c1\nc2\n"}, dies: blobID(3), waits: blobID(2)}
	for i := 1; i <= 4; i++ {
		listing += rawEntry("A", "100644", blobID(i), fmt.Sprintf("app-%d.jar", i))
		archiveStub(run, map[string]string{blobID(i): makeZip(t, "application.yml", "a: 1\n")})
	}
	run.out[rawHistoryCmd] = listing
	r := builtinRule()
	recordFiles(r)
	_, err := r.Evaluate(context.Background(), policy.Input{Runner: run, History: true, ArchiveReaders: 2})
	if err == nil || !strings.Contains(err.Error(), "exit 128") || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the failure of the second reader", err)
	}
	if run.reads[blobID(2)] != 0 {
		t.Errorf("reads = %v, want the first reader stopped before blob 2", run.reads)
	}
}
