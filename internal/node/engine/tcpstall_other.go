//go:build !linux

package engine

import "net"

// TCPStallOf: TCP_INFO with byte counters is Linux's; elsewhere no stall is counted (nodes run Linux).
func TCPStallOf(net.Conn) (stalled, first16K bool) { return false, false }
