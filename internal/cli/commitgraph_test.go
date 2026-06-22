package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

// --- ③ central detection + decoration -----------------------------------

func TestIsCommitGraphCorruptError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"full phrase string err", errors.New("fatal: invalid commit position. commit-graph is likely corrupt"), true},
		{"position phrase only", errors.New("error: invalid commit position"), true},
		{"corrupt phrase only via stderr", &git.ExitError{Code: 128, Stderr: "fatal: commit-graph is likely corrupt"}, true},
		{"wrapped exit error", fmt.Errorf("rebase main: %w", &git.ExitError{Code: 128, Stderr: "fatal: invalid commit position. commit-graph is likely corrupt"}), true},
		{"unrelated", errors.New("merge conflict in foo.go"), false},
		{"unrelated exit error", &git.ExitError{Code: 1, Stderr: "fatal: not a git repository"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCommitGraphCorruptError(tc.err); got != tc.want {
				t.Errorf("isCommitGraphCorruptError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDecorateRawGitError_CommitGraph(t *testing.T) {
	raw := &git.ExitError{Code: 128, Args: []string{"rebase", "main"}, Stderr: "fatal: invalid commit position. commit-graph is likely corrupt"}

	dec := decorateRawGitError(raw)

	// A doctor remedy is attached…
	r := RemediesFrom(dec)
	if len(r) != 1 || r[0].Command != "gk doctor --fix" || r[0].Safety != "safe" {
		t.Fatalf("remedies = %+v, want one safe `gk doctor --fix`", r)
	}
	// …and the hint points at doctor.
	if h := HintFrom(dec); !strings.Contains(h, "gk doctor") {
		t.Errorf("hint = %q, want mention of gk doctor", h)
	}
	// The underlying git error is preserved (Unwrap still reaches it), so
	// errorCodeFromError and any errors.As consumers keep working.
	var ee *git.ExitError
	if !errors.As(dec, &ee) {
		t.Error("decorated error must still unwrap to *git.ExitError")
	}
}

func TestDecorateRawGitError_IndexLockPermission(t *testing.T) {
	raw := &git.ExitError{Code: 128, Args: []string{"add", "file.txt"}, Stderr: "fatal: Unable to create '/repo/.git/index.lock': Operation not permitted"}

	dec := decorateRawGitError(raw)

	if stateFrom(dec) != envStateBlocked {
		t.Fatalf("state = %q, want blocked", stateFrom(dec))
	}
	if codeFrom(dec) != "permission-denied-index-lock" {
		t.Fatalf("code = %q, want permission-denied-index-lock", codeFrom(dec))
	}
	if h := HintFrom(dec); !strings.Contains(h, "filesystem write access") {
		t.Fatalf("hint = %q", h)
	}
	if r := RemediesFrom(dec); len(r) != 0 {
		t.Fatalf("index lock permission should not fabricate command remedies: %+v", r)
	}
	var ee *git.ExitError
	if !errors.As(dec, &ee) {
		t.Error("decorated error must still unwrap to *git.ExitError")
	}
}

func TestDecorateRawGitError_NoOp(t *testing.T) {
	// Already-hinted errors are left untouched (don't clobber a richer hint).
	pre := WithHint(errors.New("invalid commit position"), "custom hint")
	if got := HintFrom(decorateRawGitError(pre)); got != "custom hint" {
		t.Errorf("existing hint overwritten: %q", got)
	}
	// Unrelated errors get no fabricated remedy.
	if r := RemediesFrom(decorateRawGitError(errors.New("something else"))); r != nil {
		t.Errorf("unrelated error gained remedies: %+v", r)
	}
	if decorateRawGitError(nil) != nil {
		t.Error("nil must stay nil")
	}
}

// FormatErrorJSON is the agent contract — corruption surfacing from any
// command must carry the doctor remedy and the stable code without each
// call site knowing about commit-graph at all.
func TestFormatErrorJSON_CommitGraphRemedy(t *testing.T) {
	prevA := flagAgent
	flagAgent = true
	t.Cleanup(func() { flagAgent = prevA })

	raw := &git.ExitError{Code: 128, Args: []string{"rebase", "main"}, Stderr: "fatal: invalid commit position. commit-graph is likely corrupt"}
	out := FormatErrorJSON(raw)

	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code     string `json:"code"`
			Hint     string `json:"hint"`
			Remedies []struct {
				Command string `json:"command"`
				Safety  string `json:"safety"`
			} `json:"remedies"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("invalid JSON envelope: %v\n%s", err, out)
	}
	if env.OK {
		t.Error("ok must be false")
	}
	if env.Error.Code != "commit-graph-corrupt" {
		t.Errorf("code = %q, want commit-graph-corrupt", env.Error.Code)
	}
	if len(env.Error.Remedies) != 1 || !strings.Contains(env.Error.Remedies[0].Command, "doctor --fix") {
		t.Errorf("remedies = %+v, want a doctor --fix entry", env.Error.Remedies)
	}
}

func TestFormatErrorJSON_IndexLockPermissionBlocked(t *testing.T) {
	prevA := flagAgent
	flagAgent = true
	t.Cleanup(func() { flagAgent = prevA })

	raw := &git.ExitError{Code: 128, Args: []string{"stash", "push"}, Stderr: "fatal: Unable to create '/repo/.git/index.lock': Operation not permitted"}
	out := FormatErrorJSON(raw)

	var env struct {
		State string `json:"state"`
		OK    bool   `json:"ok"`
		Error struct {
			Code     string `json:"code"`
			Hint     string `json:"hint"`
			Remedies []struct {
				Command string `json:"command"`
				Safety  string `json:"safety"`
			} `json:"remedies"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("invalid JSON envelope: %v\n%s", err, out)
	}
	if env.State != envStateBlocked || env.OK {
		t.Fatalf("state=%q ok=%v, want blocked false", env.State, env.OK)
	}
	if env.Error.Code != "permission-denied-index-lock" {
		t.Fatalf("code = %q", env.Error.Code)
	}
	if !strings.Contains(env.Error.Hint, "filesystem write access") {
		t.Fatalf("hint = %q", env.Error.Hint)
	}
	if len(env.Error.Remedies) != 0 {
		t.Fatalf("remedies = %+v, want none", env.Error.Remedies)
	}
}

// The human renderer must surface the same doctor guidance.
func TestFormatError_CommitGraphHint(t *testing.T) {
	disableEasyForTest(t)
	raw := &git.ExitError{Code: 128, Args: []string{"rebase", "main"}, Stderr: "fatal: invalid commit position. commit-graph is likely corrupt"}
	out := FormatError(raw)
	if !strings.Contains(out, "gk doctor") {
		t.Errorf("FormatError output missing doctor hint:\n%s", out)
	}
}

// --- ① doctor check ------------------------------------------------------

// fakeGraphRunner wires `rev-parse --git-path` at the supplied paths and lets
// the test choose the `commit-graph verify` outcome.
func fakeGraphRunner(graphPath, chainPath string, verify git.FakeResponse) *git.FakeRunner {
	return &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --git-path objects/info/commit-graph":  {Stdout: graphPath + "\n"},
		"rev-parse --git-path objects/info/commit-graphs": {Stdout: chainPath + "\n"},
		"commit-graph verify":                             verify,
	}}
}

