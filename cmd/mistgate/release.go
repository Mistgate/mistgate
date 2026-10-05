package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/release"
)

const releaseUsage = `usage:
  mistgate release keygen --out FILE
  mistgate release build --version V [--built UNIX] [--key PUBLIC] [--source DIR] NAME... --out DIR
  mistgate release sign --key FILE --version V [--built UNIX] [--expires 30d] [--source DIR] BINARY... --out DIR

  keygen  make the owner's release key: the private key goes to FILE (mode 0600, never overwritten), the public key and
          its fingerprint are printed. Put the public key into the build (RELEASE_KEY=<public key> make build).
  build   build release binaries (mistgate-linux-<arch>, mistgate-node-linux-<arch>) from the checkout --source (default
          .) the one reproducible way; CI and make build use it. --built defaults to the commit time of the checkout.
  sign    rebuild every binary from --source, which must be a clean checkout of the tag --version, refuse any binary
          that does not come out byte for byte the same, then write DIR/manifest.json and DIR/manifest.sig for the node
          binaries, DIR/panel-manifest.json and DIR/panel-manifest.sig for the panel binaries, and copy the binaries
          into DIR. Signing a panel binary needs the SPA built in --source first (cd web && pnpm install
          --frozen-lockfile && pnpm build).
`

// runRelease implements `mistgate release ...`, the owner's commands on the machine that holds the release key.
func runRelease(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(releaseUsage)
	}
	switch args[0] {
	case "keygen":
		return releaseKeygen(args[1:], out)
	case "build":
		return releaseBuild(args[1:], out)
	case "sign":
		return releaseSign(args[1:], out, time.Now())
	}
	return errors.New(releaseUsage)
}

func releaseKeygen(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("release keygen", flag.ContinueOnError)
	outFile := fs.String("out", "", "file for the private key (must not exist)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outFile == "" || fs.NArg() != 0 {
		return errors.New(releaseUsage)
	}
	pub, priv, err := release.GenerateKey()
	if err != nil {
		return err
	}
	// O_EXCL: a key that exists is never replaced (that would orphan every node that trusts the old public key).
	f, err := os.OpenFile(*outFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("release key file: %w", err)
	}
	_, werr := io.WriteString(f, release.EncodePrivateKey(priv)+"\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(*outFile)
		return fmt.Errorf("write %s: %w", *outFile, werr)
	}
	fmt.Fprintf(out, "public key: %s\nfingerprint: %s\n", release.EncodePublicKey(pub), buildinfo.KeyFingerprint(pub))
	fmt.Fprintln(out, "Keep the key file offline and back it up: without it every node must be updated by hand.")
	return nil
}

// parseInterspersed is flag.Parse that accepts flags after the positional arguments
// (`sign --key k BIN1 BIN2 --out dir`): it returns the positionals in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
}

// parseExpires reads "30d" (days) or a Go duration ("720h").
func parseExpires(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--expires %q: want a number of days like 30d or a duration like 720h", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--expires %q: want a number of days like 30d or a duration like 720h", s)
	}
	return d, nil
}

// releasePackages maps the base name of a release binary to the command it is built from.
var releasePackages = map[string]string{"mistgate": "./cmd/mistgate", "mistgate-node": "./cmd/mistgate-node"}

// releaseRebuild builds one release binary; a test seam (goBuildRelease in production).
var releaseRebuild = goBuildRelease

