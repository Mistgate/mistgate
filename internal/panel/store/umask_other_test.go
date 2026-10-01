//go:build !unix

package store

func umask(int) int { return 0 }
