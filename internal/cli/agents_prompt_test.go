package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestGitIntentGate is the trigger/no-trigger corpus: per the "no false
// positives, false negatives allowed" contract, gitIntentGate must return
// false on every case that should not fire and true on every case that
// should.
func TestGitIntentGate(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   bool
	}{
		// --- should fire ---
		{"korean commit please", "커밋해줘", true},
		{"korean commit then push", "커밋하고 푸시해", true},
		{"git commit instrumental", "git commit으로 정리해", true},
		{"bare rebase target", "rebase develop", true},
		{"korean merge please", "머지해줘", true},
		{"korean resolve conflict", "충돌 해결해줘", true},
		{"english push with korean ending", "push해", true},
		{"pr creation", "PR 만들어줘", true},
		{"branch creation slang", "브랜치 새로 파서 작업해", true},
		{"stash with korean ending", "stash 해놔", true},

		// --- must not fire ---
		{"commit to idiom", "commit to this plan", false},
		{"discussion about commit message style", "커밋 메시지 스타일 어때?", false},
		{"committed to quality idiom", "we are committed to quality", false},
		{"explain branch strategy", "이 브랜치 전략 설명해줘", false},
		{"what is git question", "git이 뭐야?", false},
		{"empty prompt", "", false},
		{"git only inside code block", "```\ngit commit -am wip\n```", false},

		// --- extra robustness (not part of the spec corpus, but the same
		// no-false-positive bar applies) ---
		{"discussion comparing git verbs", "explain the difference between git merge and git rebase", false},
		{"merge as a noun in a complaint", "merge conflicts are annoying today", false},
		{"push notifications bug report", "push notifications broken on ios", false},
		{"pull request status question", "pull request status?", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitIntentGate(tc.prompt); got != tc.want {
				t.Errorf("gitIntentGate(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}
}

// TestGitIntentGate_SizeCap verifies the 64KB size cap short-circuits to
// false regardless of content — a prompt containing an obvious trigger
// phrase must still be rejected once it crosses the cap.
func TestGitIntentGate_SizeCap(t *testing.T) {
	huge := strings.Repeat("a", gitIntentPromptSizeCap+1) + " 커밋해줘"
	if gitIntentGate(huge) {
		t.Errorf("gitIntentGate should reject a prompt over the size cap")
	}
	// Just under the cap with a trigger phrase should still work normally.
	underCap := strings.Repeat("a", gitIntentPromptSizeCap-20) + " 커밋해줘"
	if !gitIntentGate(underCap) {
		t.Errorf("gitIntentGate should still fire under the size cap")
	}
}

// TestCollectPromptPayload exercises the real probe path against a temp
// repo: a clean repo reports the branch with no dirty segment; after an
// untracked write the dirty counts show up; and dir outside any repo
// degrades to ("", false) rather than a partial/misleading string.
func TestCollectPromptPayload(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	summary, ok := collectPromptPayload(ctx, repo.Dir)
	if !ok {
		t.Fatalf("collectPromptPayload: ok = false for a real repo")
	}
	branch := strings.TrimSpace(runGitOut(t, runner, "branch", "--show-current"))
	if branch == "" {
		t.Fatalf("test setup: could not determine current branch")
	}
	if !strings.Contains(summary, branch) {
		t.Errorf("summary %q does not mention branch %q", summary, branch)
	}
	if strings.Contains(summary, "dirty(") {
		t.Errorf("clean repo summary should not mention dirty counts: %q", summary)
	}

	repo.WriteFile("untracked.txt", "x")
	summary, ok = collectPromptPayload(ctx, repo.Dir)
	if !ok {
		t.Fatalf("collectPromptPayload: ok = false after an untracked write")
	}
	if !strings.Contains(summary, "untracked=1") {
		t.Errorf("dirty repo summary = %q, want it to mention untracked=1", summary)
	}
}

// TestCollectPromptPayload_NotARepo confirms the "not a usable git repo"
// case degrades to the documented failure contract: ("", false), never a
// partial string that could be mistaken for real orientation.
func TestCollectPromptPayload_NotARepo(t *testing.T) {
	dir := t.TempDir() // no git init
	summary, ok := collectPromptPayload(context.Background(), dir)
	if ok || summary != "" {
		t.Errorf("collectPromptPayload(non-repo) = (%q, %v), want (\"\", false)", summary, ok)
	}
}

// TestCollectPromptPayload_PausedOperation simulates a mid-rebase repo (the
// same fixture shape gitstate's own tests use: a hand-built rebase-merge
// directory under .git) and checks the resume hint surfaces using whatever
// name this binary was invoked as, so the assertion doesn't hardcode "gk".
func TestCollectPromptPayload_PausedOperation(t *testing.T) {
	repo := testutil.NewRepo(t)
	rebaseMergeDir := repo.GitDir + "/rebase-merge"
	if err := writeRebaseMergeFixture(rebaseMergeDir); err != nil {
		t.Fatalf("fixture setup: %v", err)
	}

	summary, ok := collectPromptPayload(context.Background(), repo.Dir)
	if !ok {
		t.Fatalf("collectPromptPayload: ok = false with a paused rebase")
	}
	if !strings.Contains(summary, "rebase") {
		t.Errorf("summary %q should mention the paused rebase", summary)
	}
	resume := selfCmd("continue")
	if !strings.Contains(summary, resume) {
		t.Errorf("summary %q should mention the resume hint %q", summary, resume)
	}
}

// TestCollectPromptPayload_CharCap constructs a pathologically long branch
// name so the real composed summary exceeds promptPayloadCharCap, then
// checks the returned string is clipped to the rune cap rather than growing
// unbounded.
func TestCollectPromptPayload_CharCap(t *testing.T) {
	repo := testutil.NewRepo(t)
	// A single path component this long would blow past the filesystem's
	// filename-length limit (git refs are files under .git/refs/heads/...),
	// and the *total* path (tempdir + .git/refs/heads/... + .lock) has its
	// own ceiling too — so the length is spread across several
	// slash-separated segments, sized to clear promptPayloadCharCap while
	// staying under a real path-length limit.
	segment := strings.Repeat("x", 150)
	longName := strings.Join([]string{segment, segment, segment, segment, segment}, "/")
	repo.CreateBranch(longName)

	summary, ok := collectPromptPayload(context.Background(), repo.Dir)
	if !ok {
		t.Fatalf("collectPromptPayload: ok = false")
	}
	if n := len([]rune(summary)); n > promptPayloadCharCap {
		t.Errorf("summary is %d runes, want <= %d", n, promptPayloadCharCap)
	}
}

func runGitOut(t *testing.T, runner *git.ExecRunner, args ...string) string {
	t.Helper()
	out, _, err := runner.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// writeRebaseMergeFixture hand-builds the minimal set of files gitstate.Detect
// looks for under .git/rebase-merge, mirroring the fixture shape used by
// gitstate's own tests (internal/gitstate/detect_test.go) — enough to make
// DetectFromGitDir report StateRebaseMerge without actually driving a real
// rebase to a conflict.
func writeRebaseMergeFixture(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"head-name": "refs/heads/feat/x\n",
		"onto":      "abc1234\n",
		"orig-head": "def5678\n",
		"msgnum":    "2\n",
		"end":       "5\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// --- runAgentsHookPrompt (UserPromptSubmit handler) ---

// promptHookOutput drives runAgentsHookPrompt exactly the way Claude Code
// would: JSON on stdin, captured stdout, no flags (the handler doesn't read
// any).
func promptHookOutput(t *testing.T, stdin string) string {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(stdin))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runAgentsHookPrompt(cmd, nil); err != nil {
		t.Fatalf("runAgentsHookPrompt: %v", err)
	}
	return buf.String()
}

// TestAgentsHookPrompt_FailOpen covers every "give up quietly" path: bad
// stdin, a missing/blank prompt field, and a cwd that isn't a usable git
// repo. Every case must emit nothing and (checked by promptHookOutput
// itself) never return a non-nil error.
func TestAgentsHookPrompt_FailOpen(t *testing.T) {
	repo := testutil.NewRepo(t)
	notARepo := t.TempDir()

	cases := []struct {
		name, stdin string
	}{
		{"garbage stdin", `not json at all`},
		{"empty stdin", ``},
		{"missing prompt field", fmt.Sprintf(`{"cwd":%q}`, repo.Dir)},
		{"blank prompt", fmt.Sprintf(`{"prompt":"","cwd":%q}`, repo.Dir)},
		{"whitespace-only prompt", fmt.Sprintf(`{"prompt":"   ","cwd":%q}`, repo.Dir)},
		{"non-repo cwd", fmt.Sprintf(`{"prompt":"커밋해줘","cwd":%q}`, notARepo)},
		{"missing cwd entirely", `{"prompt":"커밋해줘"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if out := promptHookOutput(t, tc.stdin); strings.TrimSpace(out) != "" {
				t.Errorf("expected no output (fail-open), got: %q", out)
			}
		})
	}
}

// TestAgentsHookPrompt_SlashCommandNoOp confirms a slash command is never
// treated as a git-action request, even when its body would otherwise trip
// gitIntentGate.
func TestAgentsHookPrompt_SlashCommandNoOp(t *testing.T) {
	repo := testutil.NewRepo(t)
	stdin := fmt.Sprintf(`{"prompt":"/커밋해줘","cwd":%q}`, repo.Dir)
	if out := promptHookOutput(t, stdin); strings.TrimSpace(out) != "" {
		t.Errorf("slash command must be a no-op, got: %q", out)
	}
}

// TestAgentsHookPrompt_NoGitIntentNoOp confirms a prompt with no git intent
// (per gitIntentGate) never triggers the prefetch.
func TestAgentsHookPrompt_NoGitIntentNoOp(t *testing.T) {
	repo := testutil.NewRepo(t)
	stdin := fmt.Sprintf(`{"prompt":"오늘 날씨 어때?","cwd":%q}`, repo.Dir)
	if out := promptHookOutput(t, stdin); strings.TrimSpace(out) != "" {
		t.Errorf("non-git-intent prompt must be a no-op, got: %q", out)
	}
}

// TestAgentsHookPrompt_NormalPath drives the full path against a real repo:
// a triggering prompt with no transcript on file produces the
// UserPromptSubmit JSON schema, carrying the marker and the branch name.
func TestAgentsHookPrompt_NormalPath(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	branch := strings.TrimSpace(runGitOut(t, runner, "branch", "--show-current"))

	stdin := fmt.Sprintf(`{"prompt":"커밋해줘","cwd":%q}`, repo.Dir)
	out := promptHookOutput(t, stdin)
	if got := gjson.Get(out, "hookSpecificOutput.hookEventName").String(); got != "UserPromptSubmit" {
		t.Fatalf("hookEventName = %q, want UserPromptSubmit: %s", got, out)
	}
	ctx := gjson.Get(out, "hookSpecificOutput.additionalContext").String()
	if !strings.Contains(ctx, gkPromptHookMarker) {
		t.Errorf("additionalContext missing marker %q: %s", gkPromptHookMarker, ctx)
	}
	if !strings.Contains(ctx, branch) {
		t.Errorf("additionalContext %q does not mention branch %q", ctx, branch)
	}
}

// TestAgentsHookPrompt_CwdFallback exercises the cwd field-name cascade:
// workspace.current_dir wins when the top-level cwd is absent, and
// workspace_roots.0 wins when both of those are absent.
func TestAgentsHookPrompt_CwdFallback(t *testing.T) {
	repo := testutil.NewRepo(t)

	stdin := fmt.Sprintf(`{"prompt":"커밋해줘","workspace":{"current_dir":%q}}`, repo.Dir)
	if out := promptHookOutput(t, stdin); gjson.Get(out, "hookSpecificOutput.hookEventName").String() != "UserPromptSubmit" {
		t.Errorf("workspace.current_dir fallback did not fire: %q", out)
	}

	stdin = fmt.Sprintf(`{"prompt":"커밋해줘","workspace_roots":[%q]}`, repo.Dir)
	if out := promptHookOutput(t, stdin); gjson.Get(out, "hookSpecificOutput.hookEventName").String() != "UserPromptSubmit" {
		t.Errorf("workspace_roots.0 fallback did not fire: %q", out)
	}
}

// TestAgentsHookPrompt_MarkerDedupe confirms a transcript tail that already
// carries the prefetch marker suppresses a repeat injection.
func TestAgentsHookPrompt_MarkerDedupe(t *testing.T) {
	repo := testutil.NewRepo(t)

	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	line := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"%s already injected"}]}}`, gkPromptHookMarker)
	if err := os.WriteFile(tp, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdin := fmt.Sprintf(`{"prompt":"커밋해줘","cwd":%q,"transcript_path":%q}`, repo.Dir, tp)
	if out := promptHookOutput(t, stdin); strings.TrimSpace(out) != "" {
		t.Errorf("marker already in transcript tail should suppress the prefetch, got: %q", out)
	}
}

// TestAgentsHookPrompt_UnreadableTranscriptStillFires confirms the dedupe
// only forfeits itself on a missing/unreadable transcript — the prefetch
// must still fire, per the fail-open contract (one extra injection beats a
// wrongly suppressed one).
func TestAgentsHookPrompt_UnreadableTranscriptStillFires(t *testing.T) {
	repo := testutil.NewRepo(t)
	missing := filepath.Join(t.TempDir(), "missing.jsonl")

	stdin := fmt.Sprintf(`{"prompt":"커밋해줘","cwd":%q,"transcript_path":%q}`, repo.Dir, missing)
	out := promptHookOutput(t, stdin)
	if got := gjson.Get(out, "hookSpecificOutput.hookEventName").String(); got != "UserPromptSubmit" {
		t.Errorf("missing transcript must fail open to the prefetch, got: %q", out)
	}
}
