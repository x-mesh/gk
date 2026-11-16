package git

import (
	"regexp"
	"strconv"
)

// versionRE extracts MAJOR.MINOR from strings like "git version 2.54.0".
var versionRE = regexp.MustCompile(`(?m)version\s+(\d+)\.(\d+)`)

// ParseVersion returns the major and minor git version numbers. On parse
// failure it returns 0, 0.
func ParseVersion(s string) (major, minor int) {
	m := versionRE.FindStringSubmatch(s)
	if len(m) != 3 {
		return 0, 0
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor
}

// historyMergeDiffMinMinor is the first minor release with --remerge-diff.
const historyMergeDiffMinMinor = 36

// HistoryLogMergeDiffFlag names the git log flag that shows what a merge commit
// introduced, including conflict-resolution content. Git 2.36+ has --remerge-diff;
// older versions use --cc with -p/--raw.
func HistoryLogMergeDiffFlag(major, minor int) string {
	if major > 2 || (major == 2 && minor >= historyMergeDiffMinMinor) {
		return "--remerge-diff"
	}
	return "--cc"
}
