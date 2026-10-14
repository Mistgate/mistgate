//go:build linux

package tspuprobe

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var (
	supportedOnce sync.Once
	supported     bool
)

// Supported reports whether this host can run the probe: a TCP socket accepts IP_TTL and IP_RECVERR (no privilege, no raw
// socket, no AF_PACKET; the hardened unit of the node allows AF_INET).
func Supported() bool {
	supportedOnce.Do(func() {
		fd, err := newProbeSocket(64)
		if err == nil {
			unix.Close(fd)
		}
		supported = err == nil
	})
	return supported
}

func newProbeSocket(ttl int) (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("socket: %w", err)
	}
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_TTL, ttl); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("IP_TTL: %w", err)
	}
	// without IP_RECVERR a Time Exceeded is only a soft error and never reaches the socket
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVERR, 1); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("IP_RECVERR: %w", err)
	}
	return fd, nil
}

// synHop sends one SYN with the given TTL and waits up to wait for what comes back: the connection (hopOpen), a RST
// (hopRefused), an ICMP error of a router (hopICMP: Time Exceeded, or Unreachable), or nothing (hopSilent). The socket is
// closed before the kernel retransmits more than once, so a probe is at most two SYNs.
func synHop(ctx context.Context, dst netip.AddrPort, ttl int, wait time.Duration) (hop, error) {
	if err := ctx.Err(); err != nil {
		return hopSilent, err // nothing is sent for a run that is over
	}
	fd, err := newProbeSocket(ttl)
	if err != nil {
		return hopSilent, err
	}
	defer unix.Close(fd)
	switch err := unix.Connect(fd, &unix.SockaddrInet4{Port: int(dst.Port()), Addr: dst.Addr().As4()}); {
	case err == nil:
		return hopOpen, nil
	case errors.Is(err, unix.EINPROGRESS):
	default:
		return hopSilent, fmt.Errorf("connect: %w", err) // no route, a local firewall: not an answer of the network
	}
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return hopSilent, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return hopSilent, nil
		}
		// POLLERR is always reported; POLLOUT means connected
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		n, err := unix.Poll(fds, int(min(left, 100*time.Millisecond).Milliseconds())+1)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return hopSilent, fmt.Errorf("poll: %w", err)
		}
		if n > 0 {
			return answer(fd)
		}
	}
}

// answer reads what ended the wait of a socket in SYN_SENT.
func answer(fd int) (hop, error) {
	soerr, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	if err != nil {
		return hopSilent, fmt.Errorf("SO_ERROR: %w", err)
	}
	switch errno := unix.Errno(soerr); errno {
	case 0:
		return hopOpen, nil
	case unix.ECONNREFUSED, unix.ECONNRESET:
		return hopRefused, nil
	default:
		// Time Exceeded and Unreachable of a router sit in the error queue with origin ICMP; an error of this very host
		// (a firewall's reject) has origin LOCAL and says nothing about the path.
		if drainOrigin(fd) == unix.SO_EE_ORIGIN_LOCAL {
			return hopSilent, fmt.Errorf("local error: %w", errno)
		}
		return hopICMP, nil
	}
}

// drainOrigin empties the error queue of the socket and returns the origin of the last entry (0 when it was empty).
func drainOrigin(fd int) uint8 {
	var origin uint8
	p, oob := make([]byte, 128), make([]byte, 256)
	for range 8 {
		_, oobn, _, _, err := unix.Recvmsg(fd, p, oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
		if err != nil {
			break
		}
		msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			continue
		}
		for _, m := range msgs {
			// struct sock_extended_err: errno u32, origin u8, type u8, code u8, pad u8, info u32, data u32
			if m.Header.Level == unix.IPPROTO_IP && m.Header.Type == unix.IP_RECVERR && len(m.Data) >= 16 {
				origin = m.Data[4]
			}
		}
	}
	return origin
}
