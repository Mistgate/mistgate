package health

import (
	"errors"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// A node blocked from everywhere makes every probe dark (they reach the other node). Those are not suspects: the named
// slots keep rotating through the operators instead of re-asking the same probes for as long as the block lasts.
func TestCountryWantsNoSuspectsFromAFullyBlockedNode(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.onlineNode("nl1", "203.0.113.20")
	c.src.set("de1", "blocked")
	c.tick()
	c.tick()
	c.tick()
	if v := c.verdicts("de1"); v[0] != verdictBlocked {
		t.Fatalf("de1 verdicts %v", v)
	}
	rounds := c.src.roundsSnapshot()
	last := rounds[len(rounds)-1]
	for _, w := range last.Want[:last.Probes-1] {
		if w.City != "" {
			t.Errorf("a named slot went to the suspect %s@%s of a fully blocked node", w.Name, w.City)
		}
	}
}

// A failed round between two others does not make the panel forget the dark probe of the round before it.
func TestCountryWantsSurviveAFailedRound(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.onlineNode("nl1", "203.0.113.20")
	c.src.set("de1", "dark:0")
	c.tick() // partial, probe 0 dark
	c.src.mu.Lock()
	c.src.err = errors.New("boom")
	c.src.mu.Unlock()
	c.tick() // source_error: unknown rows without probes
	c.src.mu.Lock()
	c.src.err = nil
	c.src.mu.Unlock()
	c.tick()
	rounds := c.src.roundsSnapshot()
	if len(rounds) < 3 {
		t.Fatalf("%d rounds", len(rounds))
	}
	if w := rounds[2].Want; len(w) == 0 || w[0].City == "" {
		t.Errorf("after one failed round the dark probe is no longer asked first: %+v", w)
	}
}

// A full-block alert closes on two rounds in which other networks reach the node, also when one network is still dark.
func TestCountryPartialRoundsCloseTheFullBlockAlert(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.onlineNode("nl1", "203.0.113.20")
	const k = "country_blocked/de1/"
	c.src.set("de1", "blocked")
	c.tick()
	c.tick()
	mustHave(t, c, k, true)
	c.src.set("de1", "dark:0")
	c.tick()
	c.tick()
	mustHave(t, c, k, false)
}

// ICMP from one probe and nothing from two, and no TCP anywhere: nobody reaches the node, so it is not partial.
func TestCountryJudgeNoReachIsNotPartial(t *testing.T) {
	tgt := CountryTarget{Key: "de1", Ports: []uint16{443}}
	mk := func(icmp bool) ObservedProbe {
		return ObservedProbe{Network: "n", Eyeball: true, Online: true, ControlOK: true,
			Nodes: map[string]ObservedNode{"de1": {Answered: true, Complete: true, ICMPOK: icmp, TCP: []ObservedPort{{Port: 443}}}}}
	}
	obs := CountryObserved{Eyeball: true, Probes: []ObservedProbe{mk(true), mk(false), mk(false)}}
	if v, d := countryJudge(tgt, obs, []bool{true, true, true}); v == verdictPartial {
		t.Fatalf("partial with no probe reaching the node over TCP: %+v", d)
	}
}

// A probe whose network drops ICMP everywhere reaches the VLESS node over TCP. It must not make an ICMP-only node partial;
// the same probe reaching another node only over TCP still makes a node with ports partial.
func TestCountryReachedUsesTheTargetsCarrier(t *testing.T) {
	vless := CountryTarget{Key: "de1", Ports: []uint16{443}}
	awg := CountryTarget{Key: "awg1"}
	probe := func(name string, icmp bool) ObservedProbe {
		return ObservedProbe{Network: name, Eyeball: true, Online: true, ControlOK: true, Nodes: map[string]ObservedNode{
			"de1":  {Answered: true, Complete: true, ICMPOK: icmp, TCP: []ObservedPort{{Port: 443, OK: true}}},
			"awg1": {Answered: true, Complete: true, ICMPOK: icmp},
		}}
	}
	obs := CountryObserved{Eyeball: true, Probes: []ObservedProbe{probe("NoPingMobile", false), probe("A", true), probe("B", true)}}
	targets := []CountryTarget{vless, awg}
	if v, _ := countryJudge(awg, obs, countryReached(obs, targets, true)); v == verdictPartial {
		t.Errorf("an ICMP-only node is partial only because one probe's network drops ICMP")
	}

	fi := CountryTarget{Key: "fi1", Ports: []uint16{443}}
	dark := ObservedProbe{Network: "Dark", Eyeball: true, Online: true, ControlOK: true, Nodes: map[string]ObservedNode{
		"de1": {Answered: true, Complete: true, TCP: []ObservedPort{{Port: 443, OK: true}}}, // TCP only
		"fi1": {Answered: true, Complete: true, TCP: []ObservedPort{{Port: 443}}},
	}}
	seen := func(name string) ObservedProbe {
		return ObservedProbe{Network: name, Eyeball: true, Online: true, ControlOK: true, Nodes: map[string]ObservedNode{
			"fi1": {Answered: true, Complete: true, ICMPOK: true, TCP: []ObservedPort{{Port: 443, OK: true}}},
		}}
	}
	obs = CountryObserved{Eyeball: true, Probes: []ObservedProbe{dark, seen("A"), seen("B")}}
	v, d := countryJudge(fi, obs, countryReached(obs, []CountryTarget{vless, fi}, false))
	if v != verdictPartial || !d.Probes[0].Dark {
		t.Fatalf("verdict %s dark %v, want partial with probe 0 dark (it reached de1 over TCP)", v, d.Probes[0].Dark)
	}
}

// Suspects come only from valid home or mobile probes, and carry the operator's name from the table.
func TestCountryWantsSuspectFiltersAndOperatorName(t *testing.T) {
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows := []store.CountryCheck{{Country: "RU", At: base, Verdict: "partial", Detail: `{"probes":[` +
		`{"network":"DC","asn":64500,"city":"Kazan","dark":true,"valid":true},` +
		`{"network":"Gone","asn":64501,"city":"Omsk","dark":true,"eyeball":true},` +
		`{"network":"Vimpelcom","asn":3216,"city":"Ufa","dark":true,"valid":true,"eyeball":true}]}`}}
	wants := countryWants("RU", rows)
	if len(wants) == 0 || wants[0].City != "Ufa" || wants[0].Name != "Beeline" {
		t.Fatalf("first want %+v, want the Ufa probe named Beeline", wants)
	}
	for _, w := range wants {
		if w.City == "Kazan" || w.City == "Omsk" {
			t.Errorf("suspect %+v from a data-centre or invalid probe", w)
		}
	}
}
