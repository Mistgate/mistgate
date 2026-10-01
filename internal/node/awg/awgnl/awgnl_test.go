package awgnl

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/mdlayher/genetlink"
	"github.com/mdlayher/genetlink/genltest"
	"github.com/mdlayher/netlink"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
)

// The expected bytes below are built by hand (a tiny TLV writer that shares no code with the encoder) from the
// kernel module's netlink attribute table, so a drift in the encoder fails here.

var ne = binary.NativeEndian

func attr(typ uint16, payload []byte) []byte {
	n := 4 + len(payload)
	out := make([]byte, 0, (n+3)&^3)
	out = ne.AppendUint16(out, uint16(n))
	out = ne.AppendUint16(out, typ)
	out = append(out, payload...)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	return out
}

func nest(typ uint16, children ...[]byte) []byte {
	return attr(typ|0x8000, bytes.Join(children, nil))
}
func n8(v uint8) []byte          { return []byte{v} }
func n16(v uint16) []byte        { return ne.AppendUint16(nil, v) }
func n32(v uint32) []byte        { return ne.AppendUint32(nil, v) }
func n64(v uint64) []byte        { return ne.AppendUint64(nil, v) }
func str(s string) []byte        { return append([]byte(s), 0) }
func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func key(b byte) [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = b
	}
	return k
}

func r(lo, hi uint32) awgcfg.Range {
	x, _ := awgcfg.ParseRange(rangeText(lo, hi))
	return x
}

func rangeText(lo, hi uint32) string {
	return awgcfg.Range{Lo: lo, Hi: hi}.String()
}

const famID = 38

func client(t *testing.T, info Info, check func(greq genetlink.Message)) *Client {
	t.Helper()
	fn := genltest.Func(func(greq genetlink.Message, nreq netlink.Message) ([]genetlink.Message, error) {
		check(greq)
		return nil, io.EOF
	})
	return &Client{c: genltest.Dial(genltest.CheckRequest(info.ID, 0, 0, fn)), Info: info}
}

func TestSetDeviceBytes31(t *testing.T) {
	priv, hpk, pub, psk := key(0x11), key(0x22), key(0x33), key(0x44)
	o := awgcfg.Obfuscation{
		Jc: 5, Jmin: 10, Jmax: 50, S1: 24, S2: 25, S3: 26, S4: 27,
		H1: r(1, 1), H2: r(2, 2), H3: r(300, 400), H4: r(4, 4),
		I1:                     "<b 0xc7><r 8>",
		HeaderProtectionKey:    base64.StdEncoding.EncodeToString(hpk[:]),
		RandomTrailers:         true,
		ContentPaddingAddition: r(2, 10), RekeyAfterTime: r(100, 120), RekeyTimeout: r(3, 7), RejectAfterTime: r(150, 180),
		KeepaliveTimeout: r(5, 15), MaxHandshakeAttempts: r(15, 20),
	}
	peer := awgcfg.Peer{
		PublicKey: pub, PSK: &psk, UpdateOnly: true, ReplaceIPs: true,
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.66.4.5/32"), netip.MustParsePrefix("fd66:66:0:1::5/128")},
	}
	want := cat(
		attr(2, str("mgawg51842")), attr(3, priv[:]), attr(6, n16(51842)),
		attr(9, n16(5)), attr(10, n16(10)), attr(11, n16(50)),
		attr(12, n16(24)), attr(13, n16(25)), attr(19, n16(26)), attr(20, n16(27)),
		attr(14, n64(1<<32|1)), attr(15, n64(2<<32|2)), attr(16, n64(400<<32|300)), attr(17, n64(4<<32|4)),
		attr(21, str("<b 0xc7><r 8>")),
		attr(26, hpk[:]),
		attr(27, n32(10<<16|2)), attr(28, n32(120<<16|100)), attr(29, n32(7<<16|3)), attr(30, n32(180<<16|150)), attr(31, n32(15<<16|5)), attr(32, n32(20<<16|15)),
		attr(33, n8(1)), attr(34, n8(0)),
		nest(8, nest(0,
			attr(1, pub[:]), attr(3, n32(4|2)), attr(2, psk[:]),
			nest(9,
				nest(0, attr(1, n16(2)), attr(2, []byte{10, 66, 4, 5}), attr(3, n8(32))),
				nest(0, attr(1, n16(10)), attr(2, netip.MustParseAddr("fd66:66:0:1::5").AsSlice()), attr(3, n8(128))),
			),
		)),
	)
	var got []byte
	c := client(t, Info{ID: famID, GenlVersion: 3, MaxAttr: 34}, func(greq genetlink.Message) {
		if greq.Header.Command != cmdSetDevice || greq.Header.Version != 3 {
			t.Errorf("header: command %d version %d", greq.Header.Command, greq.Header.Version)
		}
		got = greq.Data
	})
	if err := c.SetDevice("mgawg51842", priv, 51842, o, []awgcfg.Peer{peer}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("SetDevice bytes differ\n got %x\nwant %x", got, want)
	}
}

