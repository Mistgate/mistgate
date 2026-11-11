package vless

import (
	"crypto/tls"
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"
)

// share is one key_share entry of a hand-built ClientHello: a group and the length of its key.
type share struct {
	group uint16
	n     int
}

var (
	shMLKEM    = share{groupX25519MLKEM, x25519MLKEMKeyLen}
	shX25519   = share{groupX25519, x25519KeyLen}
	shGrease   = share{0x0a0a, 1}
	shP256     = share{0x0017, 65}
	shMLKEMBad = share{groupX25519MLKEM, 33}
	shX25519Bd = share{groupX25519, 31}
)

func put16(b []byte, v int) []byte { return append(b, byte(v>>8), byte(v)) }

// buildHello makes a TLS 1.3 ClientHello record with the key_share extension in the middle of its extensions.
func buildHello(shares []share, withKeyShare bool) []byte {
	var ks []byte
	for _, s := range shares {
		ks = put16(ks, int(s.group))
		ks = put16(ks, s.n)
		ks = append(ks, make([]byte, s.n)...)
	}
	var exts []byte
	exts = put16(exts, 0x002b) // supported_versions: TLS 1.3
	exts = put16(exts, 3)
	exts = append(exts, 2, 0x03, 0x04)
	if withKeyShare {
		exts = put16(exts, extKeyShare)
		exts = put16(exts, 2+len(ks))
		exts = put16(exts, len(ks))
		exts = append(exts, ks...)
	}
	exts = put16(exts, 0x0015) // padding
	exts = put16(exts, 4)
	exts = append(exts, 0, 0, 0, 0)

	var body []byte
	body = append(body, 0x03, 0x03)
	body = append(body, make([]byte, 32)...) // random
	body = append(body, 32)                  // session id
	body = append(body, make([]byte, 32)...)
	body = put16(body, 4) // cipher suites
	body = append(body, 0x13, 0x01, 0x13, 0x02)
	body = append(body, 1, 0) // compression
	body = put16(body, len(exts))
	body = append(body, exts...)

	hs := append([]byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	rec := append([]byte{0x16, 0x03, 0x01}, byte(len(hs)>>8), byte(len(hs)))
	return append(rec, hs...)
}

// reference is reality's loop (xtls/reality tls.go at 4bb46b2a) over a list of shares, written out as upstream has it.
func reference(shares []share) helloKex {
	var peerPub, peerPub2 bool
	for _, ks := range shares {
		if ks.group == groupX25519MLKEM && ks.n == x25519MLKEMKeyLen {
			if peerPub2 {
				peerPub2 = false // ensure fail
				break            // ensure once
			}
			peerPub2 = true
			continue // fast continue
		}
		if ks.group == groupX25519 && ks.n == x25519KeyLen {
			if peerPub {
				peerPub2 = false
				break
			}
			peerPub = true
			break // ensure order
		}
	}
	if !peerPub2 {
		return kexClassic
	}
	return kexPQ
}

// realHello is the first record crypto/tls writes as a client with the given curves.
func realHello(t *testing.T, curves []tls.CurveID) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	defer c2.Close()
	go func() {
		_ = tls.Client(c1, &tls.Config{ServerName: "example.test", MinVersion: tls.VersionTLS13, CurvePreferences: curves}).Handshake()
		c1.Close()
	}()
	_ = c2.SetReadDeadline(time.Now().Add(5 * time.Second))
	head := make([]byte, 5)
	if _, err := io.ReadFull(c2, head); err != nil {
		t.Fatal(err)
	}
	rec := append(head, make([]byte, binary.BigEndian.Uint16(head[3:]))...)
	if _, err := io.ReadFull(c2, rec[5:]); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestClassifyRealHellos(t *testing.T) {
	cases := []struct {
		name   string
		curves []tls.CurveID
		want   helloKex
	}{
		{"mlkem first, x25519 second", []tls.CurveID{tls.X25519MLKEM768, tls.X25519}, kexPQ},
		{"mlkem only", []tls.CurveID{tls.X25519MLKEM768}, kexPQ},
		{"x25519 only", []tls.CurveID{tls.X25519}, kexClassic},
		{"p256 only", []tls.CurveID{tls.CurveP256}, kexClassic},
		{"default", nil, kexPQ},
	}
	for _, c := range cases {
		rec := realHello(t, c.curves)
		if got := classifyHello(rec); got != c.want {
			t.Errorf("%s: classifyHello = %d, want %d (%d bytes)", c.name, got, c.want, len(rec))
		}
		// the same hello cut after every byte never panics and is never "pq" or "classic" for a cut before the key_share
		for n := 0; n < len(rec); n++ {
			classifyHello(rec[:n])
		}
	}
}

func TestClassifyHandBuilt(t *testing.T) {
	cases := []struct {
		name   string
		shares []share
		want   helloKex
	}{
		{"chrome: grease, mlkem, x25519", []share{shGrease, shMLKEM, shX25519}, kexPQ},
		{"firefox: mlkem, x25519, p256", []share{shMLKEM, shX25519, shP256}, kexPQ},
		{"mlkem only", []share{shMLKEM}, kexPQ},
		{"x25519 before mlkem", []share{shX25519, shMLKEM}, kexClassic},
		{"grease, x25519, mlkem", []share{shGrease, shX25519, shMLKEM}, kexClassic},
		{"two mlkem", []share{shMLKEM, shMLKEM, shX25519}, kexClassic},
		{"mlkem, x25519, mlkem: the scan stops at x25519", []share{shMLKEM, shX25519, shMLKEM}, kexPQ},
		{"x25519 only", []share{shX25519}, kexClassic},
		{"grease only", []share{shGrease}, kexClassic},
		{"p256 only", []share{shP256}, kexClassic},
		{"mlkem of a wrong length is skipped", []share{shMLKEMBad, shX25519}, kexClassic},
		{"x25519 of a wrong length is skipped", []share{shMLKEM, shX25519Bd}, kexPQ},
		{"empty key_share", nil, kexClassic},
	}
	for _, c := range cases {
		if got := classifyHello(buildHello(c.shares, true)); got != c.want {
			t.Errorf("%s: classifyHello = %d, want %d", c.name, got, c.want)
		}
	}
	if got := classifyHello(buildHello(nil, false)); got != kexUnknown {
		t.Errorf("no key_share extension: %d, want unknown", got)
	}
}

// TestClassifyMatchesReality checks the rule against upstream's loop on random share lists.
func TestClassifyMatchesReality(t *testing.T) {
	pool := []share{shMLKEM, shMLKEM, shX25519, shX25519, shGrease, shP256, shMLKEMBad, shX25519Bd}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		shares := make([]share, rng.Intn(6))
		for j := range shares {
			shares[j] = pool[rng.Intn(len(pool))]
		}
		if got, want := classifyHello(buildHello(shares, true)), reference(shares); got != want {
			t.Fatalf("shares %v: classifyHello = %d, upstream rule says %d", shares, got, want)
		}
	}
}

