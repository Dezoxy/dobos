//go:build linux || darwin

package warmup

import (
	"os"
	"syscall"
)

// diskBytes is what a file holds on disk: st_blocks in 512-byte units. A sparse file
// counts its written blocks, not its length.
func diskBytes(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks) * 512
	}
	return fi.Size()
}
