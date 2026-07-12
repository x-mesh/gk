//go:build windows

package cli

// Windows has no RLIMIT_NOFILE and its fsnotify backend (ReadDirectoryChangesW)
// does not consume a descriptor per watched file, so both hooks are no-ops.

func raiseFDLimit() {}

func fdSoftLimit() uint64 { return 0 }
