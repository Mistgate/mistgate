package mimicry

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestParse(t *testing.T) {
	ok := map[string]int{
		"":                           0,
		"<b 0x00>":                   1,
		"<b 0xDEADbeef>":             4,
		"<r 1>":                      1,
		"<r 1000>":                   1000,
		"<rc 8><rd 3>":               11,
		"<t>":                        4,
		"<r 2><b 0x858000><t><rc 5>": 2 + 3 + 4 + 5,
		"<b 0x" + strings.Repeat("ab", 1200) + ">": 1200,
	}
	for s, size := range ok {
		info, err := Parse(s)
		if err != nil || info.Size != size {
			t.Errorf("Parse(%.40q) = %+v, %v; want size %d", s, info, err, size)
		}
	}
	bad := map[string]string{
		"<c>":            "counter",
		"<b 0x>":         "empty hex",
		"<b 0x123>":      "odd hex",
		"<b 12>":         "no 0x",
		"<b 0xzz>":       "not hex",
		"<b>":            "no argument",
		"<r 0>":          "zero",
		"<r 1001>":       "too many",
		"<r -5>":         "negative",
		"<r 2000000000>": "huge",
		"<r 1.5>":        "not a number",
		"<r>":            "no count",
		"<rc 0>":         "zero rc",
		"<rd 1001>":      "rd too many",
		"<t><t>":         "two times",
		"<t 4>":          "t with argument",
		"<x 1>":          "unknown tag",
		"<b 0x00":        "unclosed",
		"hello":          "no tags",
		"<b 0x00> <r 1>": "space between tags",
		"<b 0x00>#<r 1>": "comment char",
		"<b 0x00>\n":     "newline",
		"<b  0x00>":      "double space",
		"<r 600><r 601>": "over 1200 bytes",
		"<b 0x" + strings.Repeat("ab", 1201) + ">": "one byte over",
		strings.Repeat("<t>", 3):                   "three times",
		strings.Repeat("<r 1>", 801):               "over 3500 chars",
	}
	for s, why := range bad {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%.40q) accepted (%s)", s, why)
		}
	}
	if _, err := Parse("<c>"); err != ErrCounterTag {
		t.Errorf("<c> error = %v", err)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "<r 2><b 0x8580>", "<t>", "<c>", "<b 0x", "<<>>", "<rc 99999999999999999999>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		info, err := Parse(s)
		if err == nil && (info.Size > MaxPacketSize || len(s) > MaxChainChars) {
			t.Fatalf("accepted %q with size %d", s, info.Size)
		}
	})
}

// expand turns a chain into bytes the way the initiator does: random fill for r/rc/rd, the time for t.
func expand(t *testing.T, s string, rng *rand.Rand) []byte {
	t.Helper()
	var out []byte
	for i := 0; i < len(s); {
		end := strings.IndexByte(s[i:], '>')
		name, arg, _ := strings.Cut(s[i+1:i+end], " ")
		i += end + 1
		switch name {
		case "b":
			for j := 2; j < len(arg); j += 2 {
				v, _ := strconv.ParseUint(arg[j:j+2], 16, 8)
				out = append(out, byte(v))
			}
		case "r", "rc", "rd":
			n, _ := strconv.Atoi(arg)
			for j := 0; j < n; j++ {
				switch name {
				case "r":
					out = append(out, byte(rng.Uint32()))
				case "rc":
					out = append(out, byte('a'+rng.IntN(26)))
				default:
					out = append(out, byte('0'+rng.IntN(10)))
				}
			}
		case "t":
			out = binary.BigEndian.AppendUint32(out, 1_700_000_000)
		}
	}
	return out
}

// chainLen is how many packets a preset sends: the DNS triplet, the WebRTC pair, one for the rest.
func chainLen(id string) int {
	switch id {
	case DNS:
		return 3
	case WebRTC:
		return 2
	case Custom:
		return 0
	}
	return 1
}

// Frozen on purpose: a QUIC Initial is encrypted, a tag inside it would break the packet.
func isQUIC(id string) bool { return id == QUIC || id == CurlQUIC }

