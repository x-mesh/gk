package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestResolveBaseForStatus_ConfigBaseBranchWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "")
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cfg := &config.Config{BaseBranch: "develop"}

	res := resolveBaseForStatus(context.Background(), runner, client, cfg)
	if res.Resolved != "develop" {
		t.Errorf("Resolved: want %q, got %q", "develop", res.Resolved)
	}
	if res.Source != BaseSourceConfig {
		t.Errorf("Source: want %q (no env/git matches), got %q", BaseSourceConfig, res.Source)
	}
	if res.ConfigMerged != "develop" {
		t.Errorf("ConfigMerged: want %q, got %q", "develop", res.ConfigMerged)
	}
}

func TestResolveBaseForStatus_EnvSourceAttribution(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "develop")
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cfg := &config.Config{BaseBranch: "develop"}

	res := resolveBaseForStatus(context.Background(), runner, client, cfg)
	if res.Source != BaseSourceConfigEnv {
		t.Errorf("Source: want %q, got %q", BaseSourceConfigEnv, res.Source)
	}
	if res.ConfigEnv != "develop" {
		t.Errorf("ConfigEnv: want %q, got %q", "develop", res.ConfigEnv)
	}
}

func TestResolveBaseForStatus_GitConfigSourceAttribution(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "")
	repo := testutil.NewRepo(t)
	repo.RunGit("config", "gk.base-branch", "develop")
	t.Chdir(repo.Dir)

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cfg := &config.Config{BaseBranch: "develop"}

	res := resolveBaseForStatus(context.Background(), runner, client, cfg)
	if res.Source != BaseSourceConfigGit {
		t.Errorf("Source: want %q, got %q", BaseSourceConfigGit, res.Source)
	}
	if res.ConfigGit != "develop" {
		t.Errorf("ConfigGit: want %q, got %q", "develop", res.ConfigGit)
	}
}

func TestResolveBaseForStatus_FallbackToLocalMain(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "")
	repo := testutil.NewRepo(t)
	// New repo defaults to main as the only branch; no remote.
	t.Chdir(repo.Dir)

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cfg := &config.Config{}

	res := resolveBaseForStatus(context.Background(), runner, client, cfg)
	if res.Source != BaseSourceFallback {
		t.Errorf("Source: want %q, got %q", BaseSourceFallback, res.Source)
	}
	if res.FallbackUsed == "" {
		t.Errorf("FallbackUsed: should be set when source=fallback, got empty")
	}
}

