package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestCollectChatContext_ProjectsCoreFields exercises the same divergent
// repo shape TestIntegration_CollectContext (context_test.go) uses — one
// ahead of an upstream, one untracked file — and checks collectChatContext
// carries the identity/sync/dirty core through from collectContext.
func TestCollectChatContext_ProjectsCoreFields(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")
	upstream.RunGit("tag", "v0.1.0")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")
	downstream.WriteFile("local.txt", "x\n")
	downstream.Commit("feat: local work")
	downstream.WriteFile("wip.txt", "wip\n") // untracked

	prev := flagRepo
	flagRepo = downstream.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: downstream.Dir}
	cfg := config.Defaults()
	got, ok := collectChatContext(context.Background(), runner, &cfg, nil)
	if !ok {
		t.Fatal("collectChatContext ok = false, want true — this is a real repo, collection must succeed")
	}

	if got.Branch != "main" || got.Upstream != "origin/main" {
		t.Errorf("identity fields: %+v", got)
	}
	if got.Ahead != 1 || got.Behind != 0 {
		t.Errorf("ahead/behind = %d/%d, want 1/0", got.Ahead, got.Behind)
	}
	if got.Dirty.Untracked != 1 {
		t.Errorf("untracked = %d, want 1", got.Dirty.Untracked)
	}
	if got.InProgress != nil {
		t.Errorf("in_progress = %+v, want nil (nothing mid-flight)", got.InProgress)
	}
	if got.WorktreeCount != 1 {
		t.Errorf("worktree_count = %d, want 1", got.WorktreeCount)
	}

	// Field selection: the JSON text must carry only the light core, never
	// the heavy sections gk context fuses in via --include (those cost
	// tokens the standing prompt block can't afford).
	raw, err := chatContextJSONString(context.Background(), runner, &cfg, nil)
	if err != nil {
		t.Fatalf("chatContextJSONString: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("chatContextJSONString did not produce valid JSON: %v\n%s", err, raw)
	}
	for _, heavy := range []string{"diff", "log", "precheck", "conflict", "remotes", "release", "next_actions", "bisect", "schema", "worktrees"} {
		if _, present := m[heavy]; present {
			t.Errorf("chatContextJSONString leaked heavy field %q: %s", heavy, raw)
		}
	}
	for _, light := range []string{"branch", "upstream", "ahead", "behind", "dirty"} {
		if _, present := m[light]; !present {
			t.Errorf("chatContextJSONString missing core field %q: %s", light, raw)
		}
	}
}

// TestCollectChatContext_UnbornHEAD covers the DoD's bare/empty-repo edge
// case: a freshly `git init`-ed repo with no commits yet has an unborn
// HEAD. gk chat must still be able to start a session there — this proves
// collectChatContext degrades to sane zero values instead of erroring or
// panicking (chat.go's own `rev-parse --show-toplevel` gate already refuses
// a true bare repo before reaching this point; unborn HEAD is the
// reachable degenerate case).
func TestCollectChatContext_UnbornHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("-c", "init.defaultBranch=main", "init")

	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: dir}
	cfg := config.Defaults()

	// Must not panic; collectContext's error return is effectively unused
	// today (always nil), so this is a legitimate zero-value repo state —
	// ok must be true (see TestProjectChatContext_CollectionFailure for
	// the separately-tested ok==false branch).
	got, ok := collectChatContext(context.Background(), runner, &cfg, nil)
	if !ok {
		t.Fatal("collectChatContext ok = false, want true — unborn HEAD is a real, successfully-collected state")
	}

	if got.Detached {
		t.Errorf("unborn HEAD on a symbolic ref must not report detached: %+v", got)
	}
	if got.Ahead != 0 || got.Behind != 0 {
		t.Errorf("no upstream yet, want ahead=behind=0, got %+v", got)
	}
	if got.Dirty != (contextDirtyJSON{}) {
		t.Errorf("empty tree, want zero dirty counts, got %+v", got.Dirty)
	}
	if got.InProgress != nil || got.Base != nil {
		t.Errorf("nothing in progress, no base configured, got %+v", got)
	}
	if got.LatestTag != "" {
		t.Errorf("no tags yet, got %q", got.LatestTag)
	}

	// The tool/prompt path (marshal) must also succeed — this is what
	// actually backs REPO_CONTEXT and git_context; a chat session must be
	// able to start in this repo.
	raw, err := chatContextJSONString(context.Background(), runner, &cfg, nil)
	if err != nil {
		t.Fatalf("chatContextJSONString must not error on unborn HEAD: %v", err)
	}
	if raw == "" {
		t.Error("chatContextJSONString returned empty text")
	}
}

