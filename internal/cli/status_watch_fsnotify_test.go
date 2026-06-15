package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestFSWatcher_FiresOnChange verifies the fsnotify trigger wakes on a write
// and that the burst is debounced into a signal on fw.events.
func TestFSWatcher_FiresOnChange(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}

	fw, ok := newFSWatcher(context.Background(), runner, 50*time.Millisecond)
	if !ok {
		t.Skip("fsnotify unavailable in this environment")
	}
	defer fw.Close()

	repo.WriteFile("b.txt", "new content") // a change under the watched root

	select {
	case <-fw.events:
		// fired as expected
	case <-time.After(3 * time.Second):
		t.Fatal("fs watcher did not fire on a file write within 3s")
	}
}

// TestFSWatcher_NewDirIsWatched verifies the recursive growth path: a directory
// created at runtime is added to the watch set, so edits inside it still fire.
func TestFSWatcher_NewDirIsWatched(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}

	fw, ok := newFSWatcher(context.Background(), runner, 50*time.Millisecond)
	if !ok {
		t.Skip("fsnotify unavailable in this environment")
	}
	defer fw.Close()

	// Create a new subdir (fires + gets added), drain that signal, then write
	// a file inside it — the second write must still wake the watcher.
	sub := filepath.Join(repo.Dir, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	select {
	case <-fw.events:
	case <-time.After(3 * time.Second):
		t.Fatal("mkdir did not fire")
	}
	if err := os.WriteFile(filepath.Join(sub, "f.go"), []byte("package pkg"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-fw.events:
	case <-time.After(3 * time.Second):
		t.Fatal("write inside a runtime-created dir did not fire — recursive add broken")
	}
}

// TestFSWatcher_CloseIdempotent guards the double-close panic / Add-vs-Close
// race fixes: Close must be safe to call twice (and on a nil receiver) without
// panicking or hanging.
func TestFSWatcher_CloseIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	fw, ok := newFSWatcher(context.Background(), &git.ExecRunner{Dir: repo.Dir}, 50*time.Millisecond)
	if !ok {
		t.Skip("fsnotify unavailable in this environment")
	}
	fw.Close()
	fw.Close() // second call must be a no-op, not a panic or a hang

	var nilFW *fsWatcher
	nilFW.Close() // nil receiver is also safe
}

// TestIgnoredDirs verifies gitignored directories are collected so the watcher
// never recurses into them (the descriptor-blowup guard).
func TestIgnoredDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile(".gitignore", "node_modules/\n")
	repo.RunGit("add", ".gitignore")
	repo.Commit("add gitignore")

	if err := os.MkdirAll(filepath.Join(repo.Dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, "node_modules", "x.js"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	set := ignoredDirs(context.Background(), &git.ExecRunner{Dir: repo.Dir}, repo.Dir)
	if !set[filepath.Join(repo.Dir, "node_modules")] {
		t.Errorf("node_modules must be in the ignored set, got %v", set)
	}
}