func TestEveryPresetGeneratesValidChains(t *testing.T) {
	for _, p := range Presets() {
		distinct := map[[5]string]bool{}
		for seed := uint64(0); seed < 300; seed++ {
			rng := rand.New(rand.NewPCG(seed, 7))
			chain, err := GenerateChain(p.ID, Options{}, rng)
			if err != nil {
				t.Fatalf("%s seed %d: %v", p.ID, seed, err)
			}
			distinct[chain] = true
			if first, err := Generate(p.ID, rand.New(rand.NewPCG(seed, 7))); err != nil || first != chain[0] {
				t.Fatalf("%s: Generate is %q, I1 of the chain is %q (%v)", p.ID, first, chain[0], err)
			}
			total := 0
			for i, s := range chain {
				total += len(s)
				if i >= chainLen(p.ID) {
					if s != "" {
						t.Fatalf("%s seed %d: I%d is %q, the tail must be empty", p.ID, seed, i+1, s)
					}
					continue
				}
				info, err := Parse(s)
				if err != nil {
					t.Fatalf("%s seed %d: I%d %q does not parse: %v", p.ID, seed, i+1, s, err)
				}
				if info.Size == 0 || info.Size > MaxPacketSize {
					t.Fatalf("%s seed %d: I%d is %d bytes", p.ID, seed, i+1, info.Size)
				}
				if !isQUIC(p.ID) && !(strings.Contains(s, "<r ") || strings.Contains(s, "<rc ") || strings.Contains(s, "<rd ")) {
					t.Fatalf("%s: a frozen packet is a signature, no dynamic tag in %q", p.ID, s)
				}
				if got := len(expand(t, s, rng)); got != info.Size {
					t.Fatalf("%s: Parse says %d bytes, expansion has %d", p.ID, info.Size, got)
				}
			}
			if total > MaxChainChars {
				t.Fatalf("%s seed %d: %d characters in all, the limit is %d", p.ID, seed, total, MaxChainChars)
			}
		}
		// Another seed gives another packet, as far as the protocol has a per-client choice at all. A STUN
		// Binding request, an ICE check and a DTLS ClientHello are one shape whose every random field is a tag,
		// NTP and SSDP draw from a handful of values (poll and precision, MX and target).
		want := map[string]int{Custom: 1, STUN: 1, WebRTC: 1, DTLS: 1, NTP: 15, SSDP: 8, RTP: 60}[p.ID]
		if want == 0 {
			want = 100
		}
		if len(distinct) < want || (want == 1 && len(distinct) != 1) {
			t.Errorf("%s: %d different chains in 300 seeds, want %d", p.ID, len(distinct), want)
		}
	}
	if _, err := Generate("nope", rand.New(rand.NewPCG(1, 1))); err == nil {
		t.Error("unknown preset accepted")
	}
	if _, err := GenerateChain("nope", Options{}, rand.New(rand.NewPCG(1, 1))); err == nil {
		t.Error("unknown preset accepted by GenerateChain")
	}
}

func TestSameSeedSameChain(t *testing.T) {
	for _, p := range Presets() {
		for _, domain := range []string{"", "example.com"} {
			a, errA := GenerateChain(p.ID, Options{Domain: domain}, rand.New(rand.NewPCG(5, 6)))
			b, errB := GenerateChain(p.ID, Options{Domain: domain}, rand.New(rand.NewPCG(5, 6)))
			if errA != nil || errB != nil || a != b {
				t.Errorf("%s is not deterministic for a seed (%v, %v)", p.ID, errA, errB)
			}
		}
	}
}

// Whatever the domain, a chain stays inside the limits: the longest name (SIP repeats it) is the worst case.
func TestChainLimitsWithTheLongestDomain(t *testing.T) {
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 30) + ".com"
	if len(long) != 98 {
		t.Fatal(len(long))
	}
	for _, p := range Presets() {
		for seed := uint64(0); seed < 30; seed++ {
			chain, err := GenerateChain(p.ID, Options{Domain: long}, rand.New(rand.NewPCG(seed, 2)))
			if err != nil {
				t.Fatalf("%s: %v", p.ID, err)
			}
			total := 0
			for _, s := range chain {
				if _, err := Parse(s); err != nil {
					t.Fatalf("%s: %v", p.ID, err)
				}
				total += len(s)
			}
			if total > MaxChainChars {
				t.Fatalf("%s: %d characters", p.ID, total)
			}
		}
	}
}

func pkt(t *testing.T, id string, seed uint64) []byte {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 1))
	s, err := Generate(id, rng)
	if err != nil {
		t.Fatal(err)
	}
	return expand(t, s, rng)
}