func TestBaseResolution_Mismatch(t *testing.T) {
	cases := []struct {
		name string
		res  BaseResolution
		want bool
	}{
		{"both empty", BaseResolution{}, false},
		{"config only", BaseResolution{ConfigMerged: "develop"}, false},
		{"origin only", BaseResolution{OriginHEAD: "main"}, false},
		{"matching", BaseResolution{ConfigMerged: "main", OriginHEAD: "main"}, false},
		{"diverging", BaseResolution{ConfigMerged: "develop", OriginHEAD: "main"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.Mismatch(); got != tc.want {
				t.Errorf("Mismatch(): want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestBaseResolution_OriginHEADUnset(t *testing.T) {
	if (BaseResolution{}).OriginHEADUnset() != true {
		t.Error("empty OriginHEAD should report unset=true")
	}
	if (BaseResolution{OriginHEAD: "main"}).OriginHEADUnset() != false {
		t.Error("set OriginHEAD should report unset=false")
	}
}

func TestRenderBaseMismatchFooter(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	in := BaseResolution{
		ConfigMerged: "develop",
		Source:       BaseSourceConfig,
		OriginHEAD:   "main",
		Remote:       "origin",
	}
	got := renderBaseMismatchFooter(in)
	if !strings.Contains(got, "develop") || !strings.Contains(got, "main") {
		t.Errorf("footer should reference both branches, got: %q", got)
	}
	// Humanized DisplayLabel must appear so wording drift is caught.
	if !strings.Contains(got, "(configured)") {
		t.Errorf("footer should include humanized %q label, got: %q", "(configured)", got)
	}
	if !strings.Contains(got, "git remote set-head origin -a") {
		t.Errorf("footer should suggest set-head, got: %q", got)
	}

	// No mismatch → empty string.
	clean := BaseResolution{ConfigMerged: "main", OriginHEAD: "main", Remote: "origin"}
	if out := renderBaseMismatchFooter(clean); out != "" {
		t.Errorf("matching state should produce empty footer, got: %q", out)
	}
}

// Subtest variants verify each Source kind renders the correct label.
// Catches drift if DisplayLabel() is changed without updating the
// footer's wording contract.
func TestRenderBaseMismatchFooter_LabelByCfgSource(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cases := []struct {
		src       BaseSource
		wantLabel string
	}{
		{BaseSourceConfig, "(configured)"},
		{BaseSourceConfigEnv, "(configured)"},
		{BaseSourceConfigGit, "(configured)"},
		// origin/HEAD never triggers Mismatch() because Mismatch
		// requires ConfigMerged != "" and origin/HEAD source implies
		// ConfigMerged == "" — covered separately by the fallback test.
	}
	for _, tc := range cases {
		t.Run(string(tc.src), func(t *testing.T) {
			in := BaseResolution{
				ConfigMerged: "develop",
				Source:       tc.src,
				OriginHEAD:   "main",
				Remote:       "origin",
			}
			got := renderBaseMismatchFooter(in)
			if !strings.Contains(got, tc.wantLabel) {
				t.Errorf("source=%q: want label %q in footer, got: %q", tc.src, tc.wantLabel, got)
			}
		})
	}
}

// FallbackLabel tests the defensive `if label == "" { label = "configured" }`
// branch in renderBaseMismatchFooter. This is reachable when a future
// BaseSource constant is added without a matching DisplayLabel() entry,
// or when a caller constructs BaseResolution{} directly with Source
// left at zero value but ConfigMerged populated.
func TestRenderBaseMismatchFooter_FallbackLabel(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	in := BaseResolution{
		ConfigMerged: "develop",
		Source:       BaseSourceUnresolved, // DisplayLabel returns "" — exercises fallback
		OriginHEAD:   "main",
		Remote:       "origin",
	}
	got := renderBaseMismatchFooter(in)
	if !strings.Contains(got, "(configured)") {
		t.Errorf("fallback should default to %q label, got: %q", "(configured)", got)
	}
}

func TestRenderBaseVerboseLine_MismatchTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	in := BaseResolution{
		Resolved:     "develop",
		Source:       BaseSourceConfig,
		ConfigMerged: "develop",
		OriginHEAD:   "main",
		Remote:       "origin",
	}
	got := renderBaseVerboseLine(in)
	if !strings.Contains(got, "[base]") {
		t.Errorf("verbose line should be tagged [base], got: %q", got)
	}
	for _, k := range []string{"resolved=develop", "source=config", "origin/HEAD=main", "cfg=develop"} {
		if !strings.Contains(got, k) {
			t.Errorf("verbose line missing %q, got: %q", k, got)
		}
	}
	if !strings.Contains(got, "mismatch") {
		t.Errorf("mismatch tail expected, got: %q", got)
	}
}

func TestRenderBaseVerboseLine_OriginUnsetTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	in := BaseResolution{
		Resolved:     "develop",
		Source:       BaseSourceConfig,
		ConfigMerged: "develop",
		Remote:       "origin",
	}
	got := renderBaseVerboseLine(in)
	if !strings.Contains(got, "origin/HEAD unset") {
		t.Errorf("expected origin/HEAD unset tail, got: %q", got)
	}
}

func TestRenderExplainBase_StructureAndPickedRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	in := BaseResolution{
		Resolved:     "develop",
		Source:       BaseSourceConfigGit,
		ConfigGit:    "develop",
		ConfigMerged: "develop",
		OriginHEAD:   "main",
		Remote:       "origin",
	}
	got := renderExplainBase(in)
	for _, want := range []string{
		"base resolution:",
		"git config gk.base-branch",
		"origin/HEAD (cached)",
		"resolved: develop",
		"action hint:",
		"git remote set-head origin -a",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("explain block missing %q, got:\n%s", want, got)
		}
	}
}

func TestRenderExplainBase_LiveOriginRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	in := BaseResolution{
		Resolved:     "develop",
		Source:       BaseSourceConfig,
		ConfigMerged: "develop",
		OriginHEAD:   "main",
		OriginLive:   "main",
		Remote:       "origin",
	}
	got := renderExplainBase(in)
	if !strings.Contains(got, "origin/HEAD (live)") {
		t.Errorf("explain block should mention live origin, got:\n%s", got)
	}
	if strings.Contains(got, "skipped — pass --fetch-default") {
		t.Errorf("with OriginLive set, skipped hint should be absent, got:\n%s", got)
	}
}

