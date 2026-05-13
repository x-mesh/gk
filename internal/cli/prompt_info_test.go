package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestDetectPromptInfo_Primary verifies that a fresh repo's root reports
// Linked=false — the load-bearing bit for "should the prompt show a marker?".
func TestDetectPromptInfo_Primary(t *testing.T) {
	repo := testutil.NewRepo(t)
	r := &git.ExecRunner{Dir: repo.Dir}
	info := detectPromptInfo(context.Background(), r)
	if info.Linked {
		t.Fatalf("expected Linked=false in primary worktree, got %+v", info)
	}
}

// TestDetectPromptInfo_Linked walks the full happy path through a real
// linked worktree, since the primary signal (--git-dir vs --git-common-dir)
// is the kind of thing that's brittle to mock.
func TestDetectPromptInfo_Linked(t *testing.T) {
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "linked")
	repo.RunGit("worktree", "add", "-b", "feat-x", wtPath)

	r := &git.ExecRunner{Dir: wtPath}
	info := detectPromptInfo(context.Background(), r)
	if !info.Linked {
		t.Fatalf("expected Linked=true in linked worktree, got %+v", info)
	}
	if info.Name != "linked" {
		t.Errorf("Name: want %q, got %q", "linked", info.Name)
	}
	if info.Branch != "feat-x" {
		t.Errorf("Branch: want %q, got %q", "feat-x", info.Branch)
	}
	// Path normalization — macOS symlinks /tmp → /private/tmp, so just
	// require the basename match instead of an absolute equality check.
	if filepath.Base(info.Path) != "linked" {
		t.Errorf("Path: want basename %q, got %q", "linked", info.Path)
	}
}

// TestDetectPromptInfo_OutsideRepo verifies prompt-safe silence — git
// errors must collapse to "not linked" so prompts that pipe this output
// never see noise.
func TestDetectPromptInfo_OutsideRepo(t *testing.T) {
	dir := t.TempDir()
	r := &git.ExecRunner{Dir: dir}
	info := detectPromptInfo(context.Background(), r)
	if info.Linked {
		t.Fatalf("expected Linked=false outside a repo, got %+v", info)
	}
}

// TestPromptInfo_JSONShape locks the field names since prompt frameworks
// (starship, p10k segments) parse this output verbatim — renaming a field
// breaks user config.
func TestPromptInfo_JSONShape(t *testing.T) {
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "lnk")
	repo.RunGit("worktree", "add", "-b", "topic/x", wtPath)

	r := &git.ExecRunner{Dir: wtPath}
	info := detectPromptInfo(context.Background(), r)

	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(raw)
	for _, key := range []string{`"linked":true`, `"name":"lnk"`, `"branch":"topic/x"`, `"path":`} {
		if !strings.Contains(out, key) {
			t.Errorf("JSON missing %q: %s", key, out)
		}
	}
}
