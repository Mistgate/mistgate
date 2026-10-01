//go:build !linux

package awgprep

// aptBusy: package managers with these locks exist on Linux only (the module is Linux-only too).
func aptBusy() bool { return false }
