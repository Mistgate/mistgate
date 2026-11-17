//go:build windows

package trusttunnel

import "syscall"

const testUnreachable = syscall.Errno(wsaeNetUnreach)