func TestRenderExplainBase_NoMismatchNoHint(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	in := BaseResolution{
		Resolved:     "main",
		Source:       BaseSourceOriginHEAD,
		ConfigMerged: "",
		OriginHEAD:   "main",
		Remote:       "origin",
	}
	got := renderExplainBase(in)
	if strings.Contains(got, "action hint:") {
		t.Errorf("no mismatch / no unset → no action hint, got:\n%s", got)
	}
}

// fetchOriginLiveDefault tests — pure parsing logic via FakeRunner so we
// never spawn git or hit the network. Covers the symref happy path, the
// no-symref edge case, runner failures, and the empty-remote default.

func TestFetchOriginLiveDefault_HappyPath(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"ls-remote --symref origin HEAD": {
				Stdout: "ref: refs/heads/main\tHEAD\nabc123def\tHEAD\n",
			},
		},
	}
	got := fetchOriginLiveDefault(context.Background(), r, "origin")
	if got != "main" {
		t.Errorf("want %q, got %q", "main", got)
	}
}

func TestFetchOriginLiveDefault_NoSymref(t *testing.T) {
	// Server with no symbolic HEAD — only the SHA line. Should return "".
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"ls-remote --symref origin HEAD": {
				Stdout: "abc123def\tHEAD\n",
			},
		},
	}
	if got := fetchOriginLiveDefault(context.Background(), r, "origin"); got != "" {
		t.Errorf("no symref should yield empty, got %q", got)
	}
}

func TestFetchOriginLiveDefault_RunnerError(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"ls-remote --symref origin HEAD": {Stderr: "fatal: offline", ExitCode: 128},
		},
	}
	if got := fetchOriginLiveDefault(context.Background(), r, "origin"); got != "" {
		t.Errorf("runner error should yield empty, got %q", got)
	}
}

func TestFetchOriginLiveDefault_EmptyRemoteDefaultsToOrigin(t *testing.T) {
	// Empty remote arg should fall back to "origin" — verify by responding
	// only on that exact key.
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"ls-remote --symref origin HEAD": {
				Stdout: "ref: refs/heads/develop\tHEAD\n",
			},
		},
	}
	if got := fetchOriginLiveDefault(context.Background(), r, ""); got != "develop" {
		t.Errorf("want %q, got %q", "develop", got)
	}
}

func TestFetchOriginLiveDefault_AltRemote(t *testing.T) {
	// Custom remote name flows through to the args.
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"ls-remote --symref upstream HEAD": {
				Stdout: "ref: refs/heads/trunk\tHEAD\n",
			},
		},
	}
	if got := fetchOriginLiveDefault(context.Background(), r, "upstream"); got != "trunk" {
		t.Errorf("want %q, got %q", "trunk", got)
	}
}

// pickConfigSource — pure unit tests covering all attribution outcomes.
// The integration tests above only exercise one path each; this batches
// the priority logic into a single deterministic table.

