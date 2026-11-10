package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/asndb"
	"github.com/mistgate/mistgate/internal/release"
)

// A file rewritten with the same size right after it was hashed can carry the same modification time (the tick of a
// Linux filesystem is milliseconds long): the cached hash of a file that was not settled when it was hashed is not used.
func TestHashCacheRechecksSameSizeRewriteRightAfterHashing(t *testing.T) {
	e := newEnv(t)
	dist := filepath.Join(e.dir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dist, "same-size-table")
	first, second := []byte("alpha123"), []byte("bravo123")
	digest := func(data []byte) string {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	firstFile := release.File{Name: filepath.Base(path), Size: int64(len(first)), SHA256: digest(first)}
	if _, code := e.s.checkFile(firstFile); code != "" {
		t.Fatalf("initial file check = %q", code)
	}
	if err := os.WriteFile(path, second, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := e.s.checkFile(firstFile); code != "file_mismatch" {
		t.Fatalf("rewritten file passed its stale digest: %q", code)
	}
	secondFile := release.File{Name: filepath.Base(path), Size: int64(len(second)), SHA256: digest(second)}
	if _, code := e.s.checkFile(secondFile); code != "" {
		t.Fatalf("rewritten file failed its new digest: %q", code)
	}
	// a settled file is hashed once: the cached entry is used as long as its size and time stay
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, code := e.s.checkFile(secondFile); code != "" {
		t.Fatalf("settled file check = %q", code)
	}
	e.s.bmu.Lock()
	at := e.s.hashes[secondFile.Name].at
	e.s.bmu.Unlock()
	if _, code := e.s.checkFile(secondFile); code != "" {
		t.Fatalf("settled file check = %q", code)
	}
	e.s.bmu.Lock()
	again := e.s.hashes[secondFile.Name].at
	e.s.bmu.Unlock()
	if !again.Equal(at) {
		t.Fatal("a settled file was hashed again")
	}
}

func TestTrustedNetworkTableLoaderHandlesAbsentInvalidAndChangedSHA(t *testing.T) {
	e := newEnv(t)
	if e.s.NetworksTable() != nil {
		t.Fatal("an absent table loaded")
	}

	writeBundle(t, filepath.Join(e.dir, "dist"), e.priv, "1.0", 10, e.clk.Now().Add(time.Hour).Unix(), map[string][]byte{
		"mistgate-node-linux-amd64": []byte("test binary"),
		"mistgate-asndb-data-any":   []byte("invalid ASN table"),
	})
	invalid := e.s.rescan()
	if !invalid.trusted || e.s.NetworksTable() != nil {
		t.Fatalf("invalid table state: trusted=%v table=%v", invalid.trusted, e.s.NetworksTable())
	}
	if name, digest := e.s.NetworkTableFile(); name != "" || digest != "" {
		t.Fatalf("panel-rejected table is still offered to nodes: %q/%q", name, digest)
	}

	first := networkTableFixture(t, 1_800_000_000, 64496, "DE", "de1")
	e.bundle("1.1", 11, map[string][]byte{"mistgate-node-linux-amd64": []byte("test binary"), "mistgate-asndb-data-any": first})
	loaded := e.s.NetworksTable()
	if loaded == nil || loaded.Built().Unix() != 1_800_000_000 {
		t.Fatalf("valid table did not load: %+v", loaded)
	}
	if got := loaded.Lookup(netip.MustParseAddr("203.0.113.10")); got != 64496 {
		t.Fatalf("first table lookup = AS%d, want AS64496", got)
	}

	second := networkTableFixture(t, 1_800_000_100, 64500, "NL", "nl1")
	e.bundle("1.2", 12, map[string][]byte{"mistgate-node-linux-amd64": []byte("test binary"), "mistgate-asndb-data-any": second})
	reloaded := e.s.NetworksTable()
	if reloaded == nil || reloaded.Built().Unix() != 1_800_000_100 {
		t.Fatalf("changed table did not reload: %+v", reloaded)
	}
	if got := reloaded.Lookup(netip.MustParseAddr("203.0.113.10")); got != 64500 {
		t.Fatalf("reloaded table lookup = AS%d, want AS64500", got)
	}
	if country, name, ok := reloaded.Name(64500); !ok || country != "NL" || name != "nl1" {
		t.Fatalf("reloaded table name = %q/%q/%v, want NL/nl1/true", country, name, ok)
	}
}

func TestNetworkTableLoaderRetriesIOErrorsOnTheNextRescan(t *testing.T) {
	e := newEnv(t)
	data := networkTableFixture(t, 1_800_000_000, 64496, "DE", "de1")
	dist := filepath.Join(e.dir, "dist")
	writeBundle(t, dist, e.priv, "1.0", 10, e.clk.Now().Add(time.Hour).Unix(), map[string][]byte{
		"mistgate-node-linux-amd64": []byte("test binary"),
		"mistgate-asndb-data-any":   data,
	})
	bs := e.s.scan()
	if !bs.trusted {
		t.Fatalf("valid bundle did not scan as trusted: %+v", bs.view)
	}
	e.s.bmu.Lock()
	e.s.bundle = bs
	e.s.bmu.Unlock()
	tablePath := filepath.Join(dist, "mistgate-asndb-data-any")
	hiddenPath := tablePath + ".held"
	if err := os.Rename(tablePath, hiddenPath); err != nil {
		t.Fatal(err)
	}
	e.s.reloadNetworkTable()
	if e.s.networkTableSHA != "" {
		t.Fatalf("I/O failure memoized table sha %q, so a rescan would not retry", e.s.networkTableSHA)
	}
	if name, digest := e.s.NetworkTableFile(); name == "" || digest == "" {
		t.Fatalf("transient I/O error incorrectly rejected the signed table: %q/%q", name, digest)
	}
	if err := os.Rename(hiddenPath, tablePath); err != nil {
		t.Fatal(err)
	}
	e.s.rescan()
	if table := e.s.NetworksTable(); table == nil || table.Built().Unix() != 1_800_000_000 {
		t.Fatalf("table was not retried after I/O recovery and rescan: %+v", table)
	}
}

func TestNetworkBundleChangeCallbackTracksTableDigestChanges(t *testing.T) {
	e := newEnv(t)
	changes := 0
	e.s.SetNetworkBundleChanged(func() { changes++ })
	first := networkTableFixture(t, 1_800_000_000, 64496, "DE", "de1")
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": []byte("test binary"), "mistgate-asndb-data-any": first})
	if changes != 1 {
		t.Fatalf("initial network table notification count = %d, want 1", changes)
	}
	e.bundle("1.1", 11, map[string][]byte{"mistgate-node-linux-amd64": []byte("next binary"), "mistgate-asndb-data-any": first})
	if changes != 1 {
		t.Fatalf("unchanged table digest notified %d times, want no extra notification", changes)
	}
	second := networkTableFixture(t, 1_800_000_100, 64500, "NL", "nl1")
	e.bundle("1.2", 12, map[string][]byte{"mistgate-node-linux-amd64": []byte("next binary"), "mistgate-asndb-data-any": second})
	if changes != 2 {
		t.Fatalf("changed table digest notification count = %d, want 2", changes)
	}
}

func networkTableFixture(t *testing.T, built int64, asn uint32, country, name string) []byte {
	t.Helper()
	startAddr := netip.MustParseAddr("203.0.113.0").As4()
	endAddr := netip.MustParseAddr("203.0.113.255").As4()
	var encoded bytes.Buffer
	if err := asndb.Write(&encoded, asndb.Table{Built: time.Unix(built, 0).UTC(),
		IPv4: []asndb.IPv4Range{{Start: binary.BigEndian.Uint32(startAddr[:]), End: binary.BigEndian.Uint32(endAddr[:]), ASN: asn}},
		ASNs: []asndb.Operator{{ASN: asn, Country: country, Name: name}},
	}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
