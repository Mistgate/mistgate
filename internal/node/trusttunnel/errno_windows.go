//go:build windows

package trusttunnel

import "syscall"

// Winsock error numbers: syscall.ECONNREFUSED and the others are placeholders on Windows, a failed connect returns these.
const (
	wsaeAddrNotAvail = 10049
	wsaeNetUnreach   = 10051
	wsaeConnRefused  = 10061
	wsaeHostUnreach  = 10065
)

func errnoRefused(e syscall.Errno) bool { return e == wsaeConnRefused }

func errnoUnreachable(e syscall.Errno) bool {
	return e == wsaeNetUnreach || e == wsaeHostUnreach || e == wsaeAddrNotAvail
}