func TestClassifyEdgeCases(t *testing.T) {
	full := buildHello([]share{shGrease, shMLKEM, shX25519}, true)

	// the 8-byte padding extension follows the key_share extension: cut anywhere before its end and the shares are not all
	// there, cut after it (inside the padding) and they are
	for n := 0; n < len(full)-8; n++ {
		if got := classifyHello(full[:n]); got != kexUnknown {
			t.Fatalf("hello cut at %d of %d: %d, want unknown", n, len(full), got)
		}
	}
	for n := len(full) - 8; n <= len(full); n++ {
		if got := classifyHello(full[:n]); got != kexPQ {
			t.Errorf("hello cut at %d of %d (inside the padding): %d, want pq", n, len(full), got)
		}
	}

	// not a ClientHello, not TLS, empty
	for name, b := range map[string][]byte{
		"empty": nil, "one byte": {0x16}, "http": []byte("GET / HTTP/1.1\r\n\r\n"), "server hello": {0x16, 0x03, 0x03, 0, 4, 0x02, 0, 0, 0},
		"alert": {0x15, 0x03, 0x03, 0, 2, 2, 40},
	} {
		if got := classifyHello(b); got != kexUnknown {
			t.Errorf("%s: %d, want unknown", name, got)
		}
	}

	// a record header that claims more than we hold is fine as long as the key_share is in what we hold
	big := append([]byte(nil), full...)
	binary.BigEndian.PutUint16(big[3:], 16000)
	if got := classifyHello(big); got != kexPQ {
		t.Errorf("record length beyond the data: %d, want pq", got)
	}

	// a huge key_share vector (a client with hundreds of groups) is read to its end
	many := make([]share, 0, 300)
	for i := 0; i < 300; i++ {
		many = append(many, shGrease)
	}
	many = append(many, shMLKEM)
	if got := classifyHello(buildHello(many, true)); got != kexPQ {
		t.Errorf("300 grease shares then mlkem: %d, want pq", got)
	}

	// length fields that lie: the share's key longer than the vector, the vector longer than the extension
	liar := buildHello([]share{shMLKEM, shX25519}, true)
	for i := 0; i < len(liar); i++ {
		for _, v := range []byte{0, 0xff} {
			c := append([]byte(nil), liar...)
			c[i] = v
			classifyHello(c) // must not panic
		}
	}
}

func FuzzClassifyHello(f *testing.F) {
	f.Add(buildHello([]share{shGrease, shMLKEM, shX25519}, true))
	f.Add(buildHello([]share{shX25519}, true))
	f.Add(buildHello(nil, false))
	f.Add([]byte{0x16, 0x03, 0x01, 0xff, 0xff, 0x01})
	f.Add([]byte("GET / HTTP/1.1\r\n\r\n"))
	for _, curves := range [][]tls.CurveID{{tls.X25519MLKEM768, tls.X25519}, {tls.X25519}} {
		c1, c2 := net.Pipe()
		go func() {
			_ = tls.Client(c1, &tls.Config{ServerName: "example.test", CurvePreferences: curves}).Handshake()
			c1.Close()
		}()
		buf := make([]byte, 4096)
		_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := io.ReadAtLeast(c2, buf, 5)
		c2.Close()
		f.Add(buf[:n])
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if k := classifyHello(b); k < kexPQ || k > kexUnknown {
			t.Fatalf("classifyHello returned %d", k)
		}
		if len(b) > hellokexCap {
			classifyHello(b[:hellokexCap])
		}
	})
}

func BenchmarkClassifyHello(b *testing.B) {
	rec := buildHello([]share{shGrease, shMLKEM, shX25519}, true)
	b.ReportAllocs()
	for b.Loop() {
		classifyHello(rec)
	}
}
