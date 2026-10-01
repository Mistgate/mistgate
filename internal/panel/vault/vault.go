// Package vault encrypts secrets at rest (XChaCha20-Poly1305, AAD = record id) and
// provides a string type that never prints its value.
package vault

import (
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the master key length in bytes.
const KeySize = chacha20poly1305.KeySize

const keyFile = "master.key"

// Vault seals and opens secrets with the master key.
type Vault struct {
	aead cipher.AEAD
	key  []byte
}

// New builds a Vault from a 32-byte master key.
func New(key []byte) (*Vault, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("vault: master key must be %d bytes", KeySize)
	}
	return &Vault{aead: aead, key: append([]byte(nil), key...)}, nil
}

// Derive is a key of KeySize bytes for one purpose (HKDF-SHA256 of the master key, the label as context): the
// same master key and label always give the same key, and keys of different labels reveal nothing about each other
// or about the master key. For values that must be recomputable without storing anything (page passwords).
func (v *Vault) Derive(label string) []byte {
	k, err := hkdf.Key(sha256.New, v.key, nil, label, KeySize)
	if err != nil { // only for a length over 255 hashes
		panic(err)
	}
	return k
}

// Seal encrypts plaintext bound to recordID (the AAD): a blob sealed for one record
// cannot be opened as another. The random nonce is prepended to the result.
func (v *Vault) Seal(plaintext []byte, recordID string) []byte {
	nonce := make([]byte, v.aead.NonceSize(), v.aead.NonceSize()+len(plaintext)+v.aead.Overhead())
	rand.Read(nonce) // never fails on supported platforms
	return v.aead.Seal(nonce, nonce, plaintext, []byte(recordID))
}

// Open reverses Seal. It fails if the blob was tampered with or recordID differs.
func (v *Vault) Open(blob []byte, recordID string) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(blob) < n+v.aead.Overhead() {
		return nil, errors.New("vault: ciphertext too short")
	}
	pt, err := v.aead.Open(nil, blob[:n], blob[n:], []byte(recordID))
	if err != nil {
		return nil, errors.New("vault: cannot decrypt (wrong key, record id or corrupted data)")
	}
	return pt, nil
}

// LoadKey reads the master key: $CREDENTIALS_DIRECTORY/master.key (systemd
// LoadCredential) if present, else <dataDir>/master.key. With create set, a missing
// key file in dataDir is generated (mode 0600); otherwise a missing key is an error.
func LoadKey(dataDir string, create bool) ([]byte, error) {
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		if key, err := readKey(filepath.Join(dir, keyFile)); err == nil {
			return key, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	path := filepath.Join(dataDir, keyFile)
	key, err := readKey(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) || !create {
		return key, err
	}
	key = make([]byte, KeySize)
	rand.Read(key)
	// O_EXCL: never overwrite a key that appeared in the meantime.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

func readKey(path string) ([]byte, error) {
	// A key that other users can read is a key that has leaked. Windows has no POSIX mode
	// bits (every file reports 0666), so the check is skipped there.
	if fi, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("vault: %s is accessible to group or others (mode %04o); run chmod 600 on it", path, fi.Mode().Perm())
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("vault: %s must hold exactly %d bytes, has %d", path, KeySize, len(key))
	}
	return key, nil
}

// Redacted is a secret string: every formatting path prints [REDACTED]. Use Reveal
// at the single place the value is really needed.
type Redacted string

const redacted = "[REDACTED]"

func (Redacted) String() string               { return redacted }
func (Redacted) GoString() string             { return redacted }
func (Redacted) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Redacted) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Redacted) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (Redacted) Format(f fmt.State, _ rune)   { f.Write([]byte(redacted)) }
func (r Redacted) Reveal() string             { return string(r) }
