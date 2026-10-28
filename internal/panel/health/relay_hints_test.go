package health

import (
	"slices"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// RelayHints (relay_hints.go): what the country check and the TSPU probe say about the exits an entry could relay, and nothing
// where they say nothing.

func (c *countryEnv) refsPass() {
	c.t.Helper()
	c.tspu.Run = &adminv1.TspuProbeRun{Status: "no_dpi_hop", Reason: "refs_pass", AtUnix: c.clock.Now().Unix(), NodeId: "probe1"}
}

func TestRelayHintsSayNothingWhereNothingIsKnown(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	country, reach := c.s.RelayHints(c.ctx, "nl1", []string{"de1"})
	if len(country) != 0 || len(reach) != 0 {
		t.Fatalf("nothing measured: %v %v", country, reach)
	}
	// a round that could not say (too few probes) is not a verdict either
	c.onlineNode("nl1", "203.0.113.20")
	c.src.set("de1", "few")
	c.tick()
	if country, _ = c.s.RelayHints(c.ctx, "nl1", []string{"de1"}); len(country) != 0 {
		t.Fatalf("an unknown round: %v", country)
	}
}

func TestRelayHintsNameTheNetworksThatSeeTheExitDark(t *testing.T) {
	c := newCountryEnv(t)
	for id, ip := range map[string]string{"de1": "203.0.113.10", "nl1": "203.0.113.20", "fr1": "203.0.113.30", "fi1": "203.0.113.40"} {
		c.onlineNode(id, ip)
	}
	c.src.set("de1", "dark:0") // Rostelecom gets nothing from de1 but reaches the others
	c.src.set("fr1", "blocked")
	c.src.set("nl1", "ok")
	c.tick()
	country, reach := c.s.RelayHints(c.ctx, "fi1", []string{"de1", "nl1", "fr1", "zz9"})
	if len(reach) != 0 {
		t.Errorf("no probe node, but reach hints: %v", reach)
	}
	if h := country["de1"]; h == nil || h.Verdict != "partial" || !slices.Equal(h.Dark, []string{"Rostelecom"}) || h.AtUnix == 0 {
		t.Errorf("de1 = %v", h)
	}
	if h := country["fr1"]; h == nil || h.Verdict != "blocked" || !slices.Equal(h.Dark, []string{"Beeline", "ER-Telecom", "Rostelecom"}) {
		t.Errorf("fr1 = %v", h)
	}
	if h := country["nl1"]; h == nil || h.Verdict != "ok" || len(h.Dark) != 0 {
		t.Errorf("nl1 = %v", h)
	}
	if h := country["fi1"]; h == nil || h.Verdict != "ok" {
		t.Errorf("the entry itself is described too: %v", h)
	}
	if _, ok := country["zz9"]; ok {
		t.Error("a node nobody measured has a hint")
	}
}

func TestRelayHintsReachFromTheProbeNodeOnlyForThatEntry(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.onlineNode("nl1", "203.0.113.20")
	c.onlineNode("fr1", "203.0.113.30")
	c.tspuOn()
	exits := []string{"de1", "nl1", "fr1"}
	reachOf := func(entry string) map[string]*adminv1.RelayReachHint {
		_, reach := c.s.RelayHints(c.ctx, entry, exits)
		return reach
	}
	if r := reachOf("probe1"); len(r) != 0 {
		t.Fatalf("the probe has said nothing about these nodes yet: %v", r)
	}

	// The probe's rows from this entry: per exit.
	c.putTSPU("de1", "open", tport{Port: 443, Verdict: "open"})
	c.putTSPU("nl1", "blocked", tport{Port: 443, Verdict: "blocked"})
	r := reachOf("probe1")
	if h := r["de1"]; h == nil || h.Verdict != "reach" || h.Source != "tspu_row" || h.AtUnix != c.clock.Now().Unix() {
		t.Errorf("de1 = %v", h)
	}
	if h := r["nl1"]; h == nil || h.Verdict != "blocked" || h.Source != "tspu_row" {
		t.Errorf("nl1 = %v", h)
	}
	if h := r["fr1"]; h != nil {
		t.Errorf("fr1 has no row, and the run found a TSPU: %v", h)
	}

	// Another entry: the probe did not run from it, so the rows say nothing about its path.
	if r := reachOf("de1"); len(r) != 0 {
		t.Errorf("rows of another vantage used: %v", r)
	}

	// A run that found no TSPU on the path (a blocked reference answered): nothing is cut there, for the exits without a row.
	c.refsPass()
	r = reachOf("probe1")
	if h := r["fr1"]; h == nil || h.Verdict != "reach" || h.Source != "refs_pass" {
		t.Errorf("fr1 after refs_pass = %v", h)
	}
	if h := r["nl1"]; h == nil || h.Verdict != "blocked" || h.Source != "tspu_row" {
		t.Errorf("a row is more specific than the run: %v", h)
	}
	if r := reachOf("de1"); len(r) != 0 {
		t.Errorf("refs_pass of another vantage: %v", r)
	}

	// no_hops, or any other end of a run, is no statement about the path
	c.tspu.Run = &adminv1.TspuProbeRun{Status: "no_dpi_hop", Reason: "no_hops", AtUnix: c.clock.Now().Unix(), NodeId: "probe1"}
	if h := reachOf("probe1")["fr1"]; h != nil {
		t.Errorf("no_hops: %v", h)
	}
	c.refsPass()
	c.tspu.Run.Status = "failed"
	if h := reachOf("probe1")["fr1"]; h != nil {
		t.Errorf("a failed run: %v", h)
	}

	// Old data is no data.
	c.refsPass()
	c.clock.Advance(7 * time.Hour)
	if r := reachOf("probe1"); len(r) != 0 {
		t.Errorf("rows and a run older than the probe's age limit: %v", r)
	}

	// The source off: nothing.
	c.clock.Advance(-7 * time.Hour)
	c.tspu.On = false
	if r := reachOf("probe1"); len(r) != 0 {
		t.Errorf("the probe is off: %v", r)
	}
}
