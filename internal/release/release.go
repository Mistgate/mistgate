// Package release is the format of a signed release bundle and the checks on it. The owner signs manifest.json
// offline (`mistgate release sign`); the panel shows the bundle and relays it; the node agent is the one that
// verifies it before it replaces its own binary. One package for all three so they cannot disagree.
//
// Trust is a single ed25519 key: the public half is compiled into both binaries (buildinfo.ReleaseKey), the private
// half never leaves the owner. The signature covers the exact bytes of manifest.json, so nothing is ever
// re-serialised before it is verified.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// Schema is the only manifest schema this code understands; a different one is refused, not guessed at.
	Schema = 1
	// MaxFileSize bounds a listed file (a node binary is about 30 MB).
	MaxFileSize = 256 << 20
	maxFiles    = 16
	// ManifestName and SignatureName are the file names inside the bundle directory.
	ManifestName  = "manifest.json"
	SignatureName = "manifest.sig"
	// MaxManifestBytes bounds what a parser will read (the panel sends it inside a command).
	MaxManifestBytes = 64 << 10
)

// Sentinel errors. Code maps them to the command error vocabulary of agent.proto ("UPDATE").
var (
	ErrBadSignature = errors.New("release: signature does not verify")
	ErrBadManifest  = errors.New("release: bad manifest")
	// ErrUnsupportedSchema is an ErrBadManifest whose only fault is the schema number (a newer release format).
	ErrUnsupportedSchema = fmt.Errorf("%w: unsupported schema", ErrBadManifest)
	ErrExpired           = errors.New("release: manifest expired")
	ErrDowngrade         = errors.New("release: manifest is older than the running build")
	// ErrUpToDate is not a failure: the manifest is exactly as new as the running build.
	ErrUpToDate     = errors.New("release: already current")
	ErrNoFile       = errors.New("release: no file for this platform")
	ErrSizeMismatch = errors.New("release: file size differs from the manifest")
	ErrHashMismatch = errors.New("release: file sha256 differs from the manifest")
)

// Code is the stable error code of agent.proto for one of this package's errors ("" for anything else).
func Code(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrBadSignature):
		return "bad_signature"
	case errors.Is(err, ErrBadManifest):
		return "bad_manifest"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrDowngrade):
		return "downgrade"
	case errors.Is(err, ErrUpToDate):
		return "up_to_date"
	case errors.Is(err, ErrNoFile):
		return "no_file_for_platform"
	case errors.Is(err, ErrSizeMismatch):
		return "size_mismatch"
	case errors.Is(err, ErrHashMismatch):
		return "hash_mismatch"
	}
	return ""
}

// File is one binary of a release.
type File struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"` // lowercase hex
}

// Manifest is what the owner signs. Built (Unix time of the source commit), not Version, orders releases.
type Manifest struct {
	Schema  int    `json:"schema"`
	Version string `json:"version"`
	Built   int64  `json:"built"`
	Expires int64  `json:"expires"` // Unix seconds; bounds how long a captured manifest stays usable
	Files   []File `json:"files"`
}

var (
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	platRe    = regexp.MustCompile(`^[a-z0-9_]{2,16}$`)
	hexRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Validate checks the manifest is well formed. It says nothing about time or about the running build (see Check).
func (m *Manifest) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrBadManifest, fmt.Sprintf(format, a...))
	}
	if m.Schema != Schema {
		return fmt.Errorf("%w (%d, want %d)", ErrUnsupportedSchema, m.Schema, Schema)
	}
	if !versionRe.MatchString(m.Version) {
		return bad("version %q", m.Version)
	}
	if m.Built <= 0 {
		return bad("built must be a positive Unix time")
	}
	if m.Expires <= m.Built {
		return bad("expires must be after built")
	}
	if len(m.Files) == 0 || len(m.Files) > maxFiles {
		return bad("%d files (want 1 to %d)", len(m.Files), maxFiles)
	}
	plats, names := map[string]bool{}, map[string]bool{}
	for i, f := range m.Files {
		switch {
		case !platRe.MatchString(f.OS) || !platRe.MatchString(f.Arch):
			return bad("file %d: os/arch %q/%q", i, f.OS, f.Arch)
		case !nameRe.MatchString(f.Name) || strings.Contains(f.Name, ".."):
			return bad("file %d: name %q", i, f.Name)
		case f.Size <= 0 || f.Size > MaxFileSize:
			return bad("file %d: size %d", i, f.Size)
		case !hexRe.MatchString(f.SHA256):
			return bad("file %d: sha256", i)
		case plats[f.OS+"/"+f.Arch]:
			return bad("file %d: duplicate %s/%s", i, f.OS, f.Arch)
		case names[f.Name]:
			return bad("file %d: duplicate name %q", i, f.Name)
		}
		plats[f.OS+"/"+f.Arch], names[f.Name] = true, true
	}
	return nil
}

