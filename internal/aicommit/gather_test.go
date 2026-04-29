package aicommit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

// buildV2 splices a sequence of porcelain v2 records into the NUL-terminated
// format that `git status --porcelain=v2 -z` emits.
func buildV2(records ...string) string {
	var b strings.Builder
	for _, r := range records {
		b.WriteString(r)
		b.WriteByte(0)
	}
	return b.String()
}

func TestGatherWIP_DropsSubmoduleEntries(t *testing.T) {
	// porcelain v2 marks submodules with sub starting `S<c><m><u>`.
	// gk commit must NOT auto-stage submodule pointer changes —
	// those are deliberate commits, not AI-classified groupings.
	stdout := buildV2(
		"1 .M S.M. 160000 160000 160000 aaa bbb vendor/sub-mod",
		"1 .M N... 100644 100644 100644 aaa bbb cmd/gk/main.go",
		"2 R. S..U 160000 160000 160000 aaa bbb R100 third_party/renamed-sub",
		"third_party/old-sub",
		"2 R. N... 100644 100644 100644 aaa bbb R95 internal/foo.go",
		"internal/bar.go",
		// Unmerged submodule (rare — submodule pointer conflict).
		"u UU SCMU 160000 160000 160000 160000 aaa bbb ccc vendor/conflicted-sub",
	)
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	entries, err := GatherWIP(context.Background(), fake, GatherOptions{})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Path, "sub") {
			t.Errorf("submodule entry leaked into gather: %+v", e)
		}
	}
	want := map[string]bool{"cmd/gk/main.go": true, "internal/foo.go": true}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Path] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("expected %q in entries, got %+v", p, entries)
		}
	}
	if len(entries) != 2 {
		t.Errorf("expected exactly 2 non-submodule entries, got %d: %+v",
			len(entries), entries)
	}
}

func TestIsSubmoduleSubField(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"":     false,
		"N...": false,
		"S...": true,
		"S.M.": true,
		"SCMU": true,
		"NUUU": false,
	}
	for in, want := range cases {
		if got := isSubmoduleSubField(in); got != want {
			t.Errorf("isSubmoduleSubField(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestGatherWIPOrdinary(t *testing.T) {
	stdout := buildV2(
		"1 M. N... 100644 100644 100644 aaa bbb internal/cli/root.go",
		"1 .M N... 100644 100644 100644 aaa bbb cmd/gk/main.go",
		"? build/output.log",
	)
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	entries, err := GatherWIP(context.Background(), fake, GatherOptions{})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries: want 3, got %d (%+v)", len(entries), entries)
	}

	if entries[0].Path != "internal/cli/root.go" || !entries[0].Staged || entries[0].Unstaged {
		t.Errorf("staged modify: %+v", entries[0])
	}
	if entries[1].Path != "cmd/gk/main.go" || entries[1].Staged || !entries[1].Unstaged {
		t.Errorf("unstaged modify: %+v", entries[1])
	}
	if entries[2].Status != "untracked" || entries[2].Path != "build/output.log" {
		t.Errorf("untracked: %+v", entries[2])
	}
}

func TestGatherWIPScopeStagedOnly(t *testing.T) {
	stdout := buildV2(
		"1 M. N... 100644 100644 100644 aaa bbb a.go",
		"1 .M N... 100644 100644 100644 aaa bbb b.go",
		"? c.go",
	)
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	entries, err := GatherWIP(context.Background(), fake, GatherOptions{Scope: ScopeStagedOnly})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "a.go" {
		t.Errorf("ScopeStagedOnly: want [a.go], got %+v", entries)
	}
}

func TestGatherWIPScopeUnstagedOnly(t *testing.T) {
	stdout := buildV2(
		"1 M. N... 100644 100644 100644 aaa bbb a.go",
		"1 MM N... 100644 100644 100644 aaa bbb b.go",
		"? c.go",
	)
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	entries, err := GatherWIP(context.Background(), fake, GatherOptions{Scope: ScopeUnstagedOnly})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}
	// b.go has both sides modified, c.go is untracked → both included.
	// a.go is staged-only → dropped.
	if len(entries) != 2 {
		t.Fatalf("len: want 2, got %d (%+v)", len(entries), entries)
	}
	if entries[0].Path != "b.go" || entries[1].Path != "c.go" {
		t.Errorf("ScopeUnstagedOnly order/paths: %+v", entries)
	}
}

