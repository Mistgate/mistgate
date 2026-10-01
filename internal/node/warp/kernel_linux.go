//go:build linux

package warp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// upKernel creates the WireGuard link over netlink and configures the peer through wgctrl (generic netlink).
// The peer is written only when the live one differs (public key, endpoint, keepalive, allowed IPs, private
// key): ReplacePeers on every reconcile resets the session and stalls traffic for up to REKEY_TIMEOUT (5 s).
func (p *linuxPlane) upKernel(ctx context.Context, ls linkSpec) (string, netip.AddrPort, error) {
	p.closeTun()
	priv, peer, err := parseKeys(ls)
	if err != nil {
		return "", netip.AddrPort{}, err
	}
	l, err := netlink.LinkByName(p.s.Iface)
	var nf netlink.LinkNotFoundError
	if err == nil && l.Type() != "wireguard" {
		// a leftover of the userspace backend or something foreign with our name
		if derr := netlink.LinkDel(l); derr != nil {
			return "", netip.AddrPort{}, fmt.Errorf("delete stale %s: %w", p.s.Iface, derr)
		}
		err = netlink.LinkNotFoundError{}
	}
	if err != nil {
		if !errors.As(err, &nf) {
			return "", netip.AddrPort{}, fmt.Errorf("link %s: %w", p.s.Iface, err)
		}
		if err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: p.s.Iface}}); err != nil {
			return "", netip.AddrPort{}, fmt.Errorf("create wireguard link: %w", err)
		}
		if l, err = netlink.LinkByName(p.s.Iface); err != nil {
			return "", netip.AddrPort{}, fmt.Errorf("link %s: %w", p.s.Iface, err)
		}
	}
	wc, err := p.wgc()
	if err != nil {
		return "", netip.AddrPort{}, err
	}
	ka := time.Duration(ls.KeepaliveSeconds) * time.Second
	allowed := allowedFor(ls)
	ep := ls.Endpoint
	same := false
	if dev, err := wc.Device(p.s.Iface); err == nil && len(dev.Peers) == 1 && dev.PrivateKey == priv {
		cur := dev.Peers[0]
		if live, ok := peerEndpointMatches(cur.Endpoint, ls.Endpoints); ok {
			ep = live // keep the endpoint a live session already uses (an agent restart, a port rotation)
			same = cur.PublicKey == peer && cur.PersistentKeepaliveInterval == ka && sameAllowed(cur.AllowedIPs, allowed)
		}
	}
	if !same {
		// the "reserved" stamping must be in place before the peer is written: the kernel sends the first
		// handshake initiation the moment the peer has an endpoint
		p.stampFirst(ctx, ls, ls.Endpoint)
		cfg := wgtypes.Config{
			PrivateKey:   &priv,
			ReplacePeers: true,
			Peers: []wgtypes.PeerConfig{{
				PublicKey: peer, Endpoint: net.UDPAddrFromAddrPort(ls.Endpoint), PersistentKeepaliveInterval: &ka,
				ReplaceAllowedIPs: true, AllowedIPs: allowed,
			}},
		}
		ep = ls.Endpoint
		if err := wc.ConfigureDevice(p.s.Iface, cfg); err != nil {
			return "", netip.AddrPort{}, fmt.Errorf("configure peer: %w", err)
		}
	}
	if err := p.configureLink(l, ls); err != nil {
		return "", netip.AddrPort{}, err
	}
	return "kernel", ep, nil
}

// stampFirst installs the nft table with the reserved-bytes rule for ep ahead of the peer change (no-op without
// reserved bytes; the regular Reassert installs the same text later and finds nothing to do).
func (p *linuxPlane) stampFirst(ctx context.Context, ls linkSpec, ep netip.AddrPort) {
	if len(ls.Reserved) != 3 {
		return
	}
	if _, err := p.syncNft(ctx, routeSpec{Configured: true, Kernel: true, Reserved: ls.Reserved, Endpoint: ep}); err != nil {
		p.log.Warn("cannot install the reserved-bytes rule", "err", err)
	}
}

func (p *linuxPlane) setEndpointKernel(ctx context.Context, ep netip.AddrPort) error {
	wc, err := p.wgc()
	if err != nil {
		return err
	}
	if p.ls.PeerKey == "" {
		return errors.New("tunnel not configured")
	}
	peer, err := wgtypes.ParseKey(p.ls.PeerKey)
	if err != nil {
		return errors.New("bad peer key")
	}
	p.stampFirst(ctx, p.ls, ep)
	err = wc.ConfigureDevice(p.s.Iface, wgtypes.Config{Peers: []wgtypes.PeerConfig{{
		PublicKey: peer, UpdateOnly: true, Endpoint: net.UDPAddrFromAddrPort(ep),
	}}})
	if err != nil {
		return fmt.Errorf("set endpoint: %w", err)
	}
	return nil
}

func (p *linuxPlane) statKernel(st *stat) error {
	wc, err := p.wgc()
	if err != nil {
		return err
	}
	dev, err := wc.Device(p.s.Iface)
	if err != nil {
		return fmt.Errorf("read device: %w", err)
	}
	if len(dev.Peers) == 0 {
		return nil
	}
	pr := dev.Peers[0]
	st.Handshake = pr.LastHandshakeTime
	st.Rx, st.Tx = uint64(pr.ReceiveBytes), uint64(pr.TransmitBytes)
	if pr.Endpoint != nil {
		ap := pr.Endpoint.AddrPort()
		st.Endpoint = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	return nil
}
