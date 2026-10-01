package mimicry

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/cryptobyte"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 9001 Appendix A.1: the keys of the Initial space for the destination connection id 0x8394c8f03e515708.
func TestInitialKeysRFC9001(t *testing.T) {
	dcid := unhex(t, "8394c8f03e515708")
	for side, want := range map[string][3]string{
		"client": {"1f369613dd76d5467730efcbe3b1a22d", "fa044b2f42a3fd3b46fb255c", "9f50449e04a0e810283a1e9933adedd2"},
		"server": {"cf3a5331653c364c88f0f379b6067e37", "0ac1493ca1905853b0bba03e", "c206b8d9b9f0f37644430b490eeaa314"},
	} {
		k, err := initialKeys(dcid, side)
		if err != nil {
			t.Fatal(err)
		}
		if got := [3]string{hex.EncodeToString(k.key), hex.EncodeToString(k.iv), hex.EncodeToString(k.hp)}; got != want {
			t.Errorf("%s keys:\n got %v\nwant %v", side, got, want)
		}
	}
}

// RFC 9001 A.2: the header of the client Initial before and after protection, and the first sixteen ciphertext
// bytes, which are the header protection sample.
func TestInitialPacketPrefixRFC9001(t *testing.T) {
	dcid := unhex(t, "8394c8f03e515708")
	k, _ := initialKeys(dcid, "client")

	header := unhex(t, "c300000001088394c8f03e5157080000449e00000002")
	if err := protectHeader(header, 18, 4, k.hp, unhex(t, "d1b1c98dd7689fb8ec11d242b123dc9b")); err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(header), "c000000001088394c8f03e5157080000449e7b9aec34"; got != want {
		t.Errorf("protected header %s, want %s", got, want)
	}

	// The CRYPTO frame of A.2 starts 06 00 40f1 01 0000ed 0303 ebf8fa56f129...; only the first sixteen
	// plaintext bytes decide the first sixteen ciphertext bytes (counter mode), so the rest of the ClientHello
	// can be filler of the same length (241 bytes: the length field says 0x40f1).
	crypto := append(unhex(t, "010000ed0303ebf8fa56f129"), make([]byte, 241-12)...)
	pkt, err := initialPacket(dcid, nil, 2, 4, crypto, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt) != 1200 {
		t.Fatalf("datagram is %d bytes", len(pkt))
	}
	want := "c000000001088394c8f03e5157080000449e7b9aec34" + "d1b1c98dd7689fb8ec11d242b123dc9b"
	if got := hex.EncodeToString(pkt[:38]); got != want {
		t.Errorf("packet starts\n got %s\nwant %s", got, want)
	}
}

type rawExt struct {
	typ  uint16
	data []byte
}

// initial is what a reader that knows only RFC 9000/9001 learns from a client Initial datagram.
type initial struct {
	dcid, scid []byte
	pn         uint32
	pnLen      int
	padding    int // zero bytes after the CRYPTO frame
	hello      []byte
}

func readVarint(s *cryptobyte.String) (uint64, bool) {
	var first uint8
	if !s.ReadUint8(&first) {
		return 0, false
	}
	v := uint64(first & 0x3f)
	for i := 0; i < 1<<(first>>6)-1; i++ {
		var b uint8
		if !s.ReadUint8(&b) {
			return 0, false
		}
		v = v<<8 | uint64(b)
	}
	return v, true
}

