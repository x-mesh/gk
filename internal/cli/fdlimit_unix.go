//go:build unix

package cli

import (
	"sync"
	"syscall"
)

// --- file-descriptor headroom for the watch family -----------------------------
//
// On macOS fsnotify runs on kqueue, which opens one descriptor for EVERY
// watched file — not just every directory. A multi-repo watch over a handful
// of active worktrees can therefore hold thousands of descriptors, and once
// the process limit is hit every later git subprocess dies with EMFILE
// ("open /dev/null: too many open files") — the dashboard reads as if every
// repo became unreachable at once. Two defenses live here: raise the soft
// limit to the hard limit before watchers start, and expose the (post-raise)
// soft limit so the watch budget can be sized from reality instead of a
// constant.

var fdLimitOnce sync.Once

// raiseFDLimit lifts RLIMIT_NOFILE's soft limit toward the hard limit,
// best-effort and at most once. On darwin a request above
// kern.maxfilesperproc fails with EINVAL even when the hard limit reads
// "unlimited", so instead of one all-or-nothing call this walks a descending
// ladder and keeps the first value the kernel accepts — a default 256-fd
// terminal reliably lands on a five-digit limit instead of silently staying
// tiny. Errors are ignored throughout: a failed raise just means the
// fd-aware budget sizes itself to the smaller limit.
func raiseFDLimit() {
	fdLimitOnce.Do(func() {
		var lim syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
			return
		}
		if lim.Cur >= 1<<20 {
			return // already huge — nothing useful to gain
		}
		ladder := []rlimVal{1 << 20, 262144, 184320, 65536, 10240}
		for _, want := range ladder {
			if want <= lim.Cur || want > lim.Max {
				continue
			}
			next := lim
			next.Cur = want
			if syscall.Setrlimit(syscall.RLIMIT_NOFILE, &next) == nil {
				return
			}
		}
		// Last resort: a finite hard limit below the lowest rung (e.g. a
		// container capped at a few thousand) — take all of it rather than
		// silently staying at the tiny soft default.
		if lim.Max > lim.Cur && lim.Max < 1<<20 {
			next := lim
			next.Cur = lim.Max
			_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &next)
		}
	})
}

// fdSoftLimit returns the current RLIMIT_NOFILE soft limit (0 when unknown).
func fdSoftLimit() uint64 {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0
	}
	return uint64(lim.Cur)
}
