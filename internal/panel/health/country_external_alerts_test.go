package health

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func externalAlertEnv(t *testing.T, sources ...ExternalState) *countryEnv {
	t.Helper()
	c := partialAlertEnv(t)
	c.cfg.Enabled = false
	c.s.cfg.External = func(context.Context) []ExternalState { return sources }
	return c
}

func alertVantage(key, network, region, state string) countryreach.Vantage {
	return countryreach.Vantage{Key: key, Network: network, Region: region, Kind: countryreach.VantageKindMobile, State: state}
}

func putExternalAlertRow(t *testing.T, c *countryEnv, nodeID, source, verdict string, at time.Time, vantages ...countryreach.Vantage) {
	t.Helper()
	detail := countryreach.ExtDetail{Vantages: vantages}
	if verdict == verdictUnknown {
		detail.Reason = "cap_reached"
	}
	if err := c.st.InsertExtChecks(c.ctx, []store.ExtCheck{{NodeID: nodeID, Source: source, Verdict: verdict, At: at, DetailJSON: mustJSON(detail)}}); err != nil {
		t.Fatal(err)
	}
}

func putBsbordSweep(t *testing.T, c *countryEnv, at time.Time, deVerdict, nlVerdict, deState, nlState string) {
	t.Helper()
	putExternalAlertRow(t, c, "de1", countryreach.SourceBsbord, deVerdict, at,
		alertVantage("mts|krym|off", "MTS", "Crimea", deState))
	putExternalAlertRow(t, c, "nl1", countryreach.SourceBsbord, nlVerdict, at,
		alertVantage("mts|krym|off", "MTS", "Crimea", nlState))
}

func TestNarrowBsbordAllDarkOpensOnlyThePartialAlert(t *testing.T) {
	c := externalAlertEnv(t, ExternalState{Source: countryreach.SourceBsbord, On: true, MaxAge: 18 * time.Hour})
	first := c.clock.Now().Truncate(time.Second).Add(-12 * time.Hour)
	putBsbordSweep(t, c, first, verdictBlocked, verdictOK, countryreach.VantageStateDark, countryreach.VantageStateReach)
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/partial", false) // one D

	putBsbordSweep(t, c, first.Add(6*time.Hour), verdictBlocked, verdictOK, countryreach.VantageStateDark, countryreach.VantageStateReach)
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", false)
	mustHave(t, c, "country_blocked/de1/partial", true)
	alert := c.active()["country_blocked/de1/partial"]
	if alert.Params["sources"] != countryreach.SourceBsbord || alert.Params["networks"] != "MTS (Crimea, bsbord)" {
		t.Fatalf("partial alert = %+v, want the Crimea unit named", alert.Params)
	}
}

