//go:build !linux

package sysstat

// DiskUsage is not read outside Linux.
func DiskUsage(string) (total, used uint64) { return 0, 0 }
