package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/x-mesh/gk/internal/git"
)

// worktreeLock describes a worktree's lock state, enriched with whether
// the lock holder is still alive. git refuses to remove a locked worktree
// unless `--force` is passed twice (`-f -f`); gk gates that behind the
// holder being dead (stale) vs running (in use) so a live claude-agent
// worktree isn't yanked out from under a working process.
type worktreeLock struct {
	Locked bool
	Reason string
	// Alive is true when the lock reason names a pid that is still
	// running. False for a dead pid (stale lock) or a reason with no pid.
	Alive bool
}

// lockPidRe extracts the pid from a lock reason like
// "claude agent agent-xyz (pid 12070)". gk and several tools embed the
// owning process id this way; a reason without one yields pid 0.
var lockPidRe = regexp.MustCompile(`pid (\d+)`)

// worktreeLockInfo parses `git worktree list --porcelain` for the block
// matching path and reports its lock state. Symlink-resolved path equality
// (sameDir) keeps macOS /var → /private/var aliasing from missing a match.
func worktreeLockInfo(ctx context.Context, runner *git.ExecRunner, path string) worktreeLock {
	out, _, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return worktreeLock{}
	}
	for _, blk := range strings.Split(strings.TrimRight(string(out), "\n"), "\n\n") {
		var p, reason string
		var locked bool
		for _, ln := range strings.Split(blk, "\n") {
			switch {
			case strings.HasPrefix(ln, "worktree "):
				p = strings.TrimPrefix(ln, "worktree ")
			case ln == "locked":
				locked = true
			case strings.HasPrefix(ln, "locked "):
				locked = true
				reason = strings.TrimPrefix(ln, "locked ")
			}
		}
		if p == "" || !sameDir(p, path) {
			continue
		}
		alive := false
		if pid := pidFromLockReason(reason); pid > 0 {
			alive = pidAlive(pid)
		}
		return worktreeLock{Locked: locked, Reason: reason, Alive: alive}
	}
	return worktreeLock{}
}

// pidFromLockReason returns the pid embedded in a lock reason, or 0 when
// the reason carries none.
func pidFromLockReason(reason string) int {
	m := lockPidRe.FindStringSubmatch(reason)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// pidAlive reports whether pid names a running process, via signal 0
// (the portable "does this process exist" probe). EPERM means it exists
// but is owned by another user — still alive for our purposes.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

// forceRemoveWorktree unlocks (best-effort, since the worktree may not be
// locked) then removes with --force. This is the `-f -f` equivalent: a
// single `worktree remove --force` leaves a lock in place, so the explicit
// unlock is what lets a locked worktree actually go.
func forceRemoveWorktree(ctx context.Context, runner *git.ExecRunner, w io.Writer, path string) error {
	// Best-effort unlock: ignore the error when it wasn't locked.
	_, _, _ = runner.Run(ctx, "worktree", "unlock", path)
	if _, stderr, err := runner.Run(ctx, "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("worktree remove --force: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintf(w, "removed worktree %s\n", path)
	return nil
}