// TestCollectChatContext_NotAGitDirectory is the fully degenerate case: no
// .git at all. collectContext has no repo-ness precondition of its own (gk
// context's command handler gates via ensureGitRepo; the collector doesn't)
// — this pins that every field collapses to its zero value instead of the
// collector erroring, panicking, or hanging.
func TestCollectChatContext_NotAGitDirectory(t *testing.T) {
	dir := t.TempDir()
	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: dir}
	cfg := config.Defaults()

	got, ok := collectChatContext(context.Background(), runner, &cfg, nil)
	if !ok {
		t.Fatal("collectChatContext ok = false, want true — a non-git directory is a real, successfully-collected zero state, not a collection failure")
	}
	if got.Branch != "" {
		t.Errorf("branch = %q, want empty outside any repo", got.Branch)
	}
	if got.InProgress != nil || got.Base != nil {
		t.Errorf("got = %+v, want a fully empty context", got)
	}

	raw, err := chatContextJSONString(context.Background(), runner, &cfg, nil)
	if err != nil {
		t.Fatalf("chatContextJSONString must degrade, not error: %v", err)
	}
	if raw == "" {
		t.Error("chatContextJSONString returned empty text even in degrade mode")
	}
}

// TestBuildChatSystemPrompt_RedactsRepoContext pins the interface contract:
// REPO_CONTEXT must flow through redact before chat.SystemPrompt fences it,
// exactly like every tool result already does via Registry.Dispatch.
func TestBuildChatSystemPrompt_RedactsRepoContext(t *testing.T) {
	raw := `{"branch":"feature/hunter2-leak","ahead":1,"behind":0,"dirty":{"staged":0,"unstaged":0,"untracked":0,"conflicts":0}}`
	redact := func(s string) string {
		return strings.ReplaceAll(s, "hunter2", "[REDACTED]")
	}

	prompt := buildChatSystemPrompt(raw, "", redact, "ko", false)

	if strings.Contains(prompt, "hunter2") {
		t.Errorf("system prompt leaked unredacted repo context: %s", prompt)
	}
	if !strings.Contains(prompt, "[REDACTED]") {
		t.Errorf("system prompt missing the redacted marker: %s", prompt)
	}
	if !strings.Contains(prompt, "<REPO_CONTEXT>") || !strings.Contains(prompt, "</REPO_CONTEXT>") {
		t.Errorf("system prompt must fence REPO_CONTEXT as untrusted data: %s", prompt)
	}
}

// A nil redactor (only ever exercised in tests, mirroring Registry.redact's
// own nil-safe contract) must pass the text through unchanged rather than
// panicking.
func TestBuildChatSystemPrompt_NilRedactorPassesThrough(t *testing.T) {
	raw := `{"branch":"main"}`
	prompt := buildChatSystemPrompt(raw, "", nil, "en", false)
	if !strings.Contains(prompt, raw) {
		t.Errorf("nil redactor must pass repo context through unchanged: %s", prompt)
	}
}

// TestBuildChatSystemPrompt_EmptyRepoMapIsInvisible pins the DoD's
// "false/unset must leave existing behavior completely unchanged" contract
// one layer up from systemprompt_test.go's own version of this check: the
// empty string chatRepoMapString returns when ai.chat.auto_context is
// off/unset must produce a prompt with no REPO_MAP trace, byte-identical
// to the pre-repo-map buildChatSystemPrompt output for the same inputs.
func TestBuildChatSystemPrompt_EmptyRepoMapIsInvisible(t *testing.T) {
	raw := `{"branch":"main"}`
	withEmptyMap := buildChatSystemPrompt(raw, "", nil, "en", false)
	if strings.Contains(withEmptyMap, "REPO_MAP") {
		t.Errorf("empty repoMapRaw must not surface REPO_MAP: %s", withEmptyMap)
	}
}

// TestBuildChatSystemPrompt_RedactsRepoMap mirrors
// TestBuildChatSystemPrompt_RedactsRepoContext for the second untrusted
// input buildChatSystemPrompt now takes: repoMapRaw must flow through the
// exact same redact function before chat.SystemPrompt fences it, so a
// secret pattern accidentally caught in a tracked file's path can't leak
// into REPO_MAP unredacted.
func TestBuildChatSystemPrompt_RedactsRepoMap(t *testing.T) {
	mapRaw := "secrets/\n  hunter2.env\n"
	redact := func(s string) string {
		return strings.ReplaceAll(s, "hunter2", "[REDACTED]")
	}
	prompt := buildChatSystemPrompt("", mapRaw, redact, "en", false)
	if strings.Contains(prompt, "hunter2") {
		t.Errorf("system prompt leaked unredacted repo map: %s", prompt)
	}
	if !strings.Contains(prompt, "[REDACTED]") {
		t.Errorf("system prompt missing the redacted marker in repo map: %s", prompt)
	}
	if !strings.Contains(prompt, "<REPO_MAP>") || !strings.Contains(prompt, "</REPO_MAP>") {
		t.Errorf("system prompt must fence REPO_MAP as untrusted data: %s", prompt)
	}
}

