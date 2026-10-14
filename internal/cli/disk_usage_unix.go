//go:build unix

package cli

import (
	"io/fs"
	"syscall"
)

// diskBytes is the space a file occupies, like du: allocated blocks, so a
// sparse file counts what it uses.
func diskBytes(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return info.Size()
}
