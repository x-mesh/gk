package aichat

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

// allGitResponses returns a FakeRunner with typical git outputs for a healthy repo.
func allGitResponses() map[string]git.FakeResponse {
	return map[string]git.FakeResponse{
		"rev-parse --abbrev-ref HEAD":      {Stdout: "main\n"},
		"rev-parse --short HEAD":           {Stdout: "abc1234\n"},
		"rev-parse --abbrev-ref @{u}":      {Stdout: "origin/main\n"},
		"status --porcelain=v2":            {Stdout: " M file.go\n?? new.txt\n"},
		"reflog -10 --format=%h %gs":       {Stdout: "abc1234 commit: init\ndef5678 checkout: main\n"},
		"branch --format=%(refname:short)": {Stdout: "main\nfeat/x\nfeat/y\n"},
	}
}

func TestCollect_HappyPath(t *testing.T) {
	r := &git.FakeRunner{Responses: allGitResponses()}
	c := &RepoContextCollector{Runner: r, TokenBudget: 2000}

	rc := c.Collect(context.Background())

	if !rc.IsRepo {
		t.Fatal("expected IsRepo=true")
	}
	if rc.Branch != "main" {
		t.Errorf("Branch = %q, want %q", rc.Branch, "main")
	}
	if rc.HeadSHA != "abc1234" {
		t.Errorf("HeadSHA = %q, want %q", rc.HeadSHA, "abc1234")
	}
	if rc.Upstream != "origin/main" {
		t.Errorf("Upstream = %q, want %q", rc.Upstream, "origin/main")
	}
	if !strings.Contains(rc.Status, "file.go") {
		t.Errorf("Status should contain file.go, got %q", rc.Status)
	}
	if len(rc.RecentReflog) != 2 {
		t.Errorf("RecentReflog len = %d, want 2", len(rc.RecentReflog))
	}
}

func TestCollect_NonGitDirectory(t *testing.T) {
	// rev-parse --abbrev-ref HEAD fails → not a git repo.
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {ExitCode: 128, Stderr: "fatal: not a git repository"},
		},
	}
	c := &RepoContextCollector{Runner: r}

	rc := c.Collect(context.Background())

	if rc.IsRepo {
		t.Fatal("expected IsRepo=false for non-git directory")
	}
	if rc.Branch != "" || rc.HeadSHA != "" || rc.Upstream != "" {
		t.Error("fields should be empty for non-git directory")
	}
}

func TestCollect_PartialFailures(t *testing.T) {
	// Branch succeeds, but upstream and reflog fail.
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {Stdout: "feat/x\n"},
			"rev-parse --short HEAD":      {Stdout: "1234567\n"},
			"rev-parse --abbrev-ref @{u}": {ExitCode: 128, Stderr: "fatal: no upstream"},
			"status --porcelain=v2":       {Stdout: ""},
			"reflog -10 --format=%h %gs":  {ExitCode: 1, Stderr: "error"},
		},
	}
	c := &RepoContextCollector{Runner: r}

	rc := c.Collect(context.Background())

	if !rc.IsRepo {
		t.Fatal("expected IsRepo=true")
	}
	if rc.Branch != "feat/x" {
		t.Errorf("Branch = %q, want %q", rc.Branch, "feat/x")
	}
	if rc.HeadSHA != "1234567" {
		t.Errorf("HeadSHA = %q, want %q", rc.HeadSHA, "1234567")
	}
	if rc.Upstream != "" {
		t.Errorf("Upstream should be empty on failure, got %q", rc.Upstream)
	}
	if len(rc.RecentReflog) != 0 {
		t.Errorf("RecentReflog should be empty on failure, got %v", rc.RecentReflog)
	}
}

func TestCollect_IndividualFailureDoesNotAffectOthers(t *testing.T) {
	// Status fails but everything else succeeds.
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
			"rev-parse --short HEAD":      {Stdout: "abc1234\n"},
			"rev-parse --abbrev-ref @{u}": {Stdout: "origin/main\n"},
			"status --porcelain=v2":       {ExitCode: 1, Stderr: "error"},
			"reflog -10 --format=%h %gs":  {Stdout: "abc1234 commit: init\n"},
		},
	}
	c := &RepoContextCollector{Runner: r}

	rc := c.Collect(context.Background())

	if !rc.IsRepo {
		t.Fatal("expected IsRepo=true")
	}
	if rc.Branch != "main" {
		t.Errorf("Branch = %q, want %q", rc.Branch, "main")
	}
	if rc.HeadSHA != "abc1234" {
		t.Errorf("HeadSHA = %q, want %q", rc.HeadSHA, "abc1234")
	}
	if rc.Upstream != "origin/main" {
		t.Errorf("Upstream = %q, want %q", rc.Upstream, "origin/main")
	}
	if rc.Status != "" {
		t.Errorf("Status should be empty on failure, got %q", rc.Status)
	}
	if len(rc.RecentReflog) != 1 {
		t.Errorf("RecentReflog len = %d, want 1", len(rc.RecentReflog))
	}
}

func TestCollectForQuestion_IncludesBranchList(t *testing.T) {
	r := &git.FakeRunner{Responses: allGitResponses()}
	c := &RepoContextCollector{Runner: r, TokenBudget: 2000}

	rc := c.CollectForQuestion(context.Background(), "어떤 브랜치가 있나요?")

	if !rc.IsRepo {
		t.Fatal("expected IsRepo=true")
	}
	if len(rc.BranchList) != 3 {
		t.Errorf("BranchList len = %d, want 3", len(rc.BranchList))
	}
	if rc.BranchList[0] != "main" {
		t.Errorf("BranchList[0] = %q, want %q", rc.BranchList[0], "main")
	}
}

