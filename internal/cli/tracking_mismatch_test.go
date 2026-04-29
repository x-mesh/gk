package cli

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestBaseSource_DisplayLabel(t *testing.T) {
	cases := []struct {
		src  BaseSource
		want string
	}{
		{BaseSourceUnresolved, ""},
		{BaseSourceOriginHEAD, "default"},
		{BaseSourceConfig, "configured"},
		{BaseSourceConfigEnv, "configured"},
		{BaseSourceConfigGit, "configured"},
		{BaseSourceFallback, "guessed"},
	}
	for _, tc := range cases {
		t.Run(string(tc.src), func(t *testing.T) {
			if got := tc.src.DisplayLabel(); got != tc.want {
				t.Errorf("DisplayLabel(%q): want %q, got %q", tc.src, tc.want, got)
			}
		})
	}
}

func TestBaseSource_DetailLabel(t *testing.T) {
	cases := []struct {
		src  BaseSource
		want string
	}{
		{BaseSourceUnresolved, ""},
		{BaseSourceOriginHEAD, "default (origin/HEAD)"},
		{BaseSourceConfig, "configured (.gk.yaml)"},
		{BaseSourceConfigEnv, "configured (GK_BASE_BRANCH)"},
		{BaseSourceConfigGit, "configured (git config)"},
		{BaseSourceFallback, "guessed (no remote)"},
	}
	for _, tc := range cases {
		t.Run(string(tc.src), func(t *testing.T) {
			if got := tc.src.DetailLabel(); got != tc.want {
				t.Errorf("DetailLabel(%q): want %q, got %q", tc.src, tc.want, got)
			}
		})
	}
}

// fakeBranchConfig builds a FakeRunner that responds to the consolidated
// `git config --get-regexp '^branch\.<name>\.'` call with the given
// key=value pairs. Set respKey to override which Responses entry is
// matched (used to simulate git's exit-1 on no-match).
func fakeBranchConfig(branch string, kv map[string]string) *git.FakeRunner {
	pattern := "^branch\\." + regexp.QuoteMeta(branch) + "\\."
	key := "config --get-regexp " + pattern
	var lines []string
	for k, v := range kv {
		lines = append(lines, "branch."+branch+"."+k+" "+v)
	}
	stdout := ""
	if len(lines) > 0 {
		stdout = strings.Join(lines, "\n") + "\n"
	}
	resp := git.FakeResponse{Stdout: stdout}
	if len(kv) == 0 {
		resp = git.FakeResponse{ExitCode: 1} // git's "no match" code
	}
	return &git.FakeRunner{
		Responses: map[string]git.FakeResponse{key: resp},
	}
}

func TestDetectTrackingMismatch_HappyPath(t *testing.T) {
	r := fakeBranchConfig("main", map[string]string{
		"merge":  "refs/heads/master",
		"remote": "origin",
	})
	got := detectTrackingMismatch(context.Background(), r, "main", "origin", true)
	if !got.IsSet() {
		t.Fatalf("expected mismatch, got zero: %+v", got)
	}
	if got.Branch != "main" || got.RemoteBranch != "master" || got.Remote != "origin" {
		t.Errorf("unexpected fields: %+v", got)
	}
	if len(r.Calls) != 1 {
		t.Errorf("expected exactly 1 git call (consolidated --get-regexp), got %d", len(r.Calls))
	}
}

func TestDetectTrackingMismatch_NoUpstream(t *testing.T) {
	r := &git.FakeRunner{}
	got := detectTrackingMismatch(context.Background(), r, "main", "origin", false)
	if got.IsSet() {
		t.Errorf("hasUpstream=false should yield zero, got: %+v", got)
	}
	if len(r.Calls) != 0 {
		t.Errorf("hasUpstream=false should short-circuit before any git call, got %d calls", len(r.Calls))
	}
}

func TestDetectTrackingMismatch_Suppression(t *testing.T) {
	r := fakeBranchConfig("main", map[string]string{
		"gk-tracking-ok": "true",
		"merge":          "refs/heads/master",
	})
	got := detectTrackingMismatch(context.Background(), r, "main", "origin", true)
	if got.IsSet() {
		t.Errorf("gk-tracking-ok=true should suppress, got: %+v", got)
	}
}

