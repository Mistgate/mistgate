package health

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestCountryChecksCombineFreshExternalSourcesAndExposeVantages(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.tspuOn()
	c.tspu.Run.Reason = "refs_pass"
	now := c.clock.Now()
	c.s.cfg.External = func(context.Context) []ExternalState {
		return []ExternalState{
			{Source: countryreach.SourceCheburcheck, On: true, MaxAge: 9 * time.Hour},
			{Source: countryreach.SourceCheburcheckOwn, On: true, MaxAge: 3 * time.Hour},
			{Source: countryreach.SourceBsbord, On: true, MaxAge: 18 * time.Hour},
		}
	}
	gpDetail := `{"eyeball":true,"ports":[443],"probes":[{"network":"Rostelecom","asn":12389,"city":"Moscow","valid":true,"icmp_ok":true,"tcp":[{"port":443,"ok":true}]}]}`
	gpTCPBlockedDetail := `{"eyeball":true,"ports":[443],"probes":[{"network":"NetA","valid":true,"icmp_ok":true,"tcp":[{"port":443,"ok":false}]}]}`
	if err := c.st.InsertCountryChecks(c.ctx, []store.CountryCheck{
		{NodeID: "de1", At: now.Add(-5 * time.Minute), Country: "RU", Verdict: verdictOK, Detail: gpDetail},
		{NodeID: "fi1", At: now.Add(-5 * time.Minute), Country: "RU", Verdict: verdictTCPBlocked, Detail: gpTCPBlockedDetail},
		{NodeID: "se1", At: now.Add(-50 * time.Minute), Country: "RU", Verdict: verdictUnknown, Detail: `{"reason":"too_few_probes","ports":[443],"probes":[{"network":"NetA","valid":true,"icmp_ok":true}]}`},
		{NodeID: "nl1", At: now.Add(-5 * time.Minute), Country: "RU", Verdict: verdictUnknown, Detail: `{"reason":"too_few_probes"}`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.st.InsertTSPUChecks(c.ctx, []store.TSPUCheck{
		{NodeID: "se1", Vantage: "probe1", At: now.Add(-7 * time.Hour), Verdict: "blocked", ControlOK: true},
		{NodeID: "nl1", Vantage: "probe1", At: now.Add(-2 * time.Minute), Verdict: "unknown", ControlOK: true},
	}); err != nil {
		t.Fatal(err)
	}
	encode := func(vs ...countryreach.Vantage) string {
		t.Helper()
		b, err := json.Marshal(countryreach.ExtDetail{Reason: "no_home_probe", Vantages: vs})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	dark := countryreach.Vantage{Key: "probe:17", Network: "Volna", Region: "Crimea", Kind: countryreach.VantageKindMobile, State: countryreach.VantageStateDark, DPIHop: 5}
	uncertain := countryreach.Vantage{Key: "unit:1", Network: "Win Mobile", Region: "Crimea", Kind: countryreach.VantageKindUnknown, State: countryreach.VantageStateUncertain}
	bsbordVantages := []countryreach.Vantage{
		{Key: "mts|krym|off", Network: "MTS", Region: "Crimea", Kind: countryreach.VantageKindMobile, State: countryreach.VantageStateReach, ICMPOK: true},
		{Key: "volna|krym|off", Network: "Volna", Region: "Crimea", Kind: countryreach.VantageKindMobile, State: countryreach.VantageStateDark},
	}
	var ext []store.ExtCheck
	add := func(node, source, verdict string, at time.Time, detail string) {
		ext = append(ext, store.ExtCheck{NodeID: node, Source: source, Verdict: verdict, At: at, DetailJSON: detail})
	}
	for _, state := range c.s.cfg.External(c.ctx) {
		staleAt := now.Add(-state.MaxAge - time.Hour)
		add("se1", state.Source, verdictOK, staleAt, encode(countryreach.Vantage{Key: "stale", State: countryreach.VantageStateReach}))
		add("nl1", state.Source, verdictUnknown, now.Add(-time.Minute), encode(uncertain))
	}
	add("de1", countryreach.SourceCheburcheck, verdictTCPBlocked, now.Add(-4*time.Minute), encode(dark))
	add("de1", countryreach.SourceCheburcheckOwn, verdictUnknown, now.Add(-3*time.Minute), encode(uncertain))
	add("de1", countryreach.SourceBsbord, verdictPartial, now.Add(-2*time.Minute), encode(bsbordVantages...))
	if err := c.st.InsertExtChecks(c.ctx, ext); err != nil {
		t.Fatal(err)
	}

	rows, err := c.s.countryChecks(c.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*adminv1.CountryCheck, len(rows))
	for _, row := range rows {
		byID[row.GetNodeId()] = row
	}
	check := byID["de1"]
	if check == nil || check.GetVerdict() != adminv1.CountryVerdict_COUNTRY_VERDICT_PARTIAL || check.GetConfidence() != countryreach.ConfidenceEyeball ||
		!slices.Equal(check.GetSources(), []string{countryreach.SourceGlobalping, countryreach.SourceCheburcheck, countryreach.SourceBsbord}) {
		t.Fatalf("fresh mixed sources = %+v", check)
	}
	vantageSources := map[string]bool{}
	for _, v := range check.GetVantages() {
		vantageSources[v.GetSource()] = true
	}
	for _, source := range []string{countryreach.SourceGlobalping, countryreach.SourceCheburcheck, countryreach.SourceBsbord} {
		if !vantageSources[source] {
			t.Errorf("partial check lacks %s vantages: %+v", source, check.GetVantages())
		}
	}
	if len(check.GetSourceVerdicts()) != 4 || check.GetSourceVerdicts()[2].GetVerdict() != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN {
		t.Errorf("fresh source verdicts (including unknown own probe) = %+v", check.GetSourceVerdicts())
	}
	var gpState string
	for _, v := range check.GetVantages() {
		if v.GetSource() == countryreach.SourceGlobalping {
			gpState = v.GetState()
		}
	}
	if gpState != countryreach.VantageStateReach {
		t.Errorf("Globalping TCP vantage state = %q, want reach", gpState)
	}
	if fi := byID["fi1"]; fi == nil || len(fi.GetVantages()) != 1 || fi.GetVantages()[0].GetState() != countryreach.VantageStateUncertain || !fi.GetVantages()[0].GetIcmpOk() {
		t.Errorf("ICMP-only TCP-blocked vantage = %+v, want uncertain with ICMP recorded", fi)
	}
	stale := byID["se1"]
	if stale == nil || stale.GetVerdict() != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN || len(stale.GetSources()) != 0 || len(stale.GetSourceVerdicts()) != 0 ||
		stale.GetReason() != "too_few_probes" || stale.GetGlobalpingVerdict() != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN || len(stale.GetProbes()) != 1 {
		t.Errorf("stale Globalping row should remain visible as unknown without live sources: %+v", stale)
	}
	unknown := byID["nl1"]
	if unknown == nil || unknown.GetVerdict() != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN || len(unknown.GetSources()) != 0 || len(unknown.GetSourceVerdicts()) != 5 {
		t.Errorf("fresh unknown rows = %+v", unknown)
	}
	if got := unknown.GetSourceVerdicts(); len(got) != 5 || got[0].GetReason() != "too_few_probes" || got[1].GetReason() != "refs_pass" || got[2].GetReason() != "no_home_probe" {
		t.Errorf("source reasons = %+v", got)
	}
}

func TestGlobalpingVantageUsesNodeCarrier(t *testing.T) {
	d := countryDetail{Ports: []uint16{443}, Probes: []countryProbeDetail{{Network: "NetA", Valid: true, ICMPOK: true,
		TCP: []countryPortDetail{{Port: 443, OK: false}}}}}
	got := globalpingVantages(d)
	if len(got) != 1 || got[0].State != countryreach.VantageStateUncertain {
		t.Fatalf("TCP node with ICMP success and TCP failure = %+v, want uncertain", got)
	}
	d.Ports = nil
	got = globalpingVantages(d)
	if len(got) != 1 || got[0].State != countryreach.VantageStateReach {
		t.Fatalf("ICMP-only node with ICMP success = %+v, want reach", got)
	}
}

func TestCountryChecksKeepLatestRoundOnStretchedGlobalpingInterval(t *testing.T) {
	nodes := make([]countryNode, 25)
	for i := range nodes {
		nodes[i] = countryNode{ports: []uint16{443, 8443, 2053}}
	}
	_, every, _ := countryPlan(nodes, 15*time.Minute, 250)
	if every <= 50*time.Minute {
		t.Fatalf("effective interval = %v, want > 50m", every)
	}
	c := newCountryEnv(t)
	now := c.clock.Now().Truncate(time.Second)
	newest := now.Add(-50 * time.Minute)
	if err := c.st.InsertCountryChecks(c.ctx, []store.CountryCheck{
		{NodeID: "de1", At: newest, Country: "RU", Verdict: verdictOK, Detail: `{"eyeball":true}`},
		{NodeID: "de1", At: newest.Add(-every), Country: "RU", Verdict: verdictOK, Detail: `{"eyeball":true}`},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := c.s.countryChecks(c.ctx, "de1")
	if err != nil || len(rows) != 1 || rows[0].GetVerdict() != adminv1.CountryVerdict_COUNTRY_VERDICT_OK ||
		!slices.Equal(rows[0].GetSources(), []string{countryreach.SourceGlobalping}) {
		t.Fatalf("stretched latest round = %+v, %v; want fresh Globalping row", rows, err)
	}
}

func TestCountryCheckAtUnixPrefersGlobalpingThenNewestOtherSource(t *testing.T) {
	c := newCountryEnv(t)
	c.lists.Store(true)
	c.tspuOn()
	now := c.clock.Now().Truncate(time.Second)
	gpAt, listAt, tspuAt := now.Add(-10*time.Minute), now.Add(-time.Minute), now.Add(-5*time.Minute)
	if err := c.st.InsertCountryChecks(c.ctx, []store.CountryCheck{{NodeID: "de1", At: gpAt, Country: "RU", Verdict: verdictOK}}); err != nil {
		t.Fatal(err)
	}
	if err := c.st.InsertTSPUChecks(c.ctx, []store.TSPUCheck{{NodeID: "fi1", Vantage: "probe1", At: tspuAt, Verdict: "open", ControlOK: true}}); err != nil {
		t.Fatal(err)
	}
	if err := c.st.InsertBlocklistChecks(c.ctx, []store.BlocklistCheck{
		{NodeID: "de1", At: listAt},
		{NodeID: "fi1", At: listAt},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := c.s.countryChecks(c.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*adminv1.CountryCheck, len(rows))
	for _, row := range rows {
		byID[row.GetNodeId()] = row
	}
	if byID["de1"] == nil || byID["de1"].GetAtUnix() != gpAt.Unix() {
		t.Errorf("Globalping at_unix = %+v, want %d", byID["de1"], gpAt.Unix())
	}
	if byID["fi1"] == nil || byID["fi1"].GetAtUnix() != listAt.Unix() {
		t.Errorf("non-Globalping at_unix = %+v, want newest source time %d", byID["fi1"], listAt.Unix())
	}
}
