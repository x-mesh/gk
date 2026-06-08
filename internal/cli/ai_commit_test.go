package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func TestAICommitRegistered(t *testing.T) {
	// rootCmd should resolve "commit" directly.
	found, _, err := rootCmd.Find([]string{"commit"})
	if err != nil {
		t.Fatalf("rootCmd.Find(commit): %v", err)
	}
	if found.Use != "commit" {
		t.Errorf("Use: want %q, got %q", "commit", found.Use)
	}
}

func TestAICommitHelpListsFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	found, _, err := rootCmd.Find([]string{"commit"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	found.SetOut(buf)
	found.SetErr(buf)
	if err := found.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"--force", "--dry-run", "--provider", "--lang",
		"--staged-only", "--include-unstaged", "--abort",
		"--allow-secret-kind", "--no-verify", "--ci", "--yes",
		"--no-wip-unwrap", "--force-wip",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing flag %q\n%s", want, out)
		}
	}
}

func TestSecretBypass(t *testing.T) {
	cases := []struct {
		name       string
		noVerify   bool
		allowKinds []string
		want       bool
	}{
		{"default — gate enforced", false, nil, false},
		{"named kind is suppressed upstream, not a bypass", false, []string{"github-token"}, false},
		{"--no-verify bypasses", true, nil, true},
		{"--allow-secret-kind all bypasses", false, []string{"all"}, true},
		{"all mixed with a named kind still bypasses", false, []string{"github-token", "all"}, true},
		{"--no-verify wins regardless of kinds", true, []string{"github-token"}, true},
	}
	for _, tc := range cases {
		if got := secretBypass(tc.noVerify, tc.allowKinds); got != tc.want {
			t.Errorf("%s: secretBypass(%v, %v) = %v, want %v", tc.name, tc.noVerify, tc.allowKinds, got, tc.want)
		}
	}
}

// TestNoVerifyCanSetSkipPrivacy guards the premise of the --no-verify ⇒
// --skip-privacy implication in runAICommit: the commit command must be able
// to flip the root-level persistent `skip-privacy` flag, and applyPrivacyGate
// must read the same value back. If cobra ever stops sharing that flag with
// the subcommand, `gk commit -n` would silently re-block on the privacy gate.
func TestNoVerifyCanSetSkipPrivacy(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"commit"})
	if err != nil {
		t.Fatalf("find commit: %v", err)
	}
	t.Cleanup(func() { _ = found.Flags().Set("skip-privacy", "false") })

	if err := found.Flags().Set("skip-privacy", "true"); err != nil {
		t.Fatalf("commit cannot set inherited skip-privacy flag: %v", err)
	}
	if v, _ := found.Flags().GetBool("skip-privacy"); !v {
		t.Error("skip-privacy not readable as true from the commit command")
	}
}

func TestReadAICommitFlagsMutualExclusion(t *testing.T) {
	found, _, _ := rootCmd.Find([]string{"commit"})
	_ = found.Flags().Set("staged-only", "true")
	_ = found.Flags().Set("include-unstaged", "true")
	_, err := readAICommitFlags(found)
	if err == nil {
		t.Error("want error when both --staged-only and --include-unstaged are set")
	}
	// Reset for other tests.
	_ = found.Flags().Set("staged-only", "false")
	_ = found.Flags().Set("include-unstaged", "false")
}

func TestNewRunIDIsHex(t *testing.T) {
	id := newRunID()
	if len(id) < 8 {
		t.Errorf("runID too short: %q", id)
	}
	// Either hex (16 chars) or time-based fallback starting with 't'.
	if id[0] != 't' {
		for _, r := range id {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				t.Errorf("non-hex rune in runID: %q", id)
				break
			}
		}
	}
}

