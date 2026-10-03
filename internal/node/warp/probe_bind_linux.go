//go:build linux

package warp

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

func bindProbeDevice(d *net.Dialer, iface string) error {
	if iface == "" {
		return nil
	}
	d.ControlContext = func(_ context.Context, _, _ string, raw syscall.RawConn) error {
		var sockErr error
		if err := raw.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
		}); err != nil {
			return err
		}
		if sockErr != nil {
			return fmt.Errorf("warp probe: bind to %s: %w", iface, sockErr)
		}
		return nil
	}
	return nil
}
