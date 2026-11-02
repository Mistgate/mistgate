package mtproto

import (
	"bufio"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"

	"golang.org/x/crypto/cryptobyte"
)

// Fake TLS as Telegram clients speak it (tdlib's TlsInit, MTProxy's ext server). The client's hello is a real-looking
// TLS 1.3 ClientHello whose random field is HMAC-SHA256(key, record with the random zeroed), the last four bytes XORed
// with the client's unix time (little-endian). The server answers ServerHello, ChangeCipherSpec and one application
// data record, with HMAC-SHA256(key, client random || answer with the random zeroed) in the ServerHello random. Then
// the client sends ChangeCipherSpec and both sides carry obfuscated2 in application data records.
const (
	recordHeaderLen = 5
	recordHandshake = 0x16
	recordCCS       = 0x14
	recordAppData   = 0x17

	// maxHelloLen is the largest hello record read, header included. Larger ones go to the fallback after 5 bytes,
	// without an allocation. It is MTProxy's limit, so every client that works with the official proxy fits; tdlib's
	// hello with an X25519MLKEM768 key share is about 1.8 KB.
	maxHelloLen = 4096
	randomOff   = 11 // the random field of a hello record: header 5, handshake header 4, version 2

	// maxRecordLen is the largest record payload read after the handshake (TLS 1.3 ciphertext limit); maxWriteLen is
	// the largest one written (TLS plaintext limit).
	maxRecordLen = 1<<14 + 256
	maxWriteLen  = 1 << 14

	// The application data record of the answer stands in for the encrypted certificate flight. Its length is random
	// in this range, the default of both MTProxy (2500 + 0..1119) and mtg (2500 + 0..2199).
	noiseMin  = 2500
	noiseSpan = 2200
)

var errBadRecord = errors.New("mtproto: unexpected TLS record")

type clientHello struct {
	random    [32]byte
	sessionID []byte
	suite     uint16
	sni       string
}

// parseClientHello reads what the server needs from a hello record: the random, the session id, the first TLS 1.3
// cipher suite offered (0 if none) and the SNI. ok is false for anything that is not one well-formed ClientHello filling
// the record. What a Telegram client sends on top of that is checked by telegram.
func parseClientHello(rec []byte) (h clientHello, ok bool) {
	s := cryptobyte.String(rec[recordHeaderLen:])
	var typ uint8
	var body, sid, suites, comp, exts cryptobyte.String
	var version uint16
	var random []byte
	if !s.ReadUint8(&typ) || typ != 1 || !s.ReadUint24LengthPrefixed(&body) || !s.Empty() ||
		!body.ReadUint16(&version) || !body.ReadBytes(&random, 32) ||
		!body.ReadUint8LengthPrefixed(&sid) || len(sid) > 32 ||
		!body.ReadUint16LengthPrefixed(&suites) || !body.ReadUint8LengthPrefixed(&comp) ||
		!body.ReadUint16LengthPrefixed(&exts) || !body.Empty() {
		return h, false
	}
	copy(h.random[:], random)
	h.sessionID = sid
	for !suites.Empty() {
		var c uint16
		if !suites.ReadUint16(&c) {
			return h, false
		}
		if h.suite == 0 && c >= 0x1301 && c <= 0x1303 {
			h.suite = c
		}
	}
	for !exts.Empty() {
		var typ uint16
		var data cryptobyte.String
		if !exts.ReadUint16(&typ) || !exts.ReadUint16LengthPrefixed(&data) {
			return h, false
		}
		if typ != 0 { // server_name
			continue
		}
		var list, name cryptobyte.String
		var nameType uint8
		if !data.ReadUint16LengthPrefixed(&list) || !data.Empty() ||
			!list.ReadUint8(&nameType) || nameType != 0 || !list.ReadUint16LengthPrefixed(&name) {
			return h, false
		}
		h.sni = string(name)
	}
	return h, true
}

// telegram reports a hello a Telegram client could have sent: a 32-byte session id (echoed back), a TLS 1.3 suite, an SNI.
func (h *clientHello) telegram() bool { return len(h.sessionID) == 32 && h.suite != 0 && h.sni != "" }

// HelloSNI is the server name in a TLS ClientHello record at the start of b, "" when b does not start with a whole one.
// A fallback that forwards by name reads it from the bytes Serve hands over.
func HelloSNI(b []byte) string {
	if len(b) < recordHeaderLen || b[0] != recordHandshake {
		return ""
	}
	n := recordHeaderLen + int(binary.BigEndian.Uint16(b[3:recordHeaderLen]))
	if len(b) < n {
		return ""
	}
	h, ok := parseClientHello(b[:n])
	if !ok {
		return ""
	}
	return h.sni
}

