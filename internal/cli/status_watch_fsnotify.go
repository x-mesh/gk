package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	// fsWatchMaxDirs caps how many directories we register on platforms where
	// a watch costs one descriptor per DIRECTORY (inotify). kqueue platforms
	// derive a larger, fd-aware budget instead — see fsWatchCostBudget.
	fsWatchMaxDirs = 2048
	// fsHeartbeatInterval is the safety-net poll cadence while fsnotify drives
	// the feed — slow, because events do the real work; this only catches the
	// rare change no watched path observed.
	fsHeartbeatInterval = 12 * time.Second
)

var errTooManyDirs = errors.New("fs watch: watch budget exceeded")

// fsWatchCostBudget is the process-wide watch budget in COST units. On kqueue
// platforms the true cost of a watch is one descriptor per FILE (fsnotify
// opens each file inside a watched directory), so the budget counts files and
// derives from the fd soft limit — a third of it, leaving the rest for
// subprocess pipes and everything else the process opens. Exceeding the fd
// limit is not a degraded mode, it is an outage: every later git spawn dies
// with EMFILE and the whole dashboard reads "unreachable". On inotify
// platforms watches are not process fds, so the historical directory cap
// stands.
func fsWatchCostBudget() int {
	if !fsWatchCostPerFile {
		return fsWatchMaxDirs
	}
	raiseFDLimit()
	soft := fdSoftLimit()
	if soft == 0 {
		return fsWatchMaxDirs
	}
	budget := int(soft / 3)
	if budget > 1<<16 {
		budget = 1 << 16
	}
	if budget < 256 {
		budget = 256
	}
	return budget
}

// isFDExhausted reports whether err is the process (EMFILE) or system
// (ENFILE) descriptor limit — the one failure a watcher must never shrug off
// mid-walk, because the descriptors it already holds are what is starving
// everything else.
func isFDExhausted(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

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
	// runner/ctx let the loop re-check git-ignore status for directories
	// created AFTER startup (the `ignored` set is only the startup snapshot).
	runner git.Runner
	ctx    context.Context
	// cost is owned by loop, like w. It includes every startup watch and every
	// runtime directory admitted afterwards, so a tree that grows while watch
	// is running cannot silently spend past its original share.
	cost    int
	costCap int
	// tree and add are test seams for the runtime admission path. Production
	// uses fsWatchTree and w.Add respectively.
	tree func(string, int) (int, []string, error)
	add  func(string) error
}

