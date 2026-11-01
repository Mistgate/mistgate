package mtproto

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestParseCredential(t *testing.T) {
	const hexKey = "00112233445566778899aabbccddeeff"
	for _, in := range []string{
		hexKey,
		"ee" + hexKey,
		"dd" + hexKey,
		"ee" + hexKey + "6578616d706c652e636f6d",
	} {
		cred, err := ParseCredential("alice", in)
		if err != nil {
			t.Fatalf("ParseCredential(%q): %v", in, err)
		}
		if got := cred.Hex(); got != hexKey {
			t.Fatalf("ParseCredential(%q) = %s, want %s", in, got, hexKey)
		}
	}
	for _, in := range []string{"", "00", "ee00", "zz112233445566778899aabbccddeeff"} {
		if _, err := ParseCredential("alice", in); err == nil {
			t.Fatalf("ParseCredential(%q) accepted", in)
		}
	}
	if got := FakeTLSSecret(mustKey(t, hexKey), "example.com"); got != "ee"+hexKey+"6578616d706c652e636f6d" {
		t.Fatalf("FakeTLSSecret = %q", got)
	}
}

func mustKey(t *testing.T, s string) [KeyLen]byte {
	t.Helper()
	k, err := ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestClientHelloValidation(t *testing.T) {
	key := mustKey(t, "00112233445566778899aabbccddeeff")
	other := mustKey(t, "ffeeddccbbaa99887766554433221100")
	cred := Credential{ID: "alice", Key: key}
	now := time.Unix(1_800_000_000, 0)

	raw := makeClientHello(t, key, uint32(now.Unix()), groupX25519, make([]byte, 32), []byte("0123456789abcdef0123456789abcdef"))
	hello, err := parseClientHello(raw)
	if err != nil {
		t.Fatal(err)
	}
	replay := newReplayCache(time.Minute, func() time.Time { return now })
	got, err := validateClientHello(hello, []Credential{cred}, now, replay, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "alice" {
		t.Fatalf("matched %q", got.ID)
	}
	if _, err := validateClientHello(hello, []Credential{{ID: "bob", Key: other}}, now, nil, false); !errors.Is(err, errAuth) {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := validateClientHello(hello, []Credential{cred}, now, replay, false); !errors.Is(err, errReplay) {
		t.Fatalf("replay: %v", err)
	}

	stale := makeClientHello(t, key, uint32(now.Unix()-1000), groupX25519, make([]byte, 32), []byte("0123456789abcdef0123456789abcdef"))
	hello, err = parseClientHello(stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateClientHello(hello, []Credential{cred}, now, nil, false); !errors.Is(err, errAuth) {
		t.Fatalf("stale timestamp: %v", err)
	}
	boot := makeClientHello(t, key, 5, groupX25519, make([]byte, 32), []byte("0123456789abcdef0123456789abcdef"))
	hello, err = parseClientHello(boot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateClientHello(hello, []Credential{cred}, now, nil, false); err != nil {
		t.Fatalf("boot timestamp: %v", err)
	}
}

func TestServerHelloDigest(t *testing.T) {
	key := mustKey(t, "00112233445566778899aabbccddeeff")
	now := time.Unix(1_800_000_000, 0)
	raw := makeClientHello(t, key, uint32(now.Unix()), groupX25519, make([]byte, 32), []byte("0123456789abcdef0123456789abcdef"))
	hello, err := parseClientHello(raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := buildServerHello(key[:], hello, 64, 0, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(response) < 43 || response[0] != tlsRecordHandshake {
		t.Fatalf("bad response: %x", response[:min(len(response), 16)])
	}
	zeroed := append([]byte(nil), response...)
	clear(zeroed[tlsDigestPos : tlsDigestPos+tlsDigestLen])
	mac := hmac.New(sha256.New, key[:])
	mac.Write(hello.digest[:])
	mac.Write(zeroed)
	if subtle.ConstantTimeCompare(mac.Sum(nil), response[tlsDigestPos:tlsDigestPos+tlsDigestLen]) != 1 {
		t.Fatal("server hello digest does not verify")
	}
}

func TestServerHelloMLKEM(t *testing.T) {
	key := mustKey(t, "00112233445566778899aabbccddeeff")
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	pub := dk.EncapsulationKey().Bytes()
	share := append(append([]byte(nil), pub...), make([]byte, x25519ShareLen)...)
	now := time.Unix(1_800_000_000, 0)
	raw := makeClientHello(t, key, uint32(now.Unix()), groupX25519MLKEM768, share, []byte("0123456789abcdef0123456789abcdef"))
	hello, err := parseClientHello(raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := buildServerHello(key[:], hello, 64, 0, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := serverHelloBody(t, response)
	group, exchange := serverHelloKeyShare(t, body)
	if group != groupX25519MLKEM768 || len(exchange) != mlkem768ServerShareLen {
		t.Fatalf("key share group %#x len %d", group, len(exchange))
	}
}

type recordingObserver struct {
	mu       sync.Mutex
	opened   int
	closed   int
	up       uint64
	down     uint64
	lastInfo SessionInfo
}

func (o *recordingObserver) Opened(info SessionInfo) {
	o.mu.Lock()
	o.opened++
	o.lastInfo = info
	o.mu.Unlock()
}

func (o *recordingObserver) Closed(SessionInfo) {
	o.mu.Lock()
	o.closed++
	o.mu.Unlock()
}

func (o *recordingObserver) AddTraffic(_ string, up, down uint64) {
	o.mu.Lock()
	o.up += up
	o.down += down
	o.mu.Unlock()
}

func TestEndToEnd(t *testing.T) {
	key := mustKey(t, "00112233445566778899aabbccddeeff")
	cred := Credential{ID: "alice", Key: key}
	dcAddr, dcDone := startFakeDC(t)
	obs := &recordingObserver{}
	srv, err := New(Config{
		Credentials:      []Credential{cred},
		Observer:         obs,
		StatsInterval:    5 * time.Millisecond,
		FakeCertLen:      64,
		DCAddress:        func(int16) (string, bool) { return dcAddr, true },
		HandshakeTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	client, proxy := net.Pipe()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(context.Background(), proxy) }()

	sessionID := []byte("0123456789abcdef0123456789abcdef")
	helloRaw := makeClientHello(t, key, uint32(time.Now().Unix()), groupX25519, make([]byte, 32), sessionID)
	if _, err := client.Write(helloRaw); err != nil {
		t.Fatal(err)
	}
	hello, err := parseClientHello(helloRaw)
	if err != nil {
		t.Fatal(err)
	}
	_ = readResponse(t, client, key, hello.digest[:])

	frame, send, recv := makeClientFrame(t, key, TagIntermediate, 2)
	tw := &tlsRecordWriter{w: client}
	if _, err := tw.Write(frame[:]); err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello")
	enc := make([]byte, len(payload))
	send.XORKeyStream(enc, payload)
	if _, err := tw.Write(enc); err != nil {
		t.Fatal(err)
	}

	tr := &tlsRecordReader{r: client}
	got := make([]byte, 5)
	if _, err := io.ReadFull(tr, got); err != nil {
		t.Fatal(err)
	}
	recv.XORKeyStream(got, got)
	if !bytes.Equal(got, []byte("world")) {
		t.Fatalf("got %q, want world", got)
	}
	_ = client.Close()

	if err := <-serveErr; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	<-dcDone
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.opened != 1 || obs.closed != 1 {
		t.Fatalf("opened=%d closed=%d", obs.opened, obs.closed)
	}
	if obs.up != uint64(len(payload)) || obs.down != 5 {
		t.Fatalf("traffic up=%d down=%d", obs.up, obs.down)
	}
	if obs.lastInfo.DC != 2 || obs.lastInfo.Tag != TagIntermediate || obs.lastInfo.SNI != "example.com" {
		t.Fatalf("session info: %+v", obs.lastInfo)
	}
}

func startFakeDC(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var frame [64]byte
		if _, err := io.ReadFull(conn, frame[:]); err != nil {
			t.Errorf("fake DC handshake: %v", err)
			return
		}
		pre := frame[8:56]
		block, _ := aes.NewCipher(pre[:32])
		recv := cipher.NewCTR(block, pre[32:48])
		var plain [64]byte
		recv.XORKeyStream(plain[:], frame[:])
		if Tag(binary.LittleEndian.Uint32(plain[56:60])) != TagIntermediate {
			t.Errorf("fake DC tag = %#x", binary.LittleEndian.Uint32(plain[56:60]))
		}
		if dc := int16(binary.LittleEndian.Uint16(plain[60:62])); dc != 2 {
			t.Errorf("fake DC dc = %d", dc)
		}
		var rev [48]byte
		for i := range rev {
			rev[i] = pre[len(rev)-1-i]
		}
		sendBlock, _ := aes.NewCipher(rev[:32])
		send := cipher.NewCTR(sendBlock, rev[32:48])

		got := make([]byte, 5)
		if _, err := io.ReadFull(&cipherReader{r: conn, s: recv}, got); err != nil {
			t.Errorf("fake DC read: %v", err)
			return
		}
		if string(got) != "hello" {
			t.Errorf("fake DC got %q", got)
			return
		}
		if _, err := (&cipherWriter{w: conn, s: send}).Write([]byte("world")); err != nil {
			t.Errorf("fake DC write: %v", err)
		}
	}()
	return ln.Addr().String(), done
}

func makeClientHello(t *testing.T, key [KeyLen]byte, timestamp uint32, group uint16, share, sessionID []byte) []byte {
	t.Helper()
	name := []byte("example.com")
	var sni []byte
	sni = appendU16(sni, uint16(3+len(name)))
	sni = append(sni, 0)
	sni = appendU16(sni, uint16(len(name)))
	sni = append(sni, name...)

	var ext []byte
	ext = appendU16(ext, 0x0000)
	ext = appendU16(ext, uint16(len(sni)))
	ext = append(ext, sni...)
	ext = appendU16(ext, 0x002b)
	ext = appendU16(ext, 3)
	ext = append(ext, 2, 0x03, 0x04)
	var ks []byte
	ks = appendU16(ks, uint16(4+len(share)))
	ks = appendU16(ks, group)
	ks = appendU16(ks, uint16(len(share)))
	ks = append(ks, share...)
	ext = appendU16(ext, 0x0033)
	ext = appendU16(ext, uint16(len(ks)))
	ext = append(ext, ks...)

	var body []byte
	body = append(body, 0x03, 0x03)
	body = append(body, make([]byte, 32)...)
	body = append(body, byte(len(sessionID)))
	body = append(body, sessionID...)
	body = appendU16(body, 4)
	body = appendU16(body, 0x1301)
	body = appendU16(body, 0x1302)
	body = append(body, 1, 0)
	body = appendU16(body, uint16(len(ext)))
	body = append(body, ext...)

	var hs []byte
	hs = append(hs, 0x01)
	hs = appendU24(hs, len(body))
	hs = append(hs, body...)
	rec := []byte{tlsRecordHandshake, 0x03, 0x01}
	rec = appendU16(rec, uint16(len(hs)))
	rec = append(rec, hs...)

	mac := hmac.New(sha256.New, key[:])
	mac.Write(rec)
	sum := mac.Sum(nil)
	copy(rec[tlsDigestPos:tlsDigestPos+28], sum[:28])
	var ts [4]byte
	binary.LittleEndian.PutUint32(ts[:], timestamp)
	for i := 0; i < 4; i++ {
		rec[tlsDigestPos+28+i] = ts[i] ^ sum[28+i]
	}
	return rec
}

func makeClientFrame(t *testing.T, key [KeyLen]byte, tag Tag, dc int16) ([64]byte, cipher.Stream, cipher.Stream) {
	t.Helper()
	var frame [64]byte
	if _, err := rand.Read(frame[:]); err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(frame[56:60], uint32(tag))
	binary.LittleEndian.PutUint16(frame[60:62], uint16(dc))
	pre := frame[8:56]
	send, err := deriveCipher(pre, key[:])
	if err != nil {
		t.Fatal(err)
	}
	var encrypted [64]byte
	send.XORKeyStream(encrypted[:], frame[:])
	out := frame
	copy(out[56:], encrypted[56:])

	var rev [48]byte
	for i := range rev {
		rev[i] = pre[len(rev)-1-i]
	}
	recv, err := deriveCipher(rev[:], key[:])
	if err != nil {
		t.Fatal(err)
	}
	return out, send, recv
}

func readResponse(t *testing.T, r io.Reader, key [KeyLen]byte, clientDigest []byte) []byte {
	t.Helper()
	var response []byte
	for i := 0; i < 3; i++ {
		typ, body := readRecord(t, r)
		response = append(response, buildServerRecord(typ, body)...)
	}
	zeroed := append([]byte(nil), response...)
	clear(zeroed[tlsDigestPos : tlsDigestPos+tlsDigestLen])
	mac := hmac.New(sha256.New, key[:])
	mac.Write(clientDigest)
	mac.Write(zeroed)
	if subtle.ConstantTimeCompare(mac.Sum(nil), response[tlsDigestPos:tlsDigestPos+tlsDigestLen]) != 1 {
		t.Fatal("server hello digest does not verify")
	}
	return response
}

func readRecord(t *testing.T, r io.Reader) (byte, []byte) {
	t.Helper()
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, be16(h[3:5]))
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatal(err)
	}
	return h[0], body
}

func buildServerRecord(typ byte, body []byte) []byte {
	rec := []byte{typ, 0x03, 0x03}
	rec = appendU16(rec, uint16(len(body)))
	return append(rec, body...)
}

func serverHelloBody(t *testing.T, response []byte) []byte {
	t.Helper()
	if len(response) < 9 || response[0] != tlsRecordHandshake || response[5] != 0x02 {
		t.Fatalf("not a ServerHello: %x", response[:min(len(response), 16)])
	}
	n := be24(response[6:9])
	if 9+n > len(response) {
		t.Fatal("short ServerHello")
	}
	return response[9 : 9+n]
}

func serverHelloKeyShare(t *testing.T, body []byte) (uint16, []byte) {
	t.Helper()
	if len(body) < 2+32+1 {
		t.Fatal("short ServerHello body")
	}
	p := 2 + 32
	sid := int(body[p])
	p++
	p += sid
	p += 2 + 1
	if p+2 > len(body) {
		t.Fatal("short extensions")
	}
	n := be16(body[p:])
	p += 2
	end := p + n
	for p+4 <= end {
		typ := binary.BigEndian.Uint16(body[p:])
		l := be16(body[p+2:])
		p += 4
		if p+l > end {
			t.Fatal("short extension")
		}
		if typ == 0x0033 {
			data := body[p : p+l]
			if len(data) < 4 {
				t.Fatal("short key share")
			}
			group := binary.BigEndian.Uint16(data)
			kl := be16(data[2:])
			if 4+kl > len(data) {
				t.Fatal("short key share entry")
			}
			return group, data[4 : 4+kl]
		}
		p += l
	}
	t.Fatal("no key share")
	return 0, nil
}
