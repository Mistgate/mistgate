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

// ErrReleaseKeyMismatch is LoadReleasePublicKey's answer when <data-dir>/release.pub and the compiled-in key differ.
var ErrReleaseKeyMismatch = errors.New("buildinfo: <data-dir>/release.pub is not the release key compiled into this binary; " +
	"nothing is trusted until you confirm the new key with `mistgate release trust-key`")

// LoadReleasePublicKey is the installation's release trust root. The first keyed build stores its public key in the
// data directory. A later build with the same key uses it; one with another compiled-in key gets
// ErrReleaseKeyMismatch (a key rotation is an explicit step, TrustCompiledReleaseKey, never a side effect of a file
// in the data directory or of a new binary); a build without a key keeps using the stored one.
func LoadReleasePublicKey(dataDir string) (ed25519.PublicKey, error) {
	if dataDir == "" {
		return nil, errors.New("buildinfo: data directory is empty")
	}
	path := filepath.Join(dataDir, "release.pub")
	compiled, compiledErr := ReleasePublicKey()
	if compiledErr != nil && !errors.Is(compiledErr, ErrUnsignedBuild) {
		return nil, compiledErr
	}
	b, err := os.ReadFile(path)
	if err == nil {
		stored, err := decodeReleaseKey(strings.TrimSpace(string(b)))
		if err != nil {
			return nil, err
		}
		if compiledErr == nil && !compiled.Equal(stored) {
			return nil, ErrReleaseKeyMismatch
		}
		return stored, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("buildinfo: read release public key: %w", err)
	}
	pub, err := compiled, compiledErr
	if err != nil {
		return nil, err
	}
	// os.Link publishes the fully written key atomically without replacing an existing trust root: two simultaneous
	// first starts could otherwise disagree about which installation key won.
	if err := writeReleaseKey(path, pub, os.Link); err != nil {
		// A concurrent panel start may have created the file. Its contents are authoritative.
		if b, readErr := os.ReadFile(path); readErr == nil {
			return decodeReleaseKey(strings.TrimSpace(string(b)))
		}
		return nil, fmt.Errorf("buildinfo: persist release public key: %w", err)
	}
	return pub, nil
}

// TrustCompiledReleaseKey makes the key compiled into this binary the installation's release key: the explicit step
// of a key rotation. It returns the key it replaced (nil when there was none).
func TrustCompiledReleaseKey(dataDir string) (previous, current ed25519.PublicKey, err error) {
	current, err = ReleasePublicKey()
	if err != nil {
		return nil, nil, err
	}
	if dataDir == "" {
		return nil, nil, errors.New("buildinfo: data directory is empty")
	}
	path := filepath.Join(dataDir, "release.pub")
	if b, err := os.ReadFile(path); err == nil {
		previous, _ = decodeReleaseKey(strings.TrimSpace(string(b)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if err := writeReleaseKey(path, current, os.Rename); err != nil {
		return nil, nil, err
	}
	return previous, current, nil
}

func decodeReleaseKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("buildinfo: release.pub is not a base64 ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

// writeReleaseKey writes the key to a temporary file and publishes it at path with publish (os.Link: never over an
// existing key; os.Rename: replace it).
func writeReleaseKey(path string, pub ed25519.PublicKey, publish func(oldname, newname string) error) error {
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
	return publish(name, path)
}

// KeyFingerprint is the first 16 hex characters of SHA-256 of a public key: what `mistgate release keygen`
// prints next to the key and the Updates page shows, to compare by eye.
func KeyFingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}
