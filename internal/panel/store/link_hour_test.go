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

func TestHelloKexSQLite(t *testing.T) {
	helloKexStoreProof(t, openTemp(t))
}

func TestLinkHourD1PayloadAtEdgeCap(t *testing.T) {
	const rowsAtCap = 10_000
	const d1ValueLimit = 2_000_000
	credID := "crd_" + strings.Repeat("c", 26)
	inboundID := "inb_" + strings.Repeat("i", 26)
	userID := "usr_" + strings.Repeat("u", 26)
	row := linkRow{cred: credID, inbound: inboundID, user: userID}
	for i := range row.n {
		switch i {
		case lcProbeHandshakes:
		case lcRttMSSum, lcPktsSent, lcPktsLost, lcProbePktsSent, lcProbePktsLost:
			row.n[i] = maxLinkSum
		default:
			row.n[i] = maxLinkCount
		}
	}
	if len(linkHourColumns) != linkHourCounters || linkHourColumns[lcHsUnfinished] != "hs_unfinished" || linkHourColumns[lcProbePktsLost] != "probe_pkts_lost" {
		t.Fatalf("linkRow positions do not match linkHourColumns")
	}
	rows := make([]linkRow, rowsAtCap)
	for i := range rows {
		rows[i] = row
	}
	stmt, err := linkHourStmt("nod_"+strings.Repeat("n", 26), 1_700_000_000, rows)
	if err != nil {
		t.Fatal(err)
	}
	full := make([]byte, 0, 4<<20)
	for i := range rows {
		full = rows[i].appendJSON(append(full, ','))
	}
	// the JSON the statement binds is valid and holds the clamps
	var back [][]any
	if err := json.Unmarshal([]byte(stmt.Args[0].(string)), &back); err != nil || len(back[0]) != 3+linkHourCounters ||
		back[0][0] != credID || back[0][3+lcRttMSSum] != float64(maxLinkSum) || back[0][3+lcHsUnfinished] != float64(maxLinkCount) {
		t.Fatalf("link_hour JSON: %v, first row %v", err, back[0])
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

func TestLinkRowJSONEscapes(t *testing.T) {
	r := linkRow{cred: "c\"1\\", inbound: "inb\n", user: "пользователь"}
	var back []any
	if err := json.Unmarshal(r.appendJSON(nil), &back); err != nil || back[0] != "c\"1\\" || back[1] != "inb\n" || back[2] != "пользователь" {
		t.Fatalf("escaped row = %v, %v", back, err)
	}
}
