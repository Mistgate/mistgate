package mtproto

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/mlkem"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	tlsRecordHandshake    = 0x16
	tlsRecordChangeCipher = 0x14
	tlsRecordAlert        = 0x15
	tlsRecordApplication  = 0x17

	tlsMaxPlaintext  = 16_384
	tlsMaxCiphertext = tlsMaxPlaintext + 256

	tlsDigestPos     = 11
	tlsDigestLen     = 32
	tlsDigestHalfLen = 16

	groupX25519         = 0x001d
	groupX25519MLKEM768 = 0x11ec

	mlkem768PublicKeyLen   = 1184
	mlkem768CiphertextLen  = 1088
	x25519ShareLen         = 32
	mlkem768ClientShareLen = mlkem768PublicKeyLen + x25519ShareLen
	mlkem768ServerShareLen = mlkem768CiphertextLen + x25519ShareLen

	suiteAES128GCM    = 0x1301
	suiteAES256GCM    = 0x1302
	suiteChaCha20Poly = 0x1303

	clientHelloMinLen     = 43
	bootTimeCompatMaxSecs = 120
	timeSkewSecs          = 120
)

var (
	errNotTLS         = errors.New("mtproto: not a TLS ClientHello")
	errBadClientHello = errors.New("mtproto: invalid TLS ClientHello")
	errAuth           = errors.New("mtproto: authentication failed")
	errReplay         = errors.New("mtproto: replayed ClientHello")
)

type clientHello struct {
	raw            []byte
	digest         [tlsDigestLen]byte
	sessionID      []byte
	cipherSuites   []uint16
	sni            string
	keyShareMLKEM  []byte
	keyShareX25519 []byte
}

func be16(b []byte) int { return int(b[0])<<8 | int(b[1]) }

func be24(b []byte) int { return int(b[0])<<16 | int(b[1])<<8 | int(b[2]) }

func appendU16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }

func appendU24(b []byte, v int) []byte { return append(b, byte(v>>16), byte(v>>8), byte(v)) }

func parseClientHello(raw []byte) (*clientHello, error) {
	if len(raw) < 5+clientHelloMinLen {
		return nil, fmt.Errorf("%w: short record", errBadClientHello)
	}
	if raw[0] != tlsRecordHandshake || raw[1] != 3 {
		return nil, errNotTLS
	}
	recordLen := be16(raw[3:5])
	if recordLen < clientHelloMinLen || recordLen > tlsMaxPlaintext || len(raw) != 5+recordLen {
		return nil, fmt.Errorf("%w: record length %d", errBadClientHello, recordLen)
	}
	if raw[5] != 0x01 {
		return nil, fmt.Errorf("%w: not a ClientHello", errBadClientHello)
	}
	hsLen := be24(raw[6:9])
	if hsLen < 38 || 4+hsLen > recordLen {
		return nil, fmt.Errorf("%w: handshake length %d", errBadClientHello, hsLen)
	}
	hsEnd := 9 + hsLen

	pos := 9 + 2
	if pos+tlsDigestLen > hsEnd {
		return nil, fmt.Errorf("%w: short random", errBadClientHello)
	}
	h := &clientHello{raw: raw, sessionID: nil}
	copy(h.digest[:], raw[tlsDigestPos:tlsDigestPos+tlsDigestLen])
	pos += tlsDigestLen

	if pos >= hsEnd {
		return nil, fmt.Errorf("%w: missing session id length", errBadClientHello)
	}
	sessionIDLen := int(raw[pos])
	pos++
	if sessionIDLen > 32 || pos+sessionIDLen > hsEnd {
		return nil, fmt.Errorf("%w: session id length %d", errBadClientHello, sessionIDLen)
	}
	h.sessionID = raw[pos : pos+sessionIDLen]
	pos += sessionIDLen

	if pos+2 > hsEnd {
		return nil, fmt.Errorf("%w: missing cipher suites", errBadClientHello)
	}
	suitesLen := be16(raw[pos:])
	pos += 2
	if suitesLen == 0 || suitesLen%2 != 0 || pos+suitesLen > hsEnd {
		return nil, fmt.Errorf("%w: cipher suites length %d", errBadClientHello, suitesLen)
	}
	for i := 0; i < suitesLen; i += 2 {
		h.cipherSuites = append(h.cipherSuites, binary.BigEndian.Uint16(raw[pos+i:]))
	}
	pos += suitesLen

	if pos >= hsEnd {
		return nil, fmt.Errorf("%w: missing compression methods", errBadClientHello)
	}
	compressionLen := int(raw[pos])
	pos++
	if pos+compressionLen > hsEnd {
		return nil, fmt.Errorf("%w: compression methods", errBadClientHello)
	}
	pos += compressionLen
	if pos == hsEnd {
		return h, nil
	}
	if pos+2 > hsEnd {
		return nil, fmt.Errorf("%w: missing extensions length", errBadClientHello)
	}
	extLen := be16(raw[pos:])
	pos += 2
	if pos+extLen > hsEnd {
		return nil, fmt.Errorf("%w: extensions length %d", errBadClientHello, extLen)
	}
	extEnd := pos + extLen
	for pos+4 <= extEnd {
		typ := binary.BigEndian.Uint16(raw[pos:])
		n := be16(raw[pos+2:])
		pos += 4
		if pos+n > extEnd {
			return nil, fmt.Errorf("%w: extension 0x%04x length %d", errBadClientHello, typ, n)
		}
		data := raw[pos : pos+n]
		switch typ {
		case 0x0000:
			h.sni = parseSNI(data)
		case 0x0033:
			if err := h.parseKeyShare(data); err != nil {
				return nil, err
			}
		}
		pos += n
	}
	if h.keyShareMLKEM == nil && h.keyShareX25519 == nil {
		return nil, fmt.Errorf("%w: no supported key share", errBadClientHello)
	}
	return h, nil
}

