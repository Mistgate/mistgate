//go:build linux

package hostctl

import (
	"context"
	"strings"
	"testing"
)

func TestUDPCountInstallsReadsPacketsAndBytesThenDeletes(t *testing.T) {
	h, calls := testHost(t)
	var listed bool
	var listTable string
	base := h.run
	h.run = func(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if name == "nft" && joined == "-j list counters table inet "+NftUDPCheckTable {
			listed = true
			listTable = joined
			return []byte(`{"nftables":[{"metainfo":{"version":"1.0.9"}},{"counter":{"name":"p443","packets":7,"bytes":861,"table":"mistgate_udpcheck"}},{"counter":{"name":"p8443","packets":4,"bytes":492,"table":"mistgate_udpcheck"}}]}`), nil
		}
		return base(ctx, stdin, name, args...)
	}

	tag := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	if err := h.CountUDP(tag, []uint16{443, 8443}); err != nil {
		t.Fatal(err)
	}
	if got := lastNft(*calls); !strings.Contains(got, "table inet "+NftUDPCheckTable) || !strings.Contains(got, "0x0102030405060708") {
		t.Fatalf("install script:\n%s", got)
	}
	counts, err := h.TakeUDPCount()
	if err != nil {
		t.Fatal(err)
	}
	if !listed || listTable != "-j list counters table inet "+NftUDPCheckTable {
		t.Fatalf("counter listing = %q, listed=%v", listTable, listed)
	}
	if counts[443] != (Count{Packets: 7, Bytes: 861}) || counts[8443] != (Count{Packets: 4, Bytes: 492}) {
		t.Fatalf("counts = %+v", counts)
	}
	if got := lastNft(*calls); !strings.Contains(got, "delete table inet "+NftUDPCheckTable) {
		t.Fatalf("take did not delete the table:\n%s", got)
	}
}

func TestCleanupUDPCountIsQuietWhenTableIsAbsent(t *testing.T) {
	h, calls := testHost(t)
	// The fake nft runner treats add+delete as a successful no-op when the table is absent.
	if err := h.cleanupUDPCount(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := lastNft(*calls); !strings.Contains(got, "add table inet "+NftUDPCheckTable) ||
		!strings.Contains(got, "delete table inet "+NftUDPCheckTable) {
		t.Fatalf("cleanup script = %q", got)
	}
}