// helloDigest is the HMAC a client with key puts into the random of rec. rec must have its random zeroed.
func helloDigest(rec []byte, key *[KeyLen]byte) (sum [32]byte) {
	mac := hmac.New(sha256.New, key[:])
	mac.Write(rec)
	mac.Sum(sum[:0])
	return sum
}

// matchDigest compares a computed digest with the hello's random: the first 28 bytes must be equal, the last four are
// the client's unix time.
func matchDigest(sum, random *[32]byte) (unix uint32, ok bool) {
	if subtle.ConstantTimeCompare(sum[:28], random[:28]) != 1 {
		return 0, false
	}
	var t [4]byte
	subtle.XORBytes(t[:], sum[28:], random[28:])
	return binary.LittleEndian.Uint32(t[:]), true
}

// serverHello is the server's answer to an authenticated hello: a TLS 1.3 ServerHello (supported_versions, then an
// X25519 key_share, the order Go's own TLS server writes), ChangeCipherSpec, one application data record of random
// bytes, and the HMAC in the ServerHello random.
func serverHello(random io.Reader, key *[KeyLen]byte, h *clientHello) ([]byte, error) {
	priv, err := ecdh.X25519().GenerateKey(random)
	if err != nil {
		return nil, err
	}
	var n [2]byte
	if _, err := io.ReadFull(random, n[:]); err != nil {
		return nil, err
	}
	noise := make([]byte, noiseMin+int(binary.BigEndian.Uint16(n[:]))%noiseSpan)
	if _, err := io.ReadFull(random, noise); err != nil {
		return nil, err
	}
	b := cryptobyte.NewBuilder(make([]byte, 0, 140+len(noise)))
	b.AddUint8(recordHandshake)
	b.AddUint16(0x0303)
	b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint8(2) // server_hello
		b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
			b.AddUint16(0x0303)
			b.AddBytes(make([]byte, 32)) // the random, set below
			b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(h.sessionID) })
			b.AddUint16(h.suite)
			b.AddUint8(0) // no compression
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint16(0x002b) // supported_versions: TLS 1.3
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint16(0x0304) })
				b.AddUint16(0x0033) // key_share: x25519
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
					b.AddUint16(0x001d)
					b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(priv.PublicKey().Bytes()) })
				})
			})
		})
	})
	b.AddBytes([]byte{recordCCS, 3, 3, 0, 1, 1})
	b.AddUint8(recordAppData)
	b.AddUint16(0x0303)
	b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(noise) })
	out, err := b.Bytes()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key[:])
	mac.Write(h.random[:])
	mac.Write(out)
	copy(out[randomOff:], mac.Sum(nil))
	return out, nil
}

// tlsConn carries a byte stream in TLS application data records, as a Fake TLS client sends and expects it. Reads
// accept one ChangeCipherSpec before the first data record, as the client sends it; anything else is an error. A
// clean EOF between records is io.EOF, so the relay can tell a half-close from a broken stream.
type tlsConn struct {
	raw  net.Conn
	r    *bufio.Reader
	left int  // payload bytes left in the current record
	data bool // a data record has been read: no ChangeCipherSpec after it
	wbuf []byte
}

func newTLSConn(raw net.Conn) *tlsConn {
	return &tlsConn{raw: raw, r: bufio.NewReaderSize(raw, 32<<10)}
}

func (c *tlsConn) Read(p []byte) (int, error) {
	for c.left == 0 {
		var hdr [recordHeaderLen]byte
		if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
			return 0, err
		}
		n := int(binary.BigEndian.Uint16(hdr[3:]))
		if hdr[1] != 3 || hdr[2] != 3 || n > maxRecordLen {
			return 0, errBadRecord
		}
		switch hdr[0] {
		case recordCCS:
			var v [1]byte
			if c.data || n != 1 {
				return 0, errBadRecord
			}
			if _, err := io.ReadFull(c.r, v[:]); err != nil || v[0] != 1 {
				return 0, errBadRecord
			}
		case recordAppData:
			c.data, c.left = true, n
		default:
			return 0, errBadRecord
		}
	}
	if len(p) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= n
	if err == io.EOF && c.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (c *tlsConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), maxWriteLen)
		c.wbuf = append(append(c.wbuf[:0], recordAppData, 3, 3, byte(n>>8), byte(n)), p[:n]...)
		if _, err := c.raw.Write(c.wbuf); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}
