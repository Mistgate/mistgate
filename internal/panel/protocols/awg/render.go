package awg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/vpnkey"
	"github.com/mistgate/mistgate/internal/plugin"
)

// One clientConfig feeds all three formats, so the .conf, the vpn:// key and the Mihomo proxy cannot drift apart.

const (
	// The pair AmneziaVPN falls back to when a config carries no usable DNS.
	fallbackDNS1, fallbackDNS2 = "1.1.1.1", "8.8.8.8"
	// What a masked preview shows where the real value is unknown or secret.
	previewHost = "203.0.113.10"
)

// PickDNS returns the two plain IPv4 servers an AWG client carries (AmneziaVPN reads exactly two IPv4
// addresses): the first two usable entries of servers, a single one repeated, the built-in pair when none is
// usable. fallback reports the last case so the caller can warn (dns_fallback).
func PickDNS(servers []string) (pair [2]string, fallback bool) {
	var got []string
	for _, s := range servers {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil && a.Is4() && len(got) < 2 {
			got = append(got, a.String())
		}
	}
	switch len(got) {
	case 0:
		return [2]string{fallbackDNS1, fallbackDNS2}, true
	case 1:
		return [2]string{got[0], got[0]}, false
	}
	return [2]string{got[0], got[1]}, false
}

type clientConfig struct {
	name   string
	host   string
	port   int
	s      Settings
	ip4    string // no mask
	ip6    string
	dns    [2]string
	priv   string // the three secrets are MaskedSecret in a preview
	pub    string
	psk    string
	hpk    string
	server string // server public key
	masked bool
}

func (c clientConfig) v31() bool { return c.s.Version == Version31 }

func buildClient(in protocols.RenderInput) (clientConfig, bool) {
	s, errs := parse(in.Settings)
	if errs != nil || len(validate(s)) > 0 {
		return clientConfig{}, false
	}
	// The one place the device's own signature comes in: the .conf, the vpn:// key and the Mihomo proxy are all
	// written from this clientConfig, so they cannot disagree. A preview has no device and shows the profile's.
	if chain, ok := deviceSignature(s.Obfuscation, in.DeviceID); ok {
		s.Obfuscation.I1, s.Obfuscation.I2, s.Obfuscation.I3, s.Obfuscation.I4, s.Obfuscation.I5 = chain[0], chain[1], chain[2], chain[3], chain[4]
	}
	c := clientConfig{s: s, masked: in.MaskSecrets}
	c.host = in.NodeAddr
	if c.host == "" {
		c.host = in.Inbound.Node.Address
	}
	if c.host == "" && c.masked {
		c.host = previewHost
	}
	if !isIP(c.host) && !isHostname(c.host) {
		return clientConfig{}, false // never splice anything but a plain host into an endpoint
	}
	c.port = int(in.Inbound.Port)
	if c.port == 0 {
		c.port = s.Port
	}
	c.name = in.DisplayName
	if c.name == "" {
		n := in.Inbound.Node.Name
		if n == "" {
			n = c.host
		}
		c.name = n + " · AWG " + s.Version
	}
	c.dns, _ = PickDNS(in.DNS)

	var ps peerSecret
	raw := []byte(in.Peer)
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(in.Secret)
	}
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := json.Unmarshal(raw, &ps); err != nil {
			return clientConfig{}, false
		}
	}
	if ps.IP4 == "" && c.masked { // a profile preview has no device: show the first address of the network
		if a, b, err := peerAddrs(s, 2); err == nil {
			ps.IP4, ps.IP6 = a.String(), ""
			if b.IsValid() {
				ps.IP6 = b.String()
			}
		}
	}
	if a, err := netip.ParseAddr(ps.IP4); err != nil || !a.Is4() {
		return clientConfig{}, false
	}
	if ps.IP6 != "" {
		if a, err := netip.ParseAddr(ps.IP6); err != nil || !a.Is6() {
			return clientConfig{}, false
		}
	}
	c.ip4, c.ip6 = ps.IP4, ps.IP6

	var pub struct {
		PublicKey string `json:"public_key"`
	}
	_ = json.Unmarshal(in.InboundPublic, &pub)
	c.server = pub.PublicKey
	if c.masked {
		c.priv, c.psk, c.pub = protocols.MaskedSecret, protocols.MaskedSecret, protocols.MaskedSecret
		if s.hpk() != "" {
			c.hpk = protocols.MaskedSecret
		}
		if c.server == "" {
			c.server = protocols.MaskedSecret
		}
		return c, true
	}
	pk, err := publicKeyOf(ps.Priv)
	if err != nil || !validKey(ps.PSK) || !validKey(c.server) {
		return clientConfig{}, false
	}
	c.priv, c.pub, c.psk, c.hpk = ps.Priv, pk, ps.PSK, s.hpk()
	return c, true
}