func TestSetDevice30RefusesThirtyOneKeys(t *testing.T) {
	c := client(t, Info{ID: famID, GenlVersion: 3, MaxAttr: 32}, func(genetlink.Message) { t.Error("must not be sent") })
	o := awgcfg.Obfuscation{RandomTrailers: true}
	if err := c.SetDevice("mgawg1", key(1), 1, o, nil); err == nil {
		t.Fatal("a 3.0 module cannot take RandomTrailers")
	}
	// without the 3.1 keys the 3.0 module gets no attribute 33/34
	var got []byte
	c = client(t, Info{ID: famID, GenlVersion: 3, MaxAttr: 32}, func(g genetlink.Message) { got = g.Data })
	if err := c.SetDevice("mgawg1", key(1), 1, awgcfg.Obfuscation{}, nil); err != nil {
		t.Fatal(err)
	}
	ad, _ := netlink.NewAttributeDecoder(got)
	for ad.Next() {
		if ad.Type() == aRandomTrailers || ad.Type() == aDisableCookies {
			t.Errorf("attribute %d sent to a 3.0 module", ad.Type())
		}
	}
}

func TestSetPeersBytesAndBatchLimit(t *testing.T) {
	a, b := key(0xaa), key(0xbb)
	want := cat(
		attr(2, str("mgawg1")), attr(5, n32(1)),
		nest(8,
			nest(0, attr(1, a[:]), attr(3, n32(1))), // remove
			nest(0, attr(1, b[:]), attr(3, n32(0)), attr(5, n32(35<<16|25))),
		),
	)
	var got []byte
	c := client(t, Info{ID: famID, GenlVersion: 3, MaxAttr: 34}, func(g genetlink.Message) { got = g.Data })
	err := c.SetPeers("mgawg1", true, []awgcfg.Peer{{PublicKey: a, Remove: true}, {PublicKey: b, Keepalive: r(25, 35)}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("SetPeers bytes differ\n got %x\nwant %x", got, want)
	}
	if err := c.SetPeers("mgawg1", false, make([]awgcfg.Peer, MaxPeersPerMessage+1)); err == nil {
		t.Fatal("more peers than one message holds must be refused")
	}
}

func TestStatsDecode(t *testing.T) {
	pub := key(0x55)
	ts := append(ne.AppendUint64(nil, uint64(1_700_000_000)), ne.AppendUint64(nil, 5)...)
	sa4 := make([]byte, 16)
	ne.PutUint16(sa4[0:], 2)
	binary.BigEndian.PutUint16(sa4[2:], 40000)
	copy(sa4[4:], []byte{203, 0, 113, 7})
	peer := nest(0,
		attr(1, pub[:]), attr(4, sa4), attr(6, ts), attr(7, n64(1234)), attr(8, n64(5678)),
		nest(9, nest(0, attr(1, n16(2)), attr(2, []byte{10, 66, 4, 5}), attr(3, n8(32)))),
	)
	never := nest(0, attr(1, bytes.Repeat([]byte{0x66}, 32)), attr(6, make([]byte, 16)), attr(7, n64(0)), attr(8, n64(0)))
	// the dump also carries the private key: it must be ignored
	dump := cat(attr(2, str("mgawg1")), attr(3, bytes.Repeat([]byte{0x99}, 32)), nest(8, peer, never))

	c := &Client{Info: Info{ID: famID, GenlVersion: 3, MaxAttr: 34}}
	c.c = genltest.Dial(func(greq genetlink.Message, nreq netlink.Message) ([]genetlink.Message, error) {
		if greq.Header.Command != cmdGetDevice || nreq.Header.Flags&netlink.Dump == 0 {
			t.Errorf("stats must be a GET_DEVICE dump, got cmd %d flags %v", greq.Header.Command, nreq.Header.Flags)
		}
		return []genetlink.Message{{Data: dump}}, nil
	})
	st, err := c.Stats("mgawg1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 2 {
		t.Fatalf("peers: %d", len(st))
	}
	p := st[0]
	if p.PublicKey != pub || p.RxBytes != 1234 || p.TxBytes != 5678 || !p.LastHS.Equal(time.Unix(1_700_000_000, 5)) ||
		p.Endpoint != netip.MustParseAddrPort("203.0.113.7:40000") ||
		len(p.AllowedIPs) != 1 || p.AllowedIPs[0] != netip.MustParsePrefix("10.66.4.5/32") {
		t.Errorf("peer decoded as %+v", p)
	}
	if !st[1].LastHS.IsZero() || st[1].Endpoint.IsValid() {
		t.Errorf("a peer that never shook hands: %+v", st[1])
	}
}

func TestEndpointIPv6AndUnknownPeer(t *testing.T) {
	sa6 := make([]byte, 28)
	ne.PutUint16(sa6[0:], 10)
	binary.BigEndian.PutUint16(sa6[2:], 443)
	copy(sa6[8:], netip.MustParseAddr("2001:db8::7").AsSlice())
	if got := decodeEndpoint(sa6); got != netip.MustParseAddrPort("[2001:db8::7]:443") {
		t.Errorf("v6 endpoint: %v", got)
	}
	if decodeEndpoint([]byte{1, 2, 3}).IsValid() {
		t.Error("garbage endpoint must decode to nothing")
	}
	msg := genetlink.Message{Header: genetlink.Header{Command: cmdUnknownPeer}, Data: cat(attr(2, str("mgawg7")))}
	if n, ok := decodeUnknownPeer(msg); !ok || n != "mgawg7" {
		t.Errorf("unknown peer event: %q %v", n, ok)
	}
	msg.Header.Command = cmdSetDevice
	if _, ok := decodeUnknownPeer(msg); ok {
		t.Error("only WG_CMD_UNKNOWN_PEER is an event")
	}
}