func TestInspectWIPCommitForAICommitIncludesFiles(t *testing.T) {
	// Fake responses for the chain detector: one WIP at HEAD, then a
	// non-WIP commit at HEAD~1 to stop the walk. Files emitted in -z
	// (NUL-separated) form.
	const wipSHA = "wipsha11"
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --abbrev-ref HEAD":                     {Stdout: "improve\n"},
		"log -1 --format=%s HEAD~0":                       {Stdout: "--wip-- [skip ci]\n"},
		"rev-parse HEAD~0":                                {Stdout: wipSHA + "\n"},
		"log -1 --format=%P HEAD~0":                       {Stdout: wipSHA + "-parent\n"},
		"branch -r --contains " + wipSHA:                  {Stdout: ""},
		"diff -z --name-status " + wipSHA + "^ " + wipSHA: {Stdout: "M\x00internal/cli/merge.go\x00A\x00new.go\x00"},
		"log -1 --format=%s HEAD~1":                       {Stdout: "feat: real commit\n"},
		"rev-parse HEAD":                                  {Stdout: wipSHA + "\n"},
	}}

	cfg := config.AICommitConfig{WIPMaxChain: 5, WIPEnabled: true}
	wip, err := inspectWIPCommitForAICommit(context.Background(), runner, cfg, false, false)
	if err != nil {
		t.Fatalf("inspectWIPCommitForAICommit: %v", err)
	}
	if !wip.Present {
		t.Fatal("expected WIP commit")
	}
	if len(wip.Files) != 2 {
		t.Fatalf("expected 2 files, got %#v", wip.Files)
	}
	if wip.HeadSHA != wipSHA {
		t.Errorf("HeadSHA: want %q, got %q", wipSHA, wip.HeadSHA)
	}
	// Files end up sorted by path in MergeChainFiles.
	hasFoo, hasNew := false, false
	for _, f := range wip.Files {
		if f.Path == "internal/cli/merge.go" && f.Status == "modified" {
			hasFoo = true
		}
		if f.Path == "new.go" && f.Status == "added" {
			hasNew = true
		}
	}
	if !hasFoo || !hasNew {
		t.Fatalf("unexpected files: %#v", wip.Files)
	}
}

func TestAppendWIPCommitFilesDedupesCurrentFiles(t *testing.T) {
	files := appendWIPCommitFiles([]aicommit.FileChange{
		{Path: "current.go", Status: "modified"},
	}, []aicommit.FileChange{
		{Path: "current.go", Status: "modified"},
		{Path: "wip.go", Status: "added"},
	})
	if len(files) != 2 {
		t.Fatalf("expected deduped files, got %#v", files)
	}
	if files[1].Path != "wip.go" {
		t.Fatalf("expected WIP file appended, got %#v", files)
	}
}

func TestUnwrapWIPCommitBeforeApplySkipsDryRun(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"reset HEAD~1": {Stdout: ""},
	}}

	err := unwrapWIPCommitBeforeApply(context.Background(), runner, wipCommitForAICommit{Present: true}, aiCommitFlags{dryRun: true}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unwrapWIPCommitBeforeApply: %v", err)
	}
	calls := joinedShipCalls(runner.Calls)
	if strings.Contains(calls, "reset HEAD~1") {
		t.Fatalf("dry-run should not reset, calls:\n%s", calls)
	}
}

func TestUnwrapWIPCommitBeforeApplyResetsAfterPlan(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"reset HEAD~1": {Stdout: ""},
	}}
	var out bytes.Buffer

	err := unwrapWIPCommitBeforeApply(context.Background(), runner, wipCommitForAICommit{Present: true}, aiCommitFlags{}, &out)
	if err != nil {
		t.Fatalf("unwrapWIPCommitBeforeApply: %v", err)
	}
	calls := joinedShipCalls(runner.Calls)
	if !strings.Contains(calls, "reset HEAD~1") {
		t.Fatalf("expected WIP reset, calls:\n%s", calls)
	}
	if !strings.Contains(out.String(), "after AI plan") {
		t.Fatalf("expected after-plan output, got %q", out.String())
	}
}

