package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeDigestSession writes one Claude-shaped session fixture with a probe
// run, a branch, a commit, and an errored push.
func writeDigestSession(t *testing.T, path string) {
	t.Helper()
	lines := []string{
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git status --short"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":false}]}}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"git log --oneline -5"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","is_error":false}]}}`,
		`{"type":"assistant","message":{"id":"m3","role":"assistant","content":[{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"git checkout -b feature/digest && git commit -m \"feat: digest fixture\""}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t3","is_error":false}]}}`,
		`{"type":"assistant","message":{"id":"m4","role":"assistant","content":[{"type":"tool_use","id":"t4","name":"Bash","input":{"command":"git push origin feature/digest"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t4","is_error":true}]}}`,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionDigest_ArgAndLastAreExclusive(t *testing.T) {
	var out bytes.Buffer
	cmd := newSessionDigestCmd()
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("last", "1"); err != nil {
		t.Fatal(err)
	}
	err := runSessionDigest(cmd, []string{"some.jsonl"})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("file + --last must error, got %v", err)
	}
}

func TestRunSessionDigest_NeitherArgNorLast(t *testing.T) {
	var out bytes.Buffer
	cmd := newSessionDigestCmd()
	cmd.SetOut(&out)
	err := runSessionDigest(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--last") {
		t.Errorf("no input must error with guidance, got %v", err)
	}
}

func TestRunSessionDigest_HumanRendering(t *testing.T) {
	withAgentMode(t, false)
	prevJSON := flagJSON
	t.Cleanup(func() { flagJSON = prevJSON })
	flagJSON = false
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeDigestSession(t, path)

	var out bytes.Buffer
	cmd := newSessionDigestCmd()
	cmd.SetOut(&out)
	if err := runSessionDigest(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"session digest: " + path,
		"branches: feature/digest",
		"feat: digest fixture",
		"1 errored",
		"git-kit context",
		"unfinished:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("human output missing %q:\n%s", want, got)
		}
	}
}

func TestRunSessionDigest_AgentEnvelope(t *testing.T) {
	withAgentMode(t, true)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeDigestSession(t, path)

	var out bytes.Buffer
	cmd := newSessionDigestCmd()
	cmd.SetOut(&out)
	if err := runSessionDigest(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Schema int             `json:"schema"`
		State  string          `json:"state"`
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not valid envelope JSON: %v\n%s", err, out.String())
	}
	if env.Schema != 1 || env.State != "ok" || !env.OK {
		t.Fatalf("envelope = %+v", env)
	}
	var digest map[string]json.RawMessage
	if err := json.Unmarshal(env.Result, &digest); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema", "file", "source", "turns", "commands", "integration", "reprobes"} {
		if _, ok := digest[key]; !ok {
			t.Errorf("digest result missing %q:\n%s", key, env.Result)
		}
	}
	var file string
	if err := json.Unmarshal(digest["file"], &file); err != nil || file != path {
		t.Errorf("digest file = %q, want %q", file, path)
	}
}

func TestRunSessionDigest_LastPicksNewestUnderHome(t *testing.T) {
	withJSONOut(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".claude", "projects", "-work-p")
	older := filepath.Join(root, "older.jsonl")
	newer := filepath.Join(root, "newer.jsonl")
	writeDigestSession(t, older)
	writeDigestSession(t, newer)
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newSessionDigestCmd()
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("last", "1"); err != nil {
		t.Fatal(err)
	}
	if err := runSessionDigest(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var digest struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal(out.Bytes(), &digest); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if digest.File != newer {
		t.Errorf("--last digested %q, want newest %q", digest.File, newer)
	}
}

// --last=2 skips the newest file — from inside a live session the newest is
// the caller's own transcript, and the handoff use case wants the one before.
func TestRunSessionDigest_LastTwoPicksPreviousSession(t *testing.T) {
	withJSONOut(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".claude", "projects", "-work-p")
	previous := filepath.Join(root, "previous.jsonl")
	live := filepath.Join(root, "live.jsonl")
	writeDigestSession(t, previous)
	writeDigestSession(t, live)
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(previous, past, past); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newSessionDigestCmd()
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("last", "2"); err != nil {
		t.Fatal(err)
	}
	if err := runSessionDigest(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var digest struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal(out.Bytes(), &digest); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if digest.File != previous {
		t.Errorf("--last=2 digested %q, want the previous session %q", digest.File, previous)
	}

	// Asking beyond the corpus is a clear error, not a silent fallback.
	cmd = newSessionDigestCmd()
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("last", "3"); err != nil {
		t.Fatal(err)
	}
	if err := runSessionDigest(cmd, nil); err == nil {
		t.Error("--last=3 with two session files must error")
	}
}

func TestRunSessionDigest_LastWithEmptyRootsErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	cmd := newSessionDigestCmd()
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("last", "1"); err != nil {
		t.Fatal(err)
	}
	if err := runSessionDigest(cmd, nil); err == nil {
		t.Error("--last with no session files must error")
	}
}
