package cli

import "time"

// Shared knobs for the live `gk status --watch` change feed (status_watch_files.go).
//
// The interval floor protects against a runaway refresh loop when a user holds
// `-`; the ceiling keeps the displayed "every Ns" readable. The default 2s sits
// between them. watchPulseDuration is how long the "● just changed" accent stays
// lit after a transition — long enough to catch, short enough that quiet feels
// quiet.
const (
	watchMinInterval   = 250 * time.Millisecond
	watchMaxInterval   = 60 * time.Second
	watchPulseDuration = 1500 * time.Millisecond
)

func clampWatchInterval(d time.Duration) time.Duration {
	if d < watchMinInterval {
		return watchMinInterval
	}
	if d > watchMaxInterval {
		return watchMaxInterval
	}
	return d
}

// truncateForHeader trims s to max columns with a trailing ellipsis. Byte-length
// based — adequate for the short ASCII header fragments it guards.
func truncateForHeader(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}
