package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func TestBuildShipPlanInfersMinorFromFeat(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "feat: add ship\n\x1e"},
		"rev-parse --verify refs/tags/v1.3.0": {ExitCode: 1, Stderr: "not found"},
	}}

	plan, err := buildShipPlan(context.Background(), runner, testShipConfig(), shipFlags{})
	if err != nil {
		t.Fatalf("buildShipPlan: %v", err)
	}
	if plan.NextTag != "v1.3.0" {
		t.Fatalf("NextTag = %q, want v1.3.0", plan.NextTag)
	}
	if plan.Bump != "minor" {
		t.Fatalf("Bump = %q, want minor", plan.Bump)
	}
}

func TestRunShipCoreDryRunDoesNotTagOrPush(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "fix: patch bug\n\x1e"},
		"rev-parse --verify refs/tags/v1.2.4": {ExitCode: 1, Stderr: "not found"},
	}}
	var out bytes.Buffer

	err := runShipCore(context.Background(), shipDeps{
		Runner: runner,
		Config: testShipConfig(),
		Out:    &out,
	}, shipFlags{dryRun: true, skipPreflight: true})
	if err != nil {
		t.Fatalf("runShipCore: %v", err)
	}
	// renderShipPlan formats labels with `%s   %s` so the spacing between
	// "Next tag:" and the value can shift when label widths change. Use a
	// loose contains check that doesn't break on column alignment edits.
	got := out.String()
	if !strings.Contains(got, "Next tag:") || !strings.Contains(got, "v1.2.4") {
		t.Fatalf("expected next tag in output, got:\n%s", got)
	}
	for _, call := range runner.Calls {
		if len(call.Args) > 0 && (call.Args[0] == "tag" || call.Args[0] == "push") {
			t.Fatalf("dry-run unexpectedly called git %s", strings.Join(call.Args, " "))
		}
	}
}

func TestRunShipCoreTagsAndPushes(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir+"/CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- Patch bug.\n\n## [1.2.3] - 2026-04-01\n")
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                      {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":             {Stdout: "main\n"},
		"rev-parse --show-toplevel":               {Stdout: dir + "\n"},
		"describe --tags --abbrev=0":              {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":        {Stdout: "fix: patch bug\n\x1e"},
		"rev-parse --verify refs/tags/v1.2.4":     {ExitCode: 1, Stderr: "not found"},
		"add -A":                                  {Stdout: ""},
		"commit -m release: v1.2.4":               {Stdout: "[main abc123] release\n"},
		"tag -a v1.2.4 -m Release v1.2.4":         {Stdout: ""},
		"rev-parse --verify origin/main^{commit}": {Stdout: "abc123\n"},
		"log -p --no-color origin/main..HEAD":     {Stdout: ""},
		"push origin main":                        {Stdout: "branch pushed\n"},
		"push origin v1.2.4":                      {Stdout: "tag pushed\n"},
	}}
	var out bytes.Buffer

	err := runShipCore(context.Background(), shipDeps{
		Runner: runner,
		Config: testShipConfig(),
		Out:    &out,
	}, shipFlags{yes: true, skipPreflight: true, push: true})
	if err != nil {
		t.Fatalf("runShipCore: %v", err)
	}

	got := joinedShipCalls(runner.Calls)
	for _, want := range []string{
		"add -A",
		"commit -m release: v1.2.4",
		"tag -a v1.2.4 -m Release v1.2.4",
		"push origin main",
		"push origin v1.2.4",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing call %q in:\n%s", want, got)
		}
	}
}

func TestRunShipCoreBumpsVersionAndPromotesChangelog(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir+"/VERSION", "1.2.3\n")
	writeTestFile(t, dir+"/CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- New command.\n\n## [1.2.3] - 2026-04-01\n")

	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"rev-parse --show-toplevel":           {Stdout: dir + "\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "feat: add ship\n\x1e"},
		"rev-parse --verify refs/tags/v1.3.0": {ExitCode: 1, Stderr: "not found"},
		"add -A":                              {Stdout: ""},
		"commit -m release: v1.3.0":           {Stdout: "[main abc123] release\n"},
		"tag -a v1.3.0 -m Release v1.3.0":     {Stdout: ""},
	}}

	err := runShipCore(context.Background(), shipDeps{
		Runner: runner,
		Config: testShipConfig(),
		Out:    &bytes.Buffer{},
	}, shipFlags{yes: true, skipPreflight: true, push: false})
	if err != nil {
		t.Fatalf("runShipCore: %v", err)
	}

	if got := readTestFile(t, dir+"/VERSION"); got != "1.3.0\n" {
		t.Fatalf("VERSION = %q, want 1.3.0", got)
	}
	changelog := readTestFile(t, dir+"/CHANGELOG.md")
	if !strings.Contains(changelog, "## [1.3.0] - ") {
		t.Fatalf("changelog was not promoted:\n%s", changelog)
	}
	if !strings.Contains(changelog, "- New command.") {
		t.Fatalf("changelog lost unreleased notes:\n%s", changelog)
	}
}

