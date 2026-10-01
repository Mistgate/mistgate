package awg

import (
	"errors"
	"net/netip"
	"slices"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awguapi"
	"github.com/mistgate/mistgate/internal/panel/protocols"
)

// ClientTunnel is what an in-process AmneziaWG client (the health module's synthetic checker) needs to join one
// inbound as the device that RenderInput describes. It is written from the same clientConfig as the .conf, the
// vpn:// key and the Mihomo proxy, so the checker cannot be configured differently from a real device: the same
// obfuscation, the same per-device signature, the same keys, the same keepalive.
type ClientTunnel struct {
	IPC    string       // device.IpcSet text of amneziawg-go: key, obfuscation and the one peer with its endpoint
	Addrs  []netip.Addr // the device's tunnel addresses, IPv4 first
	DNS    []netip.Addr // the pair a real config of this profile carries (PickDNS)
	MTU    int
	Server [32]byte // the node's public key: how the peer is found in IpcGet
}

// ClientDevice turns a RenderInput into a ClientTunnel. in.NodeAddr must be an IP literal here: the UAPI
// endpoint takes no host name, so the caller resolves it. A masked (preview) input has no secrets and is refused.
func ClientDevice(in protocols.RenderInput) (ClientTunnel, error) {
	if in.MaskSecrets {
		return ClientTunnel{}, errors.New("awg: a masked preview has no client")
	}
	c, ok := buildClient(in)
	if !ok {
		return ClientTunnel{}, errors.New("awg: the profile, the credential or the inbound do not make a client config")
	}
	priv, ok1 := awgcfg.DecodeKey(c.priv)
	psk, ok2 := awgcfg.DecodeKey(c.psk)
	srv, ok3 := awgcfg.DecodeKey(c.server)
	if !ok1 || !ok2 || !ok3 {
		return ClientTunnel{}, errors.New("awg: a key of the client config is not 32 bytes of base64")
	}
	if a, err := netip.ParseAddr(c.host); err != nil || a.Zone() != "" {
		return ClientTunnel{}, errors.New("awg: the endpoint of a client device must be an IP address")
	}

	o := c.s.Obfuscation
	ob := awgcfg.Obfuscation{
		Jc: o.Jc, Jmin: o.Jmin, Jmax: o.Jmax, S1: o.S1, S2: o.S2, S3: o.S3, S4: o.S4,
		I1: o.I1, I2: o.I2, I3: o.I3, I4: o.I4, I5: o.I5,
	}
	// The ranges are validated strings of the profile; a value that does not parse is left unset (the validator
	// would have refused the profile).
	rng := func(s string) awgcfg.Range { r, _ := awgcfg.ParseRange(s); return r }
	ob.H1, ob.H2, ob.H3, ob.H4 = rng(o.H1), rng(o.H2), rng(o.H3), rng(o.H4)
	if c.v31() {
		ob.HeaderProtectionKey, ob.RandomTrailers, ob.DisableCookies = c.hpk, o.RandomTrailers, true // the client side of cookies is Amnezia's default: off
		ob.ContentPaddingAddition, ob.RekeyAfterTime, ob.RekeyTimeout = rng(o.ContentPaddingAddition), rng(o.RekeyAfterTime), rng(o.RekeyTimeout)
		ob.RejectAfterTime, ob.KeepaliveTimeout, ob.MaxHandshakeAttempts = rng(o.RejectAfterTime), rng(o.KeepaliveTimeout), rng(o.MaxHandshakeAttempts)
	}

	peer := awgcfg.Peer{PublicKey: srv, PSK: &psk, Endpoint: c.endpoint(), Keepalive: rng(o.PersistentKeepalive)}
	for _, p := range c.allowedIPs() {
		peer.AllowedIPs = append(peer.AllowedIPs, netip.MustParsePrefix(p))
	}
	t := ClientTunnel{
		IPC: awguapi.DeviceSet(c.s.Version, priv, 0, ob) + awguapi.PeersSet(false, []awgcfg.Peer{peer}),
		MTU: c.s.MTU, Server: srv,
	}
	for _, s := range []string{c.ip4, c.ip6} {
		if a, err := netip.ParseAddr(s); err == nil {
			t.Addrs = append(t.Addrs, a)
		}
	}
	for _, s := range c.dns {
		if a, err := netip.ParseAddr(s); err == nil && !slices.Contains(t.DNS, a) {
			t.DNS = append(t.DNS, a)
		}
	}
	return t, nil
}
