package mimicry

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// Preset ids. "custom" means "the admin writes the chains by hand": Generate returns no packet for it.
const (
	QUIC     = "quic"
	CurlQUIC = "curl_quic"
	DNS      = "dns"
	STUN     = "stun"
	WebRTC   = "webrtc"
	SIP      = "sip"
	NTP      = "ntp"
	RTP      = "rtp"
	SSDP     = "ssdp"
	DTLS     = "dtls"
	Custom   = "custom"
)

// Preset is one entry of the mimicry list of the profile editor.
type Preset struct {
	ID    string
	Label string
}

var presets = []Preset{
	{QUIC, "QUIC"},
	{CurlQUIC, "QUIC (curl)"},
	{DNS, "DNS"},
	{STUN, "STUN"},
	{WebRTC, "WebRTC"},
	{SIP, "SIP"},
	{NTP, "NTP"},
	{RTP, "RTP"},
	{SSDP, "SSDP"},
	{DTLS, "DTLS"},
	{Custom, "Custom"},
}

// Presets lists the presets in the order the editor shows them.
func Presets() []Preset { return append([]Preset(nil), presets...) }

// Label returns the display label of a preset id, "" if unknown.
func Label(id string) string {
	for _, p := range presets {
		if p.ID == id {
			return p.Label
		}
	}
	return ""
}

// Valid reports whether id is a known preset.
func Valid(id string) bool { return Label(id) != "" }

// NaturalPorts lists the UDP ports where the traffic of a preset is normal: a QUIC Initial to 443, a DNS query to
// 53. A profile that sends it to another port is an anomaly of its own. nil means any port is fine (STUN, WebRTC,
// RTP and DTLS run on whatever port the peers agreed on).
func NaturalPorts(id string) []int {
	switch id {
	case QUIC, CurlQUIC:
		return []int{443}
	case DNS:
		return []int{53}
	case NTP:
		return []int{123}
	case SIP:
		return []int{5060}
	case SSDP:
		return []int{1900}
	}
	return nil
}

// UsesDomain reports whether the packets of a preset carry a host name (Options.Domain): the SNI of QUIC, the
// name of the DNS queries, the SIP domain. The other protocols have no name on the wire.
func UsesDomain(id string) bool {
	switch id {
	case QUIC, CurlQUIC, DNS, SIP:
		return true
	}
	return false
}

// Options tunes a generation.
type Options struct {
	// Domain is the host name the packets carry, see UsesDomain. Empty draws one from Domains(); anything else
	// goes through NormalizeDomain.
	Domain string
}

// GenerateChain returns the chain of a preset as the strings I1..I5, the unused tail empty. Every element passes
// Parse, all of them together stay within MaxChainChars, and the same preset, options and rng state give the same
// chain. Everything the real protocol draws at random and nothing covers is a tag, and the few choices that would
// be constant on one real host (a label length, a payload type, a client name) are drawn from rng once per call,
// so two profiles do not share a packet. Where a real client sends several packets one after another the chain
// has them: DNS asks A, AAAA and HTTPS for the same name, WebRTC follows the ICE check with the DTLS ClientHello.
// A QUIC preset is the one exception to "tag what is random": its Initial is encrypted, a tag inside it would
// break the packet, so it is one literal.
func GenerateChain(id string, opt Options, rng *rand.Rand) (out [5]string, err error) {
	if id == Custom {
		return out, nil
	}
	gen, ok := generators[id]
	if !ok {
		return out, fmt.Errorf("unknown mimicry preset %q", id)
	}
	domain := ""
	if opt.Domain != "" {
		if domain, err = NormalizeDomain(opt.Domain); err != nil {
			return out, err
		}
	}
	if domain == "" && UsesDomain(id) {
		domain = domainPool[rng.IntN(len(domainPool))]
	}
	pkts, err := gen(rng, domain)
	if err != nil {
		return out, fmt.Errorf("preset %s: %w", id, err)
	}
	total := 0
	for i, c := range pkts {
		s := c.String()
		if _, err := Parse(s); err != nil { // a generator bug must not reach a profile
			return [5]string{}, fmt.Errorf("preset %s produced an invalid chain: %w", id, err)
		}
		out[i] = s
		total += len(s)
	}
	if total > MaxChainChars {
		return [5]string{}, fmt.Errorf("preset %s produced %d characters, the limit is %d", id, total, MaxChainChars)
	}
	return out, nil
}

