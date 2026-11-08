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

func TestLinkReasonsSQLite(t *testing.T) {
	linkReasonsStoreProof(t, openTemp(t))
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
	row = append(row, uint64(maxLinkSum), uint64(maxLinkSum)) // probe_pkts_sent, probe_pkts_lost
	for i := 0; i < 11; i++ {                                 // links/3: hs_unfinished through responses_unmatched
		row = append(row, uint32(maxLinkCount))
	}
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
	if len(stmt.Args) != linkHourParts+2 || len(full) <= d1ValueLimit {
		t.Fatalf("edge link_hour: %d arguments, unsplit JSON %d bytes; want %d arguments and the unsplit value over D1's limit",
			len(stmt.Args), len(full), linkHourParts+2)
	}
	parts := make([]int, linkHourParts)
	for i := range parts {
		parts[i] = len(stmt.Args[i].(string))
		if parts[i] >= d1ValueLimit*3/4 { // a quarter of headroom for counters still to come
			t.Fatalf("edge link_hour JSON part %d is %d bytes; want each under three quarters of D1's %d-byte limit", i, parts[i], d1ValueLimit)
		}
	}
	t.Logf("link_hour JSON at %d rows: unsplit=%d bytes, split=%v bytes", rowsAtCap, len(full), parts)
}
