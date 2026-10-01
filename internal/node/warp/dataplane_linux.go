//go:build linux

package warp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// runner runs an external command with optional stdin and returns its combined output.
type runner func(ctx context.Context, stdin, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

// linuxPlane is the real dataplane. Backend order: kernel WireGuard over netlink first, then
// amneziawg-go (no obfuscation parameters = plain WireGuard) on a TUN, else ErrUnavailable. The choice is made on the
// first Up and kept until Cleanup (or until the spec asks for the other one).
type linuxPlane struct {
	log *slog.Logger
	s   Settings
	run runner

	mu      sync.Mutex // serialises the plane; the Manager already serialises calls, this protects Stat from tests/CLI
	wc      *wgctrl.Client
	chosen  string // "kernel" | "userspace" | ""
	tun     *tunDev
	ls      linkSpec
	ep      netip.AddrPort
	nftText string
	procSys string
}

func newPlane(s Settings, log *slog.Logger) dataplane {
	return &linuxPlane{log: log.With("pkg", "warp"), s: s, run: execRunner, procSys: "/proc/sys"}
}

func (p *linuxPlane) wgc() (*wgctrl.Client, error) {
	if p.wc == nil {
		c, err := wgctrl.New()
		if err != nil {
			return nil, fmt.Errorf("wgctrl: %w", err)
		}
		p.wc = c
	}
	return p.wc, nil
}

func (p *linuxPlane) Up(ctx context.Context, ls linkSpec) (string, netip.AddrPort, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	want := ls.Backend
	if want == "auto" || want == "" {
		want = p.chosen
	}
	var (
		backend string
		ep      netip.AddrPort
		err     error
	)
	switch want {
	case "kernel":
		backend, ep, err = p.upKernel(ctx, ls)
	case "userspace":
		backend, ep, err = p.upTun(ls)
	default:
		backend, ep, err = p.upKernel(ctx, ls)
		if err != nil && kernelUnsupported(err) {
			p.log.Info("kernel WireGuard not available, trying a userspace tunnel", "err", err)
			backend, ep, err = p.upTun(ls)
		}
	}
	if err != nil {
		return "", ep, err
	}
	p.chosen, p.ls, p.ep = backend, ls, ep
	return backend, ep, nil
}

func kernelUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENODEV)
}

func (p *linuxPlane) SetEndpoint(ctx context.Context, ep netip.AddrPort) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var err error
	switch p.chosen {
	case "kernel":
		err = p.setEndpointKernel(ctx, ep)
	case "userspace":
		err = p.setEndpointTun(ep)
	default:
		return ErrUnavailable
	}
	if err == nil {
		p.ep = ep
	}
	return err
}

func (p *linuxPlane) Stat(ctx context.Context) (stat, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, err := netlink.LinkByName(p.s.Iface)
	var nf netlink.LinkNotFoundError
	if errors.As(err, &nf) {
		return stat{}, nil
	}
	if err != nil {
		return stat{}, err
	}
	st := stat{LinkPresent: true, LinkUp: l.Attrs().Flags&net.FlagUp != 0}
	switch p.chosen {
	case "userspace":
		if p.tun != nil {
			err = p.statTun(&st)
		}
	default:
		err = p.statKernel(&st)
	}
	return st, err
}

func (p *linuxPlane) DownLink(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.downLinkLocked()
}

func (p *linuxPlane) downLinkLocked() error {
	p.closeTun()
	p.ep = netip.AddrPort{}
	var errs []error
	if l, err := netlink.LinkByName(p.s.Iface); err == nil {
		if err := netlink.LinkDel(l); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", p.s.Iface, err))
		}
	}
	return errors.Join(errs...)
}

func (p *linuxPlane) Cleanup(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeTun()
	err := cleanupHost(ctx, p.s, p.run)
	p.chosen, p.ls, p.ep, p.nftText = "", linkSpec{}, netip.AddrPort{}, ""
	if p.wc != nil {
		p.wc.Close()
		p.wc = nil
	}
	return err
}