// goBuildRelease is the one definition of how a release binary is built: CI, make build and the rebuild check of
// release sign all run it, so one tag, key and toolchain give byte-identical files (checked between a Linux build and
// a Windows cross-build). The Go version is go.mod's toolchain line, whatever Go runs this; -buildvcs=false keeps
// untracked files of a checkout out of the binary.
func goBuildRelease(src, name, version string, built int64, key, out string) error {
	base, goos, goarch, err := release.ParseBinaryName(name)
	if err != nil {
		return err
	}
	pkg, ok := releasePackages[base]
	if !ok || goos != "linux" || (goarch != "amd64" && goarch != "arm64") {
		return fmt.Errorf("%s: release binaries are mistgate-linux-{amd64,arm64} and mistgate-node-linux-{amd64,arm64}", name)
	}
	modBytes, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		return err
	}
	mod, err := modfile.Parse("go.mod", modBytes, nil)
	if err != nil || mod.Go == nil {
		return fmt.Errorf("%s/go.mod: no go version", src)
	}
	toolchain := "go" + mod.Go.Version
	if mod.Toolchain != nil {
		toolchain = mod.Toolchain.Name
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	const bi = "github.com/mistgate/mistgate/internal/buildinfo"
	ldflags := fmt.Sprintf("-s -w -X %s.Version=%s -X %s.Built=%d -X %s.ReleaseKey=%s", bi, version, bi, built, bi, key)
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", out, pkg)
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch, "GOAMD64=v1", "GOARM64=v8.0",
		"GOFLAGS=", "GOEXPERIMENT=", "GOWORK=off", "GOTOOLCHAIN="+toolchain)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func gitOut(src string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", src}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), src, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// commitTime is the Unix time of the checkout's HEAD commit: what orders releases.
func commitTime(src string) (int64, error) {
	ct, err := gitOut(src, "log", "-1", "--format=%ct", "HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(ct, 10, 64)
}

// checkReleaseSource makes sure src is the tag being signed, without local changes to tracked files, and returns the
// commit time.
func checkReleaseSource(src, version string) (int64, error) {
	head, err := gitOut(src, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return 0, err
	}
	tag, err := gitOut(src, "rev-parse", "--verify", version+"^{commit}")
	if err != nil {
		return 0, fmt.Errorf("--source %s has no tag %s: %w", src, version, err)
	}
	if head != tag {
		return 0, fmt.Errorf("--source %s is not at %s: check out the release tag first", src, version)
	}
	if dirty, err := gitOut(src, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return 0, err
	} else if dirty != "" {
		return 0, fmt.Errorf("--source %s has local changes; sign from a clean checkout of %s", src, version)
	}
	return commitTime(src)
}

// checkReproduced rebuilds every binary from src and refuses one that differs: the owner signs what the tag builds
// to, not whatever a CI runner uploaded.
func checkReproduced(src, version string, built int64, key string, bins []string) error {
	tmp, err := os.MkdirTemp("", "mistgate-rebuild-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, b := range bins {
		name := filepath.Base(b)
		if base, _, _, _ := release.ParseBinaryName(name); base == "mistgate" {
			if _, err := os.Stat(filepath.Join(src, "web", "dist", "index.html")); err != nil {
				return fmt.Errorf("%s embeds the admin SPA: build it in %s first (cd web && pnpm install --frozen-lockfile && pnpm build)", name, src)
			}
		}
		rebuilt := filepath.Join(tmp, name)
		if err := releaseRebuild(src, name, version, built, key, rebuilt); err != nil {
			return fmt.Errorf("rebuild %s: %w", name, err)
		}
		want, err := release.FileFromPath(b)
		if err != nil {
			return err
		}
		got, err := release.FileFromPath(rebuilt)
		if err != nil {
			return err
		}
		if want.SHA256 != got.SHA256 || want.Size != got.Size {
			return fmt.Errorf("%s does not match its rebuild from %s (sha256 %s, rebuilt %s): refusing to sign it", b, version, want.SHA256[:12], got.SHA256[:12])
		}
	}
	return nil
}

// releaseBuild implements `release build`.
func releaseBuild(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("release build", flag.ContinueOnError)
	version := fs.String("version", "", "version stamped into the binaries, e.g. v0.1.4")
	built := fs.Int64("built", 0, "Unix time of the source commit; default: the commit time of --source")
	key := fs.String("key", "", "release public key (base64); empty = an unsigned build that cannot self-update")
	source := fs.String("source", ".", "the source checkout")
	outDir := fs.String("out", "", "directory for the binaries")
	names, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if !release.ValidVersion(*version) || *outDir == "" || len(names) == 0 || *built < 0 {
		return errors.New(releaseUsage)
	}
	if *key != "" {
		if b, err := base64.StdEncoding.DecodeString(*key); err != nil || len(b) != ed25519.PublicKeySize {
			return errors.New("--key must be a base64 ed25519 public key")
		}
	}
	if *built == 0 {
		if *built, err = commitTime(*source); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	for _, name := range names {
		p := filepath.Join(*outDir, name)
		if err := releaseRebuild(*source, name, *version, *built, *key, p); err != nil {
			return fmt.Errorf("build %s: %w", name, err)
		}
		f, err := release.FileFromPath(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s  %s\n", f.SHA256, p)
	}
	return nil
}

func releaseSign(args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("release sign", flag.ContinueOnError)
	keyFile := fs.String("key", "", "release private key file (made by release keygen)")
	version := fs.String("version", "", "the release tag, e.g. v0.1.4")
	built := fs.Int64("built", 0, "Unix time of the source commit; default and required value: the commit time of the tag")
	expires := fs.String("expires", "30d", "how long the manifest stays installable (30d, 720h)")
	source := fs.String("source", ".", "a clean checkout of the tag, to rebuild the binaries from")
	outDir := fs.String("out", "", "directory for the manifests, their signatures and the copies of the binaries")
	bins, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *keyFile == "" || *version == "" || *outDir == "" || len(bins) == 0 {
		return errors.New(releaseUsage)
	}
	if *built < 0 {
		return errors.New("--built must be the Unix time of the source commit (git log -1 --format=%ct)")
	}
	life, err := parseExpires(*expires)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*keyFile)
	if err != nil {
		return fmt.Errorf("release key: %w", err)
	}
	priv, err := release.DecodePrivateKey(string(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", *keyFile, err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	if expected, err := buildinfo.ReleasePublicKey(); err == nil {
		if !bytes.Equal(pub, expected) {
			return errors.New("release key does not match the public key compiled into this signer")
		}
	} else if !errors.Is(err, buildinfo.ErrUnsignedBuild) {
		return fmt.Errorf("compiled release key: %w", err)
	}

	commitBuilt, err := checkReleaseSource(*source, *version)
	if err != nil {
		return err
	}
	if *built != 0 && *built != commitBuilt {
		return fmt.Errorf("--built %d is not the commit time of %s (%d)", *built, *version, commitBuilt)
	}

	// Node binaries go into the node bundle (manifest.json), panel binaries into the panel's own manifest.
	node := &release.Manifest{Schema: release.Schema, Version: *version, Built: commitBuilt, Expires: now.Add(life).Unix()}
	panel := *node
	for _, b := range bins {
		fi, err := os.Stat(b)
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", b)
		}
		f, err := release.FileFromPath(b) // the name must be <name>-<os>-<arch>
		if err != nil {
			return err
		}
		base, _, _, _ := release.ParseBinaryName(f.Name)
		switch base {
		case "mistgate-node":
			node.Files = append(node.Files, f)
		case "mistgate":
			panel.Files = append(panel.Files, f)
		default:
			return fmt.Errorf("%s: only mistgate-node-<os>-<arch> and mistgate-<os>-<arch> binaries are signed", b)
		}
	}
	type signed struct {
		name, sigName string
		body, sig     []byte
	}
	var outputs []signed
	if len(node.Files) > 0 {
		body, err := node.Marshal() // validates: version, names, sizes, duplicates
		if err != nil {
			return err
		}
		outputs = append(outputs, signed{release.ManifestName, release.SignatureName, body, release.Sign(priv, body)})
	}
	if len(panel.Files) > 0 {
		body, err := panel.Marshal()
		if err != nil {
			return err
		}
		outputs = append(outputs, signed{release.PanelManifestName, release.PanelSignatureName, body, release.SignPanel(priv, body)})
	}
	if err := checkReproduced(*source, *version, commitBuilt, release.EncodePublicKey(pub), bins); err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	for _, b := range bins {
		if err := copyInto(b, filepath.Join(*outDir, filepath.Base(b))); err != nil {
			return err
		}
	}
	for _, o := range outputs {
		if err := writeAtomic(filepath.Join(*outDir, o.name), o.body, 0o644); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(*outDir, o.sigName), o.sig, 0o644); err != nil {
			return err
		}
	}
	// What was written must verify with the public key of the key file, file by file.
	manifests, err := verifyBundle(*outDir, pub)
	if err != nil {
		return fmt.Errorf("the bundle written to %s does not verify: %w", *outDir, err)
	}
	for _, got := range manifests {
		fmt.Fprintf(out, "version %s, built %d, expires %s\n", got.Version, got.Built, time.Unix(got.Expires, 0).UTC().Format("2006-01-02"))
		for _, f := range got.Files {
			fmt.Fprintf(out, "  %-32s %10d bytes  sha256 %s\n", f.Name, f.Size, f.SHA256[:12])
		}
	}
	fmt.Fprintf(out, "rebuilt from %s and signed with key %s\nUpload the manifests and signatures in %s to the release, or copy the node bundle to <data-dir>/dist on the panel.\n",
		*version, buildinfo.KeyFingerprint(pub), *outDir)
	return nil
}

// verifyBundle reads dir back the way a consumer would: the node bundle and the panel manifest, whichever are there
// (at least one).
func verifyBundle(dir string, pub ed25519.PublicKey) ([]*release.Manifest, error) {
	var out []*release.Manifest
	for _, kind := range []struct {
		name, sig string
		verify    func(ed25519.PublicKey, []byte, []byte) (*release.Manifest, error)
	}{
		{release.ManifestName, release.SignatureName, release.Verify},
		{release.PanelManifestName, release.PanelSignatureName, release.VerifyPanel},
	} {
		body, err := os.ReadFile(filepath.Join(dir, kind.name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sig, err := os.ReadFile(filepath.Join(dir, kind.sig))
		if err != nil {
			return nil, err
		}
		m, err := kind.verify(pub, body, sig)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", kind.name, err)
		}
		if err := verifyFiles(dir, m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no signed manifest", dir)
	}
	return out, nil
}

func verifyFiles(dir string, m *release.Manifest) error {
	for _, f := range m.Files {
		fh, err := os.Open(filepath.Join(dir, f.Name))
		if err != nil {
			return err
		}
		err = release.VerifyFile(f, fh)
		fh.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return nil
}

// copyInto copies src to dst (mode 0755) through a temporary file, so a reader never sees half a binary. Copying a
// file onto itself (--out is the directory the binary is already in) is a no-op.
func copyInto(src, dst string) error {
	if a, err := os.Stat(src); err == nil {
		if b, err := os.Stat(dst); err == nil && os.SameFile(a, b) {
			return nil
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".sign-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sign-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
