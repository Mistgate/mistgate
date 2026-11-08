package sysstat

import "syscall"

// DiskUsage is the size and the used bytes of the file system holding path (0, 0 when it cannot be read).
func DiskUsage(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	used = (st.Blocks - st.Bfree) * uint64(st.Bsize)
	return total, used
}