// --- link pieces shared by both backends ---------------------------------------------------------------------

// configureLink sets addresses (removes every other one), MTU, brings the link up and sets rp_filter=2.
func (p *linuxPlane) configureLink(l netlink.Link, ls linkSpec) error {
	want := []netip.Prefix{ls.AddrV4}
	if ls.AddrV6.IsValid() {
		want = append(want, ls.AddrV6)
	}
	cur, err := netlink.AddrList(l, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("list addresses: %w", err)
	}
	have := map[netip.Prefix]bool{}
	for _, a := range cur {
		pf, ok := prefixOfNet(a.IPNet)
		if !ok {
			continue
		}
		if contains(want, pf) {
			have[pf] = true
			continue
		}
		if pf.Addr().Is6() && pf.Addr().IsLinkLocalUnicast() {
			continue
		}
		_ = netlink.AddrDel(l, &a) // a stale address of an earlier account
	}
	for _, w := range want {
		if have[w] {
			continue
		}
		ipn := &net.IPNet{IP: w.Addr().AsSlice(), Mask: net.CIDRMask(w.Bits(), w.Addr().BitLen())}
		if err := netlink.AddrReplace(l, &netlink.Addr{IPNet: ipn}); err != nil {
			if w.Addr().Is6() { // IPv6 disabled on this host: v4 still works
				p.log.Warn("cannot add the WARP IPv6 address", "err", err)
				continue
			}
			return fmt.Errorf("add address: %w", err)
		}
	}
	if l.Attrs().MTU != ls.MTU {
		if err := netlink.LinkSetMTU(l, ls.MTU); err != nil {
			return fmt.Errorf("set mtu: %w", err)
		}
	}
	if err := netlink.LinkSetUp(l); err != nil {
		return fmt.Errorf("link up: %w", err)
	}
	if err := p.setRPFilter(); err != nil {
		p.log.Warn("cannot set rp_filter=2 on the WARP device", "err", err)
	}
	return nil
}

func prefixOfNet(n *net.IPNet) (netip.Prefix, bool) {
	if n == nil {
		return netip.Prefix{}, false
	}
	a, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(a.Unmap(), ones), true
}

func contains(l []netip.Prefix, p netip.Prefix) bool {
	for _, x := range l {
		if x == p {
			return true
		}
	}
	return false
}

// setRPFilter sets net.ipv4.conf.<dev>.rp_filter=2 (loose): strict reverse-path filtering drops the replies of
// locally originated flows that entered the WARP table. The effective value is max(all, dev).
func (p *linuxPlane) setRPFilter() error {
	path := p.procSys + "/net/ipv4/conf/" + p.s.Iface + "/rp_filter"
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == "2" {
		return nil
	}
	return os.WriteFile(path, []byte("2"), 0o644)
}

func peerEndpointMatches(live *net.UDPAddr, cands []netip.AddrPort) (netip.AddrPort, bool) {
	if live == nil {
		return netip.AddrPort{}, false
	}
	ap := live.AddrPort()
	ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	for _, c := range cands {
		if c == ap {
			return c, true
		}
	}
	return netip.AddrPort{}, false
}

func allowedFor(ls linkSpec) []net.IPNet {
	out := []net.IPNet{{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)}}
	if ls.AddrV6.IsValid() {
		out = append(out, net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)})
	}
	return out
}

func sameAllowed(have, want []net.IPNet) bool {
	if len(have) != len(want) {
		return false
	}
	set := map[string]bool{}
	for _, n := range want {
		set[n.String()] = true
	}
	for _, n := range have {
		if !set[n.String()] {
			return false
		}
	}
	return true
}

func parseKeys(ls linkSpec) (priv, peer wgtypes.Key, err error) {
	if priv, err = wgtypes.ParseKey(ls.PrivateKey); err != nil {
		return priv, peer, errors.New("bad private key")
	}
	if peer, err = wgtypes.ParseKey(ls.PeerKey); err != nil {
		return priv, peer, errors.New("bad peer key")
	}
	return priv, peer, nil
}
