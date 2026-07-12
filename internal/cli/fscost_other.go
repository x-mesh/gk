//go:build !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package cli

// fsWatchCostPerFile: inotify (linux) and ReadDirectoryChangesW (windows)
// track watches per directory without holding a descriptor per file, so only
// directories count against the watch budget.
const fsWatchCostPerFile = false