// Generate returns the first packet, I1, of GenerateChain(id, Options{}, rng): the chain of a DNS or WebRTC preset
// is longer, callers that fill I2..I5 use GenerateChain.
func Generate(id string, rng *rand.Rand) (string, error) {
	c, err := GenerateChain(id, Options{}, rng)
	return c[0], err
}

type generator func(rng *rand.Rand, domain string) ([]chain, error)

// one wraps a generator of a single packet.
func one(f func(rng *rand.Rand, domain string) chain) generator {
	return func(rng *rand.Rand, domain string) ([]chain, error) { return []chain{f(rng, domain)}, nil }
}

func quicGenerator(p quicProfile) generator {
	return func(rng *rand.Rand, domain string) ([]chain, error) {
		c, err := quicInitial(rng, p, domain)
		return []chain{c}, err
	}
}

var generators = map[string]generator{
	QUIC:     quicGenerator(chromeQUIC),
	CurlQUIC: quicGenerator(curlQUIC),
	DNS:      dnsQueries,
	STUN:     one(func(*rand.Rand, string) chain { return stunBinding() }),
	WebRTC: func(*rand.Rand, string) ([]chain, error) {
		return []chain{iceCheck(), dtlsClientHello()}, nil // ICE connectivity check, then the DTLS handshake it opens
	},
	SIP:  one(sipOptions),
	NTP:  one(func(rng *rand.Rand, _ string) chain { return ntpRequest(rng) }),
	RTP:  one(func(rng *rand.Rand, _ string) chain { return rtpPacket(rng) }),
	SSDP: one(func(rng *rand.Rand, _ string) chain { return ssdpSearch(rng) }),
	DTLS: one(func(*rand.Rand, string) chain { return dtlsClientHello() }),
}

func u16(n int) (byte, byte) { return byte(n >> 8), byte(n) }

// dnsName is the wire form of a domain (RFC 1035 3.1): length-prefixed labels and the root.
func dnsName(domain string) []byte {
	var b []byte
	for _, label := range strings.Split(domain, ".") {
		b = append(append(b, byte(len(label))), label...)
	}
	return append(b, 0)
}

// dnsQueries: what a browser sends to look up one name: A, AAAA and HTTPS (type 65, RFC 9460) queries, the three
// of them at once, so their order is not fixed. Each is an RFC 1035 query with an EDNS0 OPT record (RFC 6891) like
// every stub resolver sends. A resolver draws a new id for every query, so each packet has its own <r 2>, and the
// name and the advertised UDP size (one client, one value) are the same in all three.
func dnsQueries(rng *rand.Rand, domain string) ([]chain, error) {
	types := []int{1, 28, 65}
	rng.Shuffle(len(types), func(i, j int) { types[i], types[j] = types[j], types[i] })
	udpSize := []int{1232, 4096}[rng.IntN(2)]
	name := dnsName(domain)
	out := make([]chain, 0, len(types))
	for _, qtype := range types {
		var c chain
		c.r(2) // id
		c.lit(0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 1)
		c.lit(name...)
		c.lit(0, byte(qtype), 0, 1) // QTYPE, class IN
		hi, lo := u16(udpSize)
		c.lit(0, 0, 41, hi, lo, 0, 0, 0, 0, 0, 0) // root, OPT, UDP size, no flags, no data
		out = append(out, c)
	}
	return out, nil
}

// stunBinding: RFC 5389 Binding request without attributes, the 20 bytes a browser sends to a STUN server to learn
// its address. Magic cookie fixed, transaction id random. It stays bare on purpose: FINGERPRINT is a CRC32 over the
// whole message including the transaction id, so with a random id (a tag) it cannot be computed and a wrong one
// is worse than none, while a frozen id turns the packet into a signature; SOFTWARE alone has no honest source,
// the strings other tools send are invented per provider.
func stunBinding() chain {
	var c chain
	c.lit(0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xA4, 0x42)
	c.r(12)
	return c
}

