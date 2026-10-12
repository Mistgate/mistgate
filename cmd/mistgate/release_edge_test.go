package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// edgeRebuildAs makes the rebuild of the edge bundle write content, and records what it was asked to build.
func edgeRebuildAs(t *testing.T, content []byte) *[]string {
	t.Helper()
	var calls []string
	previous := edgeRebuild
	edgeRebuild = func(src, version string, built int64, key, out string) error {
		calls = append(calls, strings.Join([]string{version, key}, " "))
		if built != testCommitTime {
			t.Errorf("rebuilt the edge with built %d", built)
		}
		return os.WriteFile(out, content, 0o644)
	}
	t.Cleanup(func() { edgeRebuild = previous })
	return &calls
}

func writeEdgeFile(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), release.EdgeBundleName)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// `release sign` writes edge-manifest.json and edge-manifest.sig for the tarball, in the edge context, next to the other
// manifests, and copies the tarball without an executable bit.
func TestReleaseSignEdge(t *testing.T) {
	keyFile, pubB64 := newKeyFile(t)
	pub, _ := decodeTestKey(pubB64)
	src := makeSource(t, "v0.1.4")
	tarball := []byte("pretend this is a tarball")
	edge := writeEdgeFile(t, tarball)
	calls := edgeRebuildAs(t, tarball)
	binDir, bins := makeBinaries(t, "mistgate-node-linux-amd64")
	rebuildAs(t, binDir)
	now := time.Unix(1_790_000_000, 0)

	for name, given := range map[string][]string{"alone": {edge}, "with a node binary": {bins[0], edge}} {
		t.Run(name, func(t *testing.T) {
			outDir := filepath.Join(t.TempDir(), "dist")
			var out bytes.Buffer
			args := append([]string{"--key", keyFile, "--version", "v0.1.4", "--source", src, "--expires", "30d", "--out", outDir}, given...)
			if err := releaseSign(args, &out, now); err != nil {
				t.Fatal(err)
			}
			body, _ := os.ReadFile(filepath.Join(outDir, release.EdgeManifestName))
			sig, _ := os.ReadFile(filepath.Join(outDir, release.EdgeSignatureName))
			m, err := release.VerifyEdge(pub, body, sig)
			if err != nil {
				t.Fatalf("the written edge manifest does not verify: %v", err)
			}
			sum := sha256.Sum256(tarball)
			if m.Version != "v0.1.4" || m.Built != testCommitTime || m.Expires != now.Add(30*24*time.Hour).Unix() ||
				m.File != release.EdgeBundleName || m.Size != int64(len(tarball)) || m.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("manifest %+v", m)
			}
			if _, err := release.Verify(pub, body, sig); err == nil {
				t.Fatal("the edge signature verifies as a node manifest")
			}
			if _, err := release.VerifyPanel(pub, body, sig); err == nil {
				t.Fatal("the edge signature verifies as a panel manifest")
			}
			copied, err := os.ReadFile(filepath.Join(outDir, release.EdgeBundleName))
			if err != nil || !bytes.Equal(copied, tarball) {
				t.Fatalf("the tarball was not copied: %v", err)
			}
			if runtime.GOOS != "windows" {
				if fi, _ := os.Stat(filepath.Join(outDir, release.EdgeBundleName)); fi.Mode().Perm() != 0o644 {
					t.Errorf("tarball mode %v", fi.Mode().Perm())
				}
			}
			_, nodeErr := os.Stat(filepath.Join(outDir, release.ManifestName))
			if (nodeErr == nil) != (len(given) == 2) {
				t.Errorf("node manifest present: %v, with a node binary given: %v", nodeErr == nil, len(given) == 2)
			}
			for _, want := range []string{"edge version v0.1.4", release.EdgeBundleName, "signed with key " + buildinfo.KeyFingerprint(pub)} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
			// a changed tarball in the output directory is noticed
			if err := os.WriteFile(filepath.Join(outDir, release.EdgeBundleName), []byte("evil"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := verifyEdgeBundle(outDir, pub); err == nil {
				t.Fatal("a swapped tarball verifies")
			}
		})
	}
	if len(*calls) != 2 || (*calls)[0] != "v0.1.4 "+pubB64 {
		t.Fatalf("rebuilds: %q", *calls)
	}
}

// Signing rebuilds the bundle from the tag and refuses a tarball the tag does not build to, or that cannot be rebuilt.
func TestReleaseSignEdgeRefusesAMismatch(t *testing.T) {
	keyFile, _ := newKeyFile(t)
	src := makeSource(t, "v0.1.4")
	edge := writeEdgeFile(t, []byte("what the CI uploaded"))
	now := time.Unix(1_790_000_000, 0)
	sign := func(src string, extra ...string) (string, error) {
		outDir := filepath.Join(t.TempDir(), "dist")
		err := releaseSign(append([]string{"--key", keyFile, "--version", "v0.1.4", "--source", src, "--out", outDir}, extra...), &bytes.Buffer{}, now)
		if err != nil {
			if _, statErr := os.Stat(outDir); statErr == nil {
				t.Errorf("refused (%v) but wrote %s", err, outDir)
			}
		}
		return outDir, err
	}

	edgeRebuildAs(t, []byte("what the tag builds to"))
	if _, err := sign(src, edge); err == nil || !strings.Contains(err.Error(), "does not match its rebuild") {
		t.Fatalf("a tarball the tag does not build to: %v", err)
	}
	edgeRebuildAs(t, []byte("what the CI uploaded, plus one byte"))
	if _, err := sign(src, edge); err == nil || !strings.Contains(err.Error(), "does not match its rebuild") {
		t.Fatalf("a tarball one byte off: %v", err)
	}

	edgeRebuildAs(t, []byte("what the CI uploaded"))
	if _, err := sign(src, edge); err != nil {
		t.Fatalf("a reproduced tarball: %v", err)
	}
	if _, err := sign(src, edge, edge); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("the tarball given twice: %v", err)
	}
	if _, err := sign(src, filepath.Join(filepath.Dir(edge), "nope", release.EdgeBundleName)); err == nil {
		t.Fatal("a missing tarball")
	}
	// the tarball embeds the SPA, so signing needs it built, like the panel binaries do
	if err := os.Remove(filepath.Join(src, "web", "dist", "index.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := sign(src, edge); err == nil || !strings.Contains(err.Error(), "pnpm build") {
		t.Fatalf("an edge bundle without the SPA built: %v", err)
	}
	// a rebuild that fails is a refusal too
	previous := edgeRebuild
	edgeRebuild = func(string, string, int64, string, string) error { return os.ErrPermission }
	t.Cleanup(func() { edgeRebuild = previous })
	if err := os.WriteFile(filepath.Join(src, "web", "dist", "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := sign(src, edge); err == nil {
		t.Fatal("a failed rebuild")
	}
}

func TestReleaseBuildEdge(t *testing.T) {
	_, pubB64 := newKeyFile(t)
	calls := edgeRebuildAs(t, []byte("tarball"))
	src := makeSource(t, "v0.1.4")
	outDir := filepath.Join(t.TempDir(), "out")
	var out bytes.Buffer
	if err := releaseBuild([]string{"--version", "v0.1.4", "--key", pubB64, "--source", src, release.EdgeBundleName, "--out", outDir}, &out); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("tarball"))
	if want := hex.EncodeToString(sum[:]) + "  " + filepath.Join(outDir, release.EdgeBundleName); !strings.Contains(out.String(), want) {
		t.Fatalf("output %q lacks %q", out.String(), want)
	}
	if len(*calls) != 1 || (*calls)[0] != "v0.1.4 "+pubB64 {
		t.Fatalf("rebuilds: %q", *calls)
	}
}

// The real thing, no seams: the toolchain builds panel.wasm, esbuild bundles the scripts, the tarball is written twice into
// different directories. The two must be byte for byte the same. It needs what the release workflow has: the SPA built in
// web/dist and esbuild installed in edge/worker (pnpm install --frozen-lockfile), so it skips without them, and with -short.
func TestEdgeBundleRealBuildIsReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the panel for js/wasm")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, need := range []string{filepath.Join("web", "dist", "index.html"), filepath.Join("edge", "worker", "node_modules", "esbuild", "package.json")} {
		if _, err := os.Stat(filepath.Join(root, need)); err != nil {
			t.Skipf("%s is missing: build the SPA and run pnpm install in edge/worker first", need)
		}
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	_, key := newKeyFile(t)
	hashes := make([]string, 2)
	var tarball []byte
	for i := range hashes {
		out := filepath.Join(t.TempDir(), release.EdgeBundleName)
		if err := goBuildEdge(root, "v0.0.1", testCommitTime, key, out); err != nil {
			t.Fatal(err)
		}
		_, hashes[i], err = release.HashFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if tarball, err = os.ReadFile(out); err != nil {
			t.Fatal(err)
		}
	}
	if hashes[0] != hashes[1] {
		t.Fatalf("two builds of the edge bundle differ: %s %s", hashes[0], hashes[1])
	}
	files, err := release.ReadEdgeBundle(bytes.NewReader(tarball))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"edge.json", "worker.js", "provision.js", "panel.wasm", "assets/index.html"} {
		if len(files[name]) == 0 {
			t.Errorf("%s is missing or empty", name)
		}
	}
	// a path of this machine, however it got there, must not be in a bundle that is signed
	for _, name := range []string{"worker.js", "provision.js"} {
		if bytes.Contains(files[name], []byte(filepath.ToSlash(root))) {
			t.Errorf("%s holds the build directory", name)
		}
	}
	if !bytes.HasPrefix(files["panel.wasm"], []byte("\x00asm")) {
		t.Error("panel.wasm is not WebAssembly")
	}
	// the version and the release public key are stamped into the wasm
	for _, stamp := range []string{"v0.0.1", key} {
		if !bytes.Contains(files["panel.wasm"], []byte(stamp)) {
			t.Errorf("%q is not stamped into panel.wasm", stamp)
		}
	}
}
