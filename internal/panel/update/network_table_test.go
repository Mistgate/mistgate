package update

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/asndb"
)

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