func parseSNI(data []byte) string {
	if len(data) < 5 {
		return ""
	}
	listLen := be16(data)
	if listLen+2 > len(data) {
		return ""
	}
	p := 2
	for p+3 <= 2+listLen {
		nameType := data[p]
		nameLen := be16(data[p+1:])
		p += 3
		if p+nameLen > 2+listLen {
			return ""
		}
		if nameType == 0 && nameLen > 0 {
			return string(data[p : p+nameLen])
		}
		p += nameLen
	}
	return ""
}

func (h *clientHello) parseKeyShare(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: short key share", errBadClientHello)
	}
	n := be16(data)
	if 2+n > len(data) {
		return fmt.Errorf("%w: key share length %d", errBadClientHello, n)
	}
	p := 2
	for p+4 <= 2+n {
		group := binary.BigEndian.Uint16(data[p:])
		keyLen := be16(data[p+2:])
		p += 4
		if p+keyLen > 2+n {
			return fmt.Errorf("%w: key share entry length %d", errBadClientHello, keyLen)
		}
		key := data[p : p+keyLen]
		switch group {
		case groupX25519:
			if len(key) == x25519ShareLen && h.keyShareX25519 == nil {
				h.keyShareX25519 = append([]byte(nil), key...)
			}
		case groupX25519MLKEM768:
			if len(key) >= mlkem768ClientShareLen && h.keyShareMLKEM == nil {
				h.keyShareMLKEM = append([]byte(nil), key[:mlkem768ClientShareLen]...)
			}
		}
		p += keyLen
	}
	return nil
}

func validateClientHello(h *clientHello, creds []Credential, now time.Time, replay *replayCache, ignoreTimeSkew bool) (Credential, error) {
	msg := append([]byte(nil), h.raw...)
	for i := tlsDigestPos; i < tlsDigestPos+tlsDigestLen; i++ {
		msg[i] = 0
	}

	var (
		matched   Credential
		have      bool
		timestamp uint32
	)
	for _, cred := range creds {
		mac := hmac.New(sha256.New, cred.Key[:])
		mac.Write(msg)
		sum := mac.Sum(nil)
		if subtle.ConstantTimeCompare(sum[:28], h.digest[:28]) != 1 {
			continue
		}
		if !have {
			have = true
			matched = cred
			timestamp = binary.LittleEndian.Uint32([]byte{
				h.digest[28] ^ sum[28],
				h.digest[29] ^ sum[29],
				h.digest[30] ^ sum[30],
				h.digest[31] ^ sum[31],
			})
		}
	}
	if !have {
		return Credential{}, errAuth
	}
	if !ignoreTimeSkew {
		bootCap := uint32(bootTimeCompatMaxSecs)
		if replay != nil {
			if secs := uint32(replay.ttl / time.Second); secs < bootCap {
				bootCap = secs
			}
		}
		if timestamp >= bootCap {
			diff := now.Unix() - int64(timestamp)
			if diff < -timeSkewSecs || diff > timeSkewSecs {
				return Credential{}, fmt.Errorf("%w: timestamp outside window", errAuth)
			}
		}
	}
	if replay != nil && !replay.claim(h.digest[:tlsDigestHalfLen]) {
		return Credential{}, errReplay
	}
	return matched, nil
}

