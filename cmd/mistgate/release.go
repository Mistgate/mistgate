package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/release"
)

const releaseUsage = `usage:
  mistgate release keygen --out FILE
  mistgate release sign --key FILE --version V --built UNIX [--expires 30d] BINARY... --out DIR

  keygen  make the owner's release key: the private key goes to FILE (mode 0600, never overwritten), the public key and
          its fingerprint are printed. Put the public key into the build (RELEASE_KEY=<public key> make build).
  sign    write DIR/manifest.json and DIR/manifest.sig for the binaries (named <name>-<os>-<arch>, e.g.
          mistgate-node-linux-amd64) and copy them into DIR; --version must match this panel build, and --built is
          the Unix time of the source commit (git log -1 --format=%ct). Copy DIR to <data-dir>/dist on the panel.
`

// runRelease implements `mistgate release ...`, the owner's commands on the machine that holds the release key.
func runRelease(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(releaseUsage)
	}
	switch args[0] {
	case "keygen":
		return releaseKeygen(args[1:], out)
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

func releaseSign(args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("release sign", flag.ContinueOnError)
	keyFile := fs.String("key", "", "release private key file (made by release keygen)")
	version := fs.String("version", "", "release version; must match this panel build, e.g. v0.1.4")
	built := fs.Int64("built", 0, "Unix time of the source commit (git log -1 --format=%ct); it orders releases")
	expires := fs.String("expires", "30d", "how long the manifest stays installable (30d, 720h)")
	outDir := fs.String("out", "", "directory for manifest.json, manifest.sig and the copies of the binaries")
	bins, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *keyFile == "" || *version == "" || *outDir == "" || len(bins) == 0 {
		return errors.New(releaseUsage)
	}
	if *built <= 0 {
		return errors.New("--built must be the Unix time of the source commit (git log -1 --format=%ct), not 0: it is what orders releases")
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

	m := &release.Manifest{Schema: release.Schema, Version: *version, Built: *built, Expires: now.Add(life).Unix()}
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
		m.Files = append(m.Files, f)
	}
	body, err := m.Marshal() // validates: version, names, sizes, duplicates
	if err != nil {
		return err
	}
	if *version != buildinfo.Version {
		return fmt.Errorf("release version %q must match this panel build's version %q; build the panel and node binaries from the same release tag", *version, buildinfo.Version)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	for _, b := range bins {
		if err := copyInto(b, filepath.Join(*outDir, filepath.Base(b))); err != nil {
			return err
		}
	}
	sig := release.Sign(priv, body)
	if err := writeAtomic(filepath.Join(*outDir, release.ManifestName), body, 0o644); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(*outDir, release.SignatureName), sig, 0o644); err != nil {
		return err
	}
	// What was written must verify with the public key of the key file, file by file.
	got, err := verifyBundle(*outDir, pub)
	if err != nil {
		return fmt.Errorf("the bundle written to %s does not verify: %w", *outDir, err)
	}
	fmt.Fprintf(out, "version %s, built %d, expires %s\n", got.Version, got.Built, time.Unix(got.Expires, 0).UTC().Format("2006-01-02"))
	for _, f := range got.Files {
		fmt.Fprintf(out, "  %-32s %10d bytes  sha256 %s\n", f.Name, f.Size, f.SHA256[:12])
	}
	fmt.Fprintf(out, "signed with key %s\nCopy the contents of %s to <data-dir>/dist on the panel.\n", buildinfo.KeyFingerprint(pub), *outDir)
	return nil
}

// verifyBundle reads dir back the way a consumer would.
func verifyBundle(dir string, pub ed25519.PublicKey) (*release.Manifest, error) {
	body, err := os.ReadFile(filepath.Join(dir, release.ManifestName))
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(filepath.Join(dir, release.SignatureName))
	if err != nil {
		return nil, err
	}
	m, err := release.Verify(pub, body, sig)
	if err != nil {
		return nil, err
	}
	for _, f := range m.Files {
		fh, err := os.Open(filepath.Join(dir, f.Name))
		if err != nil {
			return nil, err
		}
		err = release.VerifyFile(f, fh)
		fh.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return m, nil
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
