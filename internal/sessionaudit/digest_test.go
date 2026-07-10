package sessionaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dAsst builds an assistant record like asst (turns_test.go) but JSON-encodes
// the command, so multi-line fixtures (heredocs) stay valid JSONL.
func dAsst(msgID, toolID, cmd string) string {
	b, err := json.Marshal(cmd)
	if err != nil {
		panic(err)
	}
	return `{"type":"assistant","message":{"id":"` + msgID + `","role":"assistant","content":[{"type":"tool_use","id":"` + toolID + `","name":"Bash","input":{"command":` + string(b) + `}}]}}`
}

// codexCall builds a Codex exec_command record with an optional workdir.
func codexCall(callID, cmd, workdir string) string {
	args := map[string]string{"cmd": cmd}
	if workdir != "" {
		args["workdir"] = workdir
	}
	inner, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	outer, err := json.Marshal(string(inner))
	if err != nil {
		panic(err)
	}
	return `{"payload":{"type":"function_call","name":"exec_command","arguments":` + string(outer) + `,"call_id":"` + callID + `"}}`
}

func codexOutput(callID string, exitCode int) string {
	return fmt.Sprintf(`{"payload":{"type":"function_call_output","call_id":"%s","output":"Process exited with code %d"}}`, callID, exitCode)
}

func writeDigestFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, session(lines...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDigestFile_Claude_ReposBranchesCommits(t *testing.T) {
	path := writeDigestFixture(t,
		dAsst("m1", "t1", "cd /work/repoA && git status"),
		result("t1", false),
		dAsst("m2", "t2", "git -C /work/repoA checkout -b feature/x"),
		result("t2", false),
		dAsst("m3", "t3", "git -C /work/repoB switch develop"),
		result("t3", false),
		dAsst("m4", "t4", "cd /work/repoA && git add a.go && git commit -m \"feat: add thing\""),
		result("t4", false),
		dAsst("m5", "t5", "cd /work/repoA && git commit -am \"fix: follow-up\""),
		result("t5", false),
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Schema != digestSchema || d.File != path || d.Source != "claude" {
		t.Fatalf("header = %+v", d)
	}
	if d.Turns != 5 || d.Commands != 5 {
		t.Fatalf("turns/commands = %d/%d, want 5/5", d.Turns, d.Commands)
	}
	if len(d.Repos) != 2 || d.Repos[0].Path != "/work/repoA" || d.Repos[0].Commands != 4 {
		t.Fatalf("repos must be most-active first: %+v", d.Repos)
	}
	if want := []string{"feature/x", "develop"}; !equalStrings(d.Branches, want) {
		t.Fatalf("branches = %v, want %v", d.Branches, want)
	}
	if d.CommitCount != 2 {
		t.Fatalf("commit count = %d, want 2", d.CommitCount)
	}
	if want := []string{"feat: add thing", "fix: follow-up"}; !equalStrings(d.Commits, want) {
		t.Fatalf("commits = %v, want %v", d.Commits, want)
	}
}

func TestDigestFile_HeredocBodyNeverLeaks(t *testing.T) {
	const secret = "SECRET-HEREDOC-BODY-TOKEN"
	commitCmd := "git commit -m \"$(cat <<'EOF'\nfix(core): heredoc subject line\n\n" + secret + "\nEOF\n)\""
	applyCmd := "git apply <<'PATCH'\ndiff --git a/x b/x\n" + secret + "\nPATCH"
	path := writeDigestFixture(t,
		dAsst("m1", "t1", commitCmd),
		result("t1", false),
		dAsst("m2", "t2", applyCmd),
		result("t2", true),
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Commits) != 1 || d.Commits[0] != "fix(core): heredoc subject line" {
		t.Fatalf("commit subject = %v, want the first heredoc line only", d.Commits)
	}
	if d.Unfinished == nil {
		t.Fatal("errored final git apply must raise the unfinished signal")
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("digest leaked a heredoc body:\n%s", b)
	}
}

func TestDigestFile_IntegrationErroredAndUnfinished(t *testing.T) {
	path := writeDigestFixture(t,
		dAsst("m1", "t1", "git pull --rebase"),
		result("t1", false),
		dAsst("m2", "t2", "GK_AGENT=1 git-kit merge feature/x"),
		result("t2", false),
		dAsst("m3", "t3", "git push origin main"),
		result("t3", true),
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	it := d.Integration
	if it == nil || it.Attempts != 3 || it.Errored != 1 {
		t.Fatalf("integration = %+v, want 3 attempts / 1 errored", it)
	}
	if it.Verbs["pull"] != 1 || it.Verbs["merge"] != 1 || it.Verbs["push"] != 1 {
		t.Fatalf("verbs = %v", it.Verbs)
	}
	if !strings.Contains(it.LastError, "git push") {
		t.Fatalf("last error = %q, want the errored push", it.LastError)
	}
	if d.Unfinished == nil || !strings.Contains(d.Unfinished.Command, "git push") {
		t.Fatalf("unfinished = %+v, want the final errored push", d.Unfinished)
	}
}

func TestDigestFile_EarlyErrorIsNotUnfinished(t *testing.T) {
	lines := []string{
		dAsst("m0", "t0", "git push origin main"),
		result("t0", true),
	}
	// Enough successful trailing turns to push the failure out of the window.
	for i := 0; i < digestUnfinishedWindow+1; i++ {
		id := fmt.Sprintf("t%d", i+1)
		lines = append(lines, dAsst(fmt.Sprintf("m%d", i+1), id, "git commit -m ok"), result(id, false))
	}
	path := writeDigestFixture(t, lines...)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Unfinished != nil {
		t.Fatalf("failure moved past by later work must not signal unfinished: %+v", d.Unfinished)
	}
}

// A failed mutating command whose retry landed is resolved state, not
// unfinished work — the doc contract is "nothing after it that resolved the
// state", and a resuming agent must not re-fix a push that already landed.
func TestDigestFile_UnfinishedClearedByLaterSuccessfulRetry(t *testing.T) {
	path := writeDigestFixture(t,
		dAsst("m1", "t1", "git push"),
		result("t1", true), // stale remote ref
		dAsst("m2", "t2", "git pull --rebase"),
		result("t2", false),
		dAsst("m3", "t3", "git push"),
		result("t3", false), // the retry landed
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Unfinished != nil {
		t.Fatalf("a failure resolved by a later successful mutation must not signal unfinished: %+v", d.Unfinished)
	}
}

// A later successful PROBE resolves nothing — only a mutating command clears
// the candidate.
func TestDigestFile_UnfinishedSurvivesLaterProbe(t *testing.T) {
	path := writeDigestFixture(t,
		dAsst("m1", "t1", "git push"),
		result("t1", true),
		dAsst("m2", "t2", "git status"),
		result("t2", false),
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Unfinished == nil || !strings.Contains(d.Unfinished.Command, "git push") {
		t.Fatalf("a trailing read-only probe must not clear the signal: %+v", d.Unfinished)
	}
}

// A failed git-kit mutating verb at the session's end is unfinished work —
// the integration counter already tracks it, and the headline signal must not
// disagree for exactly the commands gk tells agents to use.
func TestDigestFile_UnfinishedGitKitVerb(t *testing.T) {
	path := writeDigestFixture(t,
		dAsst("m1", "t1", "git commit -m \"feat: work\""),
		result("t1", false),
		dAsst("m2", "t2", "GK_AGENT=1 git-kit land"),
		result("t2", true), // push rejected
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Integration == nil || d.Integration.Errored != 1 {
		t.Fatalf("integration = %+v, want the errored land counted", d.Integration)
	}
	if d.Unfinished == nil || !strings.Contains(d.Unfinished.Command, "git-kit land") {
		t.Fatalf("unfinished = %+v, want the final errored git-kit land", d.Unfinished)
	}
}

func TestDigestFile_ReprobeGroupsWithGkCollapse(t *testing.T) {
	path := writeDigestFixture(t,
		dAsst("m1", "t1", "git status"),
		result("t1", false),
		dAsst("m2", "t2", "git log --oneline -5"),
		result("t2", false),
		dAsst("m3", "t3", "git status --short"),
		result("t3", false),
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Reprobes) != 1 {
		t.Fatalf("reprobes = %+v, want one context group", d.Reprobes)
	}
	r := d.Reprobes[0]
	if r.Group != "context" || r.TurnsSaved != 2 || r.GkCommand != "git-kit context" {
		t.Fatalf("reprobe = %+v", r)
	}
}

func TestDigestFile_Codex_WorkdirAndErrorJoin(t *testing.T) {
	path := writeDigestFixture(t,
		codexCall("c1", "git checkout -b feat/codex", "/work/repoC"),
		codexOutput("c1", 0),
		codexCall("c2", "git commit -m \"chore: codex commit\"", "/work/repoC"),
		codexOutput("c2", 0),
		codexCall("c3", "git rebase origin/main", "/work/repoC"),
		codexOutput("c3", 1),
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "codex" {
		t.Fatalf("source = %q, want codex (sniffed)", d.Source)
	}
	if len(d.Repos) != 1 || d.Repos[0].Path != "/work/repoC" || d.Repos[0].Commands != 3 {
		t.Fatalf("repos = %+v", d.Repos)
	}
	if !equalStrings(d.Branches, []string{"feat/codex"}) {
		t.Fatalf("branches = %v", d.Branches)
	}
	if !equalStrings(d.Commits, []string{"chore: codex commit"}) {
		t.Fatalf("commits = %v", d.Commits)
	}
	it := d.Integration
	if it == nil || it.Attempts != 1 || it.Errored != 1 || !strings.Contains(it.LastError, "git rebase") {
		t.Fatalf("integration = %+v, want the errored rebase", it)
	}
	if d.Unfinished == nil || !strings.Contains(d.Unfinished.Command, "git rebase") {
		t.Fatalf("unfinished = %+v", d.Unfinished)
	}
}

func TestDigestFile_CommitCapKeepsMostRecent(t *testing.T) {
	total := digestMaxCommits + 3
	lines := make([]string, 0, total*2)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("t%d", i)
		lines = append(lines,
			dAsst(fmt.Sprintf("m%d", i), id, fmt.Sprintf("git commit -m \"feat: change %d\"", i)),
			result(id, false),
		)
	}
	path := writeDigestFixture(t, lines...)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.CommitCount != total {
		t.Fatalf("commit count = %d, want %d", d.CommitCount, total)
	}
	if len(d.Commits) != digestMaxCommits {
		t.Fatalf("commits = %d entries, want cap %d", len(d.Commits), digestMaxCommits)
	}
	if want := fmt.Sprintf("feat: change %d", total-1); d.Commits[len(d.Commits)-1] != want {
		t.Fatalf("last commit = %q, want most recent %q", d.Commits[len(d.Commits)-1], want)
	}
	if d.Commits[0] != fmt.Sprintf("feat: change %d", total-digestMaxCommits) {
		t.Fatalf("cap must drop the OLDEST subjects, got first = %q", d.Commits[0])
	}
}

func TestDigestFile_TruncatesLongCommandText(t *testing.T) {
	long := "git push origin " + strings.Repeat("x", digestTruncateLen*2)
	path := writeDigestFixture(t,
		dAsst("m1", "t1", long),
		result("t1", true),
	)

	d, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Integration == nil || d.Integration.LastError == "" {
		t.Fatalf("integration = %+v", d.Integration)
	}
	if got := len(d.Integration.LastError); got > digestTruncateLen+len("…") {
		t.Fatalf("last error = %d bytes, want <= %d", got, digestTruncateLen+len("…"))
	}
	if !strings.HasSuffix(d.Integration.LastError, "…") {
		t.Fatalf("truncation must be marked: %q", d.Integration.LastError)
	}
}

func TestNewestSessionFile_PicksNewest(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "projects", "-work-p")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(root, "older.jsonl")
	newer := filepath.Join(root, "newer.jsonl")
	for _, p := range []string{older, newer} {
		if err := os.WriteFile(p, session(dAsst("m1", "t1", "git status")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	got, err := NewestSessionFile(home)
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Fatalf("newest = %q, want %q", got, newer)
	}
}

func TestNewestSessionFile_EmptyRoots(t *testing.T) {
	if _, err := NewestSessionFile(t.TempDir()); err == nil {
		t.Fatal("empty roots must error")
	}
	if _, err := NewestSessionFile(""); err == nil {
		t.Fatal("missing home must error")
	}
}

// n=2 skips the newest file — from inside a live agent session the newest is
// the caller's own transcript, and the handoff use case wants the one before.
func TestNthNewestSessionFile(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "projects", "-work-p")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(root, "previous.jsonl")
	live := filepath.Join(root, "live.jsonl")
	for _, p := range []string{previous, live} {
		if err := os.WriteFile(p, session(dAsst("m1", "t1", "git status")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(previous, past, past); err != nil {
		t.Fatal(err)
	}

	got, err := NthNewestSessionFile(home, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != previous {
		t.Fatalf("n=2 = %q, want %q", got, previous)
	}
	if _, err := NthNewestSessionFile(home, 3); err == nil {
		t.Fatal("n beyond the corpus must error, not silently fall back")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
