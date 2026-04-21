package git

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// helper: build a FakeRunner with a single response keyed by joined args.
func fakeWithResponse(key string, resp FakeResponse) *FakeRunner {
	return &FakeRunner{
		Responses: map[string]FakeResponse{key: resp},
	}
}

// ---- NewClient / Raw ----

func TestNewClient_Raw(t *testing.T) {
	r := &FakeRunner{}
	c := NewClient(r)
	if c.Raw() != r {
		t.Errorf("Raw() should return the underlying runner")
	}
}

// ---- Fetch ----

func TestFetch_Basic(t *testing.T) {
	r := &FakeRunner{}
	c := NewClient(r)
	if err := c.Fetch(context.Background(), "origin", "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.Calls))
	}
	got := strings.Join(r.Calls[0].Args, " ")
	if got != "fetch origin" {
		t.Errorf("expected 'fetch origin', got %q", got)
	}
}

func TestFetch_WithRef(t *testing.T) {
	r := &FakeRunner{}
	c := NewClient(r)
	if err := c.Fetch(context.Background(), "origin", "main", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(r.Calls[0].Args, " ")
	if got != "fetch origin main" {
		t.Errorf("expected 'fetch origin main', got %q", got)
	}
}

func TestFetch_WithPrune(t *testing.T) {
	r := &FakeRunner{}
	c := NewClient(r)
	if err := c.Fetch(context.Background(), "upstream", "refs/heads/feat", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(r.Calls[0].Args, " ")
	if got != "fetch --prune upstream refs/heads/feat" {
		t.Errorf("unexpected args: %q", got)
	}
}

func TestFetch_DefaultRemote(t *testing.T) {
	r := &FakeRunner{}
	c := NewClient(r)
	_ = c.Fetch(context.Background(), "", "", false)
	got := strings.Join(r.Calls[0].Args, " ")
	if got != "fetch origin" {
		t.Errorf("expected default remote 'origin', got %q", got)
	}
}

func TestFetch_Error(t *testing.T) {
	r := fakeWithResponse("fetch origin", FakeResponse{ExitCode: 1, Stderr: "network error"})
	c := NewClient(r)
	err := c.Fetch(context.Background(), "origin", "", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- RebaseOnto ----

func TestRebaseOnto_Success(t *testing.T) {
	r := fakeWithResponse("rebase main", FakeResponse{Stdout: "Successfully rebased\n"})
	c := NewClient(r)
	res, err := c.RebaseOnto(context.Background(), "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Errorf("expected Success=true")
	}
	if res.Conflict || res.NothingTo {
		t.Errorf("unexpected flags: conflict=%v nothingTo=%v", res.Conflict, res.NothingTo)
	}
}

func TestRebaseOnto_NothingToDo(t *testing.T) {
	r := fakeWithResponse("rebase main", FakeResponse{Stdout: "Current branch main is up to date.\n"})
	c := NewClient(r)
	res, err := c.RebaseOnto(context.Background(), "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Errorf("expected Success=true")
	}
	if !res.NothingTo {
		t.Errorf("expected NothingTo=true, stdout=%q", res.Stdout)
	}
}

func TestRebaseOnto_Conflict(t *testing.T) {
	r := fakeWithResponse("rebase main", FakeResponse{
		ExitCode: 1,
		Stdout:   "CONFLICT (content): Merge conflict in foo.go\n",
		Stderr:   "error: could not apply abc1234... some commit",
	})
	c := NewClient(r)
	res, err := c.RebaseOnto(context.Background(), "main")
	if err != nil {
		t.Fatalf("conflict should not return error, got: %v", err)
	}
	if !res.Conflict {
		t.Errorf("expected Conflict=true")
	}
	if res.Success {
		t.Errorf("expected Success=false on conflict")
	}
}

func TestRebaseOnto_FatalError(t *testing.T) {
	r := fakeWithResponse("rebase main", FakeResponse{ExitCode: 128, Stderr: "fatal: not a git repo"})
	c := NewClient(r)
	_, err := c.RebaseOnto(context.Background(), "main")
	if err == nil {
		t.Fatal("expected error for fatal exit")
	}
}

// ---- CurrentBranch ----

func TestCurrentBranch_Happy(t *testing.T) {
	r := fakeWithResponse("symbolic-ref --short HEAD", FakeResponse{Stdout: "feature/foo\n"})
	c := NewClient(r)
	branch, err := c.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "feature/foo" {
		t.Errorf("expected 'feature/foo', got %q", branch)
	}
}

func TestCurrentBranch_DetachedHEAD(t *testing.T) {
	r := fakeWithResponse("symbolic-ref --short HEAD", FakeResponse{
		ExitCode: 128,
		Stderr:   "fatal: ref HEAD is not a symbolic ref",
	})
	c := NewClient(r)
	branch, err := c.CurrentBranch(context.Background())
	if branch != "" {
		t.Errorf("expected empty branch on detached HEAD, got %q", branch)
	}
	if !errors.Is(err, ErrDetachedHEAD) {
		t.Errorf("expected ErrDetachedHEAD, got %v", err)
	}
}

func TestCurrentBranch_OtherError(t *testing.T) {
	r := fakeWithResponse("symbolic-ref --short HEAD", FakeResponse{
		ExitCode: 128,
		Stderr:   "fatal: not a git repository",
	})
	c := NewClient(r)
	_, err := c.CurrentBranch(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrDetachedHEAD) {
		t.Errorf("should not be ErrDetachedHEAD")
	}
}

// ---- DefaultBranch ----

func TestDefaultBranch_SymbolicRef(t *testing.T) {
	r := &FakeRunner{
		Responses: map[string]FakeResponse{
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
		},
	}
	c := NewClient(r)
	branch, err := c.DefaultBranch(context.Background(), "origin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "main" {
		t.Errorf("expected 'main', got %q", branch)
	}
}

func TestDefaultBranch_FallbackDevelop(t *testing.T) {
	r := &FakeRunner{
		Responses: map[string]FakeResponse{
			// symbolic-ref fails
			"symbolic-ref --short refs/remotes/origin/HEAD": {ExitCode: 128, Stderr: "no such ref"},
			// develop exists
			"show-ref --verify --quiet refs/remotes/origin/develop": {ExitCode: 0},
		},
	}
	c := NewClient(r)
	branch, err := c.DefaultBranch(context.Background(), "origin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "develop" {
		t.Errorf("expected 'develop', got %q", branch)
	}
}

func TestDefaultBranch_FallbackMain(t *testing.T) {
	r := &FakeRunner{
		Responses: map[string]FakeResponse{
			"symbolic-ref --short refs/remotes/origin/HEAD":          {ExitCode: 128},
			"show-ref --verify --quiet refs/remotes/origin/develop":  {ExitCode: 1},
			"show-ref --verify --quiet refs/remotes/origin/main":     {ExitCode: 0},
		},
	}
	c := NewClient(r)
	branch, err := c.DefaultBranch(context.Background(), "origin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "main" {
		t.Errorf("expected 'main', got %q", branch)
	}
}

func TestDefaultBranch_AllFail(t *testing.T) {
	r := &FakeRunner{
		DefaultResp: FakeResponse{ExitCode: 1},
	}
	c := NewClient(r)
	_, err := c.DefaultBranch(context.Background(), "origin")
	if !errors.Is(err, ErrNoDefaultBranch) {
		t.Errorf("expected ErrNoDefaultBranch, got %v", err)
	}
}

// ---- Status ----

func TestStatus_BasicParse(t *testing.T) {
	// Simulated porcelain v2 -z output with NUL separators.
	// Headers and one ordinary changed entry.
	lines := []string{
		"# branch.head main",
		"# branch.upstream origin/main",
		"# branch.ab +2 -1",
		"1 M. N... 100644 100644 100644 abc def foo.go",
		"? untracked.go",
	}
	raw := strings.Join(lines, "\x00") + "\x00"

	r := fakeWithResponse("status --porcelain=v2 -z --branch", FakeResponse{Stdout: raw})
	c := NewClient(r)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Branch != "main" {
		t.Errorf("Branch: want 'main', got %q", st.Branch)
	}
	if st.Upstream != "origin/main" {
		t.Errorf("Upstream: want 'origin/main', got %q", st.Upstream)
	}
	if st.Ahead != 2 {
		t.Errorf("Ahead: want 2, got %d", st.Ahead)
	}
	if st.Behind != 1 {
		t.Errorf("Behind: want 1, got %d", st.Behind)
	}
	if len(st.Entries) != 2 {
		t.Fatalf("Entries: want 2, got %d", len(st.Entries))
	}
	if st.Entries[0].Kind != KindOrdinary {
		t.Errorf("entry[0] kind: want KindOrdinary, got %v", st.Entries[0].Kind)
	}
	if st.Entries[0].XY != "M." {
		t.Errorf("entry[0] XY: want 'M.', got %q", st.Entries[0].XY)
	}
	if st.Entries[1].Kind != KindUntracked {
		t.Errorf("entry[1] kind: want KindUntracked, got %v", st.Entries[1].Kind)
	}
}

func TestStatus_RenamedEntry(t *testing.T) {
	lines := []string{
		"# branch.head feature",
		"2 R. N... 100644 100644 100644 aaa bbb R100 new.go",
		"old.go",
	}
	raw := strings.Join(lines, "\x00") + "\x00"

	r := fakeWithResponse("status --porcelain=v2 -z --branch", FakeResponse{Stdout: raw})
	c := NewClient(r)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(st.Entries))
	}
	e := st.Entries[0]
	if e.Kind != KindRenamed {
		t.Errorf("kind: want KindRenamed, got %v", e.Kind)
	}
	if e.Path != "new.go" {
		t.Errorf("Path: want 'new.go', got %q", e.Path)
	}
	if e.Orig != "old.go" {
		t.Errorf("Orig: want 'old.go', got %q", e.Orig)
	}
}

func TestStatus_EmptyOutput(t *testing.T) {
	r := fakeWithResponse("status --porcelain=v2 -z --branch", FakeResponse{Stdout: ""})
	c := NewClient(r)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.Entries) != 0 {
		t.Errorf("expected no entries, got %d", len(st.Entries))
	}
}

func TestStatus_Error(t *testing.T) {
	r := fakeWithResponse("status --porcelain=v2 -z --branch", FakeResponse{ExitCode: 128, Stderr: "not a repo"})
	c := NewClient(r)
	_, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---- IsDirty ----

func TestIsDirty_Clean(t *testing.T) {
	r := fakeWithResponse("status --porcelain=v1 -uno", FakeResponse{Stdout: ""})
	c := NewClient(r)
	dirty, err := c.IsDirty(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dirty {
		t.Errorf("expected clean repo")
	}
}

func TestIsDirty_Modified(t *testing.T) {
	r := fakeWithResponse("status --porcelain=v1 -uno", FakeResponse{Stdout: " M file.go\n"})
	c := NewClient(r)
	dirty, err := c.IsDirty(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dirty {
		t.Errorf("expected dirty repo")
	}
}

func TestIsDirty_Error(t *testing.T) {
	r := fakeWithResponse("status --porcelain=v1 -uno", FakeResponse{ExitCode: 128, Stderr: "not a repo"})
	c := NewClient(r)
	_, err := c.IsDirty(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---- GitDir ----

func TestGitDir(t *testing.T) {
	r := fakeWithResponse("rev-parse --git-dir", FakeResponse{Stdout: ".git\n"})
	c := NewClient(r)
	dir, err := c.GitDir(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != ".git" {
		t.Errorf("expected '.git', got %q", dir)
	}
}

func TestGitDir_Error(t *testing.T) {
	r := fakeWithResponse("rev-parse --git-dir", FakeResponse{ExitCode: 128, Stderr: "not a repo"})
	c := NewClient(r)
	_, err := c.GitDir(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---- GitCommonDir ----

func TestGitCommonDir(t *testing.T) {
	r := fakeWithResponse("rev-parse --git-common-dir", FakeResponse{Stdout: "/repo/.git\n"})
	c := NewClient(r)
	dir, err := c.GitCommonDir(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != "/repo/.git" {
		t.Errorf("expected '/repo/.git', got %q", dir)
	}
}

func TestGitCommonDir_Error(t *testing.T) {
	r := fakeWithResponse("rev-parse --git-common-dir", FakeResponse{ExitCode: 128})
	c := NewClient(r)
	_, err := c.GitCommonDir(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---- CheckRefFormat ----

func TestCheckRefFormat_Valid(t *testing.T) {
	r := fakeWithResponse("check-ref-format --branch feature/foo", FakeResponse{Stdout: "feature/foo\n"})
	c := NewClient(r)
	if err := c.CheckRefFormat(context.Background(), "feature/foo"); err != nil {
		t.Errorf("expected nil error for valid ref, got: %v", err)
	}
}

func TestCheckRefFormat_Invalid(t *testing.T) {
	r := fakeWithResponse("check-ref-format --branch .bad ref", FakeResponse{
		ExitCode: 1,
		Stderr:   "",
	})
	c := NewClient(r)
	err := c.CheckRefFormat(context.Background(), ".bad ref")
	if err == nil {
		t.Fatal("expected error for invalid ref")
	}
	if !strings.Contains(err.Error(), ".bad ref") {
		t.Errorf("error should mention the ref, got: %v", err)
	}
}

// ---- UnmergedEntry (coverage for 'u' records) ----

func TestStatus_UnmergedEntry(t *testing.T) {
	// u <xy> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
	raw := "# branch.head main\x00u UU N... 100644 100644 100644 100644 aaa bbb ccc conflict.go\x00"
	r := fakeWithResponse("status --porcelain=v2 -z --branch", FakeResponse{Stdout: raw})
	c := NewClient(r)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(st.Entries))
	}
	if st.Entries[0].Kind != KindUnmerged {
		t.Errorf("expected KindUnmerged, got %v", st.Entries[0].Kind)
	}
	if st.Entries[0].XY != "UU" {
		t.Errorf("XY: want 'UU', got %q", st.Entries[0].XY)
	}
}

// ---- IgnoredEntry (coverage for '!' records) ----

func TestStatus_IgnoredEntry(t *testing.T) {
	raw := "# branch.head main\x00! !! ignored.log\x00"
	r := fakeWithResponse("status --porcelain=v2 -z --branch", FakeResponse{Stdout: raw})
	c := NewClient(r)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(st.Entries))
	}
	if st.Entries[0].Kind != KindIgnored {
		t.Errorf("expected KindIgnored, got %v", st.Entries[0].Kind)
	}
}
