//go:build !windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// An untracked symbolic link shows as a link. Its target can be a file outside
// the worktree, a private key for example, so the pane does not read it.
func TestLoadChangePreviewDoesNotFollowASymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOP-SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	p := loadChangePreview(context.Background(), &git.ExecRunner{Dir: dir}, dir, "link.txt", fileSig{xy: "??"}, "")
	if !strings.Contains(p.note, "symbolic link") || !strings.Contains(p.note, outside) {
		t.Errorf("note = %q, want the link and its target", p.note)
	}
	if len(p.rows) != 0 || strings.Contains(joined(p)+p.note, "TOP-SECRET") {
		t.Errorf("the pane read the target of the link: %+v", p)
	}
}

// git does not list a named pipe as untracked, but a file can become one
// between the snapshot and the read. A read of a pipe waits for a writer, so
// the pane does not start one.
func TestLoadChangePreviewDoesNotReadANamedPipe(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "pending.pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan changePreview, 1)
	go func() {
		done <- loadChangePreview(context.Background(), &git.ExecRunner{Dir: dir}, dir, "pending.pipe", fileSig{xy: "??"}, "")
	}()
	select {
	case p := <-done:
		if !strings.Contains(p.note, "named pipe") || len(p.rows) != 0 {
			t.Errorf("preview = %+v, want a note", p)
		}
	case <-time.After(5 * time.Second):
		// Let the blocked read end: a writer opens the pipe and closes it.
		if w, err := os.OpenFile(pipe, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		t.Fatal("the read of a named pipe did not return")
	}
}
