package vless

import (
	"encoding/binary"
	"sync"
)

// Which key exchange a REALITY client's ClientHello offers (agent.proto LinkCounters hello_pq, hello_classic, hello_unknown;
// capability "vless-hello-kex/1"). The node counts it on the current Xray so the owner knows which devices a strict REALITY
// library would turn away: github.com/xtls/reality (4bb46b2a and later) refuses every hello that does not carry an
// X25519MLKEM768 key share before an optional X25519 share, and treats such a client as a stranger.
//
// The rule is upstream's, read share by share in the order the client sent them (reality tls.go, serverHandshake):
//   - a share of group X25519MLKEM768 whose key is 1216 bytes (1184 of ML-KEM-768 + 32 of X25519): the first one is taken,
//     a second one before any X25519 share fails the client;
//   - the first share of group X25519 with a 32-byte key ends the scan: if no ML-KEM share came before it, the client fails;
//   - every other share (GREASE, P-256, a wrong length) is skipped.
// So "pq" is: a valid ML-KEM share is met exactly once before the first valid X25519 share (or the list ends without one).
//
// The hello is read from the bytes REALITY itself reads (trackedTCPConn.Read copies them; nothing is read ahead, so the
// connection's timing and behaviour do not change), the whole first record up to hellokexCap bytes. A hello that was cut
// short, spread over several records past the key_share extension, malformed or not a ClientHello is "unknown".

type helloKex uint8

const (
	kexPending helloKex = iota // still being read
	kexPQ
	kexClassic
	kexUnknown
)

const (
	hellokexCap        = 4096 // the most of the first TLS record that is kept
	tlsRecordHeaderLen = 5

	extKeyShare       = 0x0033
	groupX25519       = 0x001d
	groupX25519MLKEM  = 0x11ec
	x25519KeyLen      = 32
	x25519MLKEMKeyLen = 1184 + 32
)

// classifyHello classifies the first TLS record of a connection (b starts with the record header; it may be cut short). It
// never reads outside b, allocates nothing, and takes any input.
func classifyHello(b []byte) helloKex {
	// record header: type 22 (handshake), version, length; then the handshake header: type 1 (ClientHello), length (3 bytes)
	if len(b) < tlsRecordHeaderLen+4 || b[0] != 0x16 || b[1] != 0x03 || b[tlsRecordHeaderLen] != 0x01 {
		return kexUnknown
	}
	end := min(len(b), tlsRecordHeaderLen+int(binary.BigEndian.Uint16(b[3:5])))
	if end < tlsRecordHeaderLen+4 {
		return kexUnknown // the record is shorter than a handshake header
	}
	p := b[tlsRecordHeaderLen+4 : end] // the ClientHello body (what the first record holds of it)
	// legacy_version(2) random(32)
	if len(p) < 34 {
		return kexUnknown
	}
	p = p[34:]
	// session id, cipher suites, compression methods
	var ok bool
	if p, ok = skipVec8(p); !ok {
		return kexUnknown
	}
	if p, ok = skipVec16(p); !ok {
		return kexUnknown
	}
	if p, ok = skipVec8(p); !ok {
		return kexUnknown
	}
	if len(p) < 2 {
		return kexUnknown
	}
	extLen := int(binary.BigEndian.Uint16(p))
	p = p[2:]
	if extLen < len(p) {
		p = p[:extLen]
	} // else: the extensions run past what we hold; the walk below ends at the cut
	for len(p) >= 4 {
		typ, n := binary.BigEndian.Uint16(p), int(binary.BigEndian.Uint16(p[2:]))
		p = p[4:]
		if n > len(p) {
			return kexUnknown // cut inside this extension
		}
		if typ == extKeyShare {
			return classifyKeyShares(p[:n])
		}
		p = p[n:]
	}
	return kexUnknown // no key_share extension in what we hold (a TLS 1.2 hello has none, but it is no REALITY client either)
}

// classifyKeyShares walks the client_shares vector of a key_share extension (RFC 8446, 4.2.8) as reality does.
func classifyKeyShares(p []byte) helloKex {
	if len(p) < 2 {
		return kexUnknown
	}
	n := int(binary.BigEndian.Uint16(p))
	p = p[2:]
	if n != len(p) {
		return kexUnknown
	}
	mlkem := false
	for len(p) > 0 {
		if len(p) < 4 {
			return kexUnknown
		}
		group, keyLen := binary.BigEndian.Uint16(p), int(binary.BigEndian.Uint16(p[2:]))
		p = p[4:]
		if keyLen > len(p) {
			return kexUnknown
		}
		p = p[keyLen:]
		switch {
		case group == groupX25519MLKEM && keyLen == x25519MLKEMKeyLen:
			if mlkem {
				return kexClassic // a second ML-KEM share before any X25519 one
			}
			mlkem = true
		case group == groupX25519 && keyLen == x25519KeyLen:
			if mlkem {
				return kexPQ
			}
			return kexClassic // X25519 before (or without) the ML-KEM share
		}
	}
	if mlkem {
		return kexPQ
	}
	return kexClassic
}

func skipVec8(p []byte) ([]byte, bool) {
	if len(p) < 1 || len(p) < 1+int(p[0]) {
		return nil, false
	}
	return p[1+int(p[0]):], true
}

func skipVec16(p []byte) ([]byte, bool) {
	if len(p) < 2 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(p))
	if len(p) < 2+n {
		return nil, false
	}
	return p[2+n:], true
}

// helloBuf holds the start of a connection while its ClientHello is read; pooled, so a connection costs no allocation.
type helloBuf struct {
	b       [hellokexCap]byte
	n       int
	counted bool // the hellos counter has seen this connection
}

var helloBufs = sync.Pool{New: func() any { return new(helloBuf) }}
