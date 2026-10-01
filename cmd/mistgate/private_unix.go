//go:build unix

package main

import "syscall"

// privateUmask makes every file the process creates (database, WAL, ACME cache ...)
// unreadable to group and others unless the code asks for more.
func privateUmask() { syscall.Umask(0o077) }
