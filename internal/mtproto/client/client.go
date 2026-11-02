package client

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"golang.org/x/crypto/cryptobyte"
)

const keyLen = 16

// TagAbridged is the obfuscated2 transport tag for MTProto's abridged framing.
const TagAbridged uint32 = 0xefefefef

// ErrNotProxy means a connection did not return the keyed Fake TLS ServerHello a client expects.
var ErrNotProxy = errors.New("mtproto: the answer is not a Fake TLS ServerHello for this key")

// ErrFakeTLSAuth is the Fake TLS-shaped response a site or decoy sends after rejecting a domain or key.
var ErrFakeTLSAuth = fmt.Errorf("%w: authentication failed", ErrNotProxy)

// ErrFakeTLSProtocol means the response did not have the framing of a Fake TLS ServerHello.
var ErrFakeTLSProtocol = fmt.Errorf("%w: invalid Fake TLS response", ErrNotProxy)

// BuildClientHello builds the Telegram Fake TLS ClientHello for one domain and Unix timestamp.
func BuildClientHello(key [keyLen]byte, domain string, unix uint32) ([]byte, error) {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if !validDomain(domain) {
		return nil, errors.New("mtproto: Fake TLS domain is not a host name")
	}
	random := func(n int) ([]byte, error) {
		b := make([]byte, n)
		if _, err := io.ReadFull(rand.Reader, b); err != nil {
			return nil, err
		}
		return b, nil
	}
	build := func(pad int) ([]byte, error) {
		sessionID, err := random(32)
		if err != nil {
			return nil, err
		}
		keyShare, err := random(32)
		if err != nil {
			return nil, err
		}
		b := cryptobyte.NewBuilder(nil)
		b.AddUint8(0x16)
		b.AddUint16(0x0301)
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			b.AddUint8(1)
			b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint16(0x0303)
				b.AddBytes(make([]byte, 32))
				b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(sessionID) })
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
					for _, c := range []uint16{0x7a7a, 0x1301, 0x1302, 0x1303, 0xc02b, 0xc02f} {
						b.AddUint16(c)
					}
				})
				b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint8(0) })
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
					b.AddUint16(0x0000) // server_name
					b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
						b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
							b.AddUint8(0)
							b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes([]byte(domain)) })
						})
					})
					b.AddUint16(0x000a) // supported_groups
					b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
						b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint16(0x001d) })
					})
					b.AddUint16(0x0033) // key_share
					b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
						b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
							b.AddUint16(0x001d)
							b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(keyShare) })
						})
					})
					b.AddUint16(0x002b) // supported_versions
					b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
						b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint16(0x0304) })
					})
					if pad >= 0 {
						b.AddUint16(0x0015) // padding
						b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(make([]byte, pad)) })
					}
				})
			})
		})
		return b.Bytes()
	}

	rec, err := build(-1)
	if err != nil {
		return nil, err
	}
	if len(rec)+4 <= 517 {
		rec, err = build(517 - len(rec) - 4)
		if err != nil {
			return nil, err
		}
	}
	mac := hmac.New(sha256.New, key[:])
	mac.Write(rec)
	sum := mac.Sum(nil)
	binary.LittleEndian.PutUint32(sum[28:], binary.LittleEndian.Uint32(sum[28:])^unix)
	copy(rec[11:43], sum)
	return rec, nil
}

// Client is a proxy connection after the Fake TLS handshake.
type Client struct {
	Conn net.Conn
	// HeaderKeys are bytes 8..56 of the obfuscated2 header sent: the keys and IVs of this leg.
	HeaderKeys []byte

	key     [keyLen]byte
	r       *bufio.Reader
	enc     cipherStream
	dec     cipherStream
	sentCCS bool
	pending []byte
}

// cipherStream is the subset of cipher.Stream used here, kept private so callers cannot change key state.
type cipherStream interface {
	XORKeyStream(dst, src []byte)
}

// StartClient sends the Fake TLS hello and validates the keyed ServerHello answer.
func StartClient(conn net.Conn, key [keyLen]byte, hello []byte) (*Client, error) {
	if err := writeFull(conn, hello); err != nil {
		return nil, err
	}
	r := bufio.NewReader(conn)
	serverHello, err := readFakeTLSRecord(r, 0x16)
	if err != nil {
		return nil, err
	}
	if len(serverHello) < 43 || serverHello[5] != 2 ||
		binary.BigEndian.Uint16(serverHello[9:11]) != 0x0303 ||
		int(serverHello[6])<<16|int(serverHello[7])<<8|int(serverHello[8]) != len(serverHello)-9 {
		return nil, ErrFakeTLSProtocol
	}
	ccs, err := readFakeTLSRecord(r, 0x14)
	if err != nil {
		return nil, err
	}
	if len(ccs) != 6 || ccs[5] != 1 {
		return nil, ErrFakeTLSProtocol
	}
	appData, err := readFakeTLSRecord(r, 0x17)
	if err != nil {
		return nil, err
	}
	resp := append(append(serverHello, ccs...), appData...)
	if len(resp) < 43 {
		return nil, ErrFakeTLSProtocol
	}
	got := bytes.Clone(resp[11:43])
	clear(resp[11:43])
	mac := hmac.New(sha256.New, key[:])
	mac.Write(hello[11:43])
	mac.Write(resp)
	if !hmac.Equal(mac.Sum(nil), got) {
		return nil, ErrFakeTLSAuth
	}
	return &Client{Conn: conn, key: key, r: r}, nil
}

