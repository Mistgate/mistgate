package mtproto

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyLen is the length of an MTProto proxy key, the 16 bytes every other key of a connection is derived from.
const KeyLen = 16

// Credential is one key the server accepts. ID is the caller's name for it: what the Observer reports and what
// CloseCredential takes. It is not secret; Key is.
type Credential struct {
	ID  string
	Key [KeyLen]byte
}

// ParseCredential reads a proxy secret in any of the forms Telegram links carry: 32 hex characters (a plain obfuscated2
// proxy), "dd" and 32 hex (the same with padding), "ee", 32 hex and the Fake TLS domain in hex. All three give the same
// key. The server speaks only Fake TLS and takes the domains it accepts from Config.Domains, so the domain of an "ee"
// secret is only checked to be a plausible host name. Errors never quote the secret.
func ParseCredential(id, secret string) (Credential, error) {
	if id == "" {
		return Credential{}, errors.New("mtproto: credential without id")
	}
	s := strings.ToLower(strings.TrimSpace(secret))
	var keyHex, domainHex string
	switch {
	case len(s) == 2*KeyLen:
		keyHex = s
	case len(s) == 2+2*KeyLen && strings.HasPrefix(s, "dd"):
		keyHex = s[2:]
	case len(s) > 2+2*KeyLen && strings.HasPrefix(s, "ee"):
		keyHex, domainHex = s[2:2+2*KeyLen], s[2+2*KeyLen:]
	default:
		return Credential{}, fmt.Errorf("mtproto: credential %s: secret is not 32 hex characters, dd and 32, or ee, 32 and a domain", id)
	}
	raw, err := hex.DecodeString(keyHex)
	if err != nil {
		return Credential{}, fmt.Errorf("mtproto: credential %s: key is not hex", id)
	}
	if domainHex != "" {
		domain, err := hex.DecodeString(domainHex)
		if err != nil || !hostName(string(domain)) {
			return Credential{}, fmt.Errorf("mtproto: credential %s: the Fake TLS domain is not a host name in hex", id)
		}
	}
	c := Credential{ID: id}
	copy(c.Key[:], raw)
	return c, nil
}

// hostName reports whether s looks like a DNS name a client can put into SNI: letters, digits, '-' and '.', at least
// one dot, no empty label.
func hostName(s string) bool {
	if len(s) == 0 || len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
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

// normalizeDomain is the form domains are compared in: lower case, no trailing dot. "" when it is not a host name.
func normalizeDomain(s string) string {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if !hostName(s) {
		return ""
	}
	return s
}
