//go:build !linux

package hostctl

import "context"

var _ TunnelHost = stub{}

func (stub) SetTunnels(context.Context, []Tunnel) error { return nil }

func (stub) TunnelCounters(context.Context) (map[uint16]uint64, error) {
	return map[uint16]uint64{}, nil
}

// CleanupTunnels has nothing to remove on this OS.
func CleanupTunnels(context.Context) error { return nil }
