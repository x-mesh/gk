package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/x-mesh/gk/internal/git"
)

// --- fsnotify trigger for the change feed --------------------------------------
//
// The change feed (`gk status --watch`) is polling-first, but a poll only
// sees the NET change between ticks and burns a `git status` on every tick even
// when nothing moved. This adds a filesystem-event trigger: fsnotify wakes the
// feed the instant a file changes (idle cost ≈ 0), while a slow heartbeat poll
// stays as a safety net for changes events can't see (e.g. an index-only `git
// add`). When fsnotify can't be set up — unsupported platform, a tree too large
// for the descriptor budget, or any setup error — the caller falls back to plain
// interval polling, so the feature degrades instead of failing.

const (
	// fsWatchDebounce coalesces the burst of events an editor or agent emits
	// for one logical save (write + chmod, or the create+rename of an atomic
	// write) into a single feed refresh.
	fsWatchDebounce = 200 * time.Millisecond
	// fsWatchMaxDirs caps how many directories we register. kqueue (macOS) and
	// inotify (Linux) consume a descriptor per watched directory, so an
	// unbounded walk of a giant tree would exhaust the process's fd budget.
	// Past the cap we tear down and let the caller poll instead.
	fsWatchMaxDirs = 2048
	// fsHeartbeatInterval is the safety-net poll cadence while fsnotify drives
	// the feed — slow, because events do the real work; this only catches the
	// rare change no watched path observed.
	fsHeartbeatInterval = 12 * time.Second
)

var errTooManyDirs = errors.New("fs watch: directory budget exceeded")

// fsWatcher is a recursive, gitignore-aware filesystem watcher. events emits one
// debounced signal per burst of changes; Close tears it down.
//
// Threading: the loop goroutine is the SOLE owner of w (it Adds directories as
// they appear), so it — not Close() — closes the underlying watcher. Close()
// only signals done and waits for the loop to release the descriptors, which
// avoids racing fw.w.Add against fw.w.Close.
type fsWatcher struct {
	w       *fsnotify.Watcher
	events  chan struct{}
	done    chan struct{}
	closed  chan struct{} // loop closes this after it tears the watcher down
	once    sync.Once     // makes Close() idempotent (no double-close panic)
	ignored map[string]bool
}

// newFSWatcher sets up a watcher over the repo's working tree. Returns
// (nil, false) when fsnotify is unusable (the caller then polls): an unsupported
// platform, a setup error, or a tree exceeding fsWatchMaxDirs.
func newFSWatcher(ctx context.Context, runner *git.ExecRunner, debounce time.Duration) (*fsWatcher, bool) {
	root := repoToplevel(ctx, runner)
	if root == "" {
		return nil, false
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, false
	}

	ignored := ignoredDirs(ctx, runner, root)
	ignored[filepath.Join(root, ".git")] = true // never recurse into the gitdir

	count := 0
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil // unreadable entry / non-dir — skip, don't abort the walk
		}
		if ignored[path] {
			return filepath.SkipDir
		}
		if aerr := w.Add(path); aerr != nil {
			return nil // best-effort per directory
		}
		count++
		if count > fsWatchMaxDirs {
			return errTooManyDirs
		}
		return nil
	})
	if errors.Is(walkErr, errTooManyDirs) {
		_ = w.Close()
		return nil, false
	}

	fw := &fsWatcher{
		w: w, events: make(chan struct{}, 1),
		done: make(chan struct{}), closed: make(chan struct{}), ignored: ignored,
	}
	go fw.loop(debounce)
	return fw, true
}

// loop debounces raw fsnotify events into fw.events and grows the watch set as
// new directories appear (kqueue/inotify are per-directory, not recursive).
func (fw *fsWatcher) loop(debounce time.Duration) {
	// Defers run LIFO: drain-signal events, then close the watcher (this
	// goroutine owns it), then signal `closed` last so Close() unblocks only
	// after the OS descriptors are released.
	defer close(fw.closed)
	defer func() { _ = fw.w.Close() }()
	defer close(fw.events)
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case ev, ok := <-fw.w.Events:
			if !ok {
				return
			}
			// A freshly created directory must be watched too, or edits inside
			// it would go unseen until the next heartbeat.
			if ev.Op&fsnotify.Create != 0 && !fw.ignored[ev.Name] {
				if fi, serr := os.Stat(ev.Name); serr == nil && fi.IsDir() {
					_ = fw.w.Add(ev.Name)
				}
			}
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
			}
		case <-timerC:
			select {
			case fw.events <- struct{}{}:
			default: // a pending signal is already queued — coalesce
			}
			timer, timerC = nil, nil
		case <-fw.w.Errors:
			// Best-effort: a transient watcher error shouldn't kill the feed.
		case <-fw.done:
			if timer != nil {
				timer.Stop()
			}
			return
		}
	}
}

// Close stops the debounce loop and waits for it to release the OS watch
// descriptors. Idempotent and safe to call from any goroutine: the loop owns
// the watcher, so Close() never touches fw.w directly.
func (fw *fsWatcher) Close() {
	if fw == nil {
		return
	}
	fw.once.Do(func() { close(fw.done) })
	<-fw.closed // wait for the loop's teardown (drain + watcher close)
}

// repoToplevel returns the working-tree root, or "" on any error.
func repoToplevel(ctx context.Context, runner *git.ExecRunner) string {
	out, _, err := runner.Run(ctx, "--no-optional-locks", "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ignoredDirs returns the absolute paths of gitignored directories so the
// watcher never recurses into node_modules/.venv/target/etc. — the usual
// source of a descriptor blowup. Only directory entries (trailing "/") are
// collected; individually-ignored files are harmless (a stray wake at most).
func ignoredDirs(ctx context.Context, runner *git.ExecRunner, root string) map[string]bool {
	set := map[string]bool{}
	out, _, err := runner.Run(ctx, "--no-optional-locks", "ls-files", "-z",
		"--others", "--ignored", "--exclude-standard", "--directory", "--no-empty-directory")
	if err != nil {
		return set
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || !strings.HasSuffix(rel, "/") {
			continue
		}
		set[filepath.Join(root, rel)] = true
	}
	return set
}