// shipLiveHarness builds a temp repo + FakeRunner that drives a full local
// `ship -y` (minor bump v1.2.3 → v1.3.0, no push). Shared by the agent-mode
// --json routing tests below.
func shipLiveHarness(t *testing.T) (string, *git.FakeRunner) {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, dir+"/VERSION", "1.2.3\n")
	writeTestFile(t, dir+"/CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- New command.\n\n## [1.2.3] - 2026-04-01\n")
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"rev-parse --show-toplevel":           {Stdout: dir + "\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "feat: add ship\n\x1e"},
		"rev-parse --verify refs/tags/v1.3.0": {ExitCode: 1, Stderr: "not found"},
		"add -A":                              {Stdout: ""},
		"commit -m release: v1.3.0":           {Stdout: "[main abc123] release\n"},
		"tag -a v1.3.0 -m Release v1.3.0":     {Stdout: ""},
	}}
	return dir, runner
}

// TestRunShipCore_ImpliedJSONStreamsToStderrAndEmitsEnvelope locks the fix for
// `GK_AGENT=1 gk ship -y`: an --json that was IMPLIED by agent mode (not typed)
// must not be refused on a live run. The human pipeline streams to stderr so
// stdout stays a clean result envelope.
func TestRunShipCore_ImpliedJSONStreamsToStderrAndEmitsEnvelope(t *testing.T) {
	withAgentMode(t, true) // GK_AGENT → AgentOut() true so the result wraps in an envelope
	_, runner := shipLiveHarness(t)

	var out, errOut bytes.Buffer
	err := runShipCore(context.Background(), shipDeps{
		Runner: runner, Config: testShipConfig(), Out: &out, ErrOut: &errOut,
	}, shipFlags{yes: true, skipPreflight: true, push: false, jsonOut: true, jsonExplicit: false})
	if err != nil {
		t.Fatalf("implied --json on a live run must not error: %v", err)
	}

	// stdout: a clean envelope, no human banner. A successful json.Unmarshal
	// of the WHOLE buffer is the strong proof — any leaked banner text would
	// make it fail on leading/trailing non-JSON.
	so := out.String()
	if strings.Contains(so, "Ship complete") {
		t.Errorf("stdout must stay clean for the envelope, leaked banner:\n%s", so)
	}
	var env struct {
		OK     bool           `json:"ok"`
		Result shipResultJSON `json:"result"`
	}
	if jerr := json.Unmarshal([]byte(so), &env); jerr != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", jerr, so)
	}
	if !env.OK || env.Result.Tag != "v1.3.0" {
		t.Errorf("envelope = %+v, want ok=true tag=v1.3.0", env)
	}

	// stderr: the human progress (banner) was streamed here.
	if se := errOut.String(); !strings.Contains(se, "shipped") || !strings.Contains(se, "v1.3.0") {
		t.Errorf("stderr must carry the human pipeline, got:\n%s", se)
	}
}

func TestRunShipCoreSquashModeUsesSoftResetAndCommit(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                                                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":                                         {Stdout: "feature/ship\n"},
		"rev-parse --show-toplevel":                                           {Stdout: t.TempDir() + "\n"},
		"describe --tags --abbrev=0":                                          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":                                    {Stdout: "fix: a\n\x1efeat: b\n\x1e"},
		"rev-parse --verify refs/tags/v1.3.0":                                 {ExitCode: 1, Stderr: "not found"},
		"rev-parse --abbrev-ref --symbolic-full-name feature/ship@{upstream}": {ExitCode: 1, Stderr: "no upstream"},
		"rev-parse HEAD":                                                      {Stdout: "head123\n"},
		"reset --soft v1.2.3":                                                 {Stdout: ""},
		"commit -m feat: release changes":                                     {Stdout: "[feature/ship def456] feat\n"},
		"diff head123 HEAD":                                                   {Stdout: ""},
	}}

	err := runShipCore(context.Background(), shipDeps{
		Runner: runner,
		Config: testShipConfig(),
		Out:    &bytes.Buffer{},
	}, shipFlags{mode: shipModeSquash, allowNonBase: true, yes: true, push: false})
	if err != nil {
		t.Fatalf("runShipCore: %v", err)
	}

	got := joinedShipCalls(runner.Calls)
	for _, want := range []string{
		"reset --soft v1.2.3",
		"commit -m feat: release changes",
		"diff head123 HEAD",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing call %q in:\n%s", want, got)
		}
	}
}