// Marshal validates and encodes the manifest: the bytes that get signed and written to manifest.json.
func (m *Manifest) Marshal() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Parse decodes and validates manifest bytes. It is strict: an unknown field, trailing data or a schema other than 1
// is ErrBadManifest, because a signed manifest must be understood completely or not at all. Verify the signature
// first (or use Verify): Parse alone trusts nothing.
func Parse(b []byte) (*Manifest, error) {
	if len(b) == 0 || len(b) > MaxManifestBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadManifest, len(b))
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	if head.Schema != Schema { // a newer format may have fields this code would reject as unknown
		return nil, fmt.Errorf("%w (%d, want %d)", ErrUnsupportedSchema, head.Schema, Schema)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing data", ErrBadManifest)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Sign returns the detached signature of the manifest bytes.
func Sign(priv ed25519.PrivateKey, manifest []byte) []byte { return ed25519.Sign(priv, manifest) }

// Verify checks the signature over the exact manifest bytes and then parses them. A manifest whose signature
// fails is never parsed.
func Verify(pub ed25519.PublicKey, manifest, sig []byte) (*Manifest, error) {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, manifest, sig) {
		return nil, ErrBadSignature
	}
	return Parse(manifest)
}

// Check decides whether a manifest may be installed by a build whose Built is ownBuilt, at time now: ErrExpired,
// ErrDowngrade (older), ErrUpToDate (same, a no-op that is not an error for the caller), else nil (newer).
func (m *Manifest) Check(now time.Time, ownBuilt int64) error {
	switch {
	case now.Unix() > m.Expires:
		return ErrExpired
	case m.Built < ownBuilt:
		return ErrDowngrade
	case m.Built == ownBuilt:
		return ErrUpToDate
	}
	return nil
}

// FileFor picks the file for a platform (runtime.GOOS, runtime.GOARCH).
func (m *Manifest) FileFor(goos, goarch string) (File, error) {
	for _, f := range m.Files {
		if f.OS == goos && f.Arch == goarch {
			return f, nil
		}
	}
	return File{}, fmt.Errorf("%w: %s/%s", ErrNoFile, goos, goarch)
}

// FindName returns the listed file with this exact name (the panel uses it to serve downloads).
func (m *Manifest) FindName(name string) (File, bool) {
	for _, f := range m.Files {
		if f.Name == name {
			return f, true
		}
	}
	return File{}, false
}

// VerifyFile reads r to the end and compares size and SHA-256 with the manifest entry.
func VerifyFile(f File, r io.Reader) error {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, f.Size+1))
	if err != nil {
		return err
	}
	if n != f.Size {
		return fmt.Errorf("%w: %d bytes, manifest says %d", ErrSizeMismatch, n, f.Size)
	}
	if hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return ErrHashMismatch
	}
	return nil
}

// ParseBinaryName splits "mistgate-node-linux-amd64" into ("mistgate-node", "linux", "amd64").
func ParseBinaryName(name string) (base, goos, goarch string, err error) {
	parts := strings.Split(name, "-")
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("release: %q is not <name>-<os>-<arch>", name)
	}
	goos, goarch = parts[len(parts)-2], parts[len(parts)-1]
	if !platRe.MatchString(goos) || !platRe.MatchString(goarch) {
		return "", "", "", fmt.Errorf("release: %q is not <name>-<os>-<arch>", name)
	}
	return strings.Join(parts[:len(parts)-2], "-"), goos, goarch, nil
}

// FileFromPath hashes a binary and builds its manifest entry; os and arch come from the file name
// (ParseBinaryName), so the name must be "<name>-<os>-<arch>".
func FileFromPath(path string) (File, error) {
	name := filepath.Base(path)
	_, goos, goarch, err := ParseBinaryName(name)
	if err != nil {
		return File{}, err
	}
	fh, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer fh.Close()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return File{}, err
	}
	return File{OS: goos, Arch: goarch, Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// GenerateKey makes a release key pair.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// EncodePublicKey is the base64 form that goes into buildinfo.ReleaseKey.
func EncodePublicKey(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// EncodePrivateKey is the key file content: the 32-byte seed in base64 (the public half is derived from it).
func EncodePrivateKey(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv.Seed())
}

// DecodePrivateKey reads what EncodePrivateKey wrote (surrounding whitespace allowed).
func DecodePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, errors.New("release: not a release private key file")
	}
	return ed25519.NewKeyFromSeed(b), nil
}
