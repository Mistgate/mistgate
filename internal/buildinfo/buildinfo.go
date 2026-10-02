// Package buildinfo holds values stamped into the binaries at build time.
package buildinfo

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Version is set with -ldflags "-X github.com/mistgate/mistgate/internal/buildinfo.Version=1.2.3".
var Version = "0.1.0-dev"

// Built is the Unix time (decimal seconds) of the source commit, set with
// -X github.com/mistgate/mistgate/internal/buildinfo.Built=<git log -1 --format=%ct>. It orders releases: the version
// string ("0.1.0-<hash>") does not. Empty = a build without it (go run, go test): BuiltUnix is 0.
var Built = ""

// ReleaseKey is the owner's release public key, 32 bytes of ed25519 in standard base64, set with
// -X github.com/mistgate/mistgate/internal/buildinfo.ReleaseKey=<base64>. Both binaries carry it; the node agent
// trusts an update only if the owner signed it. Empty = unsigned build: self-update is off.
var ReleaseKey = ""

// ErrUnsignedBuild is what a binary without ReleaseKey answers to anything that needs to verify a release.
var ErrUnsignedBuild = errors.New("unsigned build: update by hand")

// BuiltUnix is Built as a number; 0 when it is empty or not a positive decimal integer.
func BuiltUnix() int64 {
	n, err := strconv.ParseInt(Built, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ReleasePublicKey decodes ReleaseKey. It returns ErrUnsignedBuild when there is none, and another error when
// the stamped value is not a valid key (a broken build must not look like an unsigned one).
func ReleasePublicKey() (ed25519.PublicKey, error) {
	if ReleaseKey == "" {
		return nil, ErrUnsignedBuild
	}
	b, err := base64.StdEncoding.DecodeString(ReleaseKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("buildinfo: ReleaseKey is not a base64 ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

// LoadReleasePublicKey keeps the release trust root stable when a panel binary is replaced by a generic GitHub
// release build. The first keyed build stores its public key in the data directory; subsequent builds use that
// installation key even when the downloaded binary was built without a per-installation key.
func LoadReleasePublicKey(dataDir string) (ed25519.PublicKey, error) {
	if dataDir == "" {
		return nil, errors.New("buildinfo: data directory is empty")
	}
	path := filepath.Join(dataDir, "release.pub")
	b, err := os.ReadFile(path)
	if err == nil {
		return decodeReleaseKey(strings.TrimSpace(string(b)))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("buildinfo: read release public key: %w", err)
	}
	pub, err := ReleasePublicKey()
	if err != nil {
		return nil, err
	}
	if err := writeReleaseKey(path, pub); err != nil {
		// A concurrent panel start may have created the file. Its contents are authoritative.
		if b, readErr := os.ReadFile(path); readErr == nil {
			return decodeReleaseKey(strings.TrimSpace(string(b)))
		}
		return nil, fmt.Errorf("buildinfo: persist release public key: %w", err)
	}
	return pub, nil
}

func decodeReleaseKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("buildinfo: release.pub is not a base64 ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

func writeReleaseKey(path string, pub ed25519.PublicKey) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".release.pub-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(base64.StdEncoding.EncodeToString(pub) + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Publish the fully written key atomically without replacing an existing trust root. os.Rename
	// replaces the destination on Unix, so two simultaneous first starts could otherwise disagree
	// about which installation key won.
	return os.Link(name, path)
}

// KeyFingerprint is the first 16 hex characters of SHA-256 of a public key: what `mistgate release keygen`
// prints next to the key and the Updates page shows, to compare by eye.
func KeyFingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}
