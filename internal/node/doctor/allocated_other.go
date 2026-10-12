//go:build !linux

package doctor

import "os"

func allocatedSize(fi os.FileInfo) int64 { return fi.Size() }