// Render implements protocols.Protocol.
func (*Protocol) Render(in protocols.RenderInput) (plugin.Fragment, bool) {
	switch in.Format {
	case plugin.FormatAWGConf, plugin.FormatAmneziaVPN, plugin.FormatMihomo:
	default:
		return plugin.Fragment{}, false // Happ and the URI list cannot carry AWG
	}
	if in.Format == plugin.FormatAmneziaVPN && in.MaskSecrets {
		return plugin.Fragment{}, false // a masked key would import as a broken one
	}
	c, ok := buildClient(in)
	if !ok {
		return plugin.Fragment{}, false
	}
	var data []byte
	switch in.Format {
	case plugin.FormatAWGConf:
		data = []byte(c.conf())
	case plugin.FormatAmneziaVPN:
		key, err := vpnkey.Encode(c.vpnDoc())
		if err != nil {
			return plugin.Fragment{}, false
		}
		data = []byte(key)
	case plugin.FormatMihomo:
		var err error
		if data, err = c.mihomo(); err != nil {
			return plugin.Fragment{}, false
		}
	}
	return plugin.Fragment{Format: in.Format, Name: c.name, Data: data}, true
}

func (c clientConfig) endpoint() string { return net.JoinHostPort(c.host, strconv.Itoa(c.port)) }

func (c clientConfig) address() string {
	a := c.ip4 + "/32"
	if c.ip6 != "" {
		a += ", " + c.ip6 + "/128"
	}
	return a
}

func (c clientConfig) allowedIPs() []string { return []string{"0.0.0.0/0", "::/0"} } // iOS needs ::/0

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// conf writes the client .conf. MTU is explicit: desktop AmneziaVPN keeps the MTU of
// an imported .conf (it overwrites only the one of a vpn:// key). 3.1 adds the nine keys of 3.x; 2.0 has none of
// them. RandomTrailers is written only when on: off is the default and AmneziaWG 3.0 apps reject the key.
func (c clientConfig) conf() string {
	o := c.s.Obfuscation
	var b strings.Builder
	line := func(k, v string) { b.WriteString(k + " = " + v + "\n") }
	b.WriteString("[Interface]\n")
	line("Address", c.address())
	line("DNS", c.dns[0]+", "+c.dns[1])
	line("MTU", strconv.Itoa(c.s.MTU))
	line("PrivateKey", c.priv)
	line("Jc", strconv.Itoa(o.Jc))
	line("Jmin", strconv.Itoa(o.Jmin))
	line("Jmax", strconv.Itoa(o.Jmax))
	for i, v := range o.S() {
		line("S"+strconv.Itoa(i+1), strconv.Itoa(v))
	}
	for i, v := range o.H() {
		line("H"+strconv.Itoa(i+1), v)
	}
	for i, v := range o.I() {
		if v != "" {
			line("I"+strconv.Itoa(i+1), v)
		}
	}
	if c.v31() {
		for _, kv := range [][2]string{
			{"HeaderProtectionKey", c.hpk}, {"ContentPaddingAddition", o.ContentPaddingAddition},
			{"RekeyAfterTime", o.RekeyAfterTime}, {"RekeyTimeout", o.RekeyTimeout},
			{"RejectAfterTime", o.RejectAfterTime}, {"KeepaliveTimeout", o.KeepaliveTimeout},
			{"MaxHandshakeAttempts", o.MaxHandshakeAttempts},
		} {
			if kv[1] != "" {
				line(kv[0], kv[1])
			}
		}
		if o.RandomTrailers {
			line("RandomTrailers", "on")
		}
		line("DisableCookies", "on") // the client side of it is Amnezia's default; the node side is the profile's setting
	}
	b.WriteString("\n[Peer]\n")
	line("PublicKey", c.server)
	line("PresharedKey", c.psk)
	line("AllowedIPs", strings.Join(c.allowedIPs(), ", "))
	line("Endpoint", c.endpoint())
	if o.PersistentKeepalive != "" {
		line("PersistentKeepalive", o.PersistentKeepalive)
	}
	return b.String()
}

// ---- vpn:// ----

