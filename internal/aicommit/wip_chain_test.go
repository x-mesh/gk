package aicommit

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func TestCompileWIPPatternsDefaults(t *testing.T) {
	res, err := CompileWIPPatterns(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(defaultWIPPatterns) {
		t.Errorf("default count: want %d, got %d", len(defaultWIPPatterns), len(res))
	}
}

func TestCompileWIPPatternsAdditive(t *testing.T) {
	res, err := CompileWIPPatterns([]string{`^DRAFT\b`})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(defaultWIPPatterns)+1 {
		t.Errorf("patterns: want %d, got %d", len(defaultWIPPatterns)+1, len(res))
	}
	if !IsWIPSubject("DRAFT: still figuring this out", res) {
		t.Error("custom pattern not active")
	}
	if !IsWIPSubject("--wip-- on branch", res) {
		t.Error("default pattern lost after adding custom")
	}
}

func TestCompileWIPPatternsBadRegex(t *testing.T) {
	_, err := CompileWIPPatterns([]string{`^[unclosed`})
	if err == nil {
		t.Error("want error for invalid regex")
	}
}

func TestIsWIPSubjectMatches(t *testing.T) {
	pats, _ := CompileWIPPatterns(nil)
	cases := []struct {
		subject string
		want    bool
	}{
		{"--wip-- on improve-commit", true},
		{"wip: still tweaking edges", true},
		{"WIP", true},
		{"tmp", true},
		{"Temp commit", true},
		{"save before refactor", true},
		{"checkpoint after migration", true},
		{"fixup! original", true},
		{"squash! original", true},
		{"feat(switch): add interactive picker", false},
		{"chore: update lockfiles", false},
		{"docs: explain the deny_paths convention", false},
		// Subjects polluted with leading fence markers / whitespace —
		// fenced code blocks that earlier LLM runs leaked into the
		// commit subject. These should still be recognized as WIP so
		// the unwrap chain can pick them up. See wip_chain.go for the
		// `stripNoisePrefix` helper that normalizes before matching.
		{"``` WIP(memleak): release panels on last-tab close", true},
		{"  WIP(autoreply): serialize poller ticks", true},
		{"```fixup! original", true},
		{"`` WIP only-two-backticks", true},
		// Backticks AFTER the keyword should still not match — only
		// the leading-prefix case is forgiving.
		{"feat: ```code in subject``` WIP-ish", false},
	}
	for _, tc := range cases {
		got := IsWIPSubject(tc.subject, pats)
		if got != tc.want {
			t.Errorf("IsWIPSubject(%q) = %v, want %v", tc.subject, got, tc.want)
		}
	}
}

// ----- DetectWIPChain integration via FakeRunner -----

// chainRunner builds a FakeRunner that simulates a stack of commits.
// commits are passed newest-first (HEAD first).
func chainRunner(commits []chainCommit, branch string, _ string) *git.FakeRunner {
	resp := map[string]git.FakeResponse{}
	if branch != "" {
		resp["rev-parse --abbrev-ref HEAD"] = git.FakeResponse{Stdout: branch + "\n"}
	}
	for i, c := range commits {
		ref := "HEAD~" + itoaSimple(i)
		resp["log -1 --format=%s "+ref] = git.FakeResponse{Stdout: c.Subject + "\n"}
		resp["rev-parse "+ref] = git.FakeResponse{Stdout: c.SHA + "\n"}
		parents := c.SHA + "-parent"
		if c.Merge {
			parents += " " + c.SHA + "-parent2"
		}
		if c.Root {
			parents = "" // root commit has no parents
		}
		resp["log -1 --format=%P "+ref] = git.FakeResponse{Stdout: parents + "\n"}
		// branch -r --contains: empty stdout → not pushed; non-empty → pushed
		if c.Pushed {
			resp["branch -r --contains "+c.SHA] = git.FakeResponse{Stdout: "  origin/foo\n"}
		} else {
			resp["branch -r --contains "+c.SHA] = git.FakeResponse{}
		}
		// File lookup — keep fixtures readable (tab/newline separated)
		// and convert to the -z form (NUL separated) the parser expects.
		nameStatus := tabFixtureToNUL(c.NameStatus)
		if c.Root {
			resp["diff-tree --root -z --name-status --no-commit-id -r "+c.SHA] = git.FakeResponse{Stdout: nameStatus}
		} else {
			resp["diff -z --name-status "+c.SHA+"^ "+c.SHA] = git.FakeResponse{Stdout: nameStatus}
		}
	}
	return &git.FakeRunner{Responses: resp}
}

// tabFixtureToNUL converts a human-readable tab/newline-separated
// `--name-status` fixture into the NUL-separated form `-z` produces.
// Records: status\x00path\x00 (or status\x00src\x00dst\x00 for
// rename/copy). The fixture form `M\ta.go\nM\tb.go\n` becomes
// `M\x00a.go\x00M\x00b.go\x00`.
func tabFixtureToNUL(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\t", "\x00")
	s = strings.ReplaceAll(s, "\n", "\x00")
	return s
}

type chainCommit struct {
	SHA        string
	Subject    string
	NameStatus string // tab-separated, like git diff --name-status output
	Merge      bool
	Pushed     bool
	Root       bool // true for parentless (initial) commit
}

func itoaSimple(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

func TestDetectWIPChainSingle(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: tweaking", NameStatus: "M\tinternal/cli/switch.go"},
		{SHA: "bbb", Subject: "feat(switch): add picker", NameStatus: "A\tinternal/cli/switch.go"},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, _, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 {
		t.Fatalf("want 1 commit in chain, got %d", len(chain))
	}
	if chain[0].SHA != "aaa" {
		t.Errorf("chain[0].SHA: %q", chain[0].SHA)
	}
}

func TestDetectWIPChainMultiple(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: still", NameStatus: "M\ta.go\n"},
		{SHA: "bbb", Subject: "tmp", NameStatus: "M\ta.go\nM\tb.go\n"},
		{SHA: "ccc", Subject: "wip: start", NameStatus: "A\ta.go\n"},
		{SHA: "ddd", Subject: "feat: real commit", NameStatus: ""},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, _, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("want 3 commits in chain, got %d (subjects: %v)", len(chain), chainSubjects(chain))
	}
	if chain[0].SHA != "aaa" || chain[2].SHA != "ccc" {
		t.Errorf("chain order wrong: %v", chainSubjects(chain))
	}
}

// TestDetectWIPChainPollutedSubjects reproduces the term-mesh
// 2026-05-26 case where leading fenced-code-block markers in subjects
// (left over from earlier LLM output) prevented WIP detection. Before
// stripNoisePrefix the chain walk stopped at HEAD because "``` WIP(...)"
// failed the `^[Ww][Ii][Pp]\b` anchor.
func TestDetectWIPChainPollutedSubjects(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "``` WIP(memleak): release panels on last-tab close", NameStatus: "M\ta.go"},
		{SHA: "bbb", Subject: "WIP(autoreply): serialize poller ticks", NameStatus: "M\tb.go"},
		{SHA: "ccc", Subject: "``` WIP(surface-lease): annotate close path ", NameStatus: "M\tc.go"},
		{SHA: "ddd", Subject: "feat: real commit before WIPs", NameStatus: ""},
	}
	r := chainRunner(commits, "develop", "origin/develop")
	pats, _ := CompileWIPPatterns(nil)

	chain, _, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("want 3 commits in chain (HEAD-polluted subjects), got %d: %v",
			len(chain), chainSubjects(chain))
	}
	if chain[0].SHA != "aaa" || chain[2].SHA != "ccc" {
		t.Errorf("chain order wrong: %v", chainSubjects(chain))
	}
}

