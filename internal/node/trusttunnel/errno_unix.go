//go:build !windows

package trusttunnel

import "syscall"

func errnoRefused(e syscall.Errno) bool { return e == syscall.ECONNREFUSED }

// errnoUnreachable: no route to the network or the host, or no local address to reach it from (IPv6 disabled or without an
// address on the host: the connect fails with EADDRNOTAVAIL).
func errnoUnreachable(e syscall.Errno) bool {
	return e == syscall.ENETUNREACH || e == syscall.EHOSTUNREACH || e == syscall.EADDRNOTAVAIL
}