// TestUnwrapWIPCommitRefusesWhenHEADMoved — the M4 fix.
// When the recorded HeadSHA differs from the current HEAD, the
// reset must be refused with a "HEAD moved" error.
func TestUnwrapWIPCommitRefusesWhenHEADMoved(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse HEAD": {Stdout: "now99999999\n"},
	}}
	var out bytes.Buffer
	wip := wipCommitForAICommit{
		Present:  true,
		ChainLen: 2,
		HeadSHA:  "was11111111",
	}
	err := unwrapWIPCommitBeforeApply(context.Background(), runner, wip, aiCommitFlags{}, &out)
	if err == nil {
		t.Fatal("expected refusal when HEAD moved")
	}
	if !strings.Contains(err.Error(), "HEAD moved") {
		t.Errorf("err: %v", err)
	}
	// And the reset should NOT have been issued.
	calls := joinedShipCalls(runner.Calls)
	if strings.Contains(calls, "reset HEAD~") {
		t.Errorf("must not reset after HEAD-moved detection; calls:\n%s", calls)
	}
}

// TestWIPChainSkipHint_PushedAtHEAD — the user-facing fallout from
// the develop/main story: when DetectWIPChain returned an empty chain
// because every WIP at HEAD is already on a remote, the CLI must
// surface the `--force-wip` escape hatch instead of going silent.
func TestWIPChainSkipHint_PushedAtHEAD(t *testing.T) {
	got := wipChainSkipHint(false, wipCommitForAICommit{
		Present:    false,
		StopReason: aicommit.StopReasonPushed,
	})
	if !strings.Contains(got, "--force-wip") {
		t.Errorf("hint must point at --force-wip; got %q", got)
	}
}

// TestWIPChainSkipHint_Disabled — when the feature is turned off,
// the hint must say so plainly so users don't blame protected
// branches or anything else.
func TestWIPChainSkipHint_Disabled(t *testing.T) {
	got := wipChainSkipHint(true, wipCommitForAICommit{})
	if !strings.Contains(got, "disabled") {
		t.Errorf("hint must mention 'disabled'; got %q", got)
	}
}

// TestWIPChainSkipHint_NormalNonWIP — staying out of the way on the
// common case (HEAD isn't WIP at all) keeps everyday output quiet.
func TestWIPChainSkipHint_NormalNonWIP(t *testing.T) {
	got := wipChainSkipHint(false, wipCommitForAICommit{
		Present:    false,
		StopReason: aicommit.StopReasonNonWIPSubject,
	})
	if got != "" {
		t.Errorf("non-WIP HEAD must NOT produce a hint; got %q", got)
	}
}

// TestWIPChainSkipHint_DetachedHEAD — actionable hint for the one
// case where the walk refuses outright.
func TestWIPChainSkipHint_DetachedHEAD(t *testing.T) {
	got := wipChainSkipHint(false, wipCommitForAICommit{
		Present:    false,
		StopReason: aicommit.StopReasonDetachedHEAD,
	})
	if !strings.Contains(got, "detached") {
		t.Errorf("hint must mention detached HEAD; got %q", got)
	}
}

// TestInspectWIPCommitForAICommit_ForceWIPIncludesPushed — wiring
// check: with forceWIP=true the underlying DetectWIPChain receives
// AllowPushed=true, so a pushed WIP at HEAD~1 is rolled into the chain.
func TestInspectWIPCommitForAICommit_ForceWIPIncludesPushed(t *testing.T) {
	const wipSHA1, wipSHA2 = "wipsha11", "wipsha22"
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --abbrev-ref HEAD":                       {Stdout: "develop\n"},
		"log -1 --format=%s HEAD~0":                         {Stdout: "WIP: local\n"},
		"rev-parse HEAD~0":                                  {Stdout: wipSHA1 + "\n"},
		"log -1 --format=%P HEAD~0":                         {Stdout: wipSHA1 + "-parent\n"},
		"diff -z --name-status " + wipSHA1 + "^ " + wipSHA1: {Stdout: "M\x00a.go\x00"},
		"log -1 --format=%s HEAD~1":                         {Stdout: "WIP: pushed\n"},
		"rev-parse HEAD~1":                                  {Stdout: wipSHA2 + "\n"},
		"log -1 --format=%P HEAD~1":                         {Stdout: wipSHA2 + "-parent\n"},
		"diff -z --name-status " + wipSHA2 + "^ " + wipSHA2: {Stdout: "M\x00b.go\x00"},
		"log -1 --format=%s HEAD~2":                         {Stdout: "feat: real\n"},
		"rev-parse HEAD":                                    {Stdout: wipSHA1 + "\n"},
		// branch -r --contains: only HEAD~1 is on a remote. With
		// forceWIP=true the gate is skipped so both lookups still
		// resolve cleanly.
		"branch -r --contains " + wipSHA1: {Stdout: ""},
		"branch -r --contains " + wipSHA2: {Stdout: "  origin/develop\n"},
	}}
	cfg := config.AICommitConfig{WIPMaxChain: 5, WIPEnabled: true}
	wip, err := inspectWIPCommitForAICommit(context.Background(), runner, cfg, false, true)
	if err != nil {
		t.Fatalf("inspectWIPCommitForAICommit: %v", err)
	}
	if !wip.Present || wip.ChainLen != 2 {
		t.Fatalf("forceWIP must pull the pushed commit into the chain; got %+v", wip)
	}
	if !wip.ForcePushBypass {
		t.Errorf("ForcePushBypass must propagate; got %+v", wip)
	}
}

