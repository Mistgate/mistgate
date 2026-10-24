//go:build !js

package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLinkHourSQLite(t *testing.T) {
	linkHourStoreProof(t, openTemp(t))
}

func TestLinkHourD1PayloadAtEdgeCap(t *testing.T) {
	const rowsAtCap = 10_000
	const d1ValueLimit = 2_000_000
	credID := "crd_" + strings.Repeat("c", 26)
	inboundID := "inb_" + strings.Repeat("i", 26)
	userID := "usr_" + strings.Repeat("u", 26)
	row := []any{credID, inboundID, userID}
	for i := 0; i < 8; i++ { // legacy counts
		row = append(row, uint32(maxLinkCount))
	}
	row = append(row, uint32(0)) // probe_handshakes
	for i := 0; i < 5; i++ {     // client_closes through hellos_foreign
		row = append(row, uint32(maxLinkCount))
	}
	row = append(row, uint64(maxLinkSum), uint32(maxLinkCount), uint64(maxLinkSum), uint64(maxLinkSum))
	rows := make([][]any, rowsAtCap)
	for i := range rows {
		rows[i] = row
	}
	stmt, err := linkHourStmt("nod_"+strings.Repeat("n", 26), 1_700_000_000, rows)
	if err != nil {
		t.Fatal(err)
	}
	full, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	first, second := stmt.Args[0].(string), stmt.Args[1].(string)
	if len(stmt.Args) != 4 || len(full) <= d1ValueLimit || max(len(first), len(second)) >= d1ValueLimit {
		t.Fatalf("edge link_hour JSON bytes: full=%d, split=%d/%d, want the full value over and each half under D1's %d-byte limit",
			len(full), len(first), len(second), d1ValueLimit)
	}
	t.Logf("link_hour JSON at %d rows: unsplit=%d bytes, split=%d/%d bytes", rowsAtCap, len(full), len(first), len(second))
}
