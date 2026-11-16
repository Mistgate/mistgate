package hosttunnel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

// MarkWindow is how far the panel's clock in a mark may be from the node's.
const MarkWindow = 2 * time.Minute

// The layout of a ClientHello handshake message: type (1), length (3), legacy version (2), random (32), session id
// length (1), session id.
const (
	helloRandom    = 6
	helloSIDLen    = 38
	helloSID       = 39
	helloSIDEnd    = helloSID + 32
	markInfo       = "mistgate host tunnel mark v1"
	markPlainBytes = 16
)

// markAEAD derives the mark's cipher from the tunnel secret and the hello's client random.
func markAEAD(secret string, random []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, []byte(secret), random, markInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// helloLaidOut says whether hello is a ClientHello handshake message with a 32-byte session id.
func helloLaidOut(hello []byte) bool {
	return len(hello) >= helloSIDEnd && hello[0] == 1 && hello[helloSIDLen] == 32
}

// SealMark writes the mark into the session id of hello, a ClientHello handshake message (type, 3-byte length, body)
// whose session id is 32 bytes long: the panel's time now, sealed with AES-256-GCM under a key derived from secret and
// the hello's client random, authenticated together with the rest of the hello.
func SealMark(secret string, hello []byte, now time.Time) error {
	if !helloLaidOut(hello) {
		return errors.New("hosttunnel: the client hello has no 32-byte session id")
	}
	random := hello[helloRandom : helloRandom+32]
	aead, err := markAEAD(secret, random)
	if err != nil {
		return err
	}
	clear(hello[helloSID:helloSIDEnd])
	aad := append([]byte(nil), hello...)
	var plain [markPlainBytes]byte
	binary.BigEndian.PutUint64(plain[:8], uint64(now.Unix()))
	aead.Seal(hello[helloSID:helloSID], random[20:32], plain[:], aad)
	return nil
}

// Mark opens the mark of the peeked hello. ok is false when there is none for secret.
func (h Hello) Mark(secret string) (id [32]byte, at time.Time, ok bool) {
	if !helloLaidOut(h.raw) {
		return id, at, false
	}
	random := h.raw[helloRandom : helloRandom+32]
	aead, err := markAEAD(secret, random)
	if err != nil {
		return id, at, false
	}
	copy(id[:], h.raw[helloSID:helloSIDEnd])
	aad := append([]byte(nil), h.raw...)
	clear(aad[helloSID:helloSIDEnd])
	plain, err := aead.Open(nil, random[20:32], id[:], aad)
	if err != nil || len(plain) != markPlainBytes || subtle.ConstantTimeCompare(plain[8:], make([]byte, 8)) != 1 {
		return [32]byte{}, time.Time{}, false
	}
	return id, time.Unix(int64(binary.BigEndian.Uint64(plain[:8])), 0), true
}

// helloMessage returns a copy of the ClientHello handshake message of buf when buf starts with one TLS handshake record
// that holds exactly that message, else nil (a hello split over records carries no mark the node reads).
func helloMessage(buf []byte) []byte {
	if len(buf) < 5 || buf[0] != 0x16 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(buf[3:5]))
	if len(buf) < 5+n || n < 4 {
		return nil
	}
	msg := buf[5 : 5+n]
	if msg[0] != 1 || int(msg[1])<<16|int(msg[2])<<8|int(msg[3]) != n-4 {
		return nil
	}
	return append([]byte(nil), msg...)
}

// Replay remembers the marks accepted within the window, so a recorded hello cannot open a second tunnel. The zero
// value is ready.
type Replay struct {
	mu   sync.Mutex
	seen map[[32]byte]time.Time
}

// Fresh reports whether a mark made at `at` is within MarkWindow of now and was not seen before, and remembers it. Only
// holders of the secret can make a mark, so the set needs no bound beyond the window.
func (r *Replay) Fresh(id [32]byte, at, now time.Time) bool {
	if now.Sub(at).Abs() > MarkWindow {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, t := range r.seen {
		if now.Sub(t) > 2*MarkWindow {
			delete(r.seen, k)
		}
	}
	if _, ok := r.seen[id]; ok {
		return false
	}
	if r.seen == nil {
		r.seen = make(map[[32]byte]time.Time)
	}
	r.seen[id] = now
	return true
}