func TestGatherWIPRenameRecord(t *testing.T) {
	stdout := buildV2(
		"2 R. N... 100644 100644 100644 aaa bbb R85 internal/cli/ai_old.go",
		"internal/cli/ai.go",
	)
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	entries, err := GatherWIP(context.Background(), fake, GatherOptions{})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries: %+v", entries)
	}
	got := entries[0]
	if got.Status != "renamed" || got.Path != "internal/cli/ai_old.go" || got.OrigPath != "internal/cli/ai.go" {
		t.Errorf("rename record: %+v", got)
	}
}

func TestGatherWIPDropsSubmoduleDirtinessOnly(t *testing.T) {
	stdout := buildV2(
		"1 .M S..U 160000 160000 160000 aaa aaa ghostty",
		"1 .M S.M. 160000 160000 160000 bbb bbb vendor/lib",
		"1 .M S.MU 160000 160000 160000 ccc ccc deps/tool",
	)
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	entries, err := GatherWIP(context.Background(), fake, GatherOptions{})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("submodule dirtiness-only records should be dropped, got %+v", entries)
	}
}

func TestGatherWIPDenyPaths(t *testing.T) {
	stdout := buildV2(
		"1 .M N... 100644 100644 100644 aaa bbb .env",
		"1 .M N... 100644 100644 100644 aaa bbb configs/secret.pem",
		"1 .M N... 100644 100644 100644 aaa bbb internal/cli/ai.go",
	)
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	entries, err := GatherWIP(context.Background(), fake, GatherOptions{
		DenyPaths: []string{".env", "*.pem"},
	})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries: %+v", entries)
	}
	denied := map[string]string{}
	for _, e := range entries {
		denied[e.Path] = e.DeniedBy
	}
	if denied[".env"] != ".env" {
		t.Errorf(".env should be denied by .env glob, got %q", denied[".env"])
	}
	if denied["configs/secret.pem"] != "*.pem" {
		t.Errorf("*.pem glob should match basename, got %q", denied["configs/secret.pem"])
	}
	if denied["internal/cli/ai.go"] != "" {
		t.Errorf("plain source file should not be denied, got %q", denied["internal/cli/ai.go"])
	}
}

func TestGatherWIPBadRecordReturnsError(t *testing.T) {
	stdout := buildV2("X bogus record")
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	_, err := GatherWIP(context.Background(), fake, GatherOptions{})
	if err == nil {
		t.Fatal("want parse error for unknown record type")
	}
}

func TestGatherWIPRunnerError(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {
				Stderr:   "fatal: not a git repository",
				ExitCode: 128,
			},
		},
	}
	_, err := GatherWIP(context.Background(), fake, GatherOptions{})
	if err == nil {
		t.Fatal("want error when git status fails")
	}
}

// TestGatherWIPDetectsBinary guards against a regression where IsBinary
// stayed false for every entry because GatherWIP forgot to call
// DetectBinary. Without this flag the downstream summariseForSecretScan
// and concatFileDiffs gates were no-ops, leaking __pycache__/*.pyc and
// other binary blobs into LLM payloads (blowing up token budgets and
// producing garbage in --show-prompt output).
func TestGatherWIPDetectsBinary(t *testing.T) {
	dir := t.TempDir()

	textPath := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(textPath, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}

	binPath := filepath.Join(dir, "blob.bin")
	binContent := []byte{0x00, 0x01, 0x02, 0x03, 'a', 'b', 0x00, 0xff, 0xfe}
	if err := os.WriteFile(binPath, binContent, 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}

	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	stdout := buildV2(
		"? hello.txt",
		"? blob.bin",
	)
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
		},
	}
	entries, err := GatherWIP(context.Background(), fake, GatherOptions{})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}

	got := map[string]bool{}
	for _, e := range entries {
		got[e.Path] = e.IsBinary
	}
	if got["hello.txt"] {
		t.Errorf("hello.txt: want IsBinary=false, got true")
	}
	if !got["blob.bin"] {
		t.Errorf("blob.bin: want IsBinary=true, got false")
	}
}
