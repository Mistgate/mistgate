package mtproto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
)

// Tag is the MTProto transport tag inside an obfuscated2 frame.
type Tag uint32

const (
	TagAbridged     Tag = 0xefefefef
	TagIntermediate Tag = 0xeeeeeeee
	TagSecure       Tag = 0xdddddddd
)

func (t Tag) String() string {
	switch t {
	case TagAbridged:
		return "abridged"
	case TagIntermediate:
		return "intermediate"
	case TagSecure:
		return "secure"
	default:
		return fmt.Sprintf("tag(0x%08x)", uint32(t))
	}
}

func tagFromBytes(b []byte) (Tag, bool) {
	if len(b) < 4 {
		return 0, false
	}
	t := Tag(binary.LittleEndian.Uint32(b))
	switch t {
	case TagAbridged, TagIntermediate, TagSecure:
		return t, true
	default:
		return 0, false
	}
}

type clientSession struct {
	Credential Credential
	Tag        Tag
	DC         int16
	Dec        cipher.Stream
	Enc        cipher.Stream
}

func deriveCipher(prekey []byte, secret []byte) (cipher.Stream, error) {
	h := sha256.New()
	h.Write(prekey[:32])
	h.Write(secret)
	block, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("mtproto: aes: %w", err)
	}
	return cipher.NewCTR(block, prekey[32:48]), nil
}

func parseClientHandshake(frame [64]byte, creds []Credential) (*clientSession, error) {
	var decPre, encPre [48]byte
	copy(decPre[:], frame[8:56])
	for i := 0; i < len(encPre); i++ {
		encPre[i] = decPre[len(decPre)-1-i]
	}

	var (
		matched Credential
		have    bool
		tag     Tag
		dc      int16
		dec     cipher.Stream
	)
	for _, cred := range creds {
		candidate, err := deriveCipher(decPre[:], cred.Key[:])
		if err != nil {
			continue
		}
		var plain [64]byte
		candidate.XORKeyStream(plain[:], frame[:])
		candidateTag, ok := tagFromBytes(plain[56:60])
		if !ok {
			continue
		}
		candidateDC := int16(binary.LittleEndian.Uint16(plain[60:62]))
		if candidateDC == 0 {
			candidateDC = 2
		}
		if !have {
			have = true
			matched = cred
			tag = candidateTag
			dc = candidateDC
			dec = candidate
		}
	}
	if !have {
		return nil, errAuth
	}
	enc, err := deriveCipher(encPre[:], matched.Key[:])
	if err != nil {
		return nil, err
	}
	return &clientSession{Credential: matched, Tag: tag, DC: dc, Dec: dec, Enc: enc}, nil
}

func validUpstreamNonce(n [64]byte) bool {
	if n[0] == 0xef {
		return false
	}
	switch binary.LittleEndian.Uint32(n[:4]) {
	case 0x44414548, // HEAD
		0x54534f50, // POST
		0x20544547, // GET
		0x4954504f, // OPTI
		0x02010316, // TLS handshake record
		0xdddddddd,
		0xeeeeeeee:
		return false
	}
	return n[4]|n[5]|n[6]|n[7] != 0
}

// newUpstreamHandshake builds the proxy's own obfuscated2 frame and returns
// the streams for the two directions. The send stream is already advanced by
// the 64-byte frame; the receive stream starts at zero.
func newUpstreamHandshake(tag Tag, dc int16, rng io.Reader) ([64]byte, cipher.Stream, cipher.Stream, error) {
	var nonce [64]byte
	for {
		if _, err := io.ReadFull(rng, nonce[:]); err != nil {
			return nonce, nil, nil, fmt.Errorf("mtproto: upstream nonce: %w", err)
		}
		if validUpstreamNonce(nonce) {
			break
		}
	}
	binary.LittleEndian.PutUint32(nonce[56:60], uint32(tag))
	binary.LittleEndian.PutUint16(nonce[60:62], uint16(dc))

	sendBlock, err := aes.NewCipher(nonce[8:40])
	if err != nil {
		return nonce, nil, nil, fmt.Errorf("mtproto: upstream aes: %w", err)
	}
	send := cipher.NewCTR(sendBlock, nonce[40:56])

	var rev [48]byte
	for i := 0; i < len(rev); i++ {
		rev[i] = nonce[8+len(rev)-1-i]
	}
	recvBlock, err := aes.NewCipher(rev[:32])
	if err != nil {
		return nonce, nil, nil, fmt.Errorf("mtproto: upstream aes: %w", err)
	}
	recv := cipher.NewCTR(recvBlock, rev[32:48])

	var encrypted [64]byte
	send.XORKeyStream(encrypted[:], nonce[:])
	copy(nonce[56:], encrypted[56:])
	return nonce, send, recv, nil
}
