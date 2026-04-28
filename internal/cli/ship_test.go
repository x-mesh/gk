package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

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