func TestRunShipCoreAutoSquashesInvalidReleaseCommitMessages(t *testing.T) {
	cfg := testShipConfig()
	cfg.Preflight.Steps = []config.PreflightStep{{Name: "commit-lint", Command: "commit-lint"}}
	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                                          {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":                                 {Stdout: "main\n"},
		"rev-parse --show-toplevel":                                   {Stdout: t.TempDir() + "\n"},
		"describe --tags --abbrev=0":                                  {Stdout: "v1.2.3\n"},
		"rev-parse --abbrev-ref --symbolic-full-name main@{upstream}": {ExitCode: 1, Stderr: "no upstream"},
		"rev-parse HEAD":                                              {Stdout: "head123\n"},
		"reset --soft v1.2.3":                                         {Stdout: ""},
		"commit -m feat: release changes":                             {Stdout: "[main def456] feat\n"},
		"diff head123 HEAD":                                           {Stdout: ""},
	}}
	runner := &sequenceRunner{
		FakeRunner: fake,
		sequence: map[string][]git.FakeResponse{
			"log --format=%B%x1e v1.2.3..HEAD": {
				{Stdout: "feat: valid\n\x1eWIP checkpoint\n\x1e"},
				{Stdout: "feat: release changes\n\x1e"},
			},
			"log --format=%H%x00%B%x1e v1.2.3..HEAD": {
				{Stdout: "bad123456789\x00WIP checkpoint\n\x1e"},
				{Stdout: "good12345678\x00feat: release changes\n\x1e"},
			},
		},
	}
	var out bytes.Buffer

	err := runShipCore(context.Background(), shipDeps{
		Runner: runner,
		Config: cfg,
		Out:    &out,
	}, shipFlags{yes: true, noRelease: true, push: false})
	if err != nil {
		t.Fatalf("runShipCore: %v\noutput:\n%s", err, out.String())
	}
	got := joinedShipCalls(runner.Calls)
	for _, want := range []string{
		"reset --soft v1.2.3",
		"commit -m feat: release changes",
		"log --format=%H%x00%B%x1e v1.2.3..HEAD",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing call %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(out.String(), "cleaned release commits") {
		t.Fatalf("expected cleanup output, got:\n%s", out.String())
	}
}

func TestRunShipCoreStatusModeDoesNotMutate(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"rev-parse --show-toplevel":           {Stdout: t.TempDir() + "\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "fix: patch bug\n\x1e"},
		"rev-parse --verify refs/tags/v1.2.4": {ExitCode: 1, Stderr: "not found"},
	}}
	var out bytes.Buffer

	err := runShipCore(context.Background(), shipDeps{
		Runner: runner,
		Config: testShipConfig(),
		Out:    &out,
	}, shipFlags{mode: shipModeStatus})
	if err != nil {
		t.Fatalf("runShipCore: %v", err)
	}
	if !strings.Contains(out.String(), "Ship status") {
		t.Fatalf("expected status output, got:\n%s", out.String())
	}
	for _, call := range runner.Calls {
		if len(call.Args) > 0 && (call.Args[0] == "commit" || call.Args[0] == "tag" || call.Args[0] == "push") {
			t.Fatalf("status unexpectedly called git %s", strings.Join(call.Args, " "))
		}
	}
}

func TestBuildShipPlanRejectsDirtyTree(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain": {Stdout: " M file.go\n"},
	}}

	_, err := buildShipPlan(context.Background(), runner, testShipConfig(), shipFlags{})
	if err == nil {
		t.Fatal("expected dirty tree error")
	}
	if !strings.Contains(err.Error(), "working tree is dirty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShipVersionHelpers(t *testing.T) {
	next, err := bumpShipVersion("v1.2.3", "major")
	if err != nil {
		t.Fatalf("bumpShipVersion: %v", err)
	}
	if next != "v2.0.0" {
		t.Fatalf("major bump = %q, want v2.0.0", next)
	}
	if got := normalizeShipVersion("1.2.3"); got != "v1.2.3" {
		t.Fatalf("normalize = %q, want v1.2.3", got)
	}
	if inferShipBump([]string{"fix!: change api"}) != "major" {
		t.Fatal("expected bang commit to infer major")
	}
}

func testShipConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Remote = "origin"
	cfg.BaseBranch = "main"
	cfg.Preflight.Steps = nil
	return &cfg
}

func joinedShipCalls(calls []git.FakeCall) string {
	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		lines = append(lines, strings.Join(call.Args, " "))
	}
	return strings.Join(lines, "\n")
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return string(b)
}

