package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

// gitRepoFixture builds a real git repo with two commits, a denied file,
// and enough content to exercise every tool.
func gitRepoFixture(t *testing.T) (git.Runner, *Sandbox, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("a.go", "package a\n\nfunc Hello() string { return \"hi\" }\n")
	write(".env", "API_SECRET=hunter2\n")
	run("add", "-A")
	run("commit", "-qm", "initial")
	write("a.go", "package a\n\nfunc Hello() string { return \"hello\" }\n")
	write(".env", "API_SECRET=changed-secret\n")
	run("add", "-A")
	run("commit", "-qm", "change greeting and secret")

	runner := &git.ExecRunner{Dir: root}
	// The fixture root may sit under a symlinked temp dir — NewSandbox
	// canonicalizes.
	sb, err := NewSandbox(root, []string{".env"})
	if err != nil {
		t.Fatal(err)
	}
	return runner, sb, root
}

func newTestGitTools(t *testing.T) (*Registry, *GitTools) {
	t.Helper()
	runner, sb, _ := gitRepoFixture(t)
	g := &GitTools{Runner: runner, Sandbox: sb, DenyGlobs: sb.DenyGlobs}
	r := NewRegistry(nil, 0)
	RegisterGitTools(r, g)
	return r, g
}

func dispatch(t *testing.T, r *Registry, name, input string) provider.ToolResult {
	t.Helper()
	return r.Dispatch(context.Background(), provider.ToolCall{
		ID: "c1", Name: name, Input: json.RawMessage(input),
	})
}

