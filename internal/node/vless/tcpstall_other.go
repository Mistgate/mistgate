//go:build !linux

package vless

import "net"

// tcpStallOf: TCP_INFO with byte counters is Linux's; elsewhere no stall is counted (nodes run Linux).
func tcpStallOf(net.Conn) (stalled, first16K bool) { return false, false }