// openInitial does what a DPI box does: derive the keys from the destination id in the header, remove header
// protection, decrypt (the AEAD tag must verify), walk the frames.
func openInitial(t *testing.T, d []byte) initial {
	t.Helper()
	s := cryptobyte.String(d)
	var first, tokenLen uint8
	var version uint32
	var dcid, scid cryptobyte.String
	if !s.ReadUint8(&first) || !s.ReadUint32(&version) || !s.ReadUint8LengthPrefixed(&dcid) ||
		!s.ReadUint8LengthPrefixed(&scid) || !s.ReadUint8(&tokenLen) {
		t.Fatal("short header")
	}
	if first&0xf0 != 0xc0 || version != 1 || tokenLen != 0 {
		t.Fatalf("not a v1 Initial with an empty token: first %#x version %d token %d", first, version, tokenLen)
	}
	length, ok := readVarint(&s)
	if !ok || int(length) != len(s) {
		t.Fatalf("length field %d, %d bytes follow", length, len(s))
	}
	pnOff := len(d) - len(s)

	keys, err := initialKeys(dcid, "client")
	if err != nil {
		t.Fatal(err)
	}
	hp, _ := aes.NewCipher(keys.hp)
	var mask [16]byte
	hp.Encrypt(mask[:], d[pnOff+4:pnOff+20])
	first ^= mask[0] & 0x0f
	pnLen := int(first&3) + 1
	hdr := append([]byte(nil), d[:pnOff+pnLen]...)
	hdr[0] = first
	var pn uint32
	for i := 0; i < pnLen; i++ {
		hdr[pnOff+i] ^= mask[1+i]
		pn = pn<<8 | uint32(hdr[pnOff+i])
	}

	block, _ := aes.NewCipher(keys.key)
	aead, _ := cipher.NewGCM(block)
	nonce := append([]byte(nil), keys.iv...)
	for i := 0; i < 4; i++ {
		nonce[8+i] ^= byte(pn >> (24 - 8*i))
	}
	plain, err := aead.Open(nil, nonce, d[pnOff+pnLen:], hdr)
	if err != nil {
		t.Fatalf("payload does not decrypt: %v", err)
	}

	in := initial{dcid: dcid, scid: scid, pn: pn, pnLen: pnLen}
	f := cryptobyte.String(plain)
	var ft uint8
	if !f.ReadUint8(&ft) || ft != 0x06 {
		t.Fatalf("first frame type %#x, want CRYPTO", ft)
	}
	off, ok1 := readVarint(&f)
	n, ok2 := readVarint(&f)
	if !ok1 || !ok2 || off != 0 || !f.ReadBytes(&in.hello, int(n)) {
		t.Fatalf("bad CRYPTO frame: offset %d length %d", off, n)
	}
	for _, b := range f {
		if b != 0 {
			t.Fatalf("a non-PADDING byte %#x after the CRYPTO frame", b)
		}
	}
	in.padding = len(f)
	return in
}

func parseHello(t *testing.T, hello []byte) (suites []uint16, exts []rawExt) {
	t.Helper()
	s := cryptobyte.String(hello)
	var typ uint8
	var body, sid, cs, comp, eb cryptobyte.String
	var ver uint16
	var random []byte
	if !s.ReadUint8(&typ) || typ != 1 || !s.ReadUint24LengthPrefixed(&body) || !s.Empty() {
		t.Fatal("not a ClientHello with a matching length")
	}
	if !body.ReadUint16(&ver) || ver != 0x0303 || !body.ReadBytes(&random, 32) || !body.ReadUint8LengthPrefixed(&sid) ||
		!body.ReadUint16LengthPrefixed(&cs) || !body.ReadUint8LengthPrefixed(&comp) || !body.ReadUint16LengthPrefixed(&eb) || !body.Empty() {
		t.Fatal("malformed ClientHello body")
	}
	if len(sid) != 0 {
		t.Errorf("legacy_session_id has %d bytes, RFC 9001 8.4 wants none", len(sid))
	}
	if !bytes.Equal(comp, []byte{0}) {
		t.Errorf("compression methods % x", []byte(comp))
	}
	for !cs.Empty() {
		var v uint16
		if !cs.ReadUint16(&v) {
			t.Fatal("odd cipher suite list")
		}
		suites = append(suites, v)
	}
	for !eb.Empty() {
		var e rawExt
		var d cryptobyte.String
		if !eb.ReadUint16(&e.typ) || !eb.ReadUint16LengthPrefixed(&d) {
			t.Fatal("malformed extension")
		}
		e.data = d
		exts = append(exts, e)
	}
	return suites, exts
}

func isGrease(v uint16) bool { return v&0x0f0f == 0x0a0a && v>>8 == v&0xff }

func find(exts []rawExt, typ uint16) []byte {
	for _, e := range exts {
		if e.typ == typ {
			return e.data
		}
	}
	return nil
}

func sniOf(t *testing.T, exts []rawExt) string {
	t.Helper()
	s := cryptobyte.String(find(exts, extServerName))
	var list, name cryptobyte.String
	var nt uint8
	if !s.ReadUint16LengthPrefixed(&list) || !list.ReadUint8(&nt) || nt != 0 || !list.ReadUint16LengthPrefixed(&name) || !list.Empty() {
		t.Fatal("malformed server_name")
	}
	return string(name)
}

