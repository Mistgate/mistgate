//go:build unix

package store

import "syscall"

func umask(m int) int { return syscall.Umask(m) }
