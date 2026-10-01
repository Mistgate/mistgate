package mimicry

import (
	"crypto/ecdh"
	"math/rand/v2"

	"golang.org/x/crypto/cryptobyte"
)

// TLS 1.3 ClientHello of a QUIC client (RFC 9001 4.1): the part of the Initial a DPI box decrypts to read the SNI.
// Two shapes, a Chrome-like and a curl-like one. The extension sets, their order, the signature algorithm and
// transport parameter lists follow the fingerprints of Sketchystan1/payloadGen (MIT) as carried by
// awg-multi-script and awg-manager (both MIT); they were not re-measured against a real browser here.
// What differs from those tools: nothing in a QUIC ClientHello is a frozen random (client random, key share,
// GREASE values all come from rng), the legacy session id is empty (RFC 9001 8.4 forbids the compatibility
// mode, a non-empty one is how a fake is spotted), the key share is a real X25519 public key, and a Chrome
// hello shuffles its extensions per generation as Chrome does since 110.

const (
	extServerName    = 0x0000
	extStatusRequest = 0x0005
	extGroups        = 0x000a
	extSigAlgs       = 0x000d
	extALPN          = 0x0010
	extSCT           = 0x0012
	extPadding       = 0x0015
	extCompressCert  = 0x001b
	extVersions      = 0x002b
	extPSKModes      = 0x002d
	extKeyShare      = 0x0033
	extQUICParams    = 0x0039
	extECH           = 0xfe0d

	groupX25519 = 0x001d
	versionTLS3 = 0x0304
)

// tlsBuilder collects the first error of the cryptobyte builders (a length that does not fit; with these sizes
// it cannot happen, but an invalid chain must never come out silently).
type tlsBuilder struct{ err error }

func (t *tlsBuilder) bytes(b *cryptobyte.Builder) []byte {
	out, err := b.Bytes()
	if err != nil && t.err == nil {
		t.err = err
	}
	return out
}

// ext encodes one extension.
func (t *tlsBuilder) ext(typ uint16, body func(*cryptobyte.Builder)) []byte {
	var b cryptobyte.Builder
	b.AddUint16(typ)
	b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
		if body != nil {
			body(b)
		}
	})
	return t.bytes(&b)
}

func (t *tlsBuilder) serverName(host string) []byte {
	return t.ext(extServerName, func(b *cryptobyte.Builder) {
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			b.AddUint8(0) // host_name
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes([]byte(host)) })
		})
	})
}

// u16List is the shape of supported_groups and signature_algorithms: a 16-bit list behind a 16-bit length.
func (t *tlsBuilder) u16List(typ uint16, vals []uint16) []byte {
	return t.ext(typ, func(b *cryptobyte.Builder) {
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			for _, v := range vals {
				b.AddUint16(v)
			}
		})
	})
}

func (t *tlsBuilder) alpnH3() []byte {
	return t.ext(extALPN, func(b *cryptobyte.Builder) {
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes([]byte("h3")) })
		})
	})
}

func (t *tlsBuilder) versions(vals ...uint16) []byte {
	return t.ext(extVersions, func(b *cryptobyte.Builder) {
		b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) {
			for _, v := range vals {
				b.AddUint16(v)
			}
		})
	})
}

// keyShare carries an optional GREASE entry (group, one zero byte) and the X25519 entry.
func (t *tlsBuilder) keyShare(greaseGroup uint16, pub []byte) []byte {
	return t.ext(extKeyShare, func(b *cryptobyte.Builder) {
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			if greaseGroup != 0 {
				b.AddUint16(greaseGroup)
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint8(0) })
			}
			b.AddUint16(groupX25519)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(pub) })
		})
	})
}

func (t *tlsBuilder) statusRequest() []byte {
	return t.ext(extStatusRequest, func(b *cryptobyte.Builder) {
		b.AddUint8(1) // ocsp
		b.AddUint16(0)
		b.AddUint16(0)
	})
}

func (t *tlsBuilder) pskModes() []byte { // psk_dhe_ke
	return t.ext(extPSKModes, func(b *cryptobyte.Builder) {
		b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint8(1) })
	})
}