func TestNarrowBsbordDarkAcrossEveryNodeDoesNotCreatePartialAlert(t *testing.T) {
	c := externalAlertEnv(t, ExternalState{Source: countryreach.SourceBsbord, On: true, MaxAge: 18 * time.Hour})
	first := c.clock.Now().Truncate(time.Second).Add(-12 * time.Hour)
	for _, at := range []time.Time{first, first.Add(6 * time.Hour)} {
		putBsbordSweep(t, c, at, verdictBlocked, verdictBlocked, countryreach.VantageStateDark, countryreach.VantageStateDark)
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", false)
	mustHave(t, c, "country_blocked/de1/partial", false)
}

func TestGlobalpingBlockSupersedesNarrowBsbordPartial(t *testing.T) {
	c := externalAlertEnv(t, ExternalState{Source: countryreach.SourceBsbord, On: true, MaxAge: 18 * time.Hour})
	first := c.clock.Now().Truncate(time.Second).Add(-12 * time.Hour)
	second := first.Add(6 * time.Hour)
	for _, at := range []time.Time{first, second} {
		putBsbordSweep(t, c, at, verdictBlocked, verdictOK, countryreach.VantageStateDark, countryreach.VantageStateReach)
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/partial", true)

	c.cfg.Enabled = true
	gpDetail := mustJSON(countryDetail{Eyeball: true, Ports: []uint16{443}, Probes: []countryProbeDetail{{
		Network: "Rostelecom", ASN: 12389, City: "Moscow", Eyeball: true, Valid: true, Dark: true,
		TCP: []countryPortDetail{{Port: 443}},
	}}})
	if err := c.st.InsertCountryChecks(c.ctx, []store.CountryCheck{
		{NodeID: "de1", At: first, Country: "RU", Verdict: verdictBlocked, Detail: gpDetail},
		{NodeID: "de1", At: second, Country: "RU", Verdict: verdictBlocked, Detail: gpDetail},
	}); err != nil {
		t.Fatal(err)
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", true)
	mustHave(t, c, "country_blocked/de1/partial", false)
	alert := c.active()["country_blocked/de1/"]
	if alert.Params["networks"] != "MTS (Crimea, bsbord), Rostelecom" || alert.Params["sources"] != "globalping,bsbord" {
		t.Fatalf("full alert = %+v, want dark vantages from both sources", alert.Params)
	}
}

func TestExternalReachEventDoesNotCountTowardFullAlertWithoutOwnBlock(t *testing.T) {
	c := externalAlertEnv(t, ExternalState{Source: countryreach.SourceCheburcheckOwn, On: true, MaxAge: 3 * time.Hour})
	c.cfg.Enabled = true
	first := c.clock.Now().Truncate(time.Second).Add(-time.Hour)
	gpDetail := mustJSON(countryDetail{Eyeball: true, Ports: []uint16{443}})
	if err := c.st.InsertCountryChecks(c.ctx, []store.CountryCheck{{NodeID: "de1", At: first, Country: "RU", Verdict: verdictTCPBlocked, Detail: gpDetail}}); err != nil {
		t.Fatal(err)
	}
	putExternalAlertRow(t, c, "de1", countryreach.SourceCheburcheckOwn, verdictOK, first.Add(30*time.Minute),
		alertVantage("probe:17", "Rostelecom", "Crimea", countryreach.VantageStateReach))
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", false) // the own-probe R combines to a full result, but is not a bad event

	if err := c.st.InsertCountryChecks(c.ctx, []store.CountryCheck{{NodeID: "de1", At: first.Add(time.Hour), Country: "RU", Verdict: verdictTCPBlocked, Detail: gpDetail}}); err != nil {
		t.Fatal(err)
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", true) // two Globalping events say no reach
}

// putGlobalpingRound stores a Globalping round of node with the given probes (eyeball false is the datacentre fallback).
func putGlobalpingRound(t *testing.T, c *countryEnv, node, verdict string, at time.Time, eyeball bool, probes ...countryProbeDetail) {
	t.Helper()
	b, _ := json.Marshal(countryDetail{Probes: probes, Eyeball: eyeball, Ports: []uint16{443}})
	if err := c.st.InsertCountryChecks(c.ctx, []store.CountryCheck{{NodeID: node, At: at, Country: "RU", Verdict: verdict, Detail: string(b)}}); err != nil {
		t.Fatal(err)
	}
}

// Two Globalping fallback rounds (too few home probes online) that say blocked open the full alert: Globalping is country-wide
// whatever its probe types are.
func TestGlobalpingFallbackBlockOpensTheFullAlert(t *testing.T) {
	c := partialAlertEnv(t)
	base := c.clock.Now().Add(-time.Hour)
	dc := func(net string, asn uint32) countryProbeDetail {
		return countryProbeDetail{Network: net, ASN: asn, City: "Moscow", Valid: true, TCP: []countryPortDetail{{Port: 443}}}
	}
	for i := range 2 {
		putGlobalpingRound(t, c, "de1", verdictBlocked, base.Add(time.Duration(i)*time.Minute), false, dc("DCNetA", 64530), dc("DCNetB", 64531))
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", true)
}

// A full-block round is no evidence of a partial block, even when one of its home probes carries a stored dark flag.
func TestGlobalpingFallbackBlockIsNoPartialEvidence(t *testing.T) {
	c := partialAlertEnv(t)
	base := c.clock.Now().Add(-time.Hour)
	home := countryProbeDetail{Network: "MTS", ASN: 8359, City: "Novosibirsk", Eyeball: true, Valid: true, Dark: true, TCP: []countryPortDetail{{Port: 443}}}
	dc := countryProbeDetail{Network: "DCNetA", ASN: 64530, City: "Moscow", Valid: true, TCP: []countryPortDetail{{Port: 443}}}
	for i := range 2 {
		putGlobalpingRound(t, c, "de1", verdictBlocked, base.Add(time.Duration(i)*time.Minute), false, home, dc)
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/partial", false)
	mustHave(t, c, "country_blocked/de1/", true)
}

// bsbord at the owner's 24 h interval (MaxAge 72 h) reached de1 20 hours ago. Since then Globalping's home probes see de1
// blocked in three rounds while they reach nl1: the old bsbord R must not hide the country-wide block.
func TestOldBsbordReachDoesNotHideACountryWideBlock(t *testing.T) {
	c := externalAlertEnv(t, ExternalState{Source: countryreach.SourceBsbord, On: true, MaxAge: 72 * time.Hour})
	c.cfg.Enabled = true
	now := c.clock.Now().Truncate(time.Second)
	putBsbordSweep(t, c, now.Add(-20*time.Hour), verdictOK, verdictOK, countryreach.VantageStateReach, countryreach.VantageStateReach)
	for _, ago := range []time.Duration{30 * time.Minute, 15 * time.Minute, 0} {
		insertCountryAlertRound(t, c, "de1", verdictBlocked, now.Add(-ago), reviewBlockedProbes()...)
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/partial", false)
	mustHave(t, c, "country_blocked/de1/", true)
}

// More unknown bsbord rows than the evaluator reads back (countryRecent) push the D, D rows out of the window; only the hold
// keeps the alert.
func TestBsbordOutageLongerThanTheWindowHoldsThePartialAlert(t *testing.T) {
	c := externalAlertEnv(t, ExternalState{Source: countryreach.SourceBsbord, On: true, MaxAge: 18 * time.Hour})
	first := c.clock.Now().Truncate(time.Second).Add(-40 * time.Hour)
	putBsbordSweep(t, c, first, verdictBlocked, verdictOK, countryreach.VantageStateDark, countryreach.VantageStateReach)
	putBsbordSweep(t, c, first.Add(time.Hour), verdictBlocked, verdictOK, countryreach.VantageStateDark, countryreach.VantageStateReach)
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/partial", true)
	for i := range countryRecent {
		putExternalAlertRow(t, c, "de1", countryreach.SourceBsbord, verdictUnknown, first.Add(time.Duration(2+i)*time.Hour))
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/partial", true) // cap_reached is an outage, not an R
}

// A white-list unit that gets nothing in a round that is partial for another reason is no D.
func TestWhitelistUnitIsNoDarkInAPartialRound(t *testing.T) {
	c := externalAlertEnv(t, ExternalState{Source: countryreach.SourceBsbord, On: true, MaxAge: 18 * time.Hour})
	first := c.clock.Now().Truncate(time.Second).Add(-12 * time.Hour)
	for _, at := range []time.Time{first, first.Add(6 * time.Hour)} {
		putExternalAlertRow(t, c, "de1", countryreach.SourceBsbord, verdictPartial, at,
			alertVantage("mts|krym|on", "MTS", "Crimea", countryreach.VantageStateWhitelist),
			alertVantage("volna|krym|off", "Volna", "Crimea", countryreach.VantageStateDark),
			alertVantage("miranda|krym|off", "Miranda", "Crimea", countryreach.VantageStateReach))
		putExternalAlertRow(t, c, "nl1", countryreach.SourceBsbord, verdictOK, at,
			alertVantage("mts|krym|on", "MTS", "Crimea", countryreach.VantageStateReach),
			alertVantage("volna|krym|off", "Volna", "Crimea", countryreach.VantageStateReach),
			alertVantage("miranda|krym|off", "Miranda", "Crimea", countryreach.VantageStateReach))
	}
	c.s.ReconcileCountry(c.ctx)
	alert, ok := c.active()["country_blocked/de1/partial"]
	if !ok || alert.Params["networks"] != "Volna (Crimea, bsbord)" {
		t.Fatalf("partial alert = %v (open %v), want only Volna", alert.Params, ok)
	}
}

// The vantage key is shared between sources: the owner's probe also shows in the public run as probe:17. A public R must not
// clear the own probe's D, D.
func TestPublicReachDoesNotClearOwnProbeKey(t *testing.T) {
	c := externalAlertEnv(t,
		ExternalState{Source: countryreach.SourceCheburcheck, On: true, MaxAge: 9 * time.Hour},
		ExternalState{Source: countryreach.SourceCheburcheckOwn, On: true, MaxAge: 3 * time.Hour})
	t0 := c.clock.Now().Truncate(time.Second).Add(-3 * time.Hour)
	for _, at := range []time.Time{t0, t0.Add(time.Hour)} {
		putExternalAlertRow(t, c, "de1", countryreach.SourceCheburcheckOwn, verdictTCPBlocked, at,
			alertVantage("probe:17", "Volna", "Crimea", countryreach.VantageStateDark))
		putExternalAlertRow(t, c, "nl1", countryreach.SourceCheburcheckOwn, verdictOK, at.Add(time.Minute),
			alertVantage("probe:17", "Volna", "Crimea", countryreach.VantageStateReach))
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/partial", true)
	putExternalAlertRow(t, c, "de1", countryreach.SourceCheburcheck, verdictOK, t0.Add(2*time.Hour),
		alertVantage("probe:17", "Volna", "Crimea", countryreach.VantageStateReach),
		alertVantage("probe:40", "Rostelecom", "Moscow", countryreach.VantageStateReach))
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/partial", true)
}

// The owner's rule: a full alert needs a country-wide source (Globalping, public cheburcheck, the TSPU probe). Two narrow
// sources that cover the same region (the owner's Crimea probe and bsbord's Crimea units) agreeing on a block raise only the
// partial alert, naming their networks.
func TestTwoNarrowSourcesOpenOnlyThePartialAlert(t *testing.T) {
	c := externalAlertEnv(t,
		ExternalState{Source: countryreach.SourceCheburcheckOwn, On: true, MaxAge: 3 * time.Hour},
		ExternalState{Source: countryreach.SourceBsbord, On: true, MaxAge: 72 * time.Hour})
	now := c.clock.Now().Truncate(time.Second)
	for _, ago := range []time.Duration{2 * time.Hour, time.Hour, 0} {
		putBsbordSweep(t, c, now.Add(-ago), verdictBlocked, verdictOK, countryreach.VantageStateDark, countryreach.VantageStateReach)
		putExternalAlertRow(t, c, "de1", countryreach.SourceCheburcheckOwn, verdictTCPBlocked, now.Add(-ago),
			alertVantage("probe:17", "Volna", "Crimea", countryreach.VantageStateDark))
		putExternalAlertRow(t, c, "nl1", countryreach.SourceCheburcheckOwn, verdictOK, now.Add(-ago),
			alertVantage("probe:17", "Volna", "Crimea", countryreach.VantageStateReach))
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", false)
	alert, ok := c.active()["country_blocked/de1/partial"]
	if !ok || alert.Params["sources"] != "cheburcheck_own,bsbord" || alert.Params["networks"] != "MTS (Crimea, bsbord), Volna (Crimea, cheburcheck_own)" {
		t.Fatalf("partial alert = %v (open %v), want both narrow sources and their networks", alert.Params, ok)
	}
}

// bsbord says dark while Globalping says the node is blocked from everywhere: the combined result is the full block, so the
// bsbord row is no D. Without Globalping the same row is one.
func TestBsbordDarkWhileGlobalpingSaysBlockedIsNoDark(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	row := func(node, verdict, state string) countryAlertSignal {
		return countryExternalSignal(store.ExtCheck{NodeID: node, Source: countryreach.SourceBsbord, Verdict: verdict, At: at,
			DetailJSON: mustJSON(countryreach.ExtDetail{Vantages: []countryreach.Vantage{alertVantage("mts|krym|off", "MTS", "Crimea", state)}})}, 18*time.Hour)
	}
	bsbord := []countryAlertSignal{row("de1", verdictBlocked, countryreach.VantageStateDark), row("nl1", verdictOK, countryreach.VantageStateReach)}
	gp := countryGlobalpingSignal(store.CountryCheck{NodeID: "de1", At: at, Country: "RU", Verdict: verdictBlocked,
		Detail: mustJSON(countryDetail{Eyeball: true, Ports: []uint16{443}, Probes: reviewBlockedProbes()})}, 3*time.Hour)
	dark := func(signals []countryAlertSignal) int {
		n := 0
		for _, seen := range countryObservations(countryEvents(signals, "de1"), signals) {
			for _, o := range seen {
				if o.source == countryreach.SourceBsbord && o.kind == countryObservationDark {
					n++
				}
			}
		}
		return n
	}
	if got := dark(bsbord); got != 1 {
		t.Fatalf("bsbord alone gave %d D observations, want 1", got)
	}
	if got := dark(append([]countryAlertSignal{gp}, bsbord...)); got != 0 {
		t.Fatalf("bsbord next to a Globalping block gave %d D observations, want none", got)
	}
}

// The other node's R backs a cheburcheck D only inside the source's interval (a third of its max age) on either side;
// bsbord needs the same run. A vantage without a key is matched to nothing.
func TestCheburcheckCrossNodeReachWindow(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	row := func(source, node, verdict, key, state string, when time.Time) countryAlertSignal {
		return countryExternalSignal(store.ExtCheck{NodeID: node, Source: source, Verdict: verdict, At: when,
			DetailJSON: mustJSON(countryreach.ExtDetail{Vantages: []countryreach.Vantage{alertVantage(key, "Volna", "Crimea", state)}})}, 9*time.Hour)
	}
	for _, tc := range []struct {
		name     string
		source   string
		darkKey  string
		reachKey string
		offset   time.Duration
		want     bool
	}{
		{"same run", countryreach.SourceCheburcheck, "probe:17", "probe:17", 0, true},
		{"later, inside the interval", countryreach.SourceCheburcheck, "probe:17", "probe:17", 2 * time.Hour, true},
		{"earlier, inside the interval", countryreach.SourceCheburcheck, "probe:17", "probe:17", -2 * time.Hour, true},
		{"at the interval", countryreach.SourceCheburcheckOwn, "probe:17", "probe:17", 3 * time.Hour, true},
		{"later, outside the interval", countryreach.SourceCheburcheck, "probe:17", "probe:17", 3*time.Hour + time.Minute, false},
		{"earlier, outside the interval", countryreach.SourceCheburcheck, "probe:17", "probe:17", -4 * time.Hour, false},
		{"bsbord, not the same run", countryreach.SourceBsbord, "probe:17", "probe:17", time.Minute, false},
		{"bsbord, the same run", countryreach.SourceBsbord, "probe:17", "probe:17", 0, true},
		{"another vantage", countryreach.SourceCheburcheck, "probe:40", "probe:17", 0, false},
		{"no key", countryreach.SourceCheburcheck, "", "", 0, false},
	} {
		dark := row(tc.source, "de1", verdictTCPBlocked, tc.darkKey, countryreach.VantageStateDark, at)
		other := row(tc.source, "nl1", verdictOK, tc.reachKey, countryreach.VantageStateReach, at.Add(tc.offset))
		if got := countryVantageReachedAnother(dark, dark.vantages[0], []countryAlertSignal{dark, other}); got != tc.want {
			t.Errorf("%s: reached another = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The full alert that public cheburcheck opened closes on two external reach events, and one is not enough.
func TestFullAlertClosesOnExternalReachEvents(t *testing.T) {
	c := externalAlertEnv(t, ExternalState{Source: countryreach.SourceCheburcheck, On: true, MaxAge: 9 * time.Hour})
	now := c.clock.Now().Truncate(time.Second)
	dark := alertVantage("probe:40", "Rostelecom", "Moscow", countryreach.VantageStateDark)
	reach := alertVantage("probe:40", "Rostelecom", "Moscow", countryreach.VantageStateReach)
	for _, ago := range []time.Duration{3 * time.Hour, 2 * time.Hour} {
		putExternalAlertRow(t, c, "de1", countryreach.SourceCheburcheck, verdictTCPBlocked, now.Add(-ago), dark)
	}
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", true)

	putExternalAlertRow(t, c, "de1", countryreach.SourceCheburcheck, verdictOK, now.Add(-time.Hour), reach)
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", true) // one R

	putExternalAlertRow(t, c, "de1", countryreach.SourceCheburcheck, verdictOK, now, reach)
	c.s.ReconcileCountry(c.ctx)
	mustHave(t, c, "country_blocked/de1/", false)
}