// iceCheck: the ICE connectivity check of a WebRTC peer (RFC 8445 7.2.4): USERNAME "ufrag:ufrag", PRIORITY,
// ICE-CONTROLLING, MESSAGE-INTEGRITY, FINGERPRINT. The HMAC and the CRC cannot be computed over random content,
// so they are random too; a DPI box that does not verify them sees the right attributes in the right order.
func iceCheck() chain {
	var attrs chain
	attrs.lit(0x00, 0x06, 0x00, 0x09) // USERNAME, 9 bytes, padded to 12
	attrs.rc(4)
	attrs.lit(':')
	attrs.rc(4)
	attrs.lit(0, 0, 0)
	attrs.lit(0x00, 0x24, 0x00, 0x04) // PRIORITY
	attrs.r(4)
	attrs.lit(0x80, 0x2a, 0x00, 0x08) // ICE-CONTROLLING
	attrs.r(8)
	attrs.lit(0x00, 0x08, 0x00, 0x14) // MESSAGE-INTEGRITY
	attrs.r(20)
	attrs.lit(0x80, 0x28, 0x00, 0x04) // FINGERPRINT
	attrs.r(4)

	var c chain
	hi, lo := u16(attrs.size())
	c.lit(0x00, 0x01, hi, lo, 0x21, 0x12, 0xA4, 0x42)
	c.r(12)
	c.add(attrs)
	return c
}

var (
	sipAgents = []string{"Linphone/5.2.5 (belle-sip/5.3.90)", "Zoiper rv2.10.15-mod", "MicroSIP/3.21.6", "baresip 3.8.0", "Blink 6.0.4 (Windows)"}
	sipAllow  = []string{
		"INVITE, ACK, CANCEL, OPTIONS, BYE, REFER, NOTIFY, INFO, MESSAGE, SUBSCRIBE",
		"INVITE, ACK, CANCEL, OPTIONS, BYE, UPDATE, MESSAGE",
		"INVITE, ACK, CANCEL, OPTIONS, BYE, PRACK, UPDATE",
	}
	sipUsers = []string{"100", "101", "200", "300", "400", "500", "alice", "bob", "support", "sales", "ops"}
	sipPorts = []int{5060, 5062, 5070, 5080, 5160}
)

// privateIPv4 is a LAN address (RFC 1918): a client puts its own into Via, Contact and Call-ID.
func privateIPv4(rng *rand.Rand) string {
	switch rng.IntN(3) {
	case 0:
		return fmt.Sprintf("10.%d.%d.%d", rng.IntN(256), rng.IntN(256), 2+rng.IntN(253))
	case 1:
		return fmt.Sprintf("172.%d.%d.%d", 16+rng.IntN(16), rng.IntN(256), 2+rng.IntN(253))
	}
	return fmt.Sprintf("192.168.%d.%d", rng.IntN(256), 2+rng.IntN(253))
}

// sipOptions: RFC 3261 OPTIONS request to the domain, with a magic-cookie branch (z9hG4bK). The account, the LAN
// address, the agent and the header values are one client's, drawn once; branch, From tag, Call-ID and CSeq are
// new on every request and are tags, each of them once in the packet: the tags are independent, so a value that
// had to appear twice would come out different the second time. The address, which does appear three times, is
// a literal for that reason. <rc>, not <r>: the protocol is text.
func sipOptions(rng *rand.Rand, domain string) chain {
	ip := privateIPv4(rng)
	port := strconv.Itoa(sipPorts[rng.IntN(len(sipPorts))])
	user := sipUsers[rng.IntN(len(sipUsers))] + strconv.Itoa(100+rng.IntN(900))
	var c chain
	c.text("OPTIONS sip:" + domain + " SIP/2.0\r\nVia: SIP/2.0/UDP " + ip + ":" + port + ";branch=z9hG4bK")
	c.rc(12)
	c.text(";rport\r\nMax-Forwards: 70\r\nFrom: <sip:" + user + "@" + domain + ">;tag=")
	c.rc(8)
	c.text("\r\nTo: <sip:" + domain + ">\r\nCall-ID: ")
	c.rc(16)
	c.text("@" + ip + "\r\nCSeq: ")
	c.lit(byte('1' + rng.IntN(9))) // no leading zero
	if n := rng.IntN(3); n > 0 {
		c.rd(n)
	}
	c.text(" OPTIONS\r\nContact: <sip:" + user + "@" + ip + ":" + port + ">\r\nUser-Agent: " + sipAgents[rng.IntN(len(sipAgents))])
	c.text("\r\nAllow: " + sipAllow[rng.IntN(len(sipAllow))] + "\r\nAccept: application/sdp\r\nContent-Length: 0\r\n\r\n")
	return c
}

// ntpRequest: RFC 5905 client request, 48 bytes. LI 0, version 4, mode 3; every field zero except the transmit
// timestamp, which a client fills with its clock (fuzzed in its low bits by chrony and others): random.
func ntpRequest(rng *rand.Rand) chain {
	var c chain
	poll := []byte{6, 8, 10}[rng.IntN(3)]
	precision := byte(0xe9 + rng.IntN(6)) // -23 .. -18
	c.lit(0x23, 0x00, poll, precision)
	c.lit(make([]byte, 36)...)
	c.r(8)
	return c
}

