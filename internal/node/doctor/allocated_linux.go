//go:build linux

package doctor

import (
	"os"
	"syscall"
)

func allocatedSize(fi os.FileInfo) int64 {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Blocks < 0 {
		return fi.Size()
	}
	return st.Blocks * 512
}