func chainPkts(t *testing.T, id, domain string, seed uint64) [][]byte {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 1))
	chain, err := GenerateChain(id, Options{Domain: domain}, rng)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, s := range chain {
		if s != "" {
			out = append(out, expand(t, s, rng))
		}
	}
	return out
}

// The presets claim to follow the RFCs: parse what they expand to with an independent reader where one exists.
func TestQUICInitialShape(t *testing.T) {
	for _, id := range []string{QUIC, CurlQUIC} {
		for seed := uint64(0); seed < 50; seed++ {
			p := pkt(t, id, seed)
			if len(p) != 1200 {
				t.Fatalf("%s: datagram is %d bytes, an Initial is padded to 1200", id, len(p))
			}
			if p[0]&0xF0 != 0xC0 || binary.BigEndian.Uint32(p[1:5]) != 1 {
				t.Fatalf("%s: not a v1 Initial long header: % x", id, p[:6])
			}
			dcid := int(p[5])
			scid := int(p[6+dcid])
			off := 7 + dcid + scid
			if p[off] != 0 {
				t.Fatalf("%s: token length %d, want 0", id, p[off])
			}
			if p[off+1]&0xC0 != 0x40 {
				t.Fatalf("%s: length is not a two-byte varint", id)
			}
			length := int(binary.BigEndian.Uint16(p[off+1:off+3]) & 0x3fff)
			if off+3+length != len(p) {
				t.Fatalf("%s: length field %d does not reach the end of the datagram (%d)", id, length, len(p)-off-3)
			}
		}
	}
}

// A browser looking up one name sends A, AAAA and HTTPS queries for it, each with its own id and an EDNS0 OPT.
func TestDNSTripletShape(t *testing.T) {
	for _, domain := range []string{"", "example.com", "cdn.example.co.uk"} {
		orders := map[string]bool{}
		for seed := uint64(0); seed < 100; seed++ {
			pkts := chainPkts(t, DNS, domain, seed)
			if len(pkts) != 3 {
				t.Fatalf("%d DNS packets, want 3", len(pkts))
			}
			var types []dnsmessage.Type
			var name string
			ids := map[uint16]bool{}
			udpSize := map[dnsmessage.Class]bool{}
			for i, raw := range pkts {
				var p dnsmessage.Parser
				h, err := p.Start(raw)
				if err != nil {
					t.Fatal(err)
				}
				if h.Response || !h.RecursionDesired {
					t.Errorf("not a plain recursive query: %+v", h)
				}
				ids[h.ID] = true
				q, err := p.Question()
				if err != nil || q.Class != dnsmessage.ClassINET {
					t.Fatalf("question %+v, %v", q, err)
				}
				if i == 0 {
					name = q.Name.String()
				} else if q.Name.String() != name {
					t.Errorf("packet %d asks for %q, packet 0 for %q", i, q.Name, name)
				}
				types = append(types, q.Type)
				if err := p.SkipAllQuestions(); err != nil {
					t.Fatal(err)
				}
				if err := p.SkipAllAnswers(); err != nil {
					t.Fatal(err)
				}
				if err := p.SkipAllAuthorities(); err != nil {
					t.Fatal(err)
				}
				add, err := p.AllAdditionals()
				if err != nil || len(add) != 1 || add[0].Header.Type != dnsmessage.TypeOPT {
					t.Fatalf("additionals %v, %v", add, err)
				}
				udpSize[add[0].Header.Class] = true // the OPT class field is the advertised UDP payload size
			}
			if domain != "" && name != domain+"." {
				t.Errorf("name %q, want %q", name, domain+".")
			}
			if domain == "" && !slices.Contains(domainPool, strings.TrimSuffix(name, ".")) {
				t.Errorf("name %q is not from the pool", name)
			}
			sorted := slices.Clone(types)
			slices.Sort(sorted)
			if !slices.Equal(sorted, []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA, dnsmessage.TypeHTTPS}) {
				t.Errorf("types %v, want A, AAAA and HTTPS once each", types)
			}
			if len(udpSize) != 1 {
				t.Errorf("the three queries advertise different UDP sizes %v: one client has one", udpSize)
			}
			orders[fmt.Sprint(types)] = true
			if len(ids) != 3 {
				t.Errorf("the three queries share an id: a resolver draws one per query (%v)", ids)
			}
		}
		if len(orders) < 4 {
			t.Errorf("only %d different query orders in 100 chains", len(orders))
		}
	}
	// the id of every packet is its own tag, so it is new at every send
	chain, _ := GenerateChain(DNS, Options{}, rand.New(rand.NewPCG(1, 1)))
	for i, s := range chain[:3] {
		if !strings.HasPrefix(s, "<r 2>") || strings.Count(s, "<r ") != 1 {
			t.Errorf("I%d %q: want exactly one <r 2>, the id, at the start", i+1, s)
		}
	}
}