// TestInspectWIPCommitForAICommit_PushedAtHEADReportsReason — without
// --force-wip, a pushed HEAD WIP yields Present=false but the
// StopReason rides along so the CLI can render the hint.
func TestInspectWIPCommitForAICommit_PushedAtHEADReportsReason(t *testing.T) {
	const wipSHA = "wippush1"
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --abbrev-ref HEAD":    {Stdout: "develop\n"},
		"log -1 --format=%s HEAD~0":      {Stdout: "WIP: pushed\n"},
		"rev-parse HEAD~0":               {Stdout: wipSHA + "\n"},
		"log -1 --format=%P HEAD~0":      {Stdout: wipSHA + "-parent\n"},
		"branch -r --contains " + wipSHA: {Stdout: "  origin/develop\n"},
	}}
	cfg := config.AICommitConfig{WIPMaxChain: 5, WIPEnabled: true}
	wip, err := inspectWIPCommitForAICommit(context.Background(), runner, cfg, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if wip.Present {
		t.Errorf("Present must be false when chain is empty; got %+v", wip)
	}
	if wip.StopReason != aicommit.StopReasonPushed {
		t.Errorf("StopReason: want %q, got %q", aicommit.StopReasonPushed, wip.StopReason)
	}
}

// TestUnwrapWIPCommitProceedsWhenHEADUnchanged — companion to the
// M4 test. When recorded HeadSHA matches current HEAD, reset proceeds.
func TestUnwrapWIPCommitProceedsWhenHEADUnchanged(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse HEAD": {Stdout: "abc11111aaaa\n"},
		"reset HEAD~2":   {Stdout: ""},
	}}
	var out bytes.Buffer
	wip := wipCommitForAICommit{
		Present:  true,
		ChainLen: 2,
		HeadSHA:  "abc11111aaaa",
	}
	err := unwrapWIPCommitBeforeApply(context.Background(), runner, wip, aiCommitFlags{}, &out)
	if err != nil {
		t.Fatalf("expected success when HEAD matches: %v", err)
	}
	calls := joinedShipCalls(runner.Calls)
	if !strings.Contains(calls, "reset HEAD~2") {
		t.Errorf("expected reset HEAD~2, calls:\n%s", calls)
	}
}

func TestApplyAICommitFlags_LangResolution(t *testing.T) {
	cases := []struct {
		name       string
		aiLang     string // ai.lang (already folded from output.lang by Load)
		commitLang string // ai.commit.lang
		flagLang   string // --lang
		want       string
	}{
		{"ai.lang only", "ko", "", "", "ko"},
		{"commit.lang overrides ai.lang", "ko", "en", "", "en"},
		{"flag overrides commit.lang", "ko", "en", "fr", "fr"},
		{"flag overrides ai.lang", "ko", "", "fr", "fr"},
		{"empty commit.lang keeps ai.lang", "en", "", "", "en"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ai := config.AIConfig{Lang: c.aiLang}
			ai.Commit.Lang = c.commitLang
			out := applyAICommitFlagsToConfig(ai, aiCommitFlags{lang: c.flagLang})
			if out.Lang != c.want {
				t.Errorf("Lang = %q, want %q", out.Lang, c.want)
			}
		})
	}
}
