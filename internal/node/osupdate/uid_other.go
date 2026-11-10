//go:build !linux

package osupdate

func currentUID() int { return -1 }