// TestProjectChatContext_CollectionFailure is the F6 regression test:
// before projectChatContext existed, collectChatContext silently collapsed
// a collectContext failure to the exact same zero-value chatContextJSON a
// genuinely empty repo produces (see TestCollectChatContext_UnbornHEAD/
// _NotAGitDirectory, both real zero states with ok==true) — indistinguishable
// from "we checked and there's really no branch, nothing dirty, up to
// date." ok must come back false here so callers know this is "unknown",
// not "confirmed empty." collectContext itself never actually returns a
// non-nil error today (see its own docstring), so this drives
// projectChatContext directly with a synthetic failure — the only way to
// reach this branch without a real collectContext failure mode.
func TestProjectChatContext_CollectionFailure(t *testing.T) {
	proj, ok := projectChatContext(contextJSON{Branch: "should-be-discarded"}, errors.New("boom: simulated collection failure"))
	if ok {
		t.Error("projectChatContext ok = true, want false when collectContext errored")
	}
	if proj != (chatContextJSON{}) {
		t.Errorf("proj = %+v, want the zero value when ok is false (callers must not read it as real data)", proj)
	}
}

// TestChatContextJSONString_CollectionFailureReturnsError pins
// chatContextJSONString's side of the same contract: when the underlying
// collection fails, it must return an ERROR instead of silently encoding
// the zero value as if it were the real repo state. Both of
// chatContextJSONString's callers were already built to handle this
// correctly — chat.go's session-start path degrades REPO_CONTEXT to ""
// on any error (dropping the whole fenced section, same as an off/unset
// REPO_MAP), and Registry.Dispatch turns a returned error into an
// IsError tool result for the git_context tool-call path — so fixing the
// swallow at the source is enough to fix both.
func TestChatContextJSONString_CollectionFailureReturnsError(t *testing.T) {
	proj, ok := projectChatContext(contextJSON{}, errors.New("boom"))
	if ok {
		t.Fatal("test setup: projectChatContext must report ok=false for this case")
	}
	if proj != (chatContextJSON{}) {
		t.Fatalf("test setup: proj = %+v, want zero value", proj)
	}
	// encodeChatContext is chatContextJSONString's own marshal step,
	// exercised directly here with the same (proj, ok) shape
	// collectChatContext would have produced on a real failure.
	raw, err := encodeChatContext(proj, ok)
	if err == nil {
		t.Fatal("encodeChatContext must return an error when ok is false")
	}
	if raw != "" {
		t.Errorf("encodeChatContext text = %q, want empty alongside the error", raw)
	}
}

// TestCountChatDirty_RespectsDeny pins the v2 panel finding that
// REPO_CONTEXT's dirty counts leaked denied paths: a change confined to a
// deny_paths file must not bump any dirty bucket, or the counts become an
// existence oracle for exactly what the deny list hides (REPO_CONTEXT
// exposes counts only, no names, so `staged:1` with no visible file is
// both a contradiction and a leak).
func TestCountChatDirty_RespectsDeny(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("-c", "init.defaultBranch=main", "init")
	if err := os.MkdirAll(dir+"/secrets", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/README.md", []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	// The ONLY change is a denied untracked file.
	if err := os.WriteFile(dir+"/secrets/prod.env", []byte("TOKEN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &git.ExecRunner{Dir: dir}

	// Without deny globs the untracked file counts (baseline sanity).
	if d, ok := countChatDirty(context.Background(), runner, nil); !ok || d.Untracked != 1 {
		t.Fatalf("baseline: want untracked=1 ok=true, got %+v ok=%v", d, ok)
	}
	// With the deny glob it must vanish from every bucket.
	d, ok := countChatDirty(context.Background(), runner, []string{"secrets/**"})
	if !ok {
		t.Fatal("countChatDirty ok=false on a healthy repo, want true")
	}
	if d.Untracked != 0 || d.Staged != 0 || d.Unstaged != 0 || d.Conflicts != 0 {
		t.Errorf("denied-only change must leave dirty all-zero, got %+v", d)
	}
}

// TestCollectChatContext_DirtyRecomputeFailureDegrades pins the 3rd-panel
// M1 finding: when the deny-aware dirty recompute fails, collectChatContext
// must degrade the whole context (ok=false) rather than overwrite the
// collector's real counts with zeros — otherwise REPO_CONTEXT would assert
// a clean tree on a transient `git status` failure. A non-git directory
// makes `git status` fail while collectContext itself still degrades to a
// benign zero-value with ok=true, isolating the recompute path.
func TestCollectChatContext_DirtyRecomputeFailureDegrades(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir() // NOT a git repo → `git status` errors
	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: dir}
	cfg := config.Defaults()
	// deny globs non-empty → the recompute path runs and its failure must
	// surface as ok=false, not a fabricated clean tree.
	_, ok := collectChatContext(context.Background(), runner, &cfg, []string{"secrets/**"})
	if ok {
		t.Error("dirty recompute failed but collectChatContext reported ok=true — a git status failure must degrade, not assert clean")
	}
}