func TestBuildShipPlan_FastForwardsBaseFromNonBaseBranch(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                    {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":           {Stdout: "develop\n"},
		"merge-base --is-ancestor main develop": {}, // exit 0 → FF possible
		"describe --tags --abbrev=0":            {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":      {Stdout: "feat: add x\n\x1e"},
		"rev-parse --verify refs/tags/v1.3.0":   {ExitCode: 1, Stderr: "not found"},
	}}
	plan, err := buildShipPlan(context.Background(), runner, testShipConfig(), shipFlags{})
	if err != nil {
		t.Fatalf("buildShipPlan: %v", err)
	}
	if !plan.MergeToBase {
		t.Error("MergeToBase = false, want true (FF possible from develop)")
	}
	if plan.Base != "main" {
		t.Errorf("Base = %q, want main", plan.Base)
	}
}

func TestBuildShipPlan_RejectsDivergedNonBaseBranch(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                    {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":           {Stdout: "develop\n"},
		"merge-base --is-ancestor main develop": {ExitCode: 1}, // not an ancestor
		"describe --tags --abbrev=0":            {Stdout: "v1.2.3\n"},
	}}
	_, err := buildShipPlan(context.Background(), runner, testShipConfig(), shipFlags{})
	if err == nil {
		t.Fatal("expected error for diverged non-base branch, got nil")
	}
	// A diverged base is a blocked precondition, not a hard failure: it must
	// report state:"blocked" with a stable code and the remedy that actually
	// clears it — `gk sync` (rebase branch onto base), not pull --with-base.
	if s := stateFrom(err); s != envStateBlocked {
		t.Errorf("stateFrom = %q, want %q", s, envStateBlocked)
	}
	if c := errorCodeFromError(err); c != "base-diverged" {
		t.Errorf("error code = %q, want base-diverged", c)
	}
	rems := RemediesFrom(err)
	if len(rems) != 1 || !strings.Contains(rems[0].Command, "sync") {
		t.Errorf("remedies = %+v, want one `…sync` command", rems)
	}
	if strings.Contains(rems[0].Command, "with-base") {
		t.Errorf("remedy must not point at pull --with-base (no-op for ship's divergence): %+v", rems)
	}
}

func TestBuildShipPlan_AllowNonBaseSkipsMerge(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "develop\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "feat: add x\n\x1e"},
		"rev-parse --verify refs/tags/v1.3.0": {ExitCode: 1, Stderr: "not found"},
	}}
	plan, err := buildShipPlan(context.Background(), runner, testShipConfig(), shipFlags{allowNonBase: true})
	if err != nil {
		t.Fatalf("buildShipPlan: %v", err)
	}
	if plan.MergeToBase {
		t.Error("MergeToBase = true, want false (--allow-non-base tags in place)")
	}
}

// ---------------------------------------------------------------------------
// 0.x bump convention + commit-derived changelog draft
// ---------------------------------------------------------------------------

func TestBuildShipPlanZeroXDowngradesBreakingToMinor(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"describe --tags --abbrev=0":          {Stdout: "v0.5.0\n"},
		"log --format=%B%x1e v0.5.0..HEAD":    {Stdout: "feat!: drop legacy flag\n\x1e"},
		"rev-parse --verify refs/tags/v0.6.0": {ExitCode: 1, Stderr: "not found"},
	}}

	plan, err := buildShipPlan(context.Background(), runner, testShipConfig(), shipFlags{noFetch: true})
	if err != nil {
		t.Fatalf("buildShipPlan: %v", err)
	}
	if plan.Bump != "minor" || !plan.BumpDowngraded {
		t.Errorf("0.x breaking: bump=%q downgraded=%v, want minor/true", plan.Bump, plan.BumpDowngraded)
	}
	if plan.NextTag != "v0.6.0" {
		t.Errorf("NextTag = %q, want v0.6.0", plan.NextTag)
	}
}

func TestBuildShipPlanPostZeroXKeepsMajor(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "feat!: breaking api change\n\x1e"},
		"rev-parse --verify refs/tags/v2.0.0": {ExitCode: 1, Stderr: "not found"},
	}}

	plan, err := buildShipPlan(context.Background(), runner, testShipConfig(), shipFlags{noFetch: true})
	if err != nil {
		t.Fatalf("buildShipPlan: %v", err)
	}
	if plan.Bump != "major" || plan.BumpDowngraded {
		t.Errorf("1.x breaking: bump=%q downgraded=%v, want major/false", plan.Bump, plan.BumpDowngraded)
	}
}