func TestDetectTrackingMismatch_SuppressionCaseFold(t *testing.T) {
	// Case-insensitive accept of "TRUE" / "True" via strings.EqualFold.
	for _, val := range []string{"True", "TRUE", "tRuE"} {
		t.Run(val, func(t *testing.T) {
			r := fakeBranchConfig("main", map[string]string{
				"gk-tracking-ok": val,
				"merge":          "refs/heads/master",
			})
			got := detectTrackingMismatch(context.Background(), r, "main", "origin", true)
			if got.IsSet() {
				t.Errorf("gk-tracking-ok=%q should suppress, got: %+v", val, got)
			}
		})
	}
}

func TestDetectTrackingMismatch_NameMatch(t *testing.T) {
	r := fakeBranchConfig("main", map[string]string{
		"merge": "refs/heads/main",
	})
	got := detectTrackingMismatch(context.Background(), r, "main", "origin", true)
	if got.IsSet() {
		t.Errorf("matching names should yield zero, got: %+v", got)
	}
}

func TestDetectTrackingMismatch_TagRefspec(t *testing.T) {
	r := fakeBranchConfig("main", map[string]string{
		"merge": "refs/tags/v1.0",
	})
	got := detectTrackingMismatch(context.Background(), r, "main", "origin", true)
	if got.IsSet() {
		t.Errorf("non refs/heads merge should yield zero, got: %+v", got)
	}
}

func TestDetectTrackingMismatch_RemoteOverride(t *testing.T) {
	r := fakeBranchConfig("main", map[string]string{
		"merge":  "refs/heads/master",
		"remote": "upstream",
	})
	got := detectTrackingMismatch(context.Background(), r, "main", "origin", true)
	if got.Remote != "upstream" {
		t.Errorf("Remote: want %q, got %q", "upstream", got.Remote)
	}
}

func TestDetectTrackingMismatch_BlankRemoteFallsBack(t *testing.T) {
	// branch.<name>.remote set to empty string — fall back to fallbackRemote.
	r := fakeBranchConfig("main", map[string]string{
		"merge":  "refs/heads/master",
		"remote": "",
	})
	got := detectTrackingMismatch(context.Background(), r, "main", "myremote", true)
	if got.Remote != "myremote" {
		t.Errorf("blank remote should fall back to %q, got %q", "myremote", got.Remote)
	}
}

func TestDetectTrackingMismatch_ConfigError(t *testing.T) {
	// --get-regexp returns exit 1 when nothing matches → fail open to zero.
	r := fakeBranchConfig("main", nil)
	got := detectTrackingMismatch(context.Background(), r, "main", "origin", true)
	if got.IsSet() {
		t.Errorf("no config keys should yield zero, got: %+v", got)
	}
}

func TestRenderTrackingMismatchFooter_Format(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	out := renderTrackingMismatchFooter(TrackingMismatch{
		Branch:       "main",
		RemoteBranch: "master",
		Remote:       "origin",
	})
	for _, want := range []string{
		"tracking mismatch",
		"'main'",
		"'origin/master'",
		"git branch --set-upstream-to=origin/main main",
		"git push -u origin main",
		"branch.main.gk-tracking-ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q, got:\n%s", want, out)
		}
	}
}

func TestRenderTrackingMismatchFooter_NotSet(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if out := renderTrackingMismatchFooter(TrackingMismatch{}); out != "" {
		t.Errorf("zero TrackingMismatch should produce empty footer, got: %q", out)
	}
	if out := renderTrackingMismatchFooter(TrackingMismatch{Branch: "main", RemoteBranch: "main"}); out != "" {
		t.Errorf("matching names should produce empty footer, got: %q", out)
	}
}

// Integration: runStatus on a repo where local 'main' has its merge
// refspec pointing at refs/heads/master should show the warning.
// We don't actually need a remote here — the detection is purely
// config-driven, which is exactly the situation we want to flag.

func TestRunStatus_TrackingMismatchFooter(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "")
	repo := testutil.NewRepo(t)
	// Register a remote (URL is irrelevant — we never fetch) so git
	// recognises `origin` as a real remote, then synthesize the cached
	// remote-tracking ref and the tracking config by hand.
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("update-ref", "refs/remotes/origin/master", "HEAD")
	repo.RunGit("config", "branch.main.remote", "origin")
	repo.RunGit("config", "branch.main.merge", "refs/heads/master")

	prevRepo := flagRepo
	cmd, buf := newStatusCmd(t, repo.Dir)
	t.Cleanup(func() { flagRepo = prevRepo })
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "tracking mismatch") {
		t.Errorf("expected tracking mismatch warning, got:\n%s", out)
	}
	if !strings.Contains(out, "'main'") || !strings.Contains(out, "'origin/master'") {
		t.Errorf("warning should reference both names, got:\n%s", out)
	}
}

