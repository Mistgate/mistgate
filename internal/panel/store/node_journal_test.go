//go:build !js

package store

import "testing"

func TestNodeJournalSQLite(t *testing.T) {
	nodeJournalStoreProof(t, openTemp(t))
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