func TestBuildShipPlanExplicitBumpSkipsDowngrade(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"describe --tags --abbrev=0":          {Stdout: "v0.5.0\n"},
		"log --format=%B%x1e v0.5.0..HEAD":    {Stdout: "feat!: graduate\n\x1e"},
		"rev-parse --verify refs/tags/v1.0.0": {ExitCode: 1, Stderr: "not found"},
	}}

	plan, err := buildShipPlan(context.Background(), runner, testShipConfig(), shipFlags{noFetch: true, bump: "major"})
	if err != nil {
		t.Fatalf("buildShipPlan: %v", err)
	}
	if plan.Bump != "major" || plan.BumpDowngraded {
		t.Errorf("explicit --major: bump=%q downgraded=%v, want major/false", plan.Bump, plan.BumpDowngraded)
	}
	if plan.NextTag != "v1.0.0" {
		t.Errorf("NextTag = %q, want v1.0.0", plan.NextTag)
	}
}

func TestDraftShipChangelog(t *testing.T) {
	commits := []string{
		"feat(pull): add --with-base sync\n\nbody here",
		"fix: nil config panic",
		"refactor!: rework hint pipeline",
		"chore(release): v0.76.0",
		"docs: update readme",
		"perf(log): cache graph lanes",
	}
	got := draftShipChangelog(commits)

	want := "### Added\n\n" +
		"- **pull:** add --with-base sync\n" +
		"\n### Changed\n\n" +
		"- rework hint pipeline (breaking)\n" +
		"- **log:** cache graph lanes\n" +
		"\n### Fixed\n\n" +
		"- nil config panic\n"
	if got != want {
		t.Errorf("draftShipChangelog:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestDraftShipChangelogUnmappedOnly(t *testing.T) {
	if got := draftShipChangelog([]string{"chore: tidy", "docs: notes", "WIP stuff"}); got != "" {
		t.Errorf("unmapped-only commits must yield empty draft, got %q", got)
	}
}

func shipCfgWithPreflight(steps ...config.PreflightStep) *config.Config {
	c := testShipConfig()
	c.Preflight.Steps = steps
	return c
}

// TestRunShipCorePreflightOnly covers `gk ship --preflight`: it runs the
// configured steps (shell `true`/`false`) without building a plan (no clean-tree
// gate — FakeRunner has no `status` mapping), runs EVERY step (not fail-fast),
// and never tags/pushes.
func TestRunShipCorePreflightOnly(t *testing.T) {
	// All pass → ✓, no error, no git mutation.
	runner := &git.FakeRunner{}
	var out bytes.Buffer
	err := runShipCore(context.Background(), shipDeps{
		Runner: runner, Config: shipCfgWithPreflight(
			config.PreflightStep{Name: "a", Command: "true"},
			config.PreflightStep{Name: "b", Command: "true"},
		), Out: &out,
	}, shipFlags{mode: shipModePreflight})
	if err != nil {
		t.Fatalf("all-pass: unexpected error %v", err)
	}
	if !strings.Contains(out.String(), "preflight passed") {
		t.Errorf("all-pass output: %q", out.String())
	}
	for _, call := range runner.Calls {
		if len(call.Args) > 0 && (call.Args[0] == "tag" || call.Args[0] == "push" || call.Args[0] == "commit") {
			t.Fatalf("--preflight must never mutate, but ran git %s", strings.Join(call.Args, " "))
		}
	}

	// A failing step → error, but every step still ran (run-all). Verify via
	// the --json result: 3 steps reported, the third (after the failure) ran.
	var jout bytes.Buffer
	err = runShipCore(context.Background(), shipDeps{
		Runner: &git.FakeRunner{}, Config: shipCfgWithPreflight(
			config.PreflightStep{Name: "a", Command: "true"},
			config.PreflightStep{Name: "b", Command: "false"},
			config.PreflightStep{Name: "c", Command: "true"},
		), Out: &jout,
	}, shipFlags{mode: shipModePreflight, jsonOut: true})
	if err != nil {
		t.Fatalf("json mode must not return an error, got %v", err)
	}
	var res shipPreflightJSON
	if e := json.Unmarshal(jout.Bytes(), &res); e != nil {
		t.Fatalf("not JSON: %v\n%s", e, jout.String())
	}
	if res.Result != "fail" || res.FailedStep != "b" || len(res.Steps) != 3 {
		t.Fatalf("result=%+v, want fail / failed=b / 3 steps", res)
	}
	if !res.Steps[0].OK || res.Steps[1].OK || !res.Steps[2].OK {
		t.Errorf("step OK flags = %v %v %v, want true false true (run-all)", res.Steps[0].OK, res.Steps[1].OK, res.Steps[2].OK)
	}

	// No steps configured → pass, no-op.
	var empty bytes.Buffer
	if err := runShipCore(context.Background(), shipDeps{
		Runner: &git.FakeRunner{}, Config: shipCfgWithPreflight(), Out: &empty,
	}, shipFlags{mode: shipModePreflight}); err != nil {
		t.Errorf("no-steps: unexpected error %v", err)
	}
}

func TestShipChangelogUnreleasedEmpty(t *testing.T) {
	dir := t.TempDir()
	empty := dir + "/empty.md"
	writeTestFile(t, empty, "# Changelog\n\n## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n- old\n")
	filled := dir + "/filled.md"
	writeTestFile(t, filled, "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- thing\n\n## [0.1.0] - 2026-01-01\n")
	noMarker := dir + "/nomarker.md"
	writeTestFile(t, noMarker, "# Changelog\n\n## [0.1.0] - 2026-01-01\n")

	if !shipChangelogUnreleasedEmpty(empty) {
		t.Error("empty [Unreleased] → want true")
	}
	if shipChangelogUnreleasedEmpty(filled) {
		t.Error("filled [Unreleased] → want false")
	}
	if shipChangelogUnreleasedEmpty(noMarker) {
		t.Error("missing marker → want false (no draft path)")
	}
}

func TestWriteShipChangelogSection(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/CHANGELOG.md"
	writeTestFile(t, path, "# Changelog\n\n## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n- old entry\n")

	ok, err := writeShipChangelogSection(path, "0.2.0", "### Added\n\n- new thing\n")
	if err != nil || !ok {
		t.Fatalf("writeShipChangelogSection: ok=%v err=%v", ok, err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, frag := range []string{"## [Unreleased]\n\n## [0.2.0] - ", "### Added\n\n- new thing\n\n## [0.1.0] - 2026-01-01", "- old entry"} {
		if !strings.Contains(s, frag) {
			t.Errorf("missing %q in:\n%s", frag, s)
		}
	}
	if strings.Contains(s, "\n\n\n") {
		t.Errorf("triple newline in output:\n%q", s)
	}
}

// ---------------------------------------------------------------------------
// Post-ship hooks (ship.watch / ship.verify)
// ---------------------------------------------------------------------------

func postHookDeps(cfg *config.Config, out *bytes.Buffer) shipDeps {
	return shipDeps{Runner: &git.FakeRunner{}, Config: cfg, Out: out, ErrOut: out}
}

func TestRunShipPostHooksSuccessOrder(t *testing.T) {
	cfg := testShipConfig()
	plan := shipPlan{
		Watch:  []config.PreflightStep{{Name: "ci", Command: "true"}},
		Verify: []config.PreflightStep{{Name: "cdn", Command: "true"}},
	}
	var out bytes.Buffer
	if err := runShipPostHooks(context.Background(), postHookDeps(cfg, &out), plan); err != nil {
		t.Fatalf("post hooks: %v", err)
	}
	s := out.String()
	wi, vi := strings.Index(s, "Watch"), strings.Index(s, "Verify")
	if wi < 0 || vi < 0 || wi > vi {
		t.Errorf("watch must run before verify:\n%s", s)
	}
	if !strings.Contains(s, "ci") || !strings.Contains(s, "cdn") {
		t.Errorf("step names missing:\n%s", s)
	}
}

func TestRunShipPostHooksWatchFailureAborts(t *testing.T) {
	cfg := testShipConfig()
	plan := shipPlan{
		Watch:  []config.PreflightStep{{Name: "ci", Command: "false"}},
		Verify: []config.PreflightStep{{Name: "cdn", Command: "true"}},
	}
	var out bytes.Buffer
	err := runShipPostHooks(context.Background(), postHookDeps(cfg, &out), plan)
	if err == nil || !strings.Contains(err.Error(), `watch step "ci" failed`) {
		t.Fatalf("want watch failure naming the step, got %v", err)
	}
	if !strings.Contains(HintFrom(err), "rerun the watcher") {
		t.Errorf("want rerun hint, got %q", HintFrom(err))
	}
	if strings.Contains(out.String(), "Verify") {
		t.Errorf("verify must not run after watch failure:\n%s", out.String())
	}
}

func TestRunShipPostHooksVerifyFailure(t *testing.T) {
	cfg := testShipConfig()
	plan := shipPlan{Verify: []config.PreflightStep{{Name: "cdn", Command: "false"}}}
	var out bytes.Buffer
	err := runShipPostHooks(context.Background(), postHookDeps(cfg, &out), plan)
	if err == nil || !strings.Contains(err.Error(), `verify step "cdn" failed`) {
		t.Fatalf("want verify failure naming the step, got %v", err)
	}
}

func TestRunShipPostHooksContinueOnFailure(t *testing.T) {
	cfg := testShipConfig()
	plan := shipPlan{
		Verify: []config.PreflightStep{
			{Name: "advisory", Command: "false", ContinueOnFailure: true},
			{Name: "must", Command: "true"},
		},
	}
	var out bytes.Buffer
	if err := runShipPostHooks(context.Background(), postHookDeps(cfg, &out), plan); err != nil {
		t.Fatalf("advisory failure must not abort: %v", err)
	}
	if !strings.Contains(out.String(), "must") {
		t.Errorf("later steps must still run:\n%s", out.String())
	}
}

// TestResolveShipBool: an explicit flag — either polarity — must beat the
// config default; an untouched flag falls through to config. This is what
// lets ship.auto_confirm/ship.wait act as defaults while --yes=false /
// --wait=false still escape them for one run.
func TestResolveShipBool(t *testing.T) {
	cases := []struct {
		name    string
		setFlag string // "" = not passed
		cfg     bool
		want    bool
	}{
		{"unset uses config true", "", true, true},
		{"unset uses config false", "", false, false},
		{"explicit true beats config false", "true", false, true},
		{"explicit false beats config true", "false", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("wait", true, "")
			if tc.setFlag != "" {
				if err := cmd.Flags().Set("wait", tc.setFlag); err != nil {
					t.Fatal(err)
				}
			}
			if got := resolveShipBool(cmd, "wait", tc.cfg); got != tc.want {
				t.Errorf("resolveShipBool = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRunShipCoreNoWaitSkipsPostHooks: with wait=false a live run must end
// right after the push — the configured watch step (which would fail loudly)
// never executes, and the skipped commands surface in a NOTE instead.
func TestRunShipCoreNoWaitSkipsPostHooks(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir+"/CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- Patch bug.\n\n## [1.2.3] - 2026-04-01\n")
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                      {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":             {Stdout: "main\n"},
		"rev-parse --show-toplevel":               {Stdout: dir + "\n"},
		"describe --tags --abbrev=0":              {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":        {Stdout: "fix: patch bug\n\x1e"},
		"rev-parse --verify refs/tags/v1.2.4":     {ExitCode: 1, Stderr: "not found"},
		"add -A":                                  {Stdout: ""},
		"commit -m release: v1.2.4":               {Stdout: "[main abc123] release\n"},
		"tag -a v1.2.4 -m Release v1.2.4":         {Stdout: ""},
		"rev-parse --verify origin/main^{commit}": {Stdout: "abc123\n"},
		"log -p --no-color origin/main..HEAD":     {Stdout: ""},
		"push origin main":                        {Stdout: "branch pushed\n"},
		"push origin v1.2.4":                      {Stdout: "tag pushed\n"},
	}}
	cfg := testShipConfig()
	cfg.Ship.Watch = []config.PreflightStep{{Name: "ci", Command: "false"}}

	var out bytes.Buffer
	err := runShipCore(context.Background(),
		shipDeps{Runner: runner, Config: cfg, Out: &out, ErrOut: &out},
		shipFlags{yes: true, skipPreflight: true, push: true, noWait: true})
	if err != nil {
		t.Fatalf("ship with noWait must skip post hooks: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "─── Watch") {
		t.Errorf("watch step must not run with noWait:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "wait=false") {
		t.Errorf("skipped pipeline must be announced:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Ship complete") {
		t.Errorf("ship must still complete:\n%s", out.String())
	}
}

func TestRunShipCorePostHooksSkippedWithoutPush(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "fix: y\n\x1e"},
		"rev-parse --verify refs/tags/v1.2.4": {ExitCode: 1, Stderr: "not found"},
	}}
	cfg := testShipConfig()
	// A watch step that would fail loudly if it ever ran.
	cfg.Ship.Watch = []config.PreflightStep{{Name: "ci", Command: "false"}}

	var out bytes.Buffer
	err := runShipCore(context.Background(),
		shipDeps{Runner: runner, Config: cfg, Out: &out, ErrOut: &out},
		shipFlags{yes: true, skipPreflight: true, noFetch: true, push: false})
	if err != nil {
		t.Fatalf("ship without push must skip post hooks: %v\n%s", err, out.String())
	}
	// The plan view may list the configured steps ("Watch: ci") — what must
	// be absent is the execution section header.
	if strings.Contains(out.String(), "─── Watch") {
		t.Errorf("post hooks must not run without a tag push:\n%s", out.String())
	}
}

// ---------------------------------------------------------------------------
// --dry-run --json plan output
// ---------------------------------------------------------------------------

func TestRunShipCoreDryRunJSON(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir+"/CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n## [0.5.0] - 2026-01-01\n\n- old\n")

	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"rev-parse --show-toplevel":           {Stdout: dir + "\n"},
		"describe --tags --abbrev=0":          {Stdout: "v0.5.0\n"},
		"log --format=%B%x1e v0.5.0..HEAD":    {Stdout: "feat(pull): with-base sync\n\x1efix: nil panic\n\x1e"},
		"rev-parse --verify refs/tags/v0.6.0": {ExitCode: 1, Stderr: "not found"},
	}}
	cfg := testShipConfig()
	cfg.Ship.Watch = []config.PreflightStep{{Name: "ci", Command: "gh run watch"}}
	cfg.Ship.Verify = []config.PreflightStep{{Name: "cdn", Command: "curl -fsI https://x/checksums.txt"}}

	var out bytes.Buffer
	err := runShipCore(context.Background(),
		shipDeps{Runner: runner, Config: cfg, Out: &out, ErrOut: &out},
		shipFlags{dryRun: true, jsonOut: true, noFetch: true, push: true})
	if err != nil {
		t.Fatalf("dry-run json: %v\n%s", err, out.String())
	}

	var got shipPlanJSON
	if uerr := json.Unmarshal(out.Bytes(), &got); uerr != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", uerr, out.String())
	}
	if got.NextTag != "v0.6.0" || got.Bump != "minor" || got.Branch != "main" {
		t.Errorf("plan fields: %+v", got)
	}
	if len(got.Watch) != 1 || got.Watch[0].Command != "gh run watch" {
		t.Errorf("watch steps: %+v", got.Watch)
	}
	if len(got.Verify) != 1 || got.Verify[0].Name != "cdn" {
		t.Errorf("verify steps: %+v", got.Verify)
	}
	if !strings.Contains(got.ChangelogDraft, "### Added") || !strings.Contains(got.ChangelogDraft, "**pull:** with-base sync") {
		t.Errorf("changelog draft: %q", got.ChangelogDraft)
	}
	if !got.Push {
		t.Error("push flag must round-trip")
	}
	if strings.Contains(out.String(), "Ship plan") {
		t.Errorf("human rendering must be suppressed in JSON mode:\n%s", out.String())
	}
}

func TestRunShipCoreJSONRequiresDryRun(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"describe --tags --abbrev=0":          {Stdout: "v1.2.3\n"},
		"log --format=%B%x1e v1.2.3..HEAD":    {Stdout: "fix: y\n\x1e"},
		"rev-parse --verify refs/tags/v1.2.4": {ExitCode: 1, Stderr: "not found"},
	}}
	var out bytes.Buffer
	// EXPLICIT --json (jsonExplicit) on a live run is refused; yes:true rules
	// out the confirm-refusal path so the error is specifically the json one.
	err := runShipCore(context.Background(),
		shipDeps{Runner: runner, Config: testShipConfig(), Out: &out, ErrOut: &out},
		shipFlags{jsonOut: true, jsonExplicit: true, yes: true, noFetch: true, push: true})
	if err == nil || !strings.Contains(err.Error(), "emits the release plan and requires --dry-run") {
		t.Fatalf("want --json-requires-dry-run error, got %v", err)
	}
	// The guard fires before any mutation.
	for _, call := range runner.Calls {
		if len(call.Args) > 0 && (call.Args[0] == "tag" || call.Args[0] == "commit" || call.Args[0] == "push") {
			t.Fatalf("refused run must not mutate, but called git %s", strings.Join(call.Args, " "))
		}
	}
}

// ---------------------------------------------------------------------------
// ship.version_files override
// ---------------------------------------------------------------------------

func TestBuildShipPlanVersionFilesConfig(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain":                  {Stdout: ""},
		"rev-parse --abbrev-ref HEAD":         {Stdout: "main\n"},
		"rev-parse --show-toplevel":           {Stdout: "/repo\n"},
		"describe --tags --abbrev=0":          {Stdout: "v0.5.0\n"},
		"log --format=%B%x1e v0.5.0..HEAD":    {Stdout: "fix: y\n\x1e"},
		"rev-parse --verify refs/tags/v0.5.1": {ExitCode: 1, Stderr: "not found"},
	}}
	cfg := testShipConfig()
	cfg.Ship.VersionFiles = []config.VersionFile{
		{Path: "VERSION"},
		{Path: "extension/package.json"},
		{Path: "src/app/__init__.py", Pattern: `__version__ = "{version}"`},
	}

	plan, err := buildShipPlan(context.Background(), runner, cfg, shipFlags{noFetch: true})
	if err != nil {
		t.Fatalf("buildShipPlan: %v", err)
	}
	want := []config.VersionFile{
		{Path: "/repo/VERSION"},
		{Path: "/repo/extension/package.json"},
		{Path: "/repo/src/app/__init__.py", Pattern: `__version__ = "{version}"`},
	}
	if len(plan.VersionFiles) != len(want) {
		t.Fatalf("VersionFiles = %+v, want %+v", plan.VersionFiles, want)
	}
	for i, w := range want {
		if plan.VersionFiles[i] != w {
			t.Errorf("VersionFiles[%d] = %+v, want %+v", i, plan.VersionFiles[i], w)
		}
	}
}
