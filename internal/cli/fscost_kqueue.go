//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package cli

// fsWatchCostPerFile: on kqueue platforms fsnotify opens one file descriptor
// for EVERY file inside a watched directory, so the watch budget must count
// files, not just directories.
const fsWatchCostPerFile = true
