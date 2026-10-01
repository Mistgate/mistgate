package mimicry

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	mrand "math/rand/v2"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"golang.org/x/crypto/cryptobyte"
)

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mimicry.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serverHelloOf decrypts the first Initial packet a server answers with (the keys are those of the client's
// original destination id, RFC 9001 5.2) and returns the ServerHello from its CRYPTO frame.
func serverHelloOf(t *testing.T, d []byte, origDCID []byte) []byte {
	t.Helper()
	s := cryptobyte.String(d)
	var first, tokenLen uint8
	var version uint32
	var dcid, scid cryptobyte.String
	if !s.ReadUint8(&first) || !s.ReadUint32(&version) || !s.ReadUint8LengthPrefixed(&dcid) || !s.ReadUint8LengthPrefixed(&scid) || !s.ReadUint8(&tokenLen) {
		t.Fatalf("short reply: % x", d)
	}
	if first&0xf0 != 0xc0 || version != 1 || tokenLen != 0 {
		t.Fatalf("the reply does not start with an Initial: first %#x version %#x token %d", first, version, tokenLen)
	}
	length, ok := readVarint(&s)
	if !ok || int(length) > len(s) {
		t.Fatalf("length %d, %d bytes follow", length, len(s))
	}
	pnOff := len(d) - len(s)

	keys, err := initialKeys(origDCID, "server")
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
	plain, err := aead.Open(nil, nonce, d[pnOff+pnLen:pnOff+int(length)], hdr)
	if err != nil {
		t.Fatalf("the server's Initial does not decrypt: %v", err)
	}
	f := cryptobyte.String(plain)
	for !f.Empty() {
		var ft uint8
		f.ReadUint8(&ft)
		switch ft {
		case 0x00, 0x01: // PADDING, PING
		case 0x02: // ACK: largest, delay, range count, first range
			for i := 0; i < 4; i++ {
				readVarint(&f)
			}
		case 0x06:
			off, _ := readVarint(&f)
			n, _ := readVarint(&f)
			var data []byte
			if !f.ReadBytes(&data, int(n)) || off != 0 {
				t.Fatalf("CRYPTO frame at %d, %d bytes", off, n)
			}
			return data
		case 0x1c, 0x1d:
			t.Fatalf("the server closed the connection: frame %#x % x", ft, []byte(f))
		default:
			t.Fatalf("unexpected frame %#x", ft)
		}
	}
	t.Fatal("no CRYPTO frame in the server's Initial")
	return nil
}

// The strongest check there is that the Initial is a real one: a real QUIC server (quic-go over crypto/tls)
// derives the keys from the destination id, decrypts it, parses our ClientHello, picks a suite and a group and
// answers with a ServerHello. Loopback only.
func TestRealQUICServerAnswersTheInitial(t *testing.T) {
	if testing.Short() {
		t.Skip("binds a UDP socket")
	}
	srvConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no loopback UDP:", err)
	}
	defer srvConn.Close()
	ln, err := quic.Listen(srvConn, &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t)}, NextProtos: []string{"h3"}, MinVersion: tls.VersionTLS13}, &quic.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	for _, id := range []string{QUIC, CurlQUIC} {
		for seed := uint64(0); seed < 8; seed++ {
			chain, err := GenerateChain(id, Options{}, mrand.New(mrand.NewPCG(seed, 5)))
			if err != nil {
				t.Fatal(err)
			}
			d := expand(t, chain[0], mrand.New(mrand.NewPCG(1, 1)))
			in := openInitial(t, d)

			cli, err := net.DialUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, srvConn.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cli.Write(d); err != nil {
				t.Fatal(err)
			}
			cli.SetReadDeadline(time.Now().Add(3 * time.Second))
			reply := make([]byte, 2000)
			n, err := cli.Read(reply)
			cli.Close()
			if err != nil {
				t.Fatalf("%s seed %d: the server did not answer: %v", id, seed, err)
			}
			sh := serverHelloOf(t, reply[:n], in.dcid)
			// ServerHello: type 2, length, version, random, session id, suite, compression, extensions
			s := cryptobyte.String(sh)
			var typ uint8
			var body, sid cryptobyte.String
			var ver, suite uint16
			var rnd []byte
			if !s.ReadUint8(&typ) || typ != 2 || !s.ReadUint24LengthPrefixed(&body) || !body.ReadUint16(&ver) || !body.ReadBytes(&rnd, 32) ||
				!body.ReadUint8LengthPrefixed(&sid) || !body.ReadUint16(&suite) {
				t.Fatalf("%s seed %d: malformed ServerHello % x", id, seed, sh)
			}
			if !slices.Contains([]uint16{0x1301, 0x1302, 0x1303}, suite) {
				t.Errorf("%s seed %d: the server picked suite %#x", id, seed, suite)
			}
			if !bytes.Contains(sh, []byte{0x00, 0x33, 0x00, 0x24, 0x00, 0x1d, 0x00, 0x20}) { // key_share: x25519, 32 bytes
				t.Errorf("%s seed %d: the ServerHello has no x25519 key share", id, seed)
			}
		}
	}
}
