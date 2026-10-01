//go:build linux

package doctor

import "syscall"

const hostSupported = true

func statfs(path string) (FSStat, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return FSStat{}, err
	}
	return FSStat{
		BlockSize: uint64(st.Bsize), Blocks: st.Blocks, Bfree: st.Bfree, Bavail: st.Bavail,
		Files: st.Files, Ffree: st.Ffree,
	}, nil
}