func TestDetectWIPChainStopsAtNonWIP(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "feat: real", NameStatus: ""},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, reason, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if len(chain) != 0 {
		t.Errorf("non-WIP HEAD must yield empty chain, got %v", chainSubjects(chain))
	}
	if reason != StopReasonNonWIPSubject {
		t.Errorf("stop reason: want %q, got %q", StopReasonNonWIPSubject, reason)
	}
}

// TestDetectWIPChainDetachedHEAD — the H1 fix from x-review.
// Detached HEAD must refuse outright: rev-parse --abbrev-ref returns
// the literal "HEAD", and resetting a detached pointer rewinds with
// no recovery branch.
func TestDetectWIPChainDetachedHEAD(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: bad", NameStatus: "M\ta.go\n"},
	}
	r := chainRunner(commits, "HEAD", "")
	pats, _ := CompileWIPPatterns(nil)

	chain, _, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 0 {
		t.Errorf("detached HEAD must yield empty chain, got %v", chainSubjects(chain))
	}
}

// TestDetectWIPChainOnMainBranchUnpushed — protected branch names
// (main/master/develop/trunk) used to refuse the chain outright. After
// the v0.55+ rework the per-commit push gate is the only safety net,
// so a fully-local WIP stack on `main` must still be detected.
func TestDetectWIPChainOnMainBranchUnpushed(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: local on main", NameStatus: "M\ta.go\n"},
		{SHA: "bbb", Subject: "feat: real commit", NameStatus: ""},
	}
	r := chainRunner(commits, "main", "")
	pats, _ := CompileWIPPatterns(nil)

	chain, reason, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{
		Patterns: pats,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 || chain[0].SHA != "aaa" {
		t.Errorf("main + unpushed WIP must be detected; got %v", chainSubjects(chain))
	}
	if reason != StopReasonNonWIPSubject {
		t.Errorf("stop reason: want %q, got %q", StopReasonNonWIPSubject, reason)
	}
}