// newFSWatcher sets up a watcher over the repo's working tree. costCap bounds
// this watcher's share of the process watch budget (fsWatchCostBudget) — the
// whole budget for a solo watcher, a split when several watchers coexist
// (`gk watch` runs one per active worktree). Cost counts directories, plus
// every file on kqueue platforms where each watched file holds a descriptor.
// Returns (nil, false) when fsnotify is unusable (the caller then polls): an
// unsupported platform, a setup error, a tree exceeding costCap, or — the
// case that must abort HARD — descriptor exhaustion mid-walk, where keeping
// the half-built watcher would starve every git subprocess after it.
func newFSWatcher(ctx context.Context, runner *git.ExecRunner, debounce time.Duration, costCap int) (*fsWatcher, bool) {
	raiseFDLimit()
	root := repoToplevel(ctx, runner)
	if root == "" {
		return nil, false
	}
	if costCap <= 0 {
		costCap = fsWatchCostBudget()
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, false
	}

	ignored := ignoredDirs(ctx, runner, root)
	ignored[filepath.Join(root, ".git")] = true // never recurse into the gitdir

	count := 0
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if isFDExhausted(err) {
				// WalkDir's own directory reads hit the fd wall (opendir →
				// EMFILE), not just our Adds — same hard abort, same reason.
				return err
			}
			return nil // unreadable entry — skip, don't abort the walk
		}
		if !d.IsDir() {
			if fsWatchCostPerFile {
				count++ // kqueue: every file in a watched dir costs one fd
				if count > costCap {
					return errTooManyDirs
				}
			}
			return nil
		}
		if ignored[path] {
			return filepath.SkipDir
		}
		if aerr := w.Add(path); aerr != nil {
			if isFDExhausted(aerr) {
				return aerr // hard abort: holding on would starve the process
			}
			return nil // best-effort per directory
		}
		count++
		if count > costCap {
			return errTooManyDirs
		}
		return nil
	})
	if walkErr != nil {
		_ = w.Close()
		return nil, false
	}

	fw := &fsWatcher{
		w: w, events: make(chan struct{}, 1),
		done: make(chan struct{}), closed: make(chan struct{}), ignored: ignored,
		runner: runner, ctx: ctx, cost: count, costCap: costCap,
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
			// it would go unseen until the next heartbeat — UNLESS it's
			// gitignored (e.g. node_modules from an `npm install` mid-watch),
			// which would blow the descriptor budget and spam refreshes. The
			// startup `ignored` set can't know about dirs created later, so
			// re-check with `git check-ignore`. Children of a dir we decline to
			// Add never fire events, so this stays one check per new top dir.
			if ev.Op&fsnotify.Create != 0 {
				if fi, serr := os.Stat(ev.Name); serr == nil && fi.IsDir() &&
					!fw.ignored[ev.Name] && !fw.isIgnored(ev.Name) {
					if err := fw.admitRuntimeDir(ev.Name); isFDExhausted(err) {
						// Keep no half-grown watcher after hitting the process fd
						// wall. Closing it releases its descriptors and lets the
						// caller's heartbeat polling take over completely.
						return
					}
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

// admitRuntimeDir applies the same cost cap used during startup before adding
// a directory created after the watcher began. A cap miss deliberately leaves
// the existing watcher alive: the feed's heartbeat poll then observes changes
// below that directory. Descriptor exhaustion is returned to loop so it can
// release all watcher descriptors and fall back to polling for the whole tree.
func (fw *fsWatcher) admitRuntimeDir(path string) error {
	if fw.cost >= fw.costCap {
		return errTooManyDirs
	}
	cost, dirs, err := fw.runtimeWatchTree(path, fw.costCap-fw.cost)
	if err != nil {
		return err
	}
	if fw.cost+cost > fw.costCap {
		return errTooManyDirs
	}
	add := fw.add
	if add == nil {
		add = fw.w.Add
	}
	for _, dir := range dirs {
		if err := add(dir); err != nil {
			if isFDExhausted(err) {
				return err
			}
			// Match startup's best-effort behavior for ordinary per-directory
			// failures. Reserve the cost nevertheless: a failed Add can have
			// opened descriptors before returning, and undercounting is unsafe.
		}
	}
	fw.cost += cost
	return nil
}

// runtimeWatchTree keeps runtime subtree growth subject to the same skip rules
// as startup. In particular, a newly-created parent can already contain an
// ignored child or nested .git directory before its Create event is handled.
func (fw *fsWatcher) runtimeWatchTree(root string, limit int) (int, []string, error) {
	if fw.tree != nil {
		return fw.tree(root, limit)
	}
	return fsWatchTree(root, limit, fw.skipRuntimeDir)
}

// fsWatchTree returns the startup-equivalent cost and all directories that
// must be added for a newly-created subtree. limit is the remaining budget;
// stopping the walk early avoids scanning a huge generated directory that is
// going to use polling anyway.
func fsWatchTree(root string, limit int, skipDir func(string) bool) (int, []string, error) {
	cost := 0
	dirs := make([]string, 0, 1)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if isFDExhausted(err) {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if skipDir != nil && skipDir(path) {
				return filepath.SkipDir
			}
			dirs = append(dirs, path)
			cost++
		} else if fsWatchCostPerFile {
			cost++
		}
		if cost > limit {
			return errTooManyDirs
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return cost, dirs, nil
}

func (fw *fsWatcher) skipRuntimeDir(path string) bool {
	if filepath.Base(path) == ".git" {
		return true
	}
	return fw.ignored[path] || fw.isIgnored(path)
}

// isIgnored reports whether path is gitignored, via `git check-ignore -q`
// (exit 0 = ignored). Any error (not ignored, or check failed) → false, so the
// directory is watched — conservative: only a confirmed-ignored dir is skipped.
func (fw *fsWatcher) isIgnored(path string) bool {
	if fw.runner == nil {
		return false
	}
	_, _, err := fw.runner.Run(fw.ctx, "check-ignore", "-q", "--", path)
	return err == nil
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
