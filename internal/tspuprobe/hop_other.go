//go:build !linux

package tspuprobe

import (
	"context"
	"net/netip"
	"time"
)

// Supported is false: the probe needs a Linux TCP socket with IP_TTL and IP_RECVERR.
func Supported() bool { return false }

func synHop(context.Context, netip.AddrPort, int, time.Duration) (hop, error) {
	return hopSilent, ErrUnsupported
}