// Dynamic fields are independent, so each one is used once per packet; what repeats (the domain, the LAN address)
// is literal text and stays equal across the headers.
func TestSIPFieldsAreConsistent(t *testing.T) {
	re := regexp.MustCompile(`^OPTIONS sip:([\w.-]+) SIP/2\.0\r\nVia: SIP/2\.0/UDP ([\d.]+):(\d+);branch=z9hG4bK([a-z]{12});rport\r\n` +
		`Max-Forwards: 70\r\nFrom: <sip:(\w+)@([\w.-]+)>;tag=([a-z]{8})\r\nTo: <sip:([\w.-]+)>\r\nCall-ID: ([a-z]{16})@([\d.]+)\r\nCSeq: ([1-9]\d{0,2}) OPTIONS\r\n` +
		`Contact: <sip:(\w+)@([\d.]+):(\d+)>\r\nUser-Agent: [^\r\n]+\r\nAllow: [^\r\n]+\r\nAccept: application/sdp\r\nContent-Length: 0\r\n\r\n$`)
	for _, domain := range []string{"", "example.com"} {
		for seed := uint64(0); seed < 100; seed++ {
			s := string(chainPkts(t, SIP, domain, seed)[0])
			m := re.FindStringSubmatch(s)
			if m == nil {
				t.Fatalf("SIP packet does not match the expected shape:\n%s", s)
			}
			uriDomain, viaIP, viaPort, branch, user, fromDomain, tag, toDomain, callID, callHost, _, cUser, cIP, cPort := m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8], m[9], m[10], m[11], m[12], m[13], m[14]
			if uriDomain != fromDomain || uriDomain != toDomain || (domain != "" && uriDomain != domain) {
				t.Errorf("domain differs between request line %q, From %q and To %q", uriDomain, fromDomain, toDomain)
			}
			if viaIP != callHost || viaIP != cIP || viaPort != cPort || user != cUser {
				t.Errorf("one client, but Via %s:%s, Call-ID host %s, Contact %s@%s:%s", viaIP, viaPort, callHost, cUser, cIP, cPort)
			}
			for _, tok := range []string{branch, tag, callID} {
				if strings.Count(s, tok) != 1 {
					t.Errorf("%q is in the packet %d times; a dynamic field must be used once", tok, strings.Count(s, tok))
				}
			}
		}
	}
	// Two sends of one chain differ in exactly the dynamic fields.
	chain, _ := GenerateChain(SIP, Options{}, rand.New(rand.NewPCG(7, 7)))
	rng := rand.New(rand.NewPCG(1, 1))
	a, b := string(expand(t, chain[0], rng)), string(expand(t, chain[0], rng))
	if a == b || strings.Count(chain[0], "<rc 12>") != 1 || strings.Count(chain[0], "<rc 8>") != 1 || strings.Count(chain[0], "<rc 16>") != 1 {
		t.Errorf("two expansions of one chain: equal=%v, chain %s", a == b, chain[0])
	}
}

func TestNaturalPortsAndUsesDomain(t *testing.T) {
	ports := map[string][]int{QUIC: {443}, CurlQUIC: {443}, DNS: {53}, NTP: {123}, SIP: {5060}, SSDP: {1900}}
	for _, p := range Presets() {
		if got := NaturalPorts(p.ID); !slices.Equal(got, ports[p.ID]) {
			t.Errorf("NaturalPorts(%s) = %v, want %v", p.ID, got, ports[p.ID])
		}
		if want := slices.Contains([]string{QUIC, CurlQUIC, DNS, SIP}, p.ID); UsesDomain(p.ID) != want {
			t.Errorf("UsesDomain(%s) = %v", p.ID, UsesDomain(p.ID))
		}
	}
	if NaturalPorts("nope") != nil || UsesDomain("nope") {
		t.Error("an unknown preset has no ports and no domain")
	}
	if p := NaturalPorts(QUIC); len(p) == 1 {
		p[0] = 1 // a caller changing the result must not change the next answer
		if NaturalPorts(QUIC)[0] != 443 {
			t.Error("NaturalPorts hands out shared storage")
		}
	}
}

