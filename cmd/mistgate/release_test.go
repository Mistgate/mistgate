package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/release"
)

// keygen writes the private key with mode 0600, never replaces one, and prints the public key and its fingerprint.
func TestReleaseKeygen(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "release.key")
	var out bytes.Buffer
	if err := runRelease([]string{"keygen", "--out", keyFile}, &out); err != nil {
		t.Fatal(err)
	}
	pubB64 := lineValue(t, out.String(), "public key: ")
	fp := lineValue(t, out.String(), "fingerprint: ")
	if !strings.Contains(out.String(), "Keep the key file offline and back it up") {
		t.Fatalf("no warning: %q", out.String())
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := release.DecodePrivateKey(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := decodeTestKey(pubB64)
	if err != nil || !priv.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatalf("the printed public key is not the key file's: %v", err)
	}
	if fp != buildinfo.KeyFingerprint(pub) {
		t.Fatalf("fingerprint %q", fp)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(keyFile); fi.Mode().Perm() != 0o600 {
			t.Fatalf("private key mode %v, want 0600", fi.Mode().Perm())
		}
	}
	// a second run with the same path refuses and leaves the key alone
	if err := runRelease([]string{"keygen", "--out", keyFile}, &out); err == nil {
		t.Fatal("an existing key file was overwritten")
	}
	if again, _ := os.ReadFile(keyFile); !bytes.Equal(again, raw) {
		t.Fatal("the key file changed")
	}
	if err := runRelease([]string{"keygen"}, &out); err == nil {
		t.Fatal("keygen without --out")
	}
	if err := runRelease([]string{"frobnicate"}, &out); err == nil {
		t.Fatal("an unknown subcommand")
	}
	if err := runRelease(nil, &out); err == nil {
		t.Fatal("no subcommand")
	}
}

func lineValue(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, prefix); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("no %q line in:\n%s", prefix, out)
	return ""
}

func newKeyFile(t *testing.T) (keyFile, pubB64 string) {
	t.Helper()
	keyFile = filepath.Join(t.TempDir(), "release.key")
	var out bytes.Buffer
	if err := runRelease([]string{"keygen", "--out", keyFile}, &out); err != nil {
		t.Fatal(err)
	}
	return keyFile, lineValue(t, out.String(), "public key: ")
}

