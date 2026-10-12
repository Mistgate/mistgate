package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

// fakeRelease serves <tag>/{manifest.json,manifest.sig,<binary>} like a GitHub release download prefix.
type fakeRelease struct {
	server   *httptest.Server
	tag      string
	key      ed25519.PublicKey
	binary   []byte
	override map[string][]byte
}

type fakeReleaseOptions struct {
	tag       string // the tag in the URL
	version   string // the version inside the manifest
	expires   int64  // manifest expiry (Unix)
	signWith  ed25519.PrivateKey
	platform  [2]string // os, arch listed in the manifest
	manifestN int64     // size recorded in the manifest (0: the real one)
}

func newFakeRelease(t *testing.T, binary []byte, opts fakeReleaseOptions) *fakeRelease {
	t.Helper()
	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if opts.tag == "" {
		opts.tag = "v1.2.3"
	}
	if opts.version == "" {
		opts.version = opts.tag
	}
	if opts.expires == 0 {
		opts.expires = time.Now().Add(24 * time.Hour).Unix()
	}
	if opts.platform == [2]string{} {
		opts.platform = [2]string{"linux", "amd64"}
	}
	if opts.signWith == nil {
		opts.signWith = priv
	}
	size := int64(len(binary))
	if opts.manifestN != 0 {
		size = opts.manifestN
	}
	sum := sha256.Sum256(binary)
	manifest := &release.Manifest{Schema: release.Schema, Version: opts.version, Built: 1_000, Expires: opts.expires,
		Files: []release.File{{OS: opts.platform[0], Arch: opts.platform[1], Name: "mistgate-node-" + opts.platform[0] + "-" + opts.platform[1],
			Size: size, SHA256: hex.EncodeToString(sum[:])}}}
	raw, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRelease{tag: opts.tag, key: pub, binary: binary, override: map[string][]byte{}}
	files := map[string][]byte{
		"/" + opts.tag + "/manifest.json":             raw,
		"/" + opts.tag + "/manifest.sig":              release.Sign(opts.signWith, raw),
		"/" + opts.tag + "/" + manifest.Files[0].Name: binary,
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if o, has := f.override[r.URL.Path]; has {
			body, ok = o, true
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRelease) source(version string) *ReleaseNodeBinaries {
	return &ReleaseNodeBinaries{Version: version, Key: f.key, Client: f.server.Client(), Base: f.server.URL + "/"}
}

func TestReleaseNodeBinariesStreamsTheSignedBinary(t *testing.T) {
	binary := bytes.Repeat([]byte("agent-bytes-"), 50_000) // 600 kB: more than one read
	f := newFakeRelease(t, binary, fakeReleaseOptions{})
	body, size, digest, err := f.source("v1.2.3").OpenNodeBinary(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	sum := sha256.Sum256(binary)
	if size != int64(len(binary)) || digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("size %d digest %q, want %d %q", size, digest, len(binary), hex.EncodeToString(sum[:]))
	}
	// the body is the live response, not a buffered copy: reading a small part pulls only a small part
	chunk := make([]byte, 4096)
	if _, err := io.ReadFull(body, chunk); err != nil || !bytes.Equal(chunk, binary[:4096]) {
		t.Fatalf("first chunk: %v", err)
	}
	rest, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(append(chunk, rest...), binary) {
		t.Fatalf("whole body: %d bytes, err %v", len(rest)+len(chunk), err)
	}
}

func TestReleaseNodeBinariesRefusesAnUntrustedRelease(t *testing.T) {
	binary := []byte("agent")
	_, otherPriv, _ := release.GenerateKey()
	for _, tc := range []struct {
		name    string
		opts    fakeReleaseOptions
		version string // the panel's own version; default v1.2.3
		goarch  string
		mutate  func(*fakeRelease, *ReleaseNodeBinaries)
		want    error  // an error the result must wrap
		text    string // or a message it must contain
	}{
		{name: "manifest signed by another key", opts: fakeReleaseOptions{signWith: otherPriv}, want: release.ErrBadSignature},
		{name: "manifest of another version", opts: fakeReleaseOptions{version: "v1.2.4"}, text: `the manifest is for "v1.2.4"`},
		{name: "expired manifest", opts: fakeReleaseOptions{expires: 2_000}, want: release.ErrExpired},
		{name: "no file for the platform", opts: fakeReleaseOptions{platform: [2]string{"linux", "arm64"}}, want: release.ErrNoFile},
		{name: "size differs from the manifest", opts: fakeReleaseOptions{manifestN: 99}, want: release.ErrSizeMismatch},
		{name: "development build", opts: fakeReleaseOptions{}, version: "0.1.0-dev", text: "not a release tag"},
		{name: "pre-release version", opts: fakeReleaseOptions{}, version: "v1.2.3-rc1", text: "not a release tag"},
		{name: "build without a release key", mutate: func(_ *fakeRelease, s *ReleaseNodeBinaries) { s.Key = nil }, text: "no release key"},
		{name: "release not published", version: "v9.9.9", text: "HTTP 404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRelease(t, binary, tc.opts)
			version := tc.version
			if version == "" {
				version = "v1.2.3"
			}
			source := f.source(version)
			if tc.mutate != nil {
				tc.mutate(f, source)
			}
			body, _, _, err := source.OpenNodeBinary(context.Background(), "linux", "amd64")
			if err == nil {
				body.Close()
				t.Fatal("an untrusted release was accepted")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.text)
			}
		})
	}
}

// A manifest that fits the metadata limits is read whole; one over the limit, or a longer signature, is not.
func TestReleaseNodeBinariesBoundsTheMetadata(t *testing.T) {
	f := newFakeRelease(t, []byte("agent"), fakeReleaseOptions{})
	f.override["/v1.2.3/manifest.json"] = bytes.Repeat([]byte(" "), release.MaxManifestBytes+1)
	if body, _, _, err := f.source("v1.2.3").OpenNodeBinary(context.Background(), "linux", "amd64"); err == nil {
		body.Close()
		t.Fatal("an oversized manifest was accepted")
	}
	g := newFakeRelease(t, []byte("agent"), fakeReleaseOptions{})
	g.override["/v1.2.3/manifest.sig"] = make([]byte, ed25519.SignatureSize+1)
	if body, _, _, err := g.source("v1.2.3").OpenNodeBinary(context.Background(), "linux", "amd64"); err == nil {
		body.Close()
		t.Fatal("an oversized signature was accepted")
	}
}

func TestReleaseNodeBinariesUsesTheDefaultBase(t *testing.T) {
	old := releaseDownloadBase
	t.Cleanup(func() { releaseDownloadBase = old })
	f := newFakeRelease(t, []byte("agent"), fakeReleaseOptions{})
	releaseDownloadBase = f.server.URL + "/"
	source := f.source("v1.2.3")
	source.Base = ""
	body, _, _, err := source.OpenNodeBinary(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
}