func TestGitLogMetadataOnly(t *testing.T) {
	r, _ := newTestGitTools(t)
	res := dispatch(t, r, "git_log", `{"limit":10}`)
	if res.IsError {
		t.Fatalf("git_log error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "change greeting and secret") {
		t.Errorf("log missing subject: %s", res.Content)
	}
	if strings.Contains(res.Content, "hunter2") || strings.Contains(res.Content, "API_SECRET") {
		t.Errorf("log leaked patch content: %s", res.Content)
	}
}

// The Critical research finding: `git show <sha>` must not leak a denied
// file's historic content.
func TestGitShowFiltersDeniedFiles(t *testing.T) {
	r, _ := newTestGitTools(t)
	res := dispatch(t, r, "git_show", `{"ref":"HEAD"}`)
	if res.IsError {
		t.Fatalf("git_show error: %s", res.Content)
	}
	if strings.Contains(res.Content, "changed-secret") {
		t.Errorf("git_show leaked denied file content:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "withheld by deny_paths") {
		t.Errorf("git_show must note withheld blocks:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("allowed file's diff should survive:\n%s", res.Content)
	}
}

func TestGitShowDeniedPathDirectly(t *testing.T) {
	r, _ := newTestGitTools(t)
	res := dispatch(t, r, "git_show", `{"ref":"HEAD","path":".env"}`)
	if !res.IsError {
		t.Fatalf("git_show .env must be denied, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "changed-secret") {
		t.Error("denial message leaked content")
	}
}

func TestGitDiffDigestDefault(t *testing.T) {
	r, _ := newTestGitTools(t)
	res := dispatch(t, r, "git_diff", `{"range":"HEAD~1..HEAD"}`)
	if res.IsError {
		t.Fatalf("git_diff error: %s", res.Content)
	}
	if strings.Contains(res.Content, "changed-secret") {
		t.Errorf("digest leaked denied content: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("digest missing allowed file: %s", res.Content)
	}
}

func TestGitBlameLineRange(t *testing.T) {
	r, _ := newTestGitTools(t)
	res := dispatch(t, r, "git_blame", `{"path":"a.go","start_line":1,"end_line":1}`)
	if res.IsError {
		t.Fatalf("git_blame error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "package a") {
		t.Errorf("blame output unexpected: %s", res.Content)
	}
	if dres := dispatch(t, r, "git_blame", `{"path":".env"}`); !dres.IsError {
		t.Error("blame on denied path must fail")
	}
}

// grep must structurally exclude denied files — the secret exists ONLY in
// .env, so any match line would be a leak.
func TestGitGrepExcludesDenied(t *testing.T) {
	r, _ := newTestGitTools(t)
	res := dispatch(t, r, "git_grep", `{"pattern":"API_SECRET"}`)
	if res.IsError {
		t.Fatalf("git_grep error: %s", res.Content)
	}
	if strings.Contains(res.Content, "changed-secret") || strings.Contains(res.Content, ".env") {
		t.Errorf("grep leaked denied file: %s", res.Content)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Errorf("expected no matches, got: %s", res.Content)
	}
}

func TestGitToolsRejectFlagInjection(t *testing.T) {
	r, _ := newTestGitTools(t)
	cases := map[string]string{
		"git_log":  `{"range":"--exec-path=/tmp/evil"}`,
		"git_show": `{"ref":"-c"}`,
		"git_diff": `{"range":"--output=/tmp/x"}`,
	}
	for tool, input := range cases {
		if res := dispatch(t, r, tool, input); !res.IsError {
			t.Errorf("%s with %s must be rejected", tool, input)
		}
	}
	// Leading-dash grep pattern is DATA (via -e), not a flag — must work.
	if res := dispatch(t, r, "git_grep", `{"pattern":"-n"}`); res.IsError {
		t.Errorf("leading-dash grep pattern should be safe data: %s", res.Content)
	}
}

func TestGitToolsRejectUnknownFields(t *testing.T) {
	r, _ := newTestGitTools(t)
	if res := dispatch(t, r, "git_log", `{"limit":5,"exec":"rm -rf /"}`); !res.IsError {
		t.Error("unknown input fields must be rejected")
	}
}

func TestFileTools(t *testing.T) {
	_, sb, root := gitRepoFixture(t)
	f := &FileTools{Sandbox: sb}
	r := NewRegistry(nil, 0)
	RegisterFileTools(r, f)

	res := dispatch(t, r, "file_read", `{"path":"a.go"}`)
	if res.IsError || !strings.Contains(res.Content, "package a") {
		t.Errorf("file_read a.go: err=%v content=%s", res.IsError, res.Content)
	}
	if res := dispatch(t, r, "file_read", `{"path":".env"}`); !res.IsError {
		t.Error("file_read .env must be denied")
	}
	if res := dispatch(t, r, "file_read", `{"path":"../outside"}`); !res.IsError {
		t.Error("file_read escape must be denied")
	}

	// Binary refusal.
	bin := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(bin, []byte{0x00, 0x01, 0x02, 0xFF}, 0o644); err != nil {
		t.Fatal(err)
	}
	if res := dispatch(t, r, "file_read", `{"path":"blob.bin"}`); !res.IsError {
		t.Error("binary file must be refused")
	}

	list := dispatch(t, r, "file_list", `{}`)
	if list.IsError {
		t.Fatalf("file_list: %s", list.Content)
	}
	if strings.Contains(list.Content, ".env") {
		t.Errorf("file_list must omit denied entries: %s", list.Content)
	}
	if !strings.Contains(list.Content, "a.go") {
		t.Errorf("file_list missing a.go: %s", list.Content)
	}
}

func TestRegistryDispatchGuards(t *testing.T) {
	r := NewRegistry(func(s string) string { return strings.ReplaceAll(s, "hunter2", "[REDACTED]") }, 32)
	r.Register(Tool{
		Name:   "leaky",
		Schema: json.RawMessage(`{}`),
		Handler: func(context.Context, json.RawMessage) (string, error) {
			return "the secret is hunter2 and this line is long enough to trigger the cap for sure", nil
		},
	})
	r.Register(Tool{
		Name:    "panicky",
		Schema:  json.RawMessage(`{}`),
		Handler: func(context.Context, json.RawMessage) (string, error) { panic("boom") },
	})
	r.Register(Tool{
		Name:   "failing",
		Schema: json.RawMessage(`{}`),
		Handler: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("path hunter2 rejected")
		},
	})

	res := r.Dispatch(context.Background(), provider.ToolCall{ID: "x", Name: "leaky"})
	if strings.Contains(res.Content, "hunter2") {
		t.Error("result must be redacted before capping")
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Errorf("long result must carry a truncation marker: %s", res.Content)
	}

	if res := r.Dispatch(context.Background(), provider.ToolCall{Name: "panicky"}); !res.IsError {
		t.Error("panic must become IsError")
	}
	if res := r.Dispatch(context.Background(), provider.ToolCall{Name: "nope"}); !res.IsError {
		t.Error("unknown tool must be IsError")
	}
	res = r.Dispatch(context.Background(), provider.ToolCall{Name: "failing"})
	if !res.IsError || strings.Contains(res.Content, "hunter2") {
		t.Errorf("error text must be redacted too: %s", res.Content)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if res := r.Dispatch(canceled, provider.ToolCall{Name: "leaky"}); !res.IsError {
		t.Error("canceled context must be IsError")
	}
}