// TestDetectWIPChainRootCommit — the H3 fix from x-review.
// A WIP commit at the repo root (no parent) must NOT abort the flow;
// wipChainFiles falls back to `git diff-tree --root`.
func TestDetectWIPChainRootCommit(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: initial", NameStatus: "A\tREADME.md\n", Root: true},
	}
	r := chainRunner(commits, "improve", "")
	pats, _ := CompileWIPPatterns(nil)

	chain, _, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if err != nil {
		t.Fatalf("root commit must NOT abort: %v", err)
	}
	if len(chain) != 1 {
		t.Errorf("want chain of 1 (root commit), got %d", len(chain))
	}
}

// TestDetectWIPChainAllowPushedBypassesGate — `gk commit --force-wip`
// sets AllowPushed=true so the walk continues even when the next
// commit is already on a remote. The chain length grows to include the
// pushed commit; StopReason should NOT be "pushed".
func TestDetectWIPChainAllowPushedBypassesGate(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: local", NameStatus: "M\ta.go\n", Pushed: false},
		{SHA: "bbb", Subject: "wip: pushed", NameStatus: "M\tb.go\n", Pushed: true},
		{SHA: "ccc", Subject: "feat: real", NameStatus: ""},
	}
	r := chainRunner(commits, "develop", "origin/develop")
	pats, _ := CompileWIPPatterns(nil)

	chain, reason, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{
		Patterns:    pats,
		AllowPushed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 {
		t.Fatalf("AllowPushed=true must include the pushed WIP; got %v", chainSubjects(chain))
	}
	if reason != StopReasonNonWIPSubject {
		t.Errorf("stop reason: want %q, got %q", StopReasonNonWIPSubject, reason)
	}
}

func TestDetectWIPChainStopsAtPushedCommit(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: local", NameStatus: "M\ta.go\n", Pushed: false},
		{SHA: "bbb", Subject: "wip: pushed", NameStatus: "M\tb.go\n", Pushed: true},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, reason, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if len(chain) != 1 {
		t.Errorf("want chain to stop at pushed commit (length 1), got %d", len(chain))
	}
	if reason != StopReasonPushed {
		t.Errorf("stop reason: want %q, got %q", StopReasonPushed, reason)
	}
}

// TestDetectWIPChainPushedAtHEADReportsReason — when even the HEAD WIP
// commit is already on a remote, chain is empty but StopReason must be
// "pushed" so the CLI can prompt the user with --force-wip.
func TestDetectWIPChainPushedAtHEADReportsReason(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: already pushed", NameStatus: "M\ta.go\n", Pushed: true},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, reason, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if len(chain) != 0 {
		t.Errorf("HEAD already pushed must yield empty chain; got %v", chainSubjects(chain))
	}
	if reason != StopReasonPushed {
		t.Errorf("stop reason: want %q, got %q", StopReasonPushed, reason)
	}
}

func TestDetectWIPChainStopsAtMergeCommit(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: foo", NameStatus: "M\ta.go\n"},
		{SHA: "bbb", Subject: "wip: merge wonk", NameStatus: "", Merge: true},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, reason, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if len(chain) != 1 {
		t.Errorf("merge commit must stop chain (length 1), got %d", len(chain))
	}
	if reason != StopReasonMergeCommit {
		t.Errorf("stop reason: want %q, got %q", StopReasonMergeCommit, reason)
	}
}

func TestDetectWIPChainRespectsMaxChain(t *testing.T) {
	commits := []chainCommit{
		{SHA: "a1", Subject: "wip 1", NameStatus: "M\tx.go\n"},
		{SHA: "a2", Subject: "wip 2", NameStatus: "M\tx.go\n"},
		{SHA: "a3", Subject: "wip 3", NameStatus: "M\tx.go\n"},
		{SHA: "a4", Subject: "wip 4", NameStatus: "M\tx.go\n"},
		{SHA: "a5", Subject: "wip 5", NameStatus: "M\tx.go\n"},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, reason, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{
		Patterns: pats,
		MaxChain: 3,
	})
	if len(chain) != 3 {
		t.Errorf("MaxChain=3, got %d", len(chain))
	}
	if reason != StopReasonMaxChain {
		t.Errorf("stop reason: want %q, got %q", StopReasonMaxChain, reason)
	}
}