// vpnDoc is the "user" document of AmneziaVPN's SelfHostedUser: no userName,
// password or top-level port, or the client takes the key for an administrator's.
type vpnDoc struct {
	FormatVersion    int            `json:"format_version"`
	Description      string         `json:"description"`
	HostName         string         `json:"hostName"`
	Containers       []vpnContainer `json:"containers"`
	DefaultContainer string         `json:"defaultContainer"`
	DNS1             string         `json:"dns1"`
	DNS2             string         `json:"dns2"`
}

type vpnContainer struct {
	Container string   `json:"container"`
	AWG       vpnBlock `json:"awg"`
}

type vpnBlock struct {
	Port            string `json:"port"` // a string here, a number in last_config
	TransportProto  string `json:"transport_proto"`
	ProtocolVersion string `json:"protocol_version"` // a label in the client's UI only
	LastConfig      string `json:"last_config"`      // a STRING holding JSON
}

// vpnLast is last_config: every value a string except port and allowed_ips. The client takes the connection
// parameters from these fields, not from the text of config (which is for display and export).
type vpnLast struct {
	Config              string   `json:"config"`
	HostName            string   `json:"hostName"`
	Port                int      `json:"port"`
	ClientIP            string   `json:"client_ip"` // no mask
	ClientPrivKey       string   `json:"client_priv_key"`
	ClientPubKey        string   `json:"client_pub_key"`
	ServerPubKey        string   `json:"server_pub_key"`
	PSKKey              string   `json:"psk_key"`
	ClientID            string   `json:"clientId"`
	AllowedIPs          []string `json:"allowed_ips"`
	PersistentKeepAlive string   `json:"persistent_keep_alive,omitempty"`
	MTU                 string   `json:"mtu"` // the client overwrites it on import
	Jc                  string   `json:"Jc"`
	Jmin                string   `json:"Jmin"`
	Jmax                string   `json:"Jmax"`
	S1                  string   `json:"S1"`
	S2                  string   `json:"S2"`
	S3                  string   `json:"S3"`
	S4                  string   `json:"S4"`
	H1                  string   `json:"H1"`
	H2                  string   `json:"H2"`
	H3                  string   `json:"H3"`
	H4                  string   `json:"H4"`
	I1                  string   `json:"I1,omitempty"`
	I2                  string   `json:"I2,omitempty"`
	I3                  string   `json:"I3,omitempty"`
	I4                  string   `json:"I4,omitempty"`
	I5                  string   `json:"I5,omitempty"`
	HeaderProtectionKey string   `json:"HeaderProtectionKey,omitempty"`
	ContentPadding      string   `json:"ContentPaddingAddition,omitempty"`
	RekeyAfterTime      string   `json:"RekeyAfterTime,omitempty"`
	RekeyTimeout        string   `json:"RekeyTimeout,omitempty"`
	RejectAfterTime     string   `json:"RejectAfterTime,omitempty"`
	KeepaliveTimeout    string   `json:"KeepaliveTimeout,omitempty"`
	MaxHandshake        string   `json:"MaxHandshakeAttempts,omitempty"`
	RandomTrailers      string   `json:"RandomTrailers,omitempty"`
	DisableCookies      string   `json:"DisableCookies,omitempty"`
}

func (c clientConfig) vpnDoc() vpnDoc {
	o := c.s.Obfuscation
	last := vpnLast{
		Config: c.conf(), HostName: c.host, Port: c.port, ClientIP: c.ip4,
		ClientPrivKey: c.priv, ClientPubKey: c.pub, ServerPubKey: c.server, PSKKey: c.psk, ClientID: c.pub,
		AllowedIPs: c.allowedIPs(), PersistentKeepAlive: o.PersistentKeepalive, MTU: strconv.Itoa(c.s.MTU),
		Jc: strconv.Itoa(o.Jc), Jmin: strconv.Itoa(o.Jmin), Jmax: strconv.Itoa(o.Jmax),
		S1: strconv.Itoa(o.S1), S2: strconv.Itoa(o.S2), S3: strconv.Itoa(o.S3), S4: strconv.Itoa(o.S4),
		H1: o.H1, H2: o.H2, H3: o.H3, H4: o.H4,
		I1: o.I1, I2: o.I2, I3: o.I3, I4: o.I4, I5: o.I5,
	}
	label := "2"
	if c.v31() {
		label = "3.1"
		last.HeaderProtectionKey, last.ContentPadding = c.hpk, o.ContentPaddingAddition
		last.RekeyAfterTime, last.RekeyTimeout, last.RejectAfterTime = o.RekeyAfterTime, o.RekeyTimeout, o.RejectAfterTime
		last.KeepaliveTimeout, last.MaxHandshake = o.KeepaliveTimeout, o.MaxHandshakeAttempts
		last.RandomTrailers, last.DisableCookies = onOff(o.RandomTrailers), "on"
	}
	js, _ := json.Marshal(last) // a struct of strings, numbers and a string list cannot fail
	return vpnDoc{
		FormatVersion: 1, Description: c.name, HostName: c.host,
		Containers: []vpnContainer{{Container: "amnezia-awg2", AWG: vpnBlock{
			Port: strconv.Itoa(c.port), TransportProto: "udp", ProtocolVersion: label, LastConfig: string(js),
		}}},
		DefaultContainer: "amnezia-awg2", DNS1: c.dns[0], DNS2: c.dns[1],
	}
}

