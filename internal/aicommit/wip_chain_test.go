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
		// File lookup — use diff or diff-tree depending on whether this
		// is a root commit.
		if c.Root {
			resp["diff-tree --root --name-status --no-commit-id -r "+c.SHA] = git.FakeResponse{Stdout: c.NameStatus}
		} else {
			resp["diff --name-status "+c.SHA+"^ "+c.SHA] = git.FakeResponse{Stdout: c.NameStatus}
		}
	}
	return &git.FakeRunner{Responses: resp}
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

	chain, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
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

	chain, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
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

func TestDetectWIPChainStopsAtNonWIP(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "feat: real", NameStatus: ""},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if len(chain) != 0 {
		t.Errorf("non-WIP HEAD must yield empty chain, got %v", chainSubjects(chain))
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

	chain, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 0 {
		t.Errorf("detached HEAD must yield empty chain, got %v", chainSubjects(chain))
	}
}

// TestDetectWIPChainProtectedFallback — the M1 fix from x-review.
// Empty ProtectedBranches falls back to {main, master, develop, trunk}
// rather than disabling the guard.
func TestDetectWIPChainProtectedFallback(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: bad", NameStatus: "M\ta.go\n"},
	}
	r := chainRunner(commits, "main", "")
	pats, _ := CompileWIPPatterns(nil)

	chain, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{
		Patterns:          pats,
		ProtectedBranches: nil, // user mis-configured to empty
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 0 {
		t.Errorf("empty ProtectedBranches must fall back to built-in list; got %v", chainSubjects(chain))
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

	chain, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if err != nil {
		t.Fatalf("root commit must NOT abort: %v", err)
	}
	if len(chain) != 1 {
		t.Errorf("want chain of 1 (root commit), got %d", len(chain))
	}
}

func TestDetectWIPChainProtectedBranch(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: bad", NameStatus: "M\ta.go\n"},
	}
	r := chainRunner(commits, "main", "origin/main")
	pats, _ := CompileWIPPatterns(nil)

	chain, err := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{
		Patterns:          pats,
		ProtectedBranches: []string{"main", "develop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 0 {
		t.Errorf("protected branch must yield empty chain, got %v", chainSubjects(chain))
	}
}

func TestDetectWIPChainStopsAtPushedCommit(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: local", NameStatus: "M\ta.go\n", Pushed: false},
		{SHA: "bbb", Subject: "wip: pushed", NameStatus: "M\tb.go\n", Pushed: true},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if len(chain) != 1 {
		t.Errorf("want chain to stop at pushed commit (length 1), got %d", len(chain))
	}
}

func TestDetectWIPChainStopsAtMergeCommit(t *testing.T) {
	commits := []chainCommit{
		{SHA: "aaa", Subject: "wip: foo", NameStatus: "M\ta.go\n"},
		{SHA: "bbb", Subject: "wip: merge wonk", NameStatus: "", Merge: true},
	}
	r := chainRunner(commits, "improve", "origin/improve")
	pats, _ := CompileWIPPatterns(nil)

	chain, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{Patterns: pats})
	if len(chain) != 1 {
		t.Errorf("merge commit must stop chain (length 1), got %d", len(chain))
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

	chain, _ := DetectWIPChain(context.Background(), r, DetectWIPChainOptions{
		Patterns: pats,
		MaxChain: 3,
	})
	if len(chain) != 3 {
		t.Errorf("MaxChain=3, got %d", len(chain))
	}
}

func TestMergeChainFilesAddThenModify(t *testing.T) {
	// HEAD~1 added a.go, HEAD modified it. Net effect: file is new
	// with current content → "added".
	chain := []WIPCommit{
		{SHA: "newer", Files: []FileChange{{Path: "a.go", Status: "modified", Staged: true}}},
		{SHA: "older", Files: []FileChange{{Path: "a.go", Status: "added", Staged: true}}},
	}
	out := MergeChainFiles(chain)
	if len(out) != 1 {
		t.Fatalf("want 1 file, got %+v", out)
	}
	if out[0].Status != "added" {
		t.Errorf("Status: want %q, got %q", "added", out[0].Status)
	}
}

// TestMergeChainFilesAddThenDeleteCancels — the M2 fix from x-review.
// Adding a file in HEAD~1 and deleting it in HEAD produces no net
// change; the merged list must omit the path entirely (avoid phantom
// entries that confuse the AI plan).
func TestMergeChainFilesAddThenDeleteCancels(t *testing.T) {
	chain := []WIPCommit{
		{SHA: "newer", Files: []FileChange{{Path: "tmp.txt", Status: "deleted", Staged: true}}},
		{SHA: "older", Files: []FileChange{{Path: "tmp.txt", Status: "added", Staged: true}}},
	}
	out := MergeChainFiles(chain)
	if len(out) != 0 {
		t.Errorf("add+delete must cancel; got %+v", out)
	}
}

func TestMergeChainFilesModifyThenDelete(t *testing.T) {
	// File existed pre-chain (modified in HEAD~1), then deleted in HEAD
	// → net effect is "deleted".
	chain := []WIPCommit{
		{SHA: "newer", Files: []FileChange{{Path: "a.go", Status: "deleted", Staged: true}}},
		{SHA: "older", Files: []FileChange{{Path: "a.go", Status: "modified", Staged: true}}},
	}
	out := MergeChainFiles(chain)
	if len(out) != 1 || out[0].Status != "deleted" {
		t.Errorf("modify+delete: want deleted; got %+v", out)
	}
}

func TestMergeChainFilesDeleteThenAdd(t *testing.T) {
	// File existed pre-chain (deleted in HEAD~1), then re-added in
	// HEAD → net effect is "modified" (existed before, exists now).
	chain := []WIPCommit{
		{SHA: "newer", Files: []FileChange{{Path: "a.go", Status: "added", Staged: true}}},
		{SHA: "older", Files: []FileChange{{Path: "a.go", Status: "deleted", Staged: true}}},
	}
	out := MergeChainFiles(chain)
	if len(out) != 1 || out[0].Status != "modified" {
		t.Errorf("delete+add: want modified; got %+v", out)
	}
}

func TestMergeChainFilesEmpty(t *testing.T) {
	out := MergeChainFiles(nil)
	if len(out) != 0 {
		t.Errorf("nil chain → empty result; got %+v", out)
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
func TestParseWIPDiffNameStatus(t *testing.T) {
	in := "M\tinternal/foo.go\nA\tnew.go\nD\told.go\n"
	out := parseWIPDiffNameStatus(in)
	if len(out) != 3 {
		t.Fatalf("want 3 entries, got %d", len(out))
	}
	want := []string{"modified", "added", "deleted"}
	for i, w := range want {
		if out[i].Status != w {
			t.Errorf("[%d] Status: want %q, got %q", i, w, out[i].Status)
		}
		if !out[i].Staged {
			t.Errorf("[%d] Staged: must be true", i)
		}
	}
	// Path correct
	if !strings.Contains(out[0].Path, "foo.go") {
		t.Errorf("Path: %q", out[0].Path)
	}
}