func selectCipherSuite(offered []uint16) (uint16, bool) {
	for _, want := range []uint16{suiteAES128GCM, suiteChaCha20Poly, suiteAES256GCM} {
		for _, got := range offered {
			if got == want {
				return want, true
			}
		}
	}
	return 0, false
}

func serverKeyShare(h *clientHello, rng io.Reader) (uint16, []byte, error) {
	if h.keyShareMLKEM != nil {
		if ek, err := mlkem.NewEncapsulationKey768(h.keyShareMLKEM[:mlkem768PublicKeyLen]); err == nil {
			_, ct := ek.Encapsulate()
			priv, err := ecdh.X25519().GenerateKey(rng)
			if err == nil {
				share := make([]byte, 0, mlkem768ServerShareLen)
				share = append(share, ct...)
				share = append(share, priv.PublicKey().Bytes()...)
				return groupX25519MLKEM768, share, nil
			}
		}
	}
	if h.keyShareX25519 != nil {
		priv, err := ecdh.X25519().GenerateKey(rng)
		if err != nil {
			return 0, nil, fmt.Errorf("mtproto: x25519 key: %w", err)
		}
		return groupX25519, priv.PublicKey().Bytes(), nil
	}
	return 0, nil, fmt.Errorf("%w: no usable key share", errBadClientHello)
}

