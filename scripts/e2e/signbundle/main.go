// Command signbundle signs a node bundle for scripts/e2e-wsl.sh without the rebuild check of `mistgate release sign`:
// the test signs binaries it patched and manifests that misstate the build time on purpose. Never use it for a real
// release.
//
//	signbundle --key FILE --version V --built UNIX --out DIR BINARY...
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "signbundle:", err)
		os.Exit(1)
	}
}

func run() error {
	key := flag.String("key", "", "release private key file")
	version := flag.String("version", "", "manifest version")
	built := flag.Int64("built", 0, "manifest build time (Unix)")
	out := flag.String("out", "", "output directory")
	flag.Parse()
	raw, err := os.ReadFile(*key)
	if err != nil {
		return err
	}
	priv, err := release.DecodePrivateKey(string(raw))
	if err != nil {
		return err
	}
	m := release.Manifest{Schema: release.Schema, Version: *version, Built: *built, Expires: time.Now().Add(30 * 24 * time.Hour).Unix()}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for _, bin := range flag.Args() {
		f, err := release.FileFromPath(bin)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, f)
		b, err := os.ReadFile(bin)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*out, f.Name), b, 0o755); err != nil {
			return err
		}
	}
	body, err := m.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, release.ManifestName), body, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, release.SignatureName), release.Sign(priv, body), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed %s (built %d) with %d file(s) into %s\n", m.Version, m.Built, len(m.Files), *out)
	return nil
}