func TestCheckCommitGraph(t *testing.T) {
	ctx := context.Background()

	t.Run("no cache → PASS", func(t *testing.T) {
		dir := t.TempDir()
		r := fakeGraphRunner(filepath.Join(dir, "commit-graph"), filepath.Join(dir, "commit-graphs"), git.FakeResponse{})
		c := checkCommitGraph(ctx, r)
		if c.Status != statusPass || c.Detail != "no cache" {
			t.Errorf("got %+v, want PASS/no cache", c)
		}
	})

	t.Run("present + valid → PASS", func(t *testing.T) {
		dir := t.TempDir()
		graph := filepath.Join(dir, "commit-graph")
		if err := os.WriteFile(graph, []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
		r := fakeGraphRunner(graph, filepath.Join(dir, "commit-graphs"), git.FakeResponse{})
		c := checkCommitGraph(ctx, r)
		if c.Status != statusPass || c.Detail != "valid" {
			t.Errorf("got %+v, want PASS/valid", c)
		}
	})

	t.Run("present + corrupt → FAIL with fix", func(t *testing.T) {
		dir := t.TempDir()
		graph := filepath.Join(dir, "commit-graph")
		if err := os.WriteFile(graph, []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
		verify := git.FakeResponse{ExitCode: 1, Stderr: "fatal: invalid commit position. commit-graph is likely corrupt"}
		r := fakeGraphRunner(graph, filepath.Join(dir, "commit-graphs"), verify)
		c := checkCommitGraph(ctx, r)
		if c.Status != statusFail {
			t.Fatalf("got %+v, want FAIL", c)
		}
		if !strings.Contains(c.Detail, "corrupt") {
			t.Errorf("detail = %q, want it to mention corrupt", c.Detail)
		}
		if !strings.Contains(c.Fix, "gk doctor --fix") {
			t.Errorf("fix = %q, want it to point at gk doctor --fix", c.Fix)
		}
	})
}

// --- ② repair + harden helpers -------------------------------------------

func TestRemoveCommitGraph(t *testing.T) {
	dir := t.TempDir()
	graph := filepath.Join(dir, "commit-graph")
	chain := filepath.Join(dir, "commit-graphs")
	if err := os.WriteFile(graph, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(chain, 0o755); err != nil {
		t.Fatal(err)
	}
	// A read-only file inside the split-chain dir: removal must still
	// succeed (Unix unlink needs parent-dir write, not file write).
	if err := os.WriteFile(filepath.Join(chain, "graph-abc.graph"), []byte("y"), 0o444); err != nil {
		t.Fatal(err)
	}

	r := fakeGraphRunner(graph, chain, git.FakeResponse{})
	if err := removeCommitGraph(context.Background(), r); err != nil {
		t.Fatalf("removeCommitGraph: %v", err)
	}
	for _, p := range []string{graph, chain} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err=%v)", p, err)
		}
	}
}

func TestHardenCommitGraph(t *testing.T) {
	r := &git.FakeRunner{}
	if err := hardenCommitGraph(context.Background(), r); err != nil {
		t.Fatalf("hardenCommitGraph: %v", err)
	}
	want := map[string]bool{
		"config --local gc.writeCommitGraph false":    false,
		"config --local fetch.writeCommitGraph false": false,
		"config --local core.commitGraph false":       false,
	}
	for _, call := range r.Calls {
		want[strings.Join(call.Args, " ")] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("hardenCommitGraph did not issue: git %s", k)
		}
	}
}
