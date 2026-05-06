package update

import (
	"fmt"
	"strconv"
	"strings"
)

// CompareSemver returns -1 if a < b, 0 if equal, +1 if a > b. Both inputs
// may carry a leading "v". A non-numeric segment is treated as the largest
// possible value, which makes "dev" comparisons against any release tag
// always trigger an update — exactly what unreleased builds want.
//
// Pre-release suffixes (e.g. "-rc1") are ignored: "v0.30.0-rc1" compares
// equal to "v0.30.0". gk does not currently ship pre-releases, so handling
// them precisely is YAGNI.
func CompareSemver(a, b string) int {
	ax, aDirty := parseVersion(a)
	bx, bDirty := parseVersion(b)
	for i := 0; i < 3; i++ {
		switch {
		case ax[i] < bx[i]:
			return -1
		case ax[i] > bx[i]:
			return 1
		}
	}
	// Treat dev/non-numeric as "always older" so `gk update` from a dev
	// build always offers the latest release rather than refusing.
	switch {
	case aDirty && !bDirty:
		return -1
	case !aDirty && bDirty:
		return 1
	}
	return 0
}

// parseVersion returns (major, minor, patch) plus a dirty flag set when any
// segment was non-numeric or missing.
func parseVersion(v string) ([3]int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	// Drop pre-release/build metadata.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := [3]int{}
	dirty := false
	for i := 0; i < 3; i++ {
		if i >= len(parts) {
			dirty = true
			continue
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			dirty = true
			continue
		}
		out[i] = n
	}
	return out, dirty
}

// FormatPlan renders a one-line "current → next" string for human output.
func FormatPlan(current, next string) string {
	return fmt.Sprintf("%s → %s", strings.TrimPrefix(current, "v"), strings.TrimPrefix(next, "v"))
}