// transportParams reads quic_transport_parameters into id -> value.
func transportParams(t *testing.T, data []byte) map[uint64][]byte {
	t.Helper()
	out := map[uint64][]byte{}
	s := cryptobyte.String(data)
	for !s.Empty() {
		id, ok1 := readVarint(&s)
		n, ok2 := readVarint(&s)
		var v []byte
		if !ok1 || !ok2 || !s.ReadBytes(&v, int(n)) {
			t.Fatal("malformed transport parameters")
		}
		if _, dup := out[id]; dup {
			t.Fatalf("transport parameter %#x twice", id)
		}
		out[id] = v
	}
	return out
}

func quicDatagram(t *testing.T, id, domain string, seed uint64) (string, []byte) {
	t.Helper()
	chain, err := GenerateChain(id, Options{Domain: domain}, rand.New(rand.NewPCG(seed, 9)))
	if err != nil {
		t.Fatal(err)
	}
	return chain[0], expand(t, chain[0], rand.New(rand.NewPCG(1, 1)))
}

func TestQUICInitialRoundTrip(t *testing.T) {
	domains := []string{"", "www.example.com", "a.bc", strings.Repeat("a", 63) + "." + strings.Repeat("b", 30) + ".com"} // the last one is 98 characters
	for _, id := range []string{QUIC, CurlQUIC} {
		for _, domain := range domains {
			for seed := uint64(0); seed < 40; seed++ {
				_, d := quicDatagram(t, id, domain, seed)
				if len(d) != 1200 {
					t.Fatalf("%s: datagram is %d bytes", id, len(d))
				}
				in := openInitial(t, d)
				suites, exts := parseHello(t, in.hello)

				if domain != "" {
					if got := sniOf(t, exts); got != domain {
						t.Fatalf("%s: SNI %q, want %q", id, got, domain)
					}
				} else if !slices.Contains(domainPool, sniOf(t, exts)) {
					t.Fatalf("%s: SNI %q is not from the pool", id, sniOf(t, exts))
				}
				if in.pn != 0 {
					t.Errorf("%s: first Initial has packet number %d", id, in.pn)
				}
				// ALPN h3, TLS 1.3 offered, one x25519 key share that is a real public key (top bit clear)
				if a := find(exts, extALPN); !bytes.Equal(a, []byte{0, 3, 2, 'h', '3'}) {
					t.Errorf("%s: alpn % x", id, a)
				}
				vs := cryptobyte.String(find(exts, extVersions))
				var vl cryptobyte.String
				if !vs.ReadUint8LengthPrefixed(&vl) || !bytes.Contains(vl, []byte{0x03, 0x04}) {
					t.Errorf("%s: supported_versions lacks TLS 1.3", id)
				}
				ks := cryptobyte.String(find(exts, extKeyShare))
				var entries cryptobyte.String
				if !ks.ReadUint16LengthPrefixed(&entries) {
					t.Fatalf("%s: malformed key_share", id)
				}
				x25519 := 0
				for !entries.Empty() {
					var g uint16
					var kx cryptobyte.String
					if !entries.ReadUint16(&g) || !entries.ReadUint16LengthPrefixed(&kx) {
						t.Fatalf("%s: malformed key share entry", id)
					}
					if g == groupX25519 {
						x25519++
						if len(kx) != 32 || kx[31]&0x80 != 0 {
							t.Errorf("%s: x25519 share is %d bytes, last byte %#x", id, len(kx), kx[len(kx)-1])
						}
					} else if !isGrease(g) {
						t.Errorf("%s: unexpected key share group %#x", id, g)
					}
				}
				if x25519 != 1 {
					t.Errorf("%s: %d x25519 shares", id, x25519)
				}
				// RFC 9000 7.3: initial_source_connection_id repeats the id of the header
				tp := transportParams(t, find(exts, extQUICParams))
				if v, ok := tp[0x0f]; !ok || !bytes.Equal(v, in.scid) {
					t.Errorf("%s: initial_source_connection_id % x, header scid % x", id, v, in.scid)
				}
				seen := map[uint16]bool{}
				for _, e := range exts {
					if seen[e.typ] {
						t.Errorf("%s: extension %#x twice", id, e.typ)
					}
					seen[e.typ] = true
				}

				switch id {
				case QUIC:
					if len(in.dcid) != 8 || len(in.scid) != 0 || in.pnLen != 4 {
						t.Errorf("quic ids %d/%d, pn length %d", len(in.dcid), len(in.scid), in.pnLen)
					}
					if len(in.hello) != 512 {
						t.Errorf("quic hello is %d bytes, BoringSSL pads this range to 512", len(in.hello))
					}
					if !isGrease(suites[0]) || !slices.Equal(suites[1:], []uint16{0x1301, 0x1302, 0x1303}) {
						t.Errorf("quic suites %x", suites)
					}
					// GREASE first (empty), GREASE second to last (one zero byte), padding last
					last := len(exts) - 1
					if !isGrease(exts[0].typ) || len(exts[0].data) != 0 ||
						exts[last].typ != extPadding || !isGrease(exts[last-1].typ) || !bytes.Equal(exts[last-1].data, []byte{0}) || exts[0].typ == exts[last-1].typ {
						t.Errorf("quic extension frame: first %#x, then ... %#x %#x", exts[0].typ, exts[last-1].typ, exts[last].typ)
					}
				case CurlQUIC:
					if len(in.dcid) != 18 || len(in.scid) != 18 || in.pnLen != 1 {
						t.Errorf("curl ids %d/%d, pn length %d", len(in.dcid), len(in.scid), in.pnLen)
					}
					if !slices.Equal(suites, []uint16{0x1301}) {
						t.Errorf("curl suites %x", suites)
					}
					var order []uint16
					for _, e := range exts {
						order = append(order, e.typ)
					}
					want := []uint16{extServerName, extVersions, extGroups, extSigAlgs, extALPN, extKeyShare, extPSKModes, extQUICParams, extCompressCert, extECH}
					if !slices.Equal(order, want) {
						t.Errorf("curl extension order %x, want %x", order, want)
					}
					ech := cryptobyte.String(find(exts, extECH))
					var typ uint8
					var kdf, aead uint16
					var cfg uint8
					var enc, payload cryptobyte.String
					if !ech.ReadUint8(&typ) || !ech.ReadUint16(&kdf) || !ech.ReadUint16(&aead) || !ech.ReadUint8(&cfg) ||
						!ech.ReadUint16LengthPrefixed(&enc) || !ech.ReadUint16LengthPrefixed(&payload) || !ech.Empty() ||
						typ != 0 || kdf != 1 || aead != 1 || len(enc) != 32 || (len(payload)-144)%32 != 0 || len(payload) < 144 || len(payload) > 240 {
						t.Errorf("ECH GREASE extension malformed: type %d, enc %d, payload %d", typ, len(enc), len(payload))
					}
					for _, e := range exts {
						if isGrease(e.typ) {
							t.Errorf("curl hello has a GREASE extension %#x", e.typ)
						}
					}
				}
			}
		}
	}
}

