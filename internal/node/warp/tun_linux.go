//go:build linux

package warp

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/vishvananda/netlink"
)

// tunDev is the userspace backend: amneziawg-go on a TUN device. Without any AWG parameter it speaks plain
// WireGuard (it interoperates with a kernel WG server), so the node carries one WireGuard fork, not two.
// It needs /dev/net/tun; the routing layer on top is exactly the kernel backend's.
type tunDev struct {
	dev      *device.Device
	identity string // what forces a rebuild: keys, reserved bytes, address families (not the endpoint)
}

func tunIdentity(ls linkSpec) string {
	return strings.Join([]string{ls.PrivateKey, ls.PeerKey, fmt.Sprintf("%x", ls.Reserved), strconv.FormatBool(ls.AddrV6.IsValid()), strconv.Itoa(ls.KeepaliveSeconds)}, "|")
}

func hexKey(b64 string) (string, error) {
	k, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(k) != 32 {
		return "", errors.New("bad key")
	}
	return hex.EncodeToString(k), nil
}

// reservedBind stamps the account's three "reserved" bytes into every outgoing WireGuard message (types 1..4) after
// encryption, which is what the Cloudflare relays look at.
type reservedBind struct {
	conn.Bind
	r [3]byte
}

func (b reservedBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	for _, p := range bufs {
		if len(p) >= 4 && p[0] >= 1 && p[0] <= 4 {
			copy(p[1:4], b.r[:])
		}
	}
	return b.Bind.Send(bufs, ep)
}

func tunNodeUsable() error {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

func (p *linuxPlane) upTun(ls linkSpec) (string, netip.AddrPort, error) {
	ep := ls.Endpoint
	id := tunIdentity(ls)
	if p.tun != nil && p.tun.identity == id {
		if l, err := netlink.LinkByName(p.s.Iface); err == nil {
			// same tunnel: keep a live endpoint that is one of ours, else move the peer; never rebuild
			if live, ok := p.tunLiveEndpoint(ls); ok {
				ep = live
			} else if err := p.setEndpointTun(ls.Endpoint); err != nil {
				return "", ep, err
			}
			if err := p.configureLink(l, ls); err != nil {
				return "", ep, err
			}
			return "userspace", ep, nil
		}
	}
	if err := tunNodeUsable(); err != nil {
		return "", ep, fmt.Errorf("%w: /dev/net/tun: %v", ErrUnavailable, err)
	}
	if _, err := tunConfig(ls, ls.Endpoint); err != nil {
		return "", ep, err
	}
	p.closeTun()
	if l, err := netlink.LinkByName(p.s.Iface); err == nil { // a leftover kernel link or a dead TUN with our name
		if err := netlink.LinkDel(l); err != nil {
			return "", ep, fmt.Errorf("delete stale %s: %w", p.s.Iface, err)
		}
	}
	t, err := tun.CreateTUN(p.s.Iface, ls.MTU)
	if err != nil {
		return "", ep, fmt.Errorf("create tun: %w", err)
	}
	var bind conn.Bind = conn.NewDefaultBind()
	if len(ls.Reserved) == 3 {
		bind = reservedBind{Bind: bind, r: [3]byte(ls.Reserved)}
	}
	log := p.log.With("backend", "userspace")
	dev := device.NewDevice(t, bind, &device.Logger{
		Verbosef: device.DiscardLogf,
		Errorf:   func(f string, a ...any) { log.Warn(fmt.Sprintf(f, a...)) },
	})
	cfg, _ := tunConfig(ls, ls.Endpoint)
	if err := dev.IpcSet(cfg); err != nil {
		dev.Close()
		return "", ep, fmt.Errorf("configure tunnel: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return "", ep, fmt.Errorf("tunnel up: %w", err)
	}
	p.tun = &tunDev{dev: dev, identity: id}
	l, err := netlink.LinkByName(p.s.Iface)
	if err != nil {
		p.closeTun()
		return "", ep, fmt.Errorf("link %s: %w", p.s.Iface, err)
	}
	if err := p.configureLink(l, ls); err != nil {
		p.closeTun()
		return "", ep, err
	}
	return "userspace", ep, nil
}

// tunConfig renders the UAPI text that sets the whole device.
func tunConfig(ls linkSpec, ep netip.AddrPort) (string, error) {
	priv, err := hexKey(ls.PrivateKey)
	if err != nil {
		return "", err
	}
	peer, err := hexKey(ls.PeerKey)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nreplace_peers=true\npublic_key=%s\nendpoint=%s\npersistent_keepalive_interval=%d\nreplace_allowed_ips=true\nallowed_ip=0.0.0.0/0\n",
		priv, peer, ep, ls.KeepaliveSeconds)
	if ls.AddrV6.IsValid() {
		b.WriteString("allowed_ip=::/0\n")
	}
	return b.String(), nil
}

func (p *linuxPlane) closeTun() {
	if p.tun != nil {
		p.tun.dev.Close() // closes the TUN, the kernel removes the device
		p.tun = nil
	}
}

func (p *linuxPlane) setEndpointTun(ep netip.AddrPort) error {
	if p.tun == nil {
		return errors.New("tunnel not running")
	}
	peer, err := hexKey(p.ls.PeerKey)
	if err != nil {
		return err
	}
	if err := p.tun.dev.IpcSet(fmt.Sprintf("public_key=%s\nupdate_only=true\nendpoint=%s\n", peer, ep)); err != nil {
		return fmt.Errorf("set endpoint: %w", err)
	}
	return nil
}

func (p *linuxPlane) tunLiveEndpoint(ls linkSpec) (netip.AddrPort, bool) {
	var st stat
	if p.statTun(&st) != nil || !st.Endpoint.IsValid() {
		return netip.AddrPort{}, false
	}
	for _, c := range ls.Endpoints {
		if c == st.Endpoint {
			return c, true
		}
	}
	return netip.AddrPort{}, false
}

func (p *linuxPlane) statTun(st *stat) error {
	if p.tun == nil {
		return errors.New("tunnel not running")
	}
	text, err := p.tun.dev.IpcGet()
	if err != nil {
		return fmt.Errorf("read device: %w", err)
	}
	var hs int64
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "last_handshake_time_sec":
			hs, _ = strconv.ParseInt(v, 10, 64)
		case "rx_bytes":
			st.Rx, _ = strconv.ParseUint(v, 10, 64)
		case "tx_bytes":
			st.Tx, _ = strconv.ParseUint(v, 10, 64)
		case "endpoint":
			if ap, err := netip.ParseAddrPort(v); err == nil {
				st.Endpoint = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
			}
		}
	}
	if hs > 0 {
		st.Handshake = time.Unix(hs, 0)
	}
	return nil
}