func readFakeTLSRecord(r *bufio.Reader, typ byte) ([]byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, fmt.Errorf("read Fake TLS response: %w", err)
	}
	if head[0] != typ || head[1] != 3 || head[2] != 3 {
		return nil, ErrFakeTLSProtocol
	}
	n := int(binary.BigEndian.Uint16(head[3:]))
	if n > 16<<10 {
		return nil, ErrFakeTLSProtocol
	}
	out := make([]byte, len(head)+n)
	copy(out, head[:])
	if _, err := io.ReadFull(r, out[len(head):]); err != nil {
		return nil, fmt.Errorf("read Fake TLS response: %w", err)
	}
	return out, nil
}

// SendHeader sends the obfuscated2 abridged/intermediate/padded header for one DC.
func (c *Client) SendHeader(tag uint32, dc int16) error {
	if c == nil || c.Conn == nil || c.enc != nil {
		return errors.New("mtproto: obfuscated2 header already sent or client is closed")
	}
	var h [64]byte
	if _, err := io.ReadFull(rand.Reader, h[:]); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(h[56:], tag)
	binary.LittleEndian.PutUint16(h[60:], uint16(dc))
	var rev [64]byte
	for i := range h {
		rev[i] = h[63-i]
	}
	c.enc = clientCTR(proxyKey(h[8:40], &c.key), h[40:56])
	c.dec = clientCTR(proxyKey(rev[8:40], &c.key), rev[40:56])
	var sealed [64]byte
	c.enc.XORKeyStream(sealed[:], h[:])
	copy(h[56:], sealed[56:])
	c.HeaderKeys = bytes.Clone(h[8:56])
	return c.writeRecords(h[:])
}

// Write sends encrypted bytes in Fake TLS application data records.
func (c *Client) Write(p []byte) (int, error) {
	if c == nil || c.Conn == nil || c.enc == nil {
		return 0, errors.New("mtproto: obfuscated2 header was not sent")
	}
	buf := bytes.Clone(p)
	c.enc.XORKeyStream(buf, buf)
	if err := c.writeRecords(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *Client) writeRecords(p []byte) error {
	var out []byte
	if !c.sentCCS {
		out = append(out, 0x14, 3, 3, 0, 1, 1)
		c.sentCCS = true
	}
	for len(p) > 0 {
		n := min(len(p), 2878)
		out = append(out, 0x17, 3, 3, byte(n>>8), byte(n))
		out = append(out, p[:n]...)
		p = p[n:]
	}
	return writeFull(c.Conn, out)
}

// Read returns decrypted bytes from the server's application data records.
func (c *Client) Read(p []byte) (int, error) {
	if c == nil || c.Conn == nil || c.dec == nil {
		return 0, errors.New("mtproto: obfuscated2 header was not sent")
	}
	for len(c.pending) == 0 {
		var head [5]byte
		if _, err := io.ReadFull(c.r, head[:]); err != nil {
			return 0, err
		}
		if head[0] != 0x17 || head[1] != 3 || head[2] != 3 {
			return 0, fmt.Errorf("mtproto: response record type %#x", head[0])
		}
		body := make([]byte, binary.BigEndian.Uint16(head[3:]))
		if _, err := io.ReadFull(c.r, body); err != nil {
			return 0, err
		}
		c.dec.XORKeyStream(body, body)
		c.pending = body
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// CloseWrite half-closes a TCP client connection.
func (c *Client) CloseWrite() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	tcp, ok := c.Conn.(*net.TCPConn)
	if !ok {
		return errors.New("mtproto: connection does not support CloseWrite")
	}
	return tcp.CloseWrite()
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func validDomain(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func proxyKey(header []byte, key *[keyLen]byte) []byte {
	sum := sha256.New()
	sum.Write(header)
	sum.Write(key[:])
	return sum.Sum(nil)
}

func clientCTR(key, iv []byte) cipher.Stream {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	return cipher.NewCTR(block, iv)
}