func TestCollectForQuestion_NonGitDirectory(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {ExitCode: 128, Stderr: "fatal: not a git repository"},
		},
	}
	c := &RepoContextCollector{Runner: r}

	rc := c.CollectForQuestion(context.Background(), "question")

	if rc.IsRepo {
		t.Fatal("expected IsRepo=false")
	}
	if len(rc.BranchList) != 0 {
		t.Error("BranchList should be empty for non-git directory")
	}
}

func TestFormat_HappyPath(t *testing.T) {
	rc := &RepoContext{
		Branch:       "main",
		HeadSHA:      "abc1234",
		Upstream:     "origin/main",
		Status:       " M file.go",
		RecentReflog: []string{"abc1234 commit: init", "def5678 checkout: main"},
		IsRepo:       true,
	}

	out := rc.Format()

	for _, want := range []string{"Branch: main", "HEAD: abc1234", "Upstream: origin/main", "file.go", "Reflog:", "commit: init"} {
		if !strings.Contains(out, want) {
			t.Errorf("Format() missing %q in:\n%s", want, out)
		}
	}
}

func TestFormat_NonGitRepo(t *testing.T) {
	rc := &RepoContext{IsRepo: false}
	out := rc.Format()
	if out != "Not a git repository." {
		t.Errorf("Format() = %q, want %q", out, "Not a git repository.")
	}
}

func TestFormat_EmptyOptionalFields(t *testing.T) {
	rc := &RepoContext{
		Branch: "main",
		IsRepo: true,
	}

	out := rc.Format()

	if !strings.Contains(out, "Branch: main") {
		t.Errorf("Format() missing branch, got:\n%s", out)
	}
	// Should not contain empty sections.
	if strings.Contains(out, "HEAD:") {
		t.Error("Format() should not include empty HEAD")
	}
	if strings.Contains(out, "Upstream:") {
		t.Error("Format() should not include empty Upstream")
	}
	if strings.Contains(out, "Status:") {
		t.Error("Format() should not include empty Status")
	}
	if strings.Contains(out, "Reflog:") {
		t.Error("Format() should not include empty Reflog")
	}
}

func TestFormat_WithBranchList(t *testing.T) {
	rc := &RepoContext{
		Branch:     "main",
		IsRepo:     true,
		BranchList: []string{"main", "feat/x", "feat/y"},
	}

	out := rc.Format()

	if !strings.Contains(out, "Branches:") {
		t.Errorf("Format() missing Branches section, got:\n%s", out)
	}
	for _, br := range []string{"main", "feat/x", "feat/y"} {
		if !strings.Contains(out, br) {
			t.Errorf("Format() missing branch %q", br)
		}
	}
}

func TestFormatForCollector_TokenBudget(t *testing.T) {
	// Create a context with a very large status to test truncation.
	bigStatus := strings.Repeat("M file.go\n", 500)
	bigReflog := make([]string, 100)
	for i := range bigReflog {
		bigReflog[i] = "abc1234 commit: some long message about what happened"
	}

	rc := &RepoContext{
		Branch:       "main",
		HeadSHA:      "abc1234",
		Upstream:     "origin/main",
		Status:       bigStatus,
		RecentReflog: bigReflog,
		IsRepo:       true,
	}

	// Very small budget: 100 tokens = 400 chars.
	c := &RepoContextCollector{TokenBudget: 100}
	out := rc.FormatForCollector(c)

	estimatedTokens := len(out) / 4
	if estimatedTokens > 100 {
		t.Errorf("FormatForCollector() output %d estimated tokens, budget is 100", estimatedTokens)
	}

	// Must still contain the branch (priority 1).
	if !strings.Contains(out, "Branch: main") {
		t.Errorf("FormatForCollector() missing branch even with small budget")
	}
}

func TestFormatForCollector_DefaultBudget(t *testing.T) {
	rc := &RepoContext{
		Branch: "main",
		IsRepo: true,
	}

	// TokenBudget=0 should default to 2000.
	c := &RepoContextCollector{TokenBudget: 0}
	out := rc.FormatForCollector(c)

	if !strings.Contains(out, "Branch: main") {
		t.Errorf("FormatForCollector() missing branch with default budget")
	}
}

func TestCollect_DbgCalledOnError(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
			"rev-parse --short HEAD":      {ExitCode: 1, Stderr: "error"},
			"rev-parse --abbrev-ref @{u}": {ExitCode: 1, Stderr: "error"},
			"status --porcelain=v2":       {ExitCode: 1, Stderr: "error"},
			"reflog -10 --format=%h %gs":  {ExitCode: 1, Stderr: "error"},
		},
	}

	var dbgCalls int
	c := &RepoContextCollector{
		Runner: r,
		Dbg: func(format string, args ...any) {
			dbgCalls++
		},
	}

	rc := c.Collect(context.Background())

	if !rc.IsRepo {
		t.Fatal("expected IsRepo=true")
	}
	if dbgCalls == 0 {
		t.Error("expected Dbg to be called on git errors")
	}
}

func TestFormat_TokenBudgetTruncatesStatus(t *testing.T) {
	// Status is large enough to exceed a small budget.
	bigStatus := strings.Repeat("1 .M N... file.go\n", 50)
	rc := &RepoContext{
		Branch:  "main",
		HeadSHA: "abc1234",
		Status:  bigStatus,
		IsRepo:  true,
	}

	// Budget of 50 tokens = 200 chars. Branch+HEAD take ~30 chars.
	out := rc.formatWithBudget(50)

	if len(out) > 200 {
		t.Errorf("formatWithBudget(50) output %d chars, max should be 200", len(out))
	}
	if !strings.Contains(out, "Branch: main") {
		t.Error("must contain branch even with small budget")
	}
}
