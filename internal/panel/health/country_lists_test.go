package health

import (
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// putList stores what the block lists said of a node (what the blocklist package does after a check).
func (c *countryEnv) putList(node string, listed bool, cdn string) {
	c.t.Helper()
	row := store.BlocklistCheck{NodeID: node, At: c.clock.Now(), ListsAt: c.clock.Now().Add(-time.Hour), CDNProvider: cdn, CDNRegion: "eu"}
	if listed {
		row.InRKN, row.RKNSubnet, row.ListedBy = true, "203.0.113.0/24", "antifilter_ipsum,antifilter_community"
	}
	if err := c.st.InsertBlocklistChecks(c.ctx, []store.BlocklistCheck{row}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *countryEnv) view(node string) *adminv1.CountryCheck {
	c.t.Helper()
	rows, err := c.s.countryChecks(c.ctx, node)
	if err != nil {
		c.t.Fatal(err)
	}
	if len(rows) != 1 {
		c.t.Fatalf("country_checks = %v, want one", rows)
	}
	return rows[0]
}

// GetChecks combines the live round with the block lists: a live verdict wins and the lists add detail; without one a hit is
// LISTED; a miss says nothing. A source that is off shows nothing.
func TestCountryChecksCombineTheLists(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.node("nl1", "hoster-nl1", true) // a node whose name does not resolve: the live round can say nothing of it
	c.vlessInbound("nl1", 443)
	c.src.set("de1", "tcp_blocked")
	c.tick() // one live round: de1 tcp_blocked (eyeball), nl1 unknown (no IPv4)

	// lists off: the live verdicts, with their confidence and no list detail
	de := c.view("de1")
	if de.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED || de.Confidence != "eyeball" || len(de.ListedBy) != 0 || de.CdnProvider != "" {
		t.Fatalf("lists off, de1 = %v", de)
	}
	if nl := c.view("nl1"); nl.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN || nl.Confidence != "" || nl.Reason != "no_ipv4" {
		t.Fatalf("lists off, nl1 = %v", nl)
	}

	c.lists.Store(true)
	c.putList("de1", true, "ExampleHost")
	c.putList("nl1", true, "ExampleCloud")
	de = c.view("de1")
	if de.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED || de.Confidence != "eyeball" || len(de.ListedBy) != 2 || de.ListedBy[0] != "antifilter_ipsum" ||
		de.CdnProvider != "ExampleHost" || de.CdnRegion != "eu" || len(de.Probes) == 0 {
		t.Fatalf("a live verdict keeps winning, the lists add detail: %v", de)
	}
	nl := c.view("nl1")
	if nl.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_LISTED || nl.Confidence != "listed" || nl.Reason != "no_ipv4" || nl.CdnProvider != "ExampleCloud" {
		t.Fatalf("an unknown round and a hit give LISTED: %v", nl)
	}

	// a miss proves nothing about reachability
	c.clock.Advance(time.Minute)
	c.putList("nl1", false, "ExampleCloud")
	if nl := c.view("nl1"); nl.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN || len(nl.ListedBy) != 0 || nl.CdnProvider != "ExampleCloud" {
		t.Fatalf("a miss, nl1 = %v", nl)
	}

	// the lists alone (the live source off): the nodes they know, LISTED only on a hit; the live rounds are not shown
	c.cfg.Enabled = false
	rows, err := c.s.countryChecks(c.ctx, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("lists only = %v, %v", rows, err)
	}
	if rows[0].NodeId != "de1" || rows[0].Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_LISTED || rows[0].Country != "RU" || len(rows[0].Probes) != 0 || rows[0].Confidence != "listed" {
		t.Fatalf("lists only, de1 = %v", rows[0])
	}
	if rows[1].NodeId != "nl1" || rows[1].Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN {
		t.Fatalf("lists only, nl1 = %v", rows[1])
	}

	// both off: nothing, though the rows are still stored
	c.lists.Store(false)
	if rows, err := c.s.countryChecks(c.ctx, ""); err != nil || len(rows) != 0 {
		t.Fatalf("both off = %v, %v", rows, err)
	}
}

// A list hit alone never opens COUNTRY_BLOCKED, however many times it is stored, with the live source off or with it
// reporting unknown rounds; only live verdicts do.
func TestCountryAlertNeverOpensFromAListHit(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.lists.Store(true)

	// the live source is off
	c.cfg.Enabled = false
	for range 4 {
		c.putList("de1", true, "ExampleHost")
		c.tick()
		c.clock.Advance(time.Minute)
	}
	mustHave(t, c, "country_blocked/de1/", false)

	// the live source is on but cannot say (one probe): unknown rounds neither open nor close
	c.cfg.Enabled = true
	c.src.set("de1", "few")
	for range 4 {
		c.putList("de1", true, "ExampleHost")
		c.tick()
		c.clock.Advance(time.Minute)
	}
	mustHave(t, c, "country_blocked/de1/", false)
	if v := c.view("de1").Verdict; v != adminv1.CountryVerdict_COUNTRY_VERDICT_LISTED {
		t.Fatalf("verdict = %v, want LISTED", v)
	}

	// a live bad verdict does open it, with the list hit in the view only
	c.src.set("de1", "blocked")
	c.tick()
	c.tick()
	mustHave(t, c, "country_blocked/de1/", true)

	// and an ok live verdict closes it though the lists still hold the address
	c.src.set("de1", "ok")
	c.tick()
	c.tick()
	mustHave(t, c, "country_blocked/de1/", false)
	if v := c.view("de1"); v.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_OK || len(v.ListedBy) == 0 {
		t.Fatalf("an ok round with a list hit = %v", v)
	}
}

// The retention sweep removes block-list rows older than the 7 days of the country check.
func TestCountryRetentionPrunesBlocklistChecks(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	now := c.clock.Now()
	if err := c.st.InsertBlocklistChecks(c.ctx, []store.BlocklistCheck{
		{NodeID: "de1", At: now.Add(-8 * 24 * time.Hour), InRKN: true, ListedBy: "antifilter_ipsum"},
		{NodeID: "de1", At: now.Add(-24 * time.Hour), ListedBy: ""},
	}); err != nil {
		t.Fatal(err)
	}
	c.s.sweep(c.ctx)
	rows, err := c.st.RecentBlocklistChecks(c.ctx)
	if err != nil || len(rows) != 1 || rows[0].InRKN || !rows[0].At.Equal(now.Add(-24*time.Hour).Truncate(time.Second)) {
		t.Fatalf("after the sweep: %+v, %v", rows, err)
	}
}
