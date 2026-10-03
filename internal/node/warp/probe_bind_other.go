//go:build !linux

package warp

import (
	"errors"
	"net"
)

func bindProbeDevice(_ *net.Dialer, iface string) error {
	if iface == "" {
		return nil
	}
	return errors.New("warp probe: binding to the WARP interface is supported only on Linux")
}
