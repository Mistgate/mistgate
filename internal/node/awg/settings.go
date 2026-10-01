package awg

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Protocol is the plugin id this engine serves.
const Protocol = "awg"

// ifacePrefix is the name prefix of every interface the engine creates; the host's Cleanup deletes links with it.
const ifacePrefix = "mgawg"

// nodeConfig is an awg InboundSpec after validation: everything the engine relies on.
type nodeConfig struct {
	name     string // interface name, mgawg<port> (<= 15 bytes)
	port     uint16
	version  string
	priv     [32]byte
	obf      awgcfg.Obfuscation
	tunnel   plugin.Tunnel
	identity string // changes exactly when the interface must be recreated; the MTU is not part of it
}

// parseSpec validates what the engine will rely on (the panel validated it too; never trust the wire). A disabled
// spec only needs an id and the protocol: the engine keeps nothing for it.
func parseSpec(spec plugin.InboundSpec) (nodeConfig, error) {
	var c nodeConfig
	if spec.ID == "" {
		return c, errors.New("inbound id is empty")
	}
	if spec.Protocol != Protocol {
		return c, fmt.Errorf("protocol %q is not %s", spec.Protocol, Protocol)
	}
	if !spec.Enabled {
		return c, nil
	}
	if n := spec.Listen.Network; n != "" && n != "udp" {
		return c, fmt.Errorf("listen network %q: awg is udp", n)
	}
	if spec.Listen.Port == 0 {
		return c, errors.New("listen port is 0")
	}
	c.port = spec.Listen.Port
	c.name = ifacePrefix + strconv.Itoa(int(c.port))

	t := spec.Tunnel
	if !t.AddrV4.IsValid() || !t.AddrV4.Addr().Is4() {
		return c, errors.New("tunnel: addr_v4 is required and must be IPv4")
	}
	if b := t.AddrV4.Bits(); b < 16 || b > 30 {
		return c, fmt.Errorf("tunnel: addr_v4 prefix /%d is outside /16../30", b)
	}
	if t.AddrV6.IsValid() {
		if !t.AddrV6.Addr().Is6() || t.AddrV6.Addr().Is4In6() {
			return c, errors.New("tunnel: addr_v6 must be IPv6")
		}
		if b := t.AddrV6.Bits(); b < 48 || b > 126 {
			return c, fmt.Errorf("tunnel: addr_v6 prefix /%d is outside /48../126", b)
		}
	}
	if t.MTU < 1200 || t.MTU > 1420 {
		return c, fmt.Errorf("tunnel: mtu %d is outside 1200..1420", t.MTU)
	}
	c.tunnel = t

	s, err := awgcfg.ParseSettings(spec.Settings)
	if err != nil {
		return c, err
	}
	if res := awgcfg.Validate(s, awgcfg.Options{}); !res.OK() {
		return c, res.Err()
	}
	raw, err := base64.StdEncoding.DecodeString(s.PrivateKey)
	if err != nil || len(raw) != 32 {
		return c, errors.New("settings: private_key must be base64 of 32 bytes")
	}
	copy(c.priv[:], raw)
	c.version, c.obf = s.Version, s.Obfuscation

	// Identity: everything the interface is built from. Hashed, never stored as text.
	nj, err := s.NodeJSON()
	if err != nil {
		return c, err
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x1f%s\x1f%s\x1f%s\x1f%s", c.port, t.AddrV4, t.AddrV6, nj, c.obf.I())))
	c.identity = hex.EncodeToString(sum[:])
	return c, nil
}

// credJSON is UserCred.Data of an awg credential (agent.proto, "AWG AND WARP"): the client's private key never
// reaches the node.
type credJSON struct {
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	PSK        string   `json:"psk"`
}

// wantPeer is one credential as a WireGuard peer.
type wantPeer struct {
	credID  string
	pub     [32]byte
	psk     *[32]byte
	allowed []netip.Prefix // sorted
}

var zeroKey [32]byte

// add is the operation that creates the peer.
func (w *wantPeer) add() awgcfg.Peer {
	return awgcfg.Peer{PublicKey: w.pub, PSK: w.psk, AllowedIPs: w.allowed}
}

// update changes a live peer without touching its session. An absent PSK must be cleared explicitly (the zero key).
func (w *wantPeer) update() awgcfg.Peer {
	p := awgcfg.Peer{PublicKey: w.pub, PSK: w.psk, AllowedIPs: w.allowed, UpdateOnly: true, ReplaceIPs: true}
	if p.PSK == nil {
		p.PSK = &zeroKey
	}
	return p
}

// parsePeers turns credentials into peers and refuses the whole set when one is unusable or two collide: a
// duplicate public key or allowed ip would silently move traffic between users.
func parsePeers(cfg nodeConfig, creds []plugin.UserCred) (map[[32]byte]*wantPeer, error) {
	out := make(map[[32]byte]*wantPeer, len(creds))
	ips := map[netip.Prefix]string{}
	for _, c := range creds {
		var j credJSON
		if err := json.Unmarshal(c.Data, &j); err != nil {
			return nil, fmt.Errorf("credential %s: data: %w", c.CredID, err)
		}
		pub, ok := awgcfg.DecodeKey(j.PublicKey)
		if !ok {
			return nil, fmt.Errorf("credential %s: public_key must be base64 of 32 bytes", c.CredID)
		}
		w := &wantPeer{credID: c.CredID, pub: pub}
		if j.PSK != "" {
			k, ok := awgcfg.DecodeKey(j.PSK)
			if !ok {
				return nil, fmt.Errorf("credential %s: psk must be base64 of 32 bytes", c.CredID)
			}
			w.psk = &k
		}
		if len(j.AllowedIPs) == 0 {
			return nil, fmt.Errorf("credential %s: allowed_ips is empty", c.CredID)
		}
		for _, s := range j.AllowedIPs {
			p, err := netip.ParsePrefix(s)
			if err != nil || p.Masked() != p || p.Bits() != p.Addr().BitLen() {
				return nil, fmt.Errorf("credential %s: allowed ip %q must be a single address (/32 or /128)", c.CredID, s)
			}
			if err := inSubnet(cfg.tunnel, p.Addr()); err != nil {
				return nil, fmt.Errorf("credential %s: allowed ip %s: %w", c.CredID, p, err)
			}
			if other, dup := ips[p]; dup {
				return nil, fmt.Errorf("credentials %s and %s share the address %s", other, c.CredID, p)
			}
			ips[p] = c.CredID
			w.allowed = append(w.allowed, p)
		}
		sort.Slice(w.allowed, func(i, j int) bool { return w.allowed[i].Addr().Less(w.allowed[j].Addr()) })
		if prev, dup := out[pub]; dup {
			return nil, fmt.Errorf("credentials %s and %s share a public key", prev.credID, c.CredID)
		}
		out[pub] = w
	}
	return out, nil
}

// inSubnet: a peer address must be inside the client subnet and not the node's own address.
func inSubnet(t plugin.Tunnel, a netip.Addr) error {
	pf := t.AddrV4
	if a.Is6() {
		pf = t.AddrV6
		if !pf.IsValid() {
			return errors.New("the tunnel has no IPv6")
		}
	}
	if !pf.Masked().Contains(a) {
		return fmt.Errorf("outside the client subnet %s", pf.Masked())
	}
	if a == pf.Addr() || a == pf.Masked().Addr() {
		return errors.New("is the node's own (or the network) address")
	}
	return nil
}
