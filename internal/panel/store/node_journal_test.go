//go:build !js

package store

import (
	"encoding/json"
	"testing"
)

func TestNodeJournalSQLite(t *testing.T) {
	nodeJournalStoreProof(t, openTemp(t))
}

// The store cleans the source too, and for xray the names; two records whose fields only differ by where a separator falls
// are two rows.
func TestNodeJournalStmtCleansEveryFieldAndKeysByFields(t *testing.T) {
	st, err := nodeJournalStmt("nod_1", 3600, []FleetJournal{
		{Level: 3, Source: "203.0.113.7", Message: "boom", Count: 1},
		{Level: 3, Source: "xray", Message: "failed to dial destination.example.com from 198.51.100.2:5000", Attrs: map[string]string{"err": "visit.example.org"}, Count: 1},
		{Level: 2, Source: "a|b", Message: "c", Count: 1},
		{Level: 2, Source: "a", Message: "b|c", Count: 1},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	if err := json.Unmarshal([]byte(st.Args[0].(string)), &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0][2] != "<ip>" {
		t.Errorf("source = %v, want the address cut", rows[0][2])
	}
	if rows[1][3] != "failed to dial <host> from <ip>:port" || rows[1][5] != `{"err":"<host>"}` {
		t.Errorf("xray row = %v / %v", rows[1][3], rows[1][5])
	}
	if rows[2][0] == rows[3][0] {
		t.Errorf("a|b + c and a + b|c share the key %v", rows[2][0])
	}
}

func TestScrubJournalText(t *testing.T) {
	secrets := []string{"/abcdefghijklmnopqrstuvwx/", "k3j2h4g5f6d7s8a9.example.com", "k3j2h4g5f6d7s8a9", "www", ""}
	for _, c := range []struct{ in, want string }{
		{"dial K3J2H4G5F6D7S8A9.example.com from 203.0.113.7:5000", "dial <secret> from <ip>:port"},
		{"GET /abcdefghijklmnopqrstuvwx/api and ABCDEFGHIJKLMNOPQRSTUVWX", "GET /<secret>/api and <secret>"},
		{"label k3j2h4g5f6d7s8a9 alone", "label <secret> alone"},
		{"www stays, a short secret is not one", "www stays, a short secret is not one"},
		// non-ASCII text whose lower case is longer must not shift the cut (it used to slice past the end)
		{"İİİİ k3j2h4g5f6d7s8a9 İ", "İİİİ <secret> İ"},
		{"ẞẞ abcdefghijklmnopqrstuvwx", "ẞẞ <secret>"},
	} {
		if got := scrubJournalText(c.in, secrets); got != c.want {
			t.Errorf("scrubJournalText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
