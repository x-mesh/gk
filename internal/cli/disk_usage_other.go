//go:build !unix

package cli

import "io/fs"

// diskBytes falls back to the file size where syscall.Stat_t has no block
// count, such as Windows.
func diskBytes(info fs.FileInfo) int64 {
	return info.Size()
}
