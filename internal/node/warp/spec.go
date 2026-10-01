package warp

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"

	"github.com/mistgate/mistgate/internal/plugin"
)

var defaultPorts = []uint16{2408, 500, 1701, 4500}

// parsed is a validated WarpSpec in the forms the dataplane wants. Errors never contain key material.
type parsed struct {
	privKey, peerKey string // base64, 32 bytes
	// cands are the endpoints to try in order: the IPv4 endpoint on each port, then the IPv6 endpoint on each port.
	cands          []netip.AddrPort
	addrV4, addrV6 netip.Prefix
	mtu            int
	reserved       []byte
	backend        string // "auto" | "kernel" | "userspace"
}

func parseSpec(s *plugin.WarpSpec) (parsed, error) {
	var p parsed
	if err := checkKey(s.PrivateKey); err != nil {
		return p, fmt.Errorf("private_key: %w", err)
	}
	if err := checkKey(s.PeerPublicKey); err != nil {
		return p, fmt.Errorf("peer_public_key: %w", err)
	}
	p.privKey, p.peerKey = s.PrivateKey, s.PeerPublicKey

	ports := defaultPorts
	if len(s.Ports) > 0 {
		ports = s.Ports
	}
	for _, port := range ports {
		if port == 0 {
			return p, errors.New("ports: 0 is not a port")
		}
	}
	// Endpoints are IP literals, never names: a name resolved by the host can come back as a dead IPv6 address
	// on a box with a configured but broken v6 route (the reason the literal exists at all).
	for _, e := range []struct {
		v6   bool
		text string
	}{{false, s.EndpointV4}, {true, s.EndpointV6}} {
		if e.text == "" {
			continue
		}
		a, err := netip.ParseAddr(e.text)
		if err != nil || a.Zone() != "" || (a.Is4() == e.v6) || a.IsUnspecified() || a.IsMulticast() {
			return p, fmt.Errorf("endpoint %q is not an IP literal of the right family", e.text)
		}
		for _, port := range ports {
			p.cands = append(p.cands, netip.AddrPortFrom(a, port))
		}
	}
	if len(p.cands) == 0 {
		return p, errors.New("no endpoint")
	}

	var err error
	if p.addrV4, err = parseAddr(s.AddressV4, false); err != nil || !p.addrV4.IsValid() {
		return p, fmt.Errorf("address_v4: %v", errOr(err, "required"))
	}
	if p.addrV6, err = parseAddr(s.AddressV6, true); err != nil {
		return p, fmt.Errorf("address_v6: %w", err)
	}
	p.mtu = int(s.MTU)
	if p.mtu == 0 {
		p.mtu = defaultMTU
	}
	if p.mtu < 576 || p.mtu > 1500 || (p.addrV6.IsValid() && p.mtu < 1280) {
		return p, fmt.Errorf("mtu %d out of range", p.mtu)
	}
	if n := len(s.Reserved); n != 0 && n != 3 {
		return p, fmt.Errorf("reserved is %d bytes, want 0 or 3", n)
	}
	p.reserved = append([]byte(nil), s.Reserved...)
	switch s.Backend {
	case "", "auto":
		p.backend = "auto"
	case "kernel", "userspace":
		p.backend = s.Backend
	default:
		return p, fmt.Errorf("backend %q", s.Backend)
	}
	return p, nil
}

func errOr(err error, def string) any {
	if err != nil {
		return err
	}
	return def
}

func checkKey(k string) error {
	b, err := base64.StdEncoding.DecodeString(k)
	if err != nil || len(b) != 32 {
		return errors.New("not a base64 32-byte key")
	}
	return nil
}

// parseAddr accepts "172.16.0.2/32", "172.16.0.2" (host prefix) and "" (none, only when v6 is true or the caller
// checks validity).
func parseAddr(s string, v6 bool) (netip.Prefix, error) {
	if s == "" {
		return netip.Prefix{}, nil
	}
	pf, err := netip.ParsePrefix(s)
	if err != nil {
		a, err2 := netip.ParseAddr(s)
		if err2 != nil {
			return netip.Prefix{}, errors.New("not an address")
		}
		pf = netip.PrefixFrom(a, a.BitLen())
	}
	a := pf.Addr()
	if a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.Is4() == v6 {
		return netip.Prefix{}, errors.New("wrong family or not a unicast address")
	}
	return pf, nil
}

func (p parsed) hasV6() bool { return p.addrV6.IsValid() }
