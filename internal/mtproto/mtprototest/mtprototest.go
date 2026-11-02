// Package mtprototest contains the fake Telegram DC used by proxy tests. Its small client wrappers use the production
// Fake TLS and obfuscated2 client so tests can run it against the server.
package mtprototest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"

	mtclient "github.com/mistgate/mistgate/internal/mtproto/client"
)

// Transport tags (header bytes 56..60).
const (
	TagAbridged     uint32 = mtclient.TagAbridged
	TagIntermediate uint32 = 0xeeeeeeee
	TagPadded       uint32 = 0xdddddddd
)

// Hello is a ClientHello record shaped like tdlib's: TLS 1.3 suites after a GREASE value, SNI, an X25519 key share,
// supported_versions, padding to 517 bytes, and HMAC-SHA256(key, record with a zero random) in the random field with
// the last four bytes XORed with unix (little-endian).
func Hello(key [16]byte, domain string, unix uint32) []byte {
	rec, err := mtclient.BuildClientHello(key, domain, unix)
	if err != nil {
		panic(err)
	}
	return rec
}

// Client and Start keep the proxy tests on the production client implementation.
type Client = mtclient.Client

var ErrNotProxy = mtclient.ErrNotProxy

func Start(conn net.Conn, key [16]byte, hello []byte) (*Client, error) {
	return mtclient.StartClient(conn, key, hello)
}

// DC is a fake Telegram DC: it takes plain obfuscated2 (no proxy key) and hands each connection to Serve.
type DC struct {
	Addr     string
	Accepted atomic.Int32
	Headers  chan [64]byte // every header received, as sent
}

// DCConn is one connection to the fake DC, decrypted.
type DCConn struct {
	*net.TCPConn
	Tag      uint32
	DC       int16
	dec, enc cipher.Stream
}

func (c *DCConn) Read(p []byte) (int, error) {
	n, err := c.TCPConn.Read(p)
	c.dec.XORKeyStream(p[:n], p[:n])
	return n, err
}

func (c *DCConn) Write(p []byte) (int, error) {
	buf := bytes.Clone(p)
	c.enc.XORKeyStream(buf, buf)
	return c.TCPConn.Write(buf)
}

// NewDC listens on loopback until the test ends.
func NewDC(t testing.TB, serve func(*DCConn)) *DC {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	dc := &DC{Addr: ln.Addr().String(), Headers: make(chan [64]byte, 64)}
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			dc.Accepted.Add(1)
			go func() {
				defer raw.Close()
				var h [64]byte
				if _, err := io.ReadFull(raw, h[:]); err != nil {
					return
				}
				select {
				case dc.Headers <- h:
				default:
				}
				var rev [64]byte
				for i := range h {
					rev[i] = h[63-i]
				}
				c := &DCConn{TCPConn: raw.(*net.TCPConn), dec: aesCTR(h[8:40], h[40:56]), enc: aesCTR(rev[8:40], rev[40:56])}
				var plain [64]byte
				c.dec.XORKeyStream(plain[:], h[:])
				c.Tag = binary.LittleEndian.Uint32(plain[56:])
				c.DC = int16(binary.LittleEndian.Uint16(plain[60:]))
				serve(c)
			}()
		}
	}()
	return dc
}

// Echo answers every read with "dc:" and the bytes read.
func Echo(c *DCConn) {
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if _, werr := c.Write(append([]byte("dc:"), buf[:n]...)); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func aesCTR(key, iv []byte) cipher.Stream {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	return cipher.NewCTR(block, iv)
}