func (t *tlsBuilder) compressBrotli() []byte {
	return t.ext(extCompressCert, func(b *cryptobyte.Builder) {
		b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint16(2) })
	})
}

// quicParam is one transport parameter (RFC 9000 18.2): an integer, or raw bytes when raw is set (an empty
// non-nil raw is a flag parameter, a connection id is raw).
type quicParam struct {
	id  uint64
	val uint64
	raw []byte
}

// quicParams encodes quic_transport_parameters in the given order.
func (t *tlsBuilder) quicParams(params []quicParam) []byte {
	var body []byte
	for _, p := range params {
		val := p.raw
		if val == nil {
			val = appendVarint(nil, p.val)
		}
		body = appendVarint(body, p.id)
		body = appendVarint(body, uint64(len(val)))
		body = append(body, val...)
	}
	return t.ext(extQUICParams, func(b *cryptobyte.Builder) { b.AddBytes(body) })
}

// echGrease is the ECH extension of a client that has no ECHConfig and sends a plausible one anyway
// (draft-ietf-tls-esni GREASE ECH): outer type, HKDF-SHA256, AES-128-GCM, random config id, a 32-byte "enc" (a
// real X25519 public key) and a payload of the size of a padded inner hello (144-240 bytes, as BoringSSL draws).
func (t *tlsBuilder) echGrease(rng *rand.Rand) []byte {
	enc := t.x25519Public(rng)
	configID := byte(rng.Uint32())
	payload := randBytes(rng, 144+32*rng.IntN(4))
	return t.ext(extECH, func(b *cryptobyte.Builder) {
		b.AddUint8(0) // outer
		b.AddUint16(0x0001)
		b.AddUint16(0x0001)
		b.AddUint8(configID)
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(enc) })
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(payload) })
	})
}

// x25519Public is a real public key of a private key drawn from rng. Random bytes would do for a DPI box that
// does not look, but real keys never have the top bit of the last byte set and these cost nothing.
func (t *tlsBuilder) x25519Public(rng *rand.Rand) []byte {
	k, err := ecdh.X25519().NewPrivateKey(randBytes(rng, 32))
	if err != nil {
		if t.err == nil {
			t.err = err
		}
		return make([]byte, 32)
	}
	return k.PublicKey().Bytes()
}

// hello assembles the handshake message: type, 24-bit length, version, random, empty session id, suites,
// null compression, extensions.
func (t *tlsBuilder) hello(random []byte, suites []uint16, exts [][]byte) []byte {
	var b cryptobyte.Builder
	b.AddUint8(1) // client_hello
	b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint16(0x0303)
		b.AddBytes(random)
		b.AddUint8LengthPrefixed(func(*cryptobyte.Builder) {}) // legacy_session_id: empty, RFC 9001 8.4
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			for _, s := range suites {
				b.AddUint16(s)
			}
		})
		b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint8(0) })
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			for _, e := range exts {
				b.AddBytes(e)
			}
		})
	})
	return t.bytes(&b)
}

func randBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

// grease draws one of the sixteen GREASE values 0x0a0a, 0x1a1a ... 0xfafa (RFC 8701).
func grease(rng *rand.Rand) uint16 {
	n := uint16(rng.IntN(16))<<4 | 0x0a
	return n<<8 | n
}

// greaseExt is a GREASE extension; BoringSSL gives the first an empty body and the second a single zero byte.
func (t *tlsBuilder) greaseExt(typ uint16, body []byte) []byte {
	return t.ext(typ, func(b *cryptobyte.Builder) { b.AddBytes(body) })
}

var chromeSigAlgs = []uint16{0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501, 0x0806, 0x0601}

// chromeParams: the transport parameters of a Chrome-like client; the source connection id is empty (a browser
// client uses a zero-length one) but the parameter is still there, RFC 9000 7.3.
func chromeParams(scid []byte) []quicParam {
	return []quicParam{
		{id: 0x01, val: 30000},    // max_idle_timeout
		{id: 0x03, val: 1472},     // max_udp_payload_size
		{id: 0x04, val: 15728640}, // initial_max_data
		{id: 0x05, val: 6291456},  // initial_max_stream_data_bidi_local
		{id: 0x06, val: 6291456},  // initial_max_stream_data_bidi_remote
		{id: 0x07, val: 6291456},  // initial_max_stream_data_uni
		{id: 0x08, val: 100},      // initial_max_streams_bidi
		{id: 0x09, val: 100},      // initial_max_streams_uni
		{id: 0x0a, val: 3},        // ack_delay_exponent
		{id: 0x0b, val: 25},       // max_ack_delay
		{id: 0x0c, raw: []byte{}}, // disable_active_migration
		{id: 0x0e, val: 8},        // active_connection_id_limit
		{id: 0x0f, raw: append([]byte{}, scid...)},
	}
}