// rtpPacket: RFC 3550 fixed header, version 2, no padding, extension or CSRC; payload type PCMU, PCMA or Opus
// (dynamic 111). Sequence number, timestamp, SSRC and the codec payload are random on the wire.
func rtpPacket(rng *rand.Rand) chain {
	var c chain
	pt, payload := byte(0), 160
	switch rng.IntN(3) {
	case 1:
		pt = 8
	case 2:
		pt, payload = 111, 60+rng.IntN(101)
	}
	c.lit(0x80, pt)
	c.r(2 + 4 + 4)
	c.r(payload)
	return c
}

var ssdpTargets = []string{"ssdp:all", "upnp:rootdevice"}

// ssdpSearch: the UPnP multicast M-SEARCH (UPnP Device Architecture 1.1, 1.3.2). It names no host: the target is
// the multicast group.
func ssdpSearch(rng *rand.Rand) chain {
	var c chain
	c.text("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: ")
	c.text(fmt.Sprint(1 + rng.IntN(5)))
	c.text("\r\nST: ")
	c.text(ssdpTargets[rng.IntN(len(ssdpTargets))])
	c.text("\r\nUSER-AGENT: Linux/")
	c.rd(1)
	c.text(".")
	c.rd(2)
	c.text(" UPnP/1.1 ")
	c.rc(6)
	c.text("/")
	c.rd(1)
	c.text(".")
	c.rd(1)
	c.text("\r\n\r\n")
	return c
}

// dtlsClientHello: RFC 6347 ClientHello record in one fragment, the shape a WebRTC stack sends: record version
// 1.0 (0xfeff), body version 1.2, gmt_unix_time + 28 random bytes, empty session id and cookie, the usual
// ECDHE/AES-GCM suites, use_srtp and the extensions after it. Every length field is computed from the parts.
func dtlsClientHello() chain {
	var body chain
	body.lit(0xfe, 0xfd)
	body.t()
	body.r(28)
	body.lit(0x00, 0x00) // session id, cookie
	suites := []uint16{0xc02b, 0xc02f, 0xcca9, 0xcca8, 0xc00a, 0xc009, 0xc013, 0xc014, 0x002f, 0x0035}
	hi, lo := u16(2 * len(suites))
	body.lit(hi, lo)
	for _, s := range suites {
		hi, lo := u16(int(s))
		body.lit(hi, lo)
	}
	body.lit(0x01, 0x00) // compression: null only
	exts := []byte{
		0x00, 0x0e, 0x00, 0x07, 0x00, 0x04, 0x00, 0x01, 0x00, 0x07, 0x00, // use_srtp: AES128_CM_SHA1_80, AEAD_AES_128_GCM
		0x00, 0x0a, 0x00, 0x08, 0x00, 0x06, 0x00, 0x1d, 0x00, 0x17, 0x00, 0x18, // supported_groups: x25519, p256, p384
		0x00, 0x0b, 0x00, 0x02, 0x01, 0x00, // ec_point_formats: uncompressed
		0x00, 0x0d, 0x00, 0x12, 0x00, 0x10, 0x04, 0x03, 0x08, 0x04, 0x04, 0x01, 0x05, 0x03, 0x08, 0x05, 0x05, 0x01, 0x08, 0x06, 0x06, 0x01, // signature_algorithms
		0x00, 0x17, 0x00, 0x00, // extended_master_secret
		0xff, 0x01, 0x00, 0x01, 0x00, // renegotiation_info
	}
	hi, lo = u16(len(exts))
	body.lit(hi, lo)
	body.lit(exts...)

	var hs chain
	n := body.size()
	hs.lit(0x01, byte(n>>16), byte(n>>8), byte(n))    // ClientHello, length
	hs.lit(0, 0)                                      // message_seq
	hs.lit(0, 0, 0, byte(n>>16), byte(n>>8), byte(n)) // fragment_offset 0, fragment_length
	hs.add(body)

	var c chain
	hi, lo = u16(hs.size())
	c.lit(0x16, 0xfe, 0xff, 0, 0, 0, 0, 0, 0, 0, 0, hi, lo) // handshake, epoch 0, sequence 0
	c.add(hs)
	return c
}
