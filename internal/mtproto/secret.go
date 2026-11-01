// Package mtproto implements the client-facing half of a Telegram MTProto proxy.
//
// It speaks Fake TLS and obfuscated2, authenticates clients by their MTProxy
// secret, and relays plaintext MTProto to a Telegram datacenter. The package
// owns no listener, no panel state and no process; the node engine supplies
// connections and a credential list.
package mtproto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// KeyLen is the length of an MTProxy secret in bytes.
const KeyLen = 16

// Credential is one MTProxy secret. ID is opaque to this package and is what
// the observer callbacks receive.
type Credential struct {
	ID  string
	Key [KeyLen]byte
}

// ParseCredential parses an MTProxy secret. It accepts the bare 32-hex form and
// the link forms prefixed with ee (Fake TLS) or dd (secured obfuscated2). A
// Fake TLS link may carry the SNI domain as trailing hex; it is validated but
// is not part of the key.
func ParseCredential(id, secret string) (Credential, error) {
	key, err := ParseKey(secret)
	if err != nil {
		return Credential{}, err
	}
	return Credential{ID: id, Key: key}, nil
}

// ParseKey parses the secret part of an MTProxy link.
func ParseKey(secret string) ([KeyLen]byte, error) {
	var key [KeyLen]byte
	s := strings.TrimSpace(secret)
	switch {
	case len(s) == 2*KeyLen:
	case len(s) >= 2*KeyLen+2 && (strings.HasPrefix(s, "ee") || strings.HasPrefix(s, "dd")):
		domain := s[2*KeyLen+2:]
		if domain != "" {
			if len(domain)%2 != 0 {
				return key, fmt.Errorf("mtproto: odd-length domain hex in secret")
			}
			if _, err := hex.DecodeString(domain); err != nil {
				return key, fmt.Errorf("mtproto: domain hex in secret: %w", err)
			}
		}
		s = s[2:]
	default:
		return key, fmt.Errorf("mtproto: secret must be 32 hex characters, optionally prefixed with ee or dd")
	}
	raw, err := hex.DecodeString(s[:2*KeyLen])
	if err != nil {
		return key, fmt.Errorf("mtproto: secret hex: %w", err)
	}
	copy(key[:], raw)
	return key, nil
}

// NewKey returns a random MTProxy secret.
func NewKey() ([KeyLen]byte, error) {
	var key [KeyLen]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, fmt.Errorf("mtproto: random secret: %w", err)
	}
	return key, nil
}

// Hex returns the bare 32-hex secret used by the panel store.
func (c Credential) Hex() string { return hex.EncodeToString(c.Key[:]) }

// FakeTLSSecret returns the tg:// link secret for this key and SNI domain:
// ee + key hex + domain hex.
func FakeTLSSecret(key [KeyLen]byte, domain string) string {
	return "ee" + hex.EncodeToString(key[:]) + hex.EncodeToString([]byte(domain))
}

// SecureSecret returns the dd link secret for this key.
func SecureSecret(key [KeyLen]byte) string {
	return "dd" + hex.EncodeToString(key[:])
}