func TestPickConfigSource_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   BaseResolution
		want BaseSource
	}{
		{"empty merged", BaseResolution{}, BaseSourceUnresolved},
		{"env match", BaseResolution{ConfigEnv: "x", ConfigMerged: "x"}, BaseSourceConfigEnv},
		{"git config match", BaseResolution{ConfigGit: "x", ConfigMerged: "x"}, BaseSourceConfigGit},
		{"env beats git config when both match", BaseResolution{ConfigEnv: "x", ConfigGit: "x", ConfigMerged: "x"}, BaseSourceConfigEnv},
		{"neither matches → generic config", BaseResolution{ConfigEnv: "y", ConfigGit: "z", ConfigMerged: "x"}, BaseSourceConfig},
		{"empty env ignored", BaseResolution{ConfigEnv: "", ConfigGit: "x", ConfigMerged: "x"}, BaseSourceConfigGit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickConfigSource(tc.in); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// OriginHEAD field is populated even when config wins — required for
// Mismatch() detection. Sets up a synthetic refs/remotes/origin/HEAD
// without an actual remote (faster, deterministic).

func TestResolveBaseForStatus_OriginHEADCapturedWhenConfigWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "")
	repo := testutil.NewRepo(t)
	// Synthesize cached origin/HEAD pointing at main without an actual
	// remote being reachable. This is exactly what `git remote set-head`
	// produces.
	repo.RunGit("update-ref", "refs/remotes/origin/main", "HEAD")
	repo.RunGit("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	t.Chdir(repo.Dir)

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cfg := &config.Config{BaseBranch: "develop"}

	res := resolveBaseForStatus(context.Background(), runner, client, cfg)
	if res.Resolved != "develop" {
		t.Errorf("Resolved: want %q, got %q", "develop", res.Resolved)
	}
	if res.Source != BaseSourceConfig {
		t.Errorf("Source: want %q, got %q", BaseSourceConfig, res.Source)
	}
	if res.OriginHEAD != "main" {
		t.Errorf("OriginHEAD: want %q, got %q (must be populated even when config wins)", "main", res.OriginHEAD)
	}
	if !res.Mismatch() {
		t.Error("Mismatch() should be true when config=develop and origin/HEAD=main")
	}
}

// Render functions must not crash on a zero-value BaseResolution. The
// runStatusOnce hoist passes a zero value for detached HEAD / no-branch
// repos, so this contract matters at runtime.

func TestRenderFunctions_ZeroValueSafety(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var zero BaseResolution
	if got := renderBaseMismatchFooter(zero); got != "" {
		t.Errorf("zero footer should be empty (no Mismatch), got: %q", got)
	}
	got := renderBaseVerboseLine(zero)
	if got == "" {
		t.Error("zero verbose line should still produce a string with (unset) markers, got empty")
	}
	if !strings.Contains(got, "[base]") {
		t.Errorf("verbose line missing [base] tag, got: %q", got)
	}
	expl := renderExplainBase(zero)
	if !strings.Contains(expl, "base resolution:") {
		t.Errorf("zero explain should still include header, got: %q", expl)
	}
}

// runStatus integration: --explain-base flag flips the package-level var
// and the block actually appears in output. Bypasses cobra flag parsing
// since runStatus reads statusExplainBase directly.

func TestRunStatus_ExplainBaseFlagWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "")
	repo := testutil.NewRepo(t)

	prevRepo := flagRepo
	cmd, buf := newStatusCmd(t, repo.Dir)
	statusExplainBase = true
	t.Cleanup(func() {
		statusExplainBase = false
		flagRepo = prevRepo
	})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"base resolution:", "→ resolved:", "(live)"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in --explain-base output, got:\n%s", want, out)
		}
	}
	// --fetch-default was not set, so the live row should be marked skipped.
	if !strings.Contains(out, "skipped — pass --fetch-default") {
		t.Errorf("live row should advertise --fetch-default, got:\n%s", out)
	}
}

// Detached HEAD must suppress the mismatch footer + explain block so
// users in detached state don't see spurious base warnings.

func TestRunStatus_DetachedHEADSuppressesBaseFooter(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("GK_BASE_BRANCH", "")
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a\n")
	repo.RunGit("add", "a.txt")
	repo.RunGit("commit", "-m", "a")
	// Synthesize an origin/HEAD that disagrees with a config we'll pin —
	// this would normally trigger the mismatch footer on a branch.
	repo.RunGit("update-ref", "refs/remotes/origin/develop", "HEAD")
	repo.RunGit("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	repo.RunGit("config", "gk.base-branch", "main")
	// Detach.
	repo.RunGit("checkout", "--detach", "HEAD")

	prevRepo := flagRepo
	cmd, buf := newStatusCmd(t, repo.Dir)
	statusExplainBase = true
	t.Cleanup(func() {
		statusExplainBase = false
		flagRepo = prevRepo
	})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "⚠ base") {
		t.Errorf("detached HEAD should not surface mismatch footer, got:\n%s", out)
	}
	if strings.Contains(out, "base resolution:") {
		t.Errorf("detached HEAD should not surface --explain-base block, got:\n%s", out)
	}
}