func TestSTUNAndICE(t *testing.T) {
	s := pkt(t, STUN, 1)
	if len(s) != 20 || binary.BigEndian.Uint16(s[0:2]) != 1 || binary.BigEndian.Uint32(s[4:8]) != 0x2112A442 || binary.BigEndian.Uint16(s[2:4]) != 0 {
		t.Errorf("stun: % x", s)
	}
	w := pkt(t, WebRTC, 1)
	if binary.BigEndian.Uint32(w[4:8]) != 0x2112A442 || int(binary.BigEndian.Uint16(w[2:4])) != len(w)-20 {
		t.Fatalf("ice: length field %d, body %d", binary.BigEndian.Uint16(w[2:4]), len(w)-20)
	}
	// walk the attributes: every one 4-byte aligned, exactly filling the body
	var types []uint16
	for off := 20; off < len(w); {
		typ, l := binary.BigEndian.Uint16(w[off:]), int(binary.BigEndian.Uint16(w[off+2:]))
		types = append(types, typ)
		off += 4 + (l+3)&^3
		if off > len(w) {
			t.Fatalf("attribute overruns the message")
		}
	}
	want := []uint16{0x0006, 0x0024, 0x802a, 0x0008, 0x8028}
	if len(types) != len(want) {
		t.Fatalf("attributes %x", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("attribute %d = %#x, want %#x", i, types[i], want[i])
		}
	}
}

func TestTextAndFixedSizePresets(t *testing.T) {
	if p := pkt(t, NTP, 1); len(p) != 48 || p[0] != 0x23 {
		t.Errorf("ntp: %d bytes, first %#x", len(p), p[0])
	}
	if p := pkt(t, RTP, 1); p[0] != 0x80 || len(p) < 12+60 {
		t.Errorf("rtp: % x", p[:4])
	}
	sip := string(pkt(t, SIP, 1))
	if !strings.HasPrefix(sip, "OPTIONS sip:") || !strings.Contains(sip, "SIP/2.0\r\n") || !strings.Contains(sip, ";branch=z9hG4bK") || !strings.HasSuffix(sip, "\r\n\r\n") {
		t.Errorf("sip:\n%s", sip)
	}
	ssdp := string(pkt(t, SSDP, 1))
	if !strings.HasPrefix(ssdp, "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\n") || !strings.HasSuffix(ssdp, "\r\n\r\n") {
		t.Errorf("ssdp:\n%s", ssdp)
	}
}

func TestDTLSClientHelloLengths(t *testing.T) {
	d := pkt(t, DTLS, 1)
	if d[0] != 0x16 || d[1] != 0xfe {
		t.Fatalf("not a DTLS handshake record: % x", d[:3])
	}
	if int(binary.BigEndian.Uint16(d[11:13])) != len(d)-13 {
		t.Fatalf("record length %d, body %d", binary.BigEndian.Uint16(d[11:13]), len(d)-13)
	}
	hs := d[13:]
	n := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
	fl := int(hs[9])<<16 | int(hs[10])<<8 | int(hs[11])
	if hs[0] != 1 || n != len(hs)-12 || fl != n {
		t.Fatalf("handshake: type %d length %d fragment %d, body %d", hs[0], n, fl, len(hs)-12)
	}
	body := hs[12:]
	p := 2 + 32 // version, random
	p += 1 + int(body[p])
	p += 1 + int(body[p])
	p += 2 + int(binary.BigEndian.Uint16(body[p:]))
	p += 1 + int(body[p])
	ext := int(binary.BigEndian.Uint16(body[p:]))
	if p+2+ext != len(body) {
		t.Fatalf("extensions: %d declared, %d left", ext, len(body)-p-2)
	}
	for q := p + 2; q < len(body); {
		q += 4 + int(binary.BigEndian.Uint16(body[q+2:]))
		if q > len(body) {
			t.Fatal("an extension overruns the body")
		}
	}
}

func TestBuilderMerges(t *testing.T) {
	var c chain
	c.lit(1, 2)
	c.lit(3)
	c.r(2)
	c.r(3)
	c.rc(4)
	c.t()
	if got, want := c.String(), "<b 0x010203><r 5><rc 4><t>"; got != want {
		t.Errorf("chain = %q, want %q", got, want)
	}
	if c.size() != 3+5+4+4 {
		t.Errorf("size = %d", c.size())
	}
}
