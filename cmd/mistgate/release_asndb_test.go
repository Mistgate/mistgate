package main

import (
	"bytes"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/asndb"
	"github.com/mistgate/mistgate/internal/release"
)

func TestReleaseASNDBBuildsLocalFixture(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	sourceDir := t.TempDir()
	v4 := copyFixtureSource(t, filepath.Join("..", "..", "internal", "asndb", "testdata", "ip2asn-v4.tsv"), sourceDir, now.Add(-time.Hour))
	v6 := copyFixtureSource(t, filepath.Join("..", "..", "internal", "asndb", "testdata", "ip2asn-v6.tsv"), sourceDir, now.Add(-2*time.Hour))
	outPath := filepath.Join(t.TempDir(), "mistgate-asndb-data-any")
	var out bytes.Buffer
	limits := asndb.ReleaseLimits{MinIPv4Ranges: 4, MinASNs: 4, MaxAge: 45 * 24 * time.Hour}
	if err := buildASNDB(v4, v6, outPath, now, limits, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "source date 2026-11-09") || !strings.Contains(out.String(), "4 IPv4 ranges") {
		t.Fatalf("generator output = %q", out.String())
	}
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatal(err)
	}
	db, parseErr := asndb.Parse(f)
	closeErr := f.Close()
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if got := db.Lookup(mustAddr("198.51.100.200")); got != 64501 {
		t.Fatalf("generated table lookup = %d, want 64501", got)
	}
	if got, want := db.Built(), now.Add(-2*time.Hour).Truncate(time.Second); !got.Equal(want) {
		t.Fatalf("generated table built time = %s, want %s", got, want)
	}
}

func TestReleaseASNDBChecksProductionMinimums(t *testing.T) {
	now := time.Now().UTC()
	sourceDir := t.TempDir()
	v4 := copyFixtureSource(t, filepath.Join("..", "..", "internal", "asndb", "testdata", "ip2asn-v4.tsv"), sourceDir, now.Add(-time.Hour))
	v6 := copyFixtureSource(t, filepath.Join("..", "..", "internal", "asndb", "testdata", "ip2asn-v6.tsv"), sourceDir, now.Add(-time.Hour))
	outPath := filepath.Join(t.TempDir(), "mistgate-asndb-data-any")
	err := releaseASNDB([]string{"--v4", v4, "--v6", v6, "--out", outPath}, &bytes.Buffer{}, now)
	if err == nil || !strings.Contains(err.Error(), "only 4 routed IPv4 ranges") {
		t.Fatalf("release asndb error = %v, want minimum-range check", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an invalid table was installed: stat error %v", err)
	}
}

func TestReleaseSignIncludesASNTableAndDoesNotRebuildIt(t *testing.T) {
	keyFile, pubB64 := newKeyFile(t)
	now := time.Now().UTC().Truncate(time.Second)
	tablePath := filepath.Join(t.TempDir(), "mistgate-asndb-data-any")
	writeFixtureASNDB(t, tablePath, now.Add(-time.Hour))
	source := makeSource(t, "v0.1.4")
	outDir := filepath.Join(t.TempDir(), "dist")
	limits := asndb.ReleaseLimits{MinIPv4Ranges: 4, MinASNs: 4, MaxAge: 45 * 24 * time.Hour}
	args := []string{"--key", keyFile, "--version", "v0.1.4", "--source", source, tablePath, "--out", outDir}
	if err := releaseSignWithASNDBLimits(args, &bytes.Buffer{}, now, limits); err != nil {
		t.Fatal(err)
	}
	pub, err := decodeTestKey(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(outDir, release.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join(outDir, release.SignatureName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := release.Verify(pub, body, sig)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Name != "mistgate-asndb-data-any" || manifest.Files[0].OS != "data" || manifest.Files[0].Arch != "any" {
		t.Fatalf("signed table manifest entry = %+v", manifest.Files)
	}
	if _, err := manifest.FileFor("linux", "amd64"); !errors.Is(err, release.ErrNoFile) {
		t.Fatalf("old agent selected the ASN table: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "mistgate-asndb-data-any")); err != nil {
		t.Fatalf("signed table copy is missing: %v", err)
	}
}

func TestReleaseSignValidatesASNTableByDefault(t *testing.T) {
	keyFile, _ := newKeyFile(t)
	now := time.Now().UTC().Truncate(time.Second)
	tablePath := filepath.Join(t.TempDir(), "mistgate-asndb-data-any")
	writeFixtureASNDB(t, tablePath, now.Add(-time.Hour))
	source := makeSource(t, "v0.1.4")
	outDir := filepath.Join(t.TempDir(), "dist")
	args := []string{"--key", keyFile, "--version", "v0.1.4", "--source", source, tablePath, "--out", outDir}
	err := releaseSign(args, &bytes.Buffer{}, now)
	if err == nil || !strings.Contains(err.Error(), "only 4 routed IPv4 ranges") {
		t.Fatalf("release sign error = %v, want the production range guard", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, release.ManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("signing wrote a manifest for a refused table: %v", err)
	}
}

func TestCheckReproducedSkipsASNTable(t *testing.T) {
	tablePath := filepath.Join(t.TempDir(), "mistgate-asndb-data-any")
	writeFixtureASNDB(t, tablePath, time.Now().UTC().Add(-time.Hour))
	if err := checkReproduced(t.TempDir(), "v0.1.4", testCommitTime, "", []string{tablePath}); err != nil {
		t.Fatalf("checkReproduced tried to rebuild the source-derived table: %v", err)
	}
}

func copyFixtureSource(t *testing.T, source, dir string, modified time.Time) string {
	t.Helper()
	b, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filepath.Base(source))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFixtureASNDB(t *testing.T, path string, built time.Time) {
	t.Helper()
	table := asndb.Table{Built: built}
	for _, source := range []struct {
		name   string
		family asndb.Family
	}{{"ip2asn-v4.tsv", asndb.FamilyIPv4}, {"ip2asn-v6.tsv", asndb.FamilyIPv6}} {
		f, err := os.Open(filepath.Join("..", "..", "internal", "asndb", "testdata", source.name))
		if err != nil {
			t.Fatal(err)
		}
		parseErr := asndb.AppendTSV(f, source.family, &table)
		closeErr := f.Close()
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := asndb.Write(f, table)
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func mustAddr(value string) netip.Addr {
	return netip.MustParseAddr(value)
}