// chromeHello is a Chrome-like QUIC ClientHello: GREASE in the suites, groups, versions, key share and as the
// first and the last-but-one extension; the rest of the extensions in a random order; padded to 512 bytes the
// way BoringSSL does when the hello would land in 256-511.
func (t *tlsBuilder) chromeHello(rng *rand.Rand, host string, scid []byte) []byte {
	gCipher, gGroup, gVersion, gExt1, gExt2 := grease(rng), grease(rng), grease(rng), grease(rng), grease(rng)
	if gExt2 == gExt1 {
		gExt2 ^= 0x1010
	}
	random := randBytes(rng, 32)
	exts := [][]byte{
		t.serverName(host),
		t.u16List(extGroups, []uint16{gGroup, groupX25519, 0x0017, 0x0018}),
		t.alpnH3(),
		t.statusRequest(),
		t.u16List(extSigAlgs, chromeSigAlgs),
		t.ext(extSCT, nil),
		t.versions(gVersion, versionTLS3),
		t.keyShare(gGroup, t.x25519Public(rng)),
		t.pskModes(),
		t.quicParams(chromeParams(scid)),
		t.compressBrotli(),
	}
	rng.Shuffle(len(exts), func(i, j int) { exts[i], exts[j] = exts[j], exts[i] })
	all := append([][]byte{t.greaseExt(gExt1, nil)}, exts...)
	all = append(all, t.greaseExt(gExt2, []byte{0}))

	suites := []uint16{gCipher, 0x1301, 0x1302, 0x1303}
	hello := t.hello(random, suites, all)
	if n := len(hello); n > 0xff && n < 0x200 {
		pad := 0x200 - n - 4 // the extension header takes four bytes; at least one byte of data
		if pad < 1 {
			pad = 1
		}
		all = append(all, t.ext(extPadding, func(b *cryptobyte.Builder) { b.AddBytes(make([]byte, pad)) }))
		hello = t.hello(random, suites, all)
	}
	return hello
}

var curlSigAlgs = []uint16{0x0403, 0x0503, 0x0603, 0x0804, 0x0805, 0x0806}

// curlParams: ngtcp2-style parameters in the order of the curl capture the fingerprint was taken from.
func curlParams(scid []byte) []quicParam {
	return []quicParam{
		{id: 0x03, val: 1472},
		{id: 0x07, val: 5242880},
		{id: 0x05, val: 5242880},
		{id: 0x09, val: 100},
		{id: 0x01, val: 30000},
		{id: 0x08, val: 100},
		{id: 0x0f, raw: append([]byte{}, scid...)},
		{id: 0x0e, val: 2},
		{id: 0x06, val: 5242880},
		{id: 0x04, val: 10485760},
	}
}

// curlHello is a curl/ngtcp2-like hello: one suite, no GREASE, a fixed extension order, an ECH GREASE extension
// at the end and no TLS padding (the QUIC PADDING frames fill the datagram).
func (t *tlsBuilder) curlHello(rng *rand.Rand, host string, scid []byte) []byte {
	random := randBytes(rng, 32)
	exts := [][]byte{
		t.serverName(host),
		t.versions(versionTLS3),
		t.u16List(extGroups, []uint16{groupX25519, 0x0017, 0x0018}),
		t.u16List(extSigAlgs, curlSigAlgs),
		t.alpnH3(),
		t.keyShare(0, t.x25519Public(rng)),
		t.pskModes(),
		t.quicParams(curlParams(scid)),
		t.compressBrotli(),
		t.echGrease(rng),
	}
	return t.hello(random, []uint16{0x1301}, exts)
}
