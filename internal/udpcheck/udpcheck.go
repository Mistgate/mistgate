// Package udpcheck sends bounded, tagged UDP delivery probes.
package udpcheck

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

var (
	ErrBadParams       = errors.New("udpcheck: bad parameters")
	ErrUnsupportedHost = errors.New("udpcheck: unsupported destination")
	ErrNoRoute         = errors.New("udpcheck: no route")
)

const (
	MaxPorts = 8
	MaxCount = 300
	MaxPPS   = 50
	MinSize  = 64
	MaxSize  = 1200
	maxRun   = 10 * time.Second
)

// Resolve resolves a node address to one IP, preferring IPv4. Its behavior matches the health probe resolver.
func Resolve(ctx context.Context, host string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Unmap(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("resolve %s: %v", host, err)
	}
	for _, a := range ips {
		if a.Unmap().Is4() {
			return a.Unmap(), nil
		}
	}
	return ips[0], nil
}

// Validate checks the same hard limits Send enforces before opening sockets.
func Validate(host string, ports []uint16, count, pps, size int) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("%w: empty host", ErrBadParams)
	}
	if len(ports) == 0 || len(ports) > MaxPorts {
		return fmt.Errorf("%w: port count must be 1-%d", ErrBadParams, MaxPorts)
	}
	seen := make(map[uint16]struct{}, len(ports))
	for _, port := range ports {
		if port == 0 {
			return fmt.Errorf("%w: port zero", ErrBadParams)
		}
		if _, ok := seen[port]; ok {
			return fmt.Errorf("%w: duplicate port %d", ErrBadParams, port)
		}
		seen[port] = struct{}{}
	}
	if count < 1 || count > MaxCount {
		return fmt.Errorf("%w: count must be 1-%d", ErrBadParams, MaxCount)
	}
	if pps < 1 || pps > MaxPPS {
		return fmt.Errorf("%w: rate must be 1-%d", ErrBadParams, MaxPPS)
	}
	if size < MinSize || size > MaxSize {
		return fmt.Errorf("%w: payload size must be %d-%d", ErrBadParams, MinSize, MaxSize)
	}
	if count > int(maxRun/time.Second)*pps {
		return fmt.Errorf("%w: send would exceed %s", ErrBadParams, maxRun)
	}
	return nil
}

// Send emits count datagrams per port, one to every port per tick. sent is the number of complete ticks delivered to
// every port. The payload is generated once and reused, with tag as its first eight bytes.
func Send(ctx context.Context, host string, ports []uint16, tag [8]byte, count, pps, size int) (family string, sent int, err error) {
	if err := Validate(host, ports, count, pps, size); err != nil {
		return "", 0, err
	}
	runCtx, cancel := context.WithTimeout(ctx, maxRun)
	defer cancel()

	ip, err := Resolve(runCtx, host)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, fmt.Errorf("%w: %v", ErrNoRoute, err)
	}
	if unsupportedDestination(ip) {
		return "", 0, fmt.Errorf("%w: %s", ErrUnsupportedHost, ip)
	}

	family, network := "4", "udp4"
	localIP := net.IPv4zero
	if ip.Is6() {
		family, network, localIP = "6", "udp6", net.IPv6unspecified
	}
	payload := make([]byte, size)
	copy(payload, tag[:])
	if _, err := rand.Read(payload[len(tag):]); err != nil {
		return "", 0, fmt.Errorf("udpcheck: random payload: %w", err)
	}

	sockets := make([]*net.UDPConn, 4)
	defer func() {
		for _, c := range sockets {
			if c != nil {
				_ = c.Close()
			}
		}
	}()
	deadline, _ := runCtx.Deadline()
	seenSources := make(map[int]struct{}, len(sockets))
	for i := range sockets {
		c, err := net.ListenUDP(network, &net.UDPAddr{IP: localIP})
		if err != nil {
			return family, 0, noRoute(runCtx, ctx, fmt.Errorf("open source socket: %w", err))
		}
		sockets[i] = c
		if err := c.SetWriteDeadline(deadline); err != nil {
			return family, 0, noRoute(runCtx, ctx, fmt.Errorf("set send deadline: %w", err))
		}
		port := c.LocalAddr().(*net.UDPAddr).Port
		if _, exists := seenSources[port]; exists {
			return family, 0, fmt.Errorf("udpcheck: source socket ports are not distinct")
		}
		seenSources[port] = struct{}{}
	}

	start := time.Now()
	for tick := 0; tick < count; tick++ {
		if tick > 0 {
			target := start.Add(time.Duration(tick) * time.Second / time.Duration(pps))
			if err := waitUntil(runCtx, target); err != nil {
				return family, sent, noRoute(runCtx, ctx, err)
			}
		}
		socket := sockets[tick%len(sockets)]
		for _, port := range ports {
			if err := runCtx.Err(); err != nil {
				return family, sent, noRoute(runCtx, ctx, err)
			}
			dst := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, port))
			if _, err := socket.WriteToUDP(payload, dst); err != nil {
				return family, sent, noRoute(runCtx, ctx, fmt.Errorf("send to port %d: %w", port, err))
			}
		}
		sent++
	}
	return family, sent, nil
}

func noRoute(runCtx, callerCtx context.Context, err error) error {
	if callerCtx.Err() != nil {
		return callerCtx.Err()
	}
	if runCtx.Err() != nil {
		err = runCtx.Err()
	}
	return fmt.Errorf("%w: %v", ErrNoRoute, err)
}

func waitUntil(ctx context.Context, target time.Time) error {
	d := time.Until(target)
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// unsupportedDestination refuses what is never a node. A subnet's directed broadcast needs no check here: the sockets do
// not set SO_BROADCAST, so the kernel refuses to send to one (EACCES, reported as no_route).
func unsupportedDestination(ip netip.Addr) bool {
	return !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}