// ---- Mihomo ----

func strNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}
func intNode(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
}
func boolNode(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(b)}
}

type ymap struct{ n *yaml.Node }

func newMap() *ymap { return &ymap{&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}} }

func (m *ymap) set(k string, v *yaml.Node) *ymap {
	m.n.Content = append(m.n.Content, strNode(k), v)
	return m
}

func (m *ymap) str(k, v string) *ymap { return m.set(k, strNode(v)) }

func (m *ymap) strIf(k, v string) *ymap {
	if v != "" {
		m.str(k, v)
	}
	return m
}

func seq(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

// mihomo returns ONE proxy as an element of a YAML list, built from nodes so that a hostile
// name cannot break out of its scalar. The assembler adds groups, dns and rules. Keys per the Mihomo format:
// `version: 3` selects the v3 device (3.0 and 3.1), for 2.0 it is left out; persistent-keepalive is an integer
// (the first number of the profile's range); H values are strings.
func (c clientConfig) mihomo() ([]byte, error) {
	o := c.s.Obfuscation
	opt := newMap()
	if c.v31() {
		opt.set("version", intNode(3))
	}
	opt.set("jc", intNode(o.Jc)).set("jmin", intNode(o.Jmin)).set("jmax", intNode(o.Jmax))
	opt.set("s1", intNode(o.S1)).set("s2", intNode(o.S2)).set("s3", intNode(o.S3)).set("s4", intNode(o.S4))
	for i, h := range o.H() {
		opt.set("h"+strconv.Itoa(i+1), quoted(h))
	}
	for i, v := range o.I() {
		if v != "" {
			opt.set("i"+strconv.Itoa(i+1), quoted(v))
		}
	}
	if c.v31() {
		opt.set("header-protection-key", quoted(c.hpk))
		for _, kv := range [][2]string{
			{"content-padding-addition", o.ContentPaddingAddition}, {"rekey-after-time", o.RekeyAfterTime},
			{"rekey-timeout", o.RekeyTimeout}, {"reject-after-time", o.RejectAfterTime},
			{"keepalive-timeout", o.KeepaliveTimeout}, {"max-handshake-attempts", o.MaxHandshakeAttempts},
		} {
			if kv[1] != "" {
				opt.set(kv[0], quoted(kv[1]))
			}
		}
		opt.set("random-trailers", boolNode(o.RandomTrailers)).set("disable-cookies", boolNode(true))
	}

	allowed := seq(quoted("0.0.0.0/0"))
	if c.ip6 != "" {
		allowed.Content = append(allowed.Content, quoted("::/0"))
	}
	p := newMap()
	p.set("name", quoted(c.name)).str("type", "wireguard").set("server", quoted(c.host)).set("port", intNode(c.port))
	p.set("ip", quoted(c.ip4+"/32"))
	if c.ip6 != "" {
		p.set("ipv6", quoted(c.ip6+"/128"))
	}
	p.set("private-key", quoted(c.priv)).set("public-key", quoted(c.server)).set("pre-shared-key", quoted(c.psk))
	p.set("allowed-ips", allowed).set("udp", boolNode(true)).set("mtu", intNode(c.s.MTU))
	if r, err := parseRange(o.PersistentKeepalive, maxUint16); err == nil {
		p.set("persistent-keepalive", intNode(int(r.lo)))
	}
	p.set("remote-dns-resolve", boolNode(true)).set("dns", seq(quoted(c.dns[0]), quoted(c.dns[1])))
	p.set("amnezia-wg-option", opt.n)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(seq(p.n)); err != nil {
		return nil, fmt.Errorf("mihomo proxy: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- host checks (the same rules as the hysteria2 plugin) ----

// isIP: a plain IP literal, no brackets and no zone.
func isIP(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Zone() == ""
}

// isHostname: a syntactically valid DNS name that is not an IP literal.
func isHostname(s string) bool {
	if s == "" || len(s) > 253 || isIP(s) {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