func buildServerHello(secret []byte, h *clientHello, fakeCertLen int, tickets int, rng io.Reader) ([]byte, error) {
	suite, ok := selectCipherSuite(h.cipherSuites)
	if !ok {
		return nil, fmt.Errorf("%w: no TLS 1.3 cipher suite", errBadClientHello)
	}
	group, keyExchange, err := serverKeyShare(h, rng)
	if err != nil {
		return nil, err
	}
	if fakeCertLen < 64 {
		fakeCertLen = 64
	}
	if fakeCertLen > tlsMaxCiphertext {
		fakeCertLen = tlsMaxCiphertext
	}

	body := make([]byte, 0, 128)
	body = append(body, 0x03, 0x03)
	body = append(body, make([]byte, tlsDigestLen)...)
	body = append(body, byte(len(h.sessionID)))
	body = append(body, h.sessionID...)
	body = appendU16(body, suite)
	body = append(body, 0x00)

	var ext []byte
	ext = appendU16(ext, 0x0033)
	ext = appendU16(ext, uint16(4+len(keyExchange)))
	ext = appendU16(ext, group)
	ext = appendU16(ext, uint16(len(keyExchange)))
	ext = append(ext, keyExchange...)
	ext = appendU16(ext, 0x002b)
	ext = appendU16(ext, 2)
	ext = appendU16(ext, 0x0304)
	body = appendU16(body, uint16(len(ext)))
	body = append(body, ext...)

	serverHello := []byte{tlsRecordHandshake, 0x03, 0x03}
	hs := []byte{0x02}
	hs = appendU24(hs, len(body))
	hs = append(hs, body...)
	serverHello = appendU16(serverHello, uint16(len(hs)))
	serverHello = append(serverHello, hs...)

	response := make([]byte, 0, len(serverHello)+6+5+fakeCertLen+tickets*105)
	response = append(response, serverHello...)
	response = append(response, tlsRecordChangeCipher, 0x03, 0x03, 0x00, 0x01, 0x01)

	cert := make([]byte, fakeCertLen)
	if _, err := io.ReadFull(rng, cert); err != nil {
		return nil, fmt.Errorf("mtproto: fake certificate: %w", err)
	}
	response = append(response, tlsRecordApplication, 0x03, 0x03)
	response = appendU16(response, uint16(len(cert)))
	response = append(response, cert...)

	if tickets > 4 {
		tickets = 4
	}
	for i := 0; i < tickets; i++ {
		n := 48
		var one [1]byte
		if _, err := io.ReadFull(rng, one[:]); err == nil {
			n += int(one[0]) % 49
		}
		ticket := make([]byte, n)
		if _, err := io.ReadFull(rng, ticket); err != nil {
			return nil, fmt.Errorf("mtproto: session ticket: %w", err)
		}
		response = append(response, tlsRecordApplication, 0x03, 0x03)
		response = appendU16(response, uint16(len(ticket)))
		response = append(response, ticket...)
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(h.digest[:])
	mac.Write(response)
	copy(response[tlsDigestPos:tlsDigestPos+tlsDigestLen], mac.Sum(nil))
	return response, nil
}

func readClientHelloRecord(r io.Reader) ([]byte, error) {
	var header [5]byte
	n, err := io.ReadFull(r, header[:])
	if err != nil {
		return header[:n], err
	}
	if header[0] != tlsRecordHandshake || header[1] != 3 {
		return header[:5], errNotTLS
	}
	length := be16(header[3:5])
	if length < clientHelloMinLen || length > tlsMaxPlaintext {
		return header[:5], fmt.Errorf("%w: record length %d", errBadClientHello, length)
	}
	raw := make([]byte, 5+length)
	copy(raw, header[:])
	n, err = io.ReadFull(r, raw[5:])
	if err != nil {
		return raw[:5+n], err
	}
	return raw, nil
}

// tlsRecordReader unwraps TLS ApplicationData records. It skips
// ChangeCipherSpec, treats Alert as EOF and refuses a second Handshake record.
type tlsRecordReader struct {
	r       io.Reader
	scratch []byte
	pending []byte
}

func (r *tlsRecordReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if len(r.pending) > 0 {
			n := copy(p, r.pending)
			r.pending = r.pending[n:]
			return n, nil
		}
		var header [5]byte
		if _, err := io.ReadFull(r.r, header[:]); err != nil {
			return 0, err
		}
		length := be16(header[3:5])
		if header[1] != 3 || length > tlsMaxCiphertext {
			return 0, fmt.Errorf("%w: TLS record type 0x%02x length %d", errBadClientHello, header[0], length)
		}
		if length == 0 {
			switch header[0] {
			case tlsRecordChangeCipher, tlsRecordApplication:
				continue
			}
			return 0, fmt.Errorf("%w: empty TLS record 0x%02x", errBadClientHello, header[0])
		}
		if cap(r.scratch) < length {
			r.scratch = make([]byte, length)
		}
		body := r.scratch[:length]
		if _, err := io.ReadFull(r.r, body); err != nil {
			return 0, err
		}
		switch header[0] {
		case tlsRecordApplication:
			r.pending = body
		case tlsRecordChangeCipher:
			continue
		case tlsRecordAlert:
			return 0, io.EOF
		case tlsRecordHandshake:
			return 0, fmt.Errorf("%w: unexpected TLS handshake record", errBadClientHello)
		default:
			return 0, fmt.Errorf("%w: unknown TLS record 0x%02x", errBadClientHello, header[0])
		}
	}
}

// tlsRecordWriter wraps every write in one or more TLS 1.3 ApplicationData
// records. It accepts the whole buffer and returns len(p) on success.
type tlsRecordWriter struct {
	w io.Writer
}

func (w *tlsRecordWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > tlsMaxPlaintext {
			n = tlsMaxPlaintext
		}
		record := make([]byte, 5+n)
		record[0] = tlsRecordApplication
		record[1] = 0x03
		record[2] = 0x03
		binary.BigEndian.PutUint16(record[3:5], uint16(n))
		copy(record[5:], p[:n])
		if err := writeFull(w.w, record); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

type replayCache struct {
	mu   sync.Mutex
	seen map[[tlsDigestHalfLen]byte]time.Time
	ttl  time.Duration
	now  func() time.Time
}

func newReplayCache(ttl time.Duration, now func() time.Time) *replayCache {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &replayCache{seen: make(map[[tlsDigestHalfLen]byte]time.Time), ttl: ttl, now: now}
}

func (c *replayCache) claim(digest []byte) bool {
	if len(digest) < tlsDigestHalfLen {
		return false
	}
	var key [tlsDigestHalfLen]byte
	copy(key[:], digest[:tlsDigestHalfLen])
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if exp, ok := c.seen[key]; ok && now.Before(exp) {
		return false
	}
	if len(c.seen) >= 4096 {
		for k, exp := range c.seen {
			if !now.Before(exp) {
				delete(c.seen, k)
			}
		}
	}
	c.seen[key] = now.Add(c.ttl)
	return true
}
