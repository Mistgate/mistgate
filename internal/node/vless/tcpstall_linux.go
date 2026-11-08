//go:build linux

package vless

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// tcpStallOf reads TCP_INFO of a TCP connection that is about to be closed (one getsockopt) and applies the stall rule
// (links.go). Anything that is not a TCP socket, or a failed read, is no stall.
func tcpStallOf(conn net.Conn) (stalled, first16K bool) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return false, false
	}
	if _, tcp := conn.LocalAddr().(*net.TCPAddr); !tcp {
		return false, false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return false, false
	}
	var info *unix.TCPInfo
	if err := raw.Control(func(fd uintptr) {
		info, err = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	}); err != nil || info == nil {
		return false, false
	}
	return tcpStallRule(info.Total_retrans, info.Bytes_sent, info.Bytes_retrans, info.Bytes_acked, info.Unacked)
}
