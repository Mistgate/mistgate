package webproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"sync"
)

const (
	capabilityDomain   = "tdesktop-web-proxy-bridge-v1\n"
	capabilityTextSize = 43
	plainSecretSize    = 16
	ddSecretSize       = plainSecretSize + 1
)

const ddSecretPrefix byte = 0xdd

type capabilityError string

func (err capabilityError) Error() string { return string(err) }

const (
	errInvalidHost         capabilityError = "invalid hostname"
	errInvalidSecret       capabilityError = "invalid secret"
	errInvalidCredential   capabilityError = "invalid credential"
	errDuplicateCapability capabilityError = "ambiguous credential capability"
	errNilCapabilityIndex  capabilityError = "nil capability index"
)

// NormalizeHost lowercases a canonical ASCII/IDNA hostname. Unicode names must
// be converted to their ASCII IDNA form before they reach this package.
func NormalizeHost(host string) (string, error) {
	if len(host) == 0 || len(host) > 253 {
		return "", errInvalidHost
	}
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return "", errInvalidHost
		}
	}

	host = strings.ToLower(host)
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errInvalidHost
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", errInvalidHost
			}
		}
	}
	return host, nil
}

// DeriveCapability returns the unpadded base64url HMAC capability for host and
// secret. A secret is either 16 raw bytes or 0xdd followed by those 16 bytes.
func DeriveCapability(host string, secret []byte) (string, error) {
	normalizedHost, err := NormalizeHost(host)
	if err != nil {
		return "", err
	}
	if !validSecret(secret) {
		return "", errInvalidSecret
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(capabilityDomain))
	_, _ = mac.Write([]byte(normalizedHost))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

type Credential struct {
	ID     string
	Secret []byte
}

type capabilityEntry struct {
	value        [capabilityTextSize]byte
	credentialID string
}

// CapabilityIndex holds the capabilities for one hostname. Rebuild atomically
// replaces the snapshot while readers can continue using the previous one.
type CapabilityIndex struct {
	mu      sync.RWMutex
	host    string
	entries []capabilityEntry
}

func NewCapabilityIndex(host string) (*CapabilityIndex, error) {
	normalizedHost, err := NormalizeHost(host)
	if err != nil {
		return nil, err
	}
	return &CapabilityIndex{host: normalizedHost}, nil
}

// Rebuild replaces all entries. Each credential accepts the plain and dd-prefixed
// forms derived from its same underlying 16-byte secret.
func (idx *CapabilityIndex) Rebuild(credentials []Credential) error {
	if idx == nil {
		return errNilCapabilityIndex
	}

	entries := make([]capabilityEntry, 0)
	seen := make(map[[capabilityTextSize]byte]string)
	for _, credential := range credentials {
		if credential.ID == "" {
			return errInvalidCredential
		}
		plain, ok := plainSecret(credential.Secret)
		if !ok {
			return errInvalidSecret
		}

		var prefixed [ddSecretSize]byte
		prefixed[0] = ddSecretPrefix
		copy(prefixed[1:], plain[:])
		forms := [2][]byte{plain[:], prefixed[:]}
		for _, secret := range forms {
			encoded := deriveCapabilityBytes(idx.host, secret)
			if previousID, exists := seen[encoded]; exists {
				if previousID != credential.ID {
					return errDuplicateCapability
				}
				continue
			}
			seen[encoded] = credential.ID
			entries = append(entries, capabilityEntry{value: encoded, credentialID: credential.ID})
		}
	}

	idx.mu.Lock()
	idx.entries = entries
	idx.mu.Unlock()
	return nil
}

// Lookup scans every stored capability and compares each fixed-width value in
// constant time with respect to the bytes of a canonical 43-character input.
func (idx *CapabilityIndex) Lookup(capability string) (string, bool) {
	if idx == nil || len(capability) != capabilityTextSize {
		return "", false
	}
	candidate := []byte(capability)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	found := 0
	selected := uint64(0)
	for i := range idx.entries {
		match := subtle.ConstantTimeCompare(candidate, idx.entries[i].value[:])
		mask := uint64(0) - uint64(match)
		selected = (selected &^ mask) | (uint64(i) & mask)
		found |= match
	}
	if found == 0 {
		return "", false
	}
	return idx.entries[selected].credentialID, true
}

func validSecret(secret []byte) bool {
	return len(secret) == plainSecretSize || len(secret) == ddSecretSize && secret[0] == ddSecretPrefix
}

func plainSecret(secret []byte) ([plainSecretSize]byte, bool) {
	var plain [plainSecretSize]byte
	switch len(secret) {
	case plainSecretSize:
		copy(plain[:], secret)
		return plain, true
	case ddSecretSize:
		if secret[0] != ddSecretPrefix {
			return plain, false
		}
		copy(plain[:], secret[1:])
		return plain, true
	default:
		return plain, false
	}
}

func deriveCapabilityBytes(host string, secret []byte) [capabilityTextSize]byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(capabilityDomain))
	_, _ = mac.Write([]byte(host))
	encoded := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	var value [capabilityTextSize]byte
	copy(value[:], encoded)
	return value
}
