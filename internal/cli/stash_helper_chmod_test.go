//go:build !windows

package cli

import "os"

// chmodOrSkip toggles the executable bit on path. On unixy filesystems
// this exercises the mode-bit detection branch of
// describeDirtyButNotStashed; on Windows / NTFS the build tag skips
// this file because filemode tracking is meaningless there.
func chmodOrSkip(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.Chmod(path, info.Mode()|0o111)
}