func makeBinaries(t *testing.T, names ...string) (dir string, paths []string) {
	t.Helper()
	dir = t.TempDir()
	for i, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, bytes.Repeat([]byte{byte('a' + i)}, 1000+i), 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

const testCommitTime = 1_789_000_000

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Alice", "-c", "user.email=alice@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=1789000000 +0000", "GIT_COMMITTER_DATE=1789000000 +0000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// makeSource is a clean checkout of tag at testCommitTime, with a built SPA (untracked, as after pnpm build).
func makeSource(t *testing.T, tag string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "go.mod")
	git(t, dir, "commit", "-q", "-m", "release")
	git(t, dir, "tag", tag)
	if err := os.MkdirAll(filepath.Join(dir, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "dist", "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// rebuildAs makes the rebuild of a binary produce the bytes of the file with the same name in dir (a faithful CI) and
// records what it was asked to build.
func rebuildAs(t *testing.T, dir string) *[]string {
	t.Helper()
	var calls []string
	previous := releaseRebuild
	releaseRebuild = func(src, name, version string, built int64, key, out string) error {
		calls = append(calls, strings.Join([]string{name, version, key}, " "))
		if built != testCommitTime {
			t.Errorf("rebuilt %s with built %d", name, built)
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o755)
	}
	t.Cleanup(func() { releaseRebuild = previous })
	return &calls
}

// The bundle `sign` writes verifies with the printed public key, file by file; flags may follow the binaries. A
// signer built locally (any version) signs any tag; node and panel binaries get their own manifests.
func TestReleaseSignRoundTrip(t *testing.T) {
	keyFile, pubB64 := newKeyFile(t)
	src := makeSource(t, "v0.1.4")
	binDir, bins := makeBinaries(t, "mistgate-node-linux-amd64", "mistgate-node-linux-arm64", "mistgate-linux-amd64")
	calls := rebuildAs(t, binDir)
	outDir := filepath.Join(t.TempDir(), "dist")
	now := time.Unix(1_790_000_000, 0)
	var out bytes.Buffer
	args := []string{"--key", keyFile, "--version", "v0.1.4", "--source", src, "--expires", "30d", bins[0], bins[1], bins[2], "--out", outDir}
	if err := releaseSign(args, &out, now); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 || (*calls)[0] != "mistgate-node-linux-amd64 v0.1.4 "+pubB64 {
		t.Fatalf("rebuilds: %q", *calls)
	}
	pub, err := decodeTestKey(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	sig, _ := os.ReadFile(filepath.Join(outDir, "manifest.sig"))
	m, err := release.Verify(pub, body, sig)
	if err != nil {
		t.Fatalf("the written bundle does not verify with the printed key: %v", err)
	}
	if m.Version != "v0.1.4" || m.Built != testCommitTime || m.Expires != now.Add(30*24*time.Hour).Unix() || len(m.Files) != 2 {
		t.Fatalf("manifest %+v", m)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes: it is raw ed25519, not base64", len(sig))
	}
	panelBody, _ := os.ReadFile(filepath.Join(outDir, "panel-manifest.json"))
	panelSig, _ := os.ReadFile(filepath.Join(outDir, "panel-manifest.sig"))
	pm, err := release.VerifyPanel(pub, panelBody, panelSig)
	if err != nil || len(pm.Files) != 1 || pm.Files[0].Name != "mistgate-linux-amd64" || pm.Built != testCommitTime {
		t.Fatalf("panel manifest %+v: %v", pm, err)
	}
	for _, f := range m.Files {
		fh, err := os.Open(filepath.Join(outDir, f.Name))
		if err != nil {
			t.Fatal(err)
		}
		err = release.VerifyFile(f, fh)
		fh.Close()
		if err != nil {
			t.Fatalf("copied file %s: %v", f.Name, err)
		}
	}
	for _, want := range []string{"version v0.1.4", "mistgate-node-linux-amd64", "mistgate-linux-amd64", "signed with key " + buildinfo.KeyFingerprint(pub)} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// another key does not verify it
	_, otherPub := newKeyFile(t)
	other, _ := decodeTestKey(otherPub)
	if _, err := release.Verify(other, body, sig); err == nil {
		t.Fatal("verified with a foreign key")
	}
	// tampering with a copied binary is noticed by the hash check
	if err := os.WriteFile(filepath.Join(outDir, pm.Files[0].Name), []byte("evil"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBundle(outDir, pub); err == nil {
		t.Fatal("a swapped binary verifies")
	}
}

func TestReleaseSignRefusals(t *testing.T) {
	keyFile, _ := newKeyFile(t)
	src := makeSource(t, "1.0.0")
	binDir, good := makeBinaries(t, "mistgate-node-linux-amd64")
	rebuildAs(t, binDir)
	_, noPlatform := makeBinaries(t, "mistgate-node")
	_, spaced := makeBinaries(t, "my node-linux-amd64")
	_, other := makeBinaries(t, "tool-linux-amd64")
	dirArg := t.TempDir()
	now := time.Unix(1_790_000_000, 0)
	base := func(extra ...string) []string {
		return append([]string{"--key", keyFile, "--version", "1.0.0", "--source", src}, extra...)
	}
	badKey := filepath.Join(t.TempDir(), "bad.key")
	os.WriteFile(badKey, []byte("not a key"), 0o600)
	for _, tc := range []struct {
		name string
		args func(out string) []string
	}{
		{"built is not the commit time", func(o string) []string { return base("--built", "1789000001", good[0], "--out", o) }},
		{"negative built", func(o string) []string { return base("--built", "-5", good[0], "--out", o) }},
		{"a version that is not a tag", func(o string) []string {
			return []string{"--key", keyFile, "--version", "1.0 beta!", "--source", src, good[0], "--out", o}
		}},
		{"no version", func(o string) []string { return []string{"--key", keyFile, "--source", src, good[0], "--out", o} }},
		{"a name without platform", func(o string) []string { return base(noPlatform[0], "--out", o) }},
		{"an unsafe name", func(o string) []string { return base(spaced[0], "--out", o) }},
		{"not a Mistgate binary", func(o string) []string { return base(other[0], "--out", o) }},
		{"a directory", func(o string) []string { return base(dirArg, "--out", o) }},
		{"a missing file", func(o string) []string { return base(filepath.Join(dirArg, "mistgate-node-linux-amd64"), "--out", o) }},
		{"no binaries", func(o string) []string { return base("--out", o) }},
		{"no out", func(string) []string { return base(good[0]) }},
		{"bad expires", func(o string) []string { return base("--expires", "soon", good[0], "--out", o) }},
		{"zero days", func(o string) []string { return base("--expires", "0d", good[0], "--out", o) }},
		{"negative duration", func(o string) []string { return base("--expires", "-5h", good[0], "--out", o) }},
		{"a bad key file", func(o string) []string {
			return []string{"--key", badKey, "--version", "1.0.0", "--source", src, good[0], "--out", o}
		}},
		{"a missing key file", func(o string) []string {
			return []string{"--key", filepath.Join(dirArg, "nope"), "--version", "1.0.0", "--source", src, good[0], "--out", o}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outDir := filepath.Join(t.TempDir(), "dist")
			if err := releaseSign(tc.args(outDir), &bytes.Buffer{}, now); err == nil {
				t.Fatal("accepted")
			}
			if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err == nil {
				t.Fatal("a manifest was written although signing was refused")
			}
		})
	}
}

// Signing rebuilds every binary from a clean checkout of the tag and refuses one it cannot reproduce.
func TestReleaseSignRebuildsFromTheTag(t *testing.T) {
	keyFile, _ := newKeyFile(t)
	now := time.Unix(1_790_000_000, 0)
	sign := func(src string, bins ...string) error {
		outDir := filepath.Join(t.TempDir(), "dist")
		err := releaseSign(append([]string{"--key", keyFile, "--version", "v0.1.4", "--source", src, "--out", outDir}, bins...), &bytes.Buffer{}, now)
		if err != nil {
			if _, statErr := os.Stat(outDir); statErr == nil {
				t.Errorf("refused (%v) but wrote %s", err, outDir)
			}
		}
		return err
	}

	ciDir, ciBins := makeBinaries(t, "mistgate-node-linux-amd64", "mistgate-linux-amd64")
	tag := t.TempDir() // what the tag really builds to: other bytes than the CI file
	if err := os.WriteFile(filepath.Join(tag, "mistgate-node-linux-amd64"), []byte("other bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	rebuildAs(t, tag)
	src := makeSource(t, "v0.1.4")
	if err := sign(src, ciBins[0]); err == nil || !strings.Contains(err.Error(), "does not match its rebuild") {
		t.Fatalf("a binary the tag does not build to: %v", err)
	}

	rebuildAs(t, ciDir)
	if err := sign(src, ciBins...); err != nil {
		t.Fatalf("reproduced binaries: %v", err)
	}
	if err := os.Remove(filepath.Join(src, "web", "dist", "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := sign(src, ciBins[1]); err == nil || !strings.Contains(err.Error(), "pnpm build") {
		t.Fatalf("a panel binary without the SPA built: %v", err)
	}
	if err := sign(src, ciBins[0]); err != nil {
		t.Fatalf("the node bundle does not need the SPA: %v", err)
	}

	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sign(src, ciBins[0]); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("a dirty checkout: %v", err)
	}
	git(t, src, "commit", "-q", "-am", "after the tag")
	if err := sign(src, ciBins[0]); err == nil || !strings.Contains(err.Error(), "not at v0.1.4") {
		t.Fatalf("a checkout past the tag: %v", err)
	}
	if err := sign(makeSource(t, "v0.1.3"), ciBins[0]); err == nil || !strings.Contains(err.Error(), "no tag v0.1.4") {
		t.Fatalf("a checkout without the tag: %v", err)
	}
}

func TestReleaseSignRequiresMatchingCompiledKey(t *testing.T) {
	keyFile, _ := newKeyFile(t)
	src := makeSource(t, "v0.1.4")
	binDir, bins := makeBinaries(t, "mistgate-node-linux-amd64")
	rebuildAs(t, binDir)
	outDir := filepath.Join(t.TempDir(), "dist")
	otherPub, _, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	previous := buildinfo.ReleaseKey
	buildinfo.ReleaseKey = release.EncodePublicKey(otherPub)
	t.Cleanup(func() { buildinfo.ReleaseKey = previous })
	args := []string{"--key", keyFile, "--version", "v0.1.4", "--source", src, bins[0], "--out", outDir}
	err = releaseSign(args, &bytes.Buffer{}, time.Unix(1_790_000_000, 0))
	if err == nil || !strings.Contains(err.Error(), "does not match the public key compiled into this signer") {
		t.Fatalf("mismatched release key error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err == nil {
		t.Fatal("a manifest was written with a key that differs from the compiled trust root")
	}
}

// --out may be the directory the binaries already sit in; --expires takes days or a Go duration.
func TestReleaseSignInPlaceAndExpires(t *testing.T) {
	keyFile, pubB64 := newKeyFile(t)
	src := makeSource(t, "1.0.0")
	dir, bins := makeBinaries(t, "mistgate-node-linux-amd64")
	rebuildAs(t, dir)
	now := time.Unix(1_790_000_000, 0)
	if err := releaseSign([]string{"--key", keyFile, "--version", "1.0.0", "--source", src, "--built", "1789000000", "--expires", "720h", bins[0], "--out", dir}, &bytes.Buffer{}, now); err != nil {
		t.Fatal(err)
	}
	pub, _ := decodeTestKey(pubB64)
	ms, err := verifyBundle(dir, pub)
	if err != nil || len(ms) != 1 || ms[0].Expires != now.Add(720*time.Hour).Unix() {
		t.Fatalf("%+v %v", ms, err)
	}
	if d, err := parseExpires("30d"); err != nil || d != 30*24*time.Hour {
		t.Fatalf("30d: %v %v", d, err)
	}
}

// The real build: two checkouts in different directories give byte-identical binaries, the key changes them.
func TestGoBuildReleaseIsReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	goMod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	toolchain := ""
	for _, l := range strings.Split(string(goMod), "\n") {
		if strings.HasPrefix(l, "go ") || strings.HasPrefix(l, "toolchain ") {
			toolchain += l + "\n"
		}
	}
	tree := map[string]string{
		"go.mod":                          "module github.com/mistgate/mistgate\n\n" + toolchain,
		"internal/buildinfo/b.go":         "package buildinfo\n\nvar Version, Built, ReleaseKey string\n",
		"cmd/mistgate-node/main.go":       "package main\n\nimport \"github.com/mistgate/mistgate/internal/buildinfo\"\n\nfunc main() { println(buildinfo.Version, buildinfo.Built, buildinfo.ReleaseKey) }\n",
		"cmd/mistgate-node/untracked.txt": "noise\n",
	}
	build := func(key string) string {
		src := t.TempDir()
		for name, body := range tree {
			p := filepath.Join(src, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out := filepath.Join(t.TempDir(), "mistgate-node-linux-arm64")
		if err := goBuildRelease(src, "mistgate-node-linux-arm64", "v0.1.4", testCommitTime, key, out); err != nil {
			t.Fatal(err)
		}
		f, err := release.FileFromPath(out)
		if err != nil {
			t.Fatal(err)
		}
		return f.SHA256
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	a, b := build(key), build(key)
	if a != b {
		t.Fatalf("two checkouts built differently: %s %s", a, b)
	}
	if build("") == a {
		t.Fatal("the release key is not stamped into the binary")
	}
	if err := goBuildRelease(t.TempDir(), "tool-linux-amd64", "v1", 1, "", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("built a binary that is not a release binary")
	}
}

// trust-key is the explicit step of a key rotation: release.pub becomes this binary's compiled-in key.
func TestReleaseTrustKey(t *testing.T) {
	_, oldPub := newKeyFile(t)
	_, newPub := newKeyFile(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.pub"), []byte(oldPub+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := buildinfo.ReleaseKey
	buildinfo.ReleaseKey = newPub
	t.Cleanup(func() { buildinfo.ReleaseKey = previous })
	if _, err := buildinfo.LoadReleasePublicKey(dir); !errors.Is(err, buildinfo.ErrReleaseKeyMismatch) {
		t.Fatalf("before: %v", err)
	}
	var out bytes.Buffer
	if err := runRelease([]string{"trust-key", "--data-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	oldKey, _ := decodeTestKey(oldPub)
	newKey, _ := decodeTestKey(newPub)
	if !strings.Contains(out.String(), buildinfo.KeyFingerprint(oldKey)+" -> "+buildinfo.KeyFingerprint(newKey)) {
		t.Fatalf("output %q", out.String())
	}
	if got, err := buildinfo.LoadReleasePublicKey(dir); err != nil || !got.Equal(newKey) {
		t.Fatalf("after: %x %v", got, err)
	}
}

func TestVersionCommandPrintsTheBuild(t *testing.T) {
	var out bytes.Buffer
	printBuild(&out)
	if !strings.Contains(out.String(), "release key: none") { // tests are built without a key
		t.Fatalf("%q", out.String())
	}
}

func decodeTestKey(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("not a public key")
	}
	return ed25519.PublicKey(b), nil
}
