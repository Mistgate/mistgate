package mimicry

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/rand/v2"
)

// A real client Initial (RFC 9000 17.2.2, RFC 9001 5): the keys come from the destination connection id that
// sits in the packet in the clear, so every DPI box can derive them, decrypt the payload and read the SNI. A
// packet of random bytes behind a QUIC header fails exactly that check, which is why the presets build the whole
// datagram. The AEAD tag covers header and payload, so nothing in it can be a per-send tag: the result is one
// <b 0x...> literal and a profile sends the same datagram at every handshake, like a client retransmitting.

const (
	quicDatagramSize = 1200 // RFC 9000 14.1: a client Initial datagram is at least this, and the chain limit is the same
	quicTagLen       = 16   // AES-128-GCM
)

// quicV1Salt is the initial salt of RFC 9001 5.2.
var quicV1Salt = []byte{0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a}

// quicKeys are the packet protection keys of one direction of the Initial space.
type quicKeys struct{ key, iv, hp []byte }

// initialKeys derives the Initial keys for a destination connection id; side is "client" or "server".
func initialKeys(dcid []byte, side string) (quicKeys, error) {
	prk, err := hkdf.Extract(sha256.New, dcid, quicV1Salt)
	if err != nil {
		return quicKeys{}, err
	}
	var l hkdfLabeler
	secret := l.expand(prk, side+" in", 32)
	k := quicKeys{key: l.expand(secret, "quic key", 16), iv: l.expand(secret, "quic iv", 12), hp: l.expand(secret, "quic hp", 16)}
	return k, l.err
}

// hkdfLabeler is HKDF-Expand-Label of RFC 8446 7.1 with an empty context, keeping the first error.
type hkdfLabeler struct{ err error }

func (l *hkdfLabeler) expand(secret []byte, label string, n int) []byte {
	if l.err != nil {
		return nil
	}
	info := binary.BigEndian.AppendUint16(nil, uint16(n))
	info = append(info, byte(len("tls13 ")+len(label)))
	info = append(info, "tls13 "...)
	info = append(info, label...)
	info = append(info, 0) // context length
	out, err := hkdf.Expand(sha256.New, secret, string(info), n)
	l.err = err
	return out
}

// appendVarint is the QUIC variable-length integer of RFC 9000 16.
func appendVarint(b []byte, v uint64) []byte {
	switch {
	case v < 1<<6:
		return append(b, byte(v))
	case v < 1<<14:
		return binary.BigEndian.AppendUint16(b, 0x4000|uint16(v))
	case v < 1<<30:
		return binary.BigEndian.AppendUint32(b, 0x80000000|uint32(v))
	}
	return binary.BigEndian.AppendUint64(b, 0xc000000000000000|v)
}

func varintLen(v uint64) int { return len(appendVarint(nil, v)) }

// protectHeader applies header protection (RFC 9001 5.4) to a long header whose packet number starts at pnOff.
// The sample is the 16 ciphertext bytes that start four bytes after the packet number field begins.
func protectHeader(header []byte, pnOff, pnLen int, hp, sample []byte) error {
	block, err := aes.NewCipher(hp)
	if err != nil {
		return err
	}
	var mask [aes.BlockSize]byte
	block.Encrypt(mask[:], sample)
	header[0] ^= mask[0] & 0x0f // long header: the four low bits only
	for i := 0; i < pnLen; i++ {
		header[pnOff+i] ^= mask[1+i]
	}
	return nil
}

// initialPacket builds a client Initial datagram of exactly size bytes: long header, empty token, the packet
// number pn in pnLen bytes, a payload of one CRYPTO frame at offset 0 carrying crypto, then PADDING frames, all
// protected with the keys of dcid.
func initialPacket(dcid, scid []byte, pn uint32, pnLen int, crypto []byte, size int) ([]byte, error) {
	keys, err := initialKeys(dcid, "client")
	if err != nil {
		return nil, err
	}
	frame := appendVarint(append(make([]byte, 0, len(crypto)+8), 0x06), 0) // CRYPTO, offset 0
	frame = appendVarint(frame, uint64(len(crypto)))
	frame = append(frame, crypto...)

	hdr := []byte{0xc0 | byte(pnLen-1)} // long header, fixed bit, type Initial
	hdr = binary.BigEndian.AppendUint32(hdr, 1)
	hdr = append(hdr, byte(len(dcid)))
	hdr = append(hdr, dcid...)
	hdr = append(hdr, byte(len(scid)))
	hdr = append(hdr, scid...)
	hdr = append(hdr, 0) // token length

	// Length covers packet number, payload and tag; its own size depends on it, so try the sizes in turn.
	var length uint64
	for _, vl := range []int{1, 2, 4} {
		if l := size - len(hdr) - vl; l > 0 && varintLen(uint64(l)) == vl {
			length = uint64(l)
			break
		}
	}
	pad := int(length) - pnLen - quicTagLen - len(frame)
	if length == 0 || pad < 0 {
		return nil, errors.New("QUIC Initial: the ClientHello does not fit the datagram")
	}
	payload := append(frame, make([]byte, pad)...) // PADDING frames are zero bytes
	hdr = appendVarint(hdr, length)
	pnOff := len(hdr)
	hdr = append(hdr, binary.BigEndian.AppendUint32(nil, pn)[4-pnLen:]...)

	block, err := aes.NewCipher(keys.key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := append([]byte(nil), keys.iv...)
	for i := 0; i < 4; i++ { // the packet number, right-aligned, xor into the iv
		nonce[len(nonce)-4+i] ^= byte(pn >> (24 - 8*i))
	}
	sealed := aead.Seal(nil, nonce, payload, hdr)
	if err := protectHeader(hdr, pnOff, pnLen, keys.hp, sealed[4-pnLen:4-pnLen+16]); err != nil {
		return nil, err
	}
	return append(hdr, sealed...), nil
}

// quicProfile is what differs between the browser-like and the curl-like client.
type quicProfile struct {
	dcidLen, scidLen int
	pnLen            int
	hello            func(t *tlsBuilder, rng *rand.Rand, host string, scid []byte) []byte
}

var (
	// A browser client: 8-byte destination id, zero-length source id, four-byte packet numbers.
	chromeQUIC = quicProfile{dcidLen: 8, scidLen: 0, pnLen: 4, hello: (*tlsBuilder).chromeHello}
	// curl over ngtcp2: long ids on both sides, a one-byte packet number. The id lengths are as the task states them
	// (18); they were not measured against a capture.
	curlQUIC = quicProfile{dcidLen: 18, scidLen: 18, pnLen: 1, hello: (*tlsBuilder).curlHello}
)

// quicInitial returns the datagram of a client that opens a connection to host. Every random value comes from
// rng, so a seed gives the same packet. The first Initial of a connection is packet number 0.
func quicInitial(rng *rand.Rand, p quicProfile, host string) (chain, error) {
	dcid := randBytes(rng, p.dcidLen)
	scid := randBytes(rng, p.scidLen)
	var t tlsBuilder
	hello := p.hello(&t, rng, host, scid)
	if t.err != nil {
		return nil, t.err
	}
	pkt, err := initialPacket(dcid, scid, 0, p.pnLen, hello, quicDatagramSize)
	if err != nil {
		return nil, err
	}
	var c chain
	c.lit(pkt...)
	return c, nil
}