// TestChainNetFilesNetZero — a chain whose commits cancel each other
// (WIP2 reverted WIP1's edit) has an empty HEAD~N→HEAD diff. The old
// per-commit union (MergeChainFiles) still listed the path here, which
// made the AI plan a commit that apply could never create ("nothing to
// commit, working tree clean" after the unwrap reset).
func TestChainNetFilesNetZero(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"diff -z --name-status HEAD~2 HEAD": {Stdout: ""},
	}}
	out, err := ChainNetFiles(context.Background(), r, 2)
	if err != nil {
		t.Fatalf("ChainNetFiles: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("net-zero chain must yield no files; got %+v", out)
	}
}

// TestChainNetFilesStatuses — the net diff parses through the same
// -z name-status reader as the per-commit path: statuses map A/D/R to
// added/deleted/renamed and a rename keeps the destination as Path.
func TestChainNetFilesStatuses(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"diff -z --name-status HEAD~3 HEAD": {
			Stdout: "M\x00a.go\x00A\x00b.go\x00R100\x00old.go\x00new.go\x00",
		},
	}}
	out, err := ChainNetFiles(context.Background(), r, 3)
	if err != nil {
		t.Fatalf("ChainNetFiles: %v", err)
	}
	want := []FileChange{
		{Path: "a.go", Status: "modified"},
		{Path: "b.go", Status: "added"},
		{Path: "new.go", Status: "renamed"},
	}
	if len(out) != len(want) {
		t.Fatalf("want %d files, got %+v", len(want), out)
	}
	for i, w := range want {
		if out[i].Path != w.Path || out[i].Status != w.Status {
			t.Errorf("[%d] = %+v, want %+v", i, out[i], w)
		}
	}
}

// TestChainNetFilesDiffError — a failing diff (e.g. HEAD~N does not
// exist) surfaces as an error instead of an empty plan.
func TestChainNetFilesDiffError(t *testing.T) {
	r := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"diff -z --name-status HEAD~2 HEAD": {ExitCode: 128, Stderr: "bad revision"},
	}}
	if _, err := ChainNetFiles(context.Background(), r, 2); err == nil {
		t.Fatal("want error from failing diff, got nil")
	}
}

func chainSubjects(c []WIPCommit) []string {
	out := make([]string, len(c))
	for i, x := range c {
		out[i] = x.Subject
	}
	return out
}

// Sanity: parseWIPDiffNameStatus handles the common cases.
// Records are NUL-separated (`-z` form). Status, then path; rename
// and copy emit a destination as a third token.
func TestParseWIPDiffNameStatus(t *testing.T) {
	in := "M\x00internal/foo.go\x00A\x00new.go\x00D\x00old.go\x00"
	out := parseWIPDiffNameStatus(in)
	if len(out) != 3 {
		t.Fatalf("want 3 entries, got %d", len(out))
	}
	want := []string{"modified", "added", "deleted"}
	for i, w := range want {
		if out[i].Status != w {
			t.Errorf("[%d] Status: want %q, got %q", i, w, out[i].Status)
		}
		// L1.1 fix — chain files are NOT staged after the mixed reset
		// that follows. Staged must be false.
		if out[i].Staged {
			t.Errorf("[%d] Staged: must be false post-reset", i)
		}
	}
	if !strings.Contains(out[0].Path, "foo.go") {
		t.Errorf("Path: %q", out[0].Path)
	}
}

// TestParseWIPDiffNameStatusRename — rename emits 3 tokens; the parser
// must keep the destination (not the source) as the canonical Path.
func TestParseWIPDiffNameStatusRename(t *testing.T) {
	in := "R100\x00old/path.go\x00new/path.go\x00M\x00other.go\x00"
	out := parseWIPDiffNameStatus(in)
	if len(out) != 2 {
		t.Fatalf("want 2 entries (rename + modify), got %d: %+v", len(out), out)
	}
	if out[0].Status != "renamed" || out[0].Path != "new/path.go" {
		t.Errorf("rename entry: %+v", out[0])
	}
	if out[1].Status != "modified" || out[1].Path != "other.go" {
		t.Errorf("modify entry: %+v", out[1])
	}
}

// TestParseWIPDiffNameStatusTabInPath — paths containing tabs (which
// `core.quotepath=true` would mangle) survive intact under -z.
func TestParseWIPDiffNameStatusTabInPath(t *testing.T) {
	in := "M\x00weird\tname.go\x00"
	out := parseWIPDiffNameStatus(in)
	if len(out) != 1 || out[0].Path != "weird\tname.go" {
		t.Errorf("tab-in-path lost: %+v", out)
	}
}