// Integration: when both a tracking mismatch AND a base mismatch are
// present, both footers render and tracking precedes base — local
// per-branch issue before global advisory. Locks in the contract that
// status.go:1543-1547 emits these in this exact order.
func TestRunStatus_TrackingAndBaseMismatch_FooterOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	// Inject the configured base via env — config.Load discovers .gk.yaml
	// / git config from CWD, which the test runner does not relocate, so
	// gk.base-branch in the temp repo would not flow through.
	t.Setenv("GK_BASE_BRANCH", "develop")
	repo := testutil.NewRepo(t)
	repo.RunGit("remote", "add", "origin", repo.Dir)
	// Tracking mismatch: local main -> origin/master
	repo.RunGit("update-ref", "refs/remotes/origin/master", "HEAD")
	repo.RunGit("config", "branch.main.remote", "origin")
	repo.RunGit("config", "branch.main.merge", "refs/heads/master")
	// Base mismatch: cfg=develop (env), origin/HEAD=main
	repo.RunGit("update-ref", "refs/remotes/origin/main", "HEAD")
	repo.RunGit("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	prevRepo := flagRepo
	cmd, buf := newStatusCmd(t, repo.Dir)
	t.Cleanup(func() { flagRepo = prevRepo })
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()
	trackIdx := strings.Index(out, "tracking mismatch")
	baseIdx := strings.Index(out, "⚠ base ")
	if trackIdx < 0 {
		t.Fatalf("expected tracking mismatch footer, got:\n%s", out)
	}
	if baseIdx < 0 {
		t.Fatalf("expected base mismatch footer, got:\n%s", out)
	}
	if trackIdx >= baseIdx {
		t.Errorf("tracking mismatch must precede base mismatch (tracking idx=%d, base idx=%d), got:\n%s",
			trackIdx, baseIdx, out)
	}
}

// Integration: gk-tracking-ok=true must suppress ONLY the tracking
// footer; an unrelated base mismatch is independently surfaced. Guards
// the independence contract between the two suppression paths.
func TestRunStatus_TrackingSuppressedDoesNotSuppressBase(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	// See TestRunStatus_TrackingAndBaseMismatch_FooterOrder for why we
	// inject GK_BASE_BRANCH instead of writing gk.base-branch git config.
	t.Setenv("GK_BASE_BRANCH", "develop")
	repo := testutil.NewRepo(t)
	repo.RunGit("remote", "add", "origin", repo.Dir)
	// Tracking mismatch (will be suppressed)
	repo.RunGit("update-ref", "refs/remotes/origin/master", "HEAD")
	repo.RunGit("config", "branch.main.remote", "origin")
	repo.RunGit("config", "branch.main.merge", "refs/heads/master")
	repo.RunGit("config", "branch.main.gk-tracking-ok", "true")
	// Base mismatch (independent — should still surface)
	repo.RunGit("update-ref", "refs/remotes/origin/main", "HEAD")
	repo.RunGit("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	prevRepo := flagRepo
	cmd, buf := newStatusCmd(t, repo.Dir)
	t.Cleanup(func() { flagRepo = prevRepo })
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "tracking mismatch") {
		t.Errorf("tracking should be suppressed by gk-tracking-ok=true, got:\n%s", out)
	}
	if !strings.Contains(out, "⚠ base ") {
		t.Errorf("base mismatch should still surface independently, got:\n%s", out)
	}
}

func TestRunStatus_TrackingMismatchSuppressed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "")
	repo := testutil.NewRepo(t)
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("update-ref", "refs/remotes/origin/master", "HEAD")
	repo.RunGit("config", "branch.main.remote", "origin")
	repo.RunGit("config", "branch.main.merge", "refs/heads/master")
	repo.RunGit("config", "branch.main.gk-tracking-ok", "true")

	prevRepo := flagRepo
	cmd, buf := newStatusCmd(t, repo.Dir)
	t.Cleanup(func() { flagRepo = prevRepo })
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "tracking mismatch") {
		t.Errorf("gk-tracking-ok=true should suppress warning, got:\n%s", out)
	}
}