// A Chrome hello shuffles its extensions; the set and the frame stay.
func TestChromeExtensionsAreShuffled(t *testing.T) {
	orders := map[string]bool{}
	var set []uint16
	for seed := uint64(0); seed < 60; seed++ {
		_, d := quicDatagram(t, QUIC, "", seed)
		_, exts := parseHello(t, openInitial(t, d).hello)
		var order []uint16
		for _, e := range exts[1 : len(exts)-2] { // without the GREASE pair and the padding
			order = append(order, e.typ)
		}
		orders[fmt.Sprint(order)] = true
		sorted := slices.Clone(order)
		slices.Sort(sorted)
		if set == nil {
			set = sorted
		} else if !slices.Equal(set, sorted) {
			t.Fatalf("seed %d: extension set %x differs from %x", seed, sorted, set)
		}
	}
	if len(orders) < 40 {
		t.Errorf("only %d different extension orders in 60 hellos", len(orders))
	}
	if len(set) != 11 {
		t.Errorf("%d extensions besides GREASE and padding, want 11", len(set))
	}
}

var literalOnly = regexp.MustCompile(`^<b 0x[0-9a-f]+>$`)

func TestQUICChainIsOneLiteral(t *testing.T) {
	for _, id := range []string{QUIC, CurlQUIC} {
		a, err := GenerateChain(id, Options{}, rand.New(rand.NewPCG(3, 4)))
		if err != nil {
			t.Fatal(err)
		}
		if !literalOnly.MatchString(a[0]) || len(a[0]) != len("<b 0x>")+2*1200 {
			t.Errorf("%s: I1 is %d chars and not one literal", id, len(a[0]))
		}
		if a[1] != "" || a[2] != "" || a[3] != "" || a[4] != "" {
			t.Errorf("%s: the tail is not empty: %q", id, a[1:])
		}
		b, _ := GenerateChain(id, Options{}, rand.New(rand.NewPCG(3, 4)))
		c, _ := GenerateChain(id, Options{}, rand.New(rand.NewPCG(3, 5)))
		if a != b || a == c {
			t.Errorf("%s: same seed must give the same packet and another seed another one", id)
		}
	}
}
