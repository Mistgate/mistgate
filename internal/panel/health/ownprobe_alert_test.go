//go:build !js

package health

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestOwnProbeSignalsCanRaisePartialButNotFullAndDeduplicateRows(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	key := "probe:prb_de1"
	makeSignal := func(node string, at time.Time, state string) countryAlertSignal {
		v := countryreach.Vantage{Key: key, Network: "ExampleNet / de1 / probe probe-de1", Region: "de1", ASN: "64512",
			Kind: "home", State: state, At: at.Unix(), Why: "tcp_timeout"}
		detail, err := json.Marshal(countryreach.ExtDetail{Vantages: []countryreach.Vantage{v}})
		if err != nil {
			t.Fatal(err)
		}
		verdict := countryreach.VerdictOK
		if state == countryreach.VantageStateDark {
			verdict = countryreach.VerdictTCPBlocked
		}
		return countryExternalSignalFor(store.ExtCheck{NodeID: node, Source: countryreach.SourceProbes, At: at,
			Verdict: verdict, DetailJSON: string(detail)}, 3*time.Hour, "test", now)
	}
	eventFor := func(signal countryAlertSignal) countryEvent {
		combined := countryreach.Combined{Verdict: countryreach.VerdictPartial, Confidence: countryreach.ConfidenceEyeball,
			Sources: []string{countryreach.SourceProbes}}
		return countryEvent{signal: signal, active: []countryAlertSignal{signal}, combined: combined}
	}

	var events []countryEvent
	var signals []countryAlertSignal
	for _, at := range []time.Time{now, now.Add(-time.Minute)} {
		dark := makeSignal("nod_de1", at, countryreach.VantageStateDark)
		reach := makeSignal("nod_nl1", at, countryreach.VantageStateReach)
		events = append(events, eventFor(dark))
		signals = append(signals, dark, reach)
		if dark.verdict != countryreach.VerdictPartial || countryBad(dark.verdict) {
			t.Fatalf("one probe dark signal = %q; want partial only", dark.verdict)
		}
	}
	condition, ok := countryPartialCond(store.NodeRow{ID: "nod_de1", Name: "de1", State: "active"}, events, signals)
	if !ok || condition.key.subject != "partial" || condition.params["sources"] != countryreach.SourceProbes {
		t.Fatalf("own probe partial condition = %+v, %v", condition, ok)
	}
	for _, event := range events {
		if event.bad() {
			t.Fatalf("own probe event qualified as full-block event: %+v", event)
		}
	}

	duplicateAt := now.Add(-2 * time.Minute)
	dark := makeSignal("nod_de1", duplicateAt, countryreach.VantageStateDark)
	reach := makeSignal("nod_nl1", duplicateAt, countryreach.VantageStateReach)
	observed := countryObservations([]countryEvent{eventFor(dark), eventFor(reach)}, []countryAlertSignal{dark, reach})
	if count := len(observed[countryObservationKey(countryreach.SourceProbes, key, "")]); count != 1 {
		t.Fatalf("one probe measurement in two rows counted %d times: %+v", count, observed)
	}
}

// Only the own probes look from the chosen country and only they are judged afresh: the signal of every other external source
// is what it was before the probes existed, whatever country is chosen and whatever the clock says.
func TestExternalSignalOfOtherSourcesIsUnchanged(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour) // far older than any probe point may be: the other sources are not cut by age here
	v := countryreach.Vantage{Key: "k", Network: "ExampleNet", Kind: "home", State: countryreach.VantageStateDark, At: old.Unix()}
	detail, _ := json.Marshal(countryreach.ExtDetail{Reason: "r", Vantages: []countryreach.Vantage{v}})
	for _, source := range []string{countryreach.SourceCheburcheck, countryreach.SourceCheburcheckOwn, countryreach.SourceBsbord,
		countryreach.SourceBsbordTunnel, countryreach.SourceBsbordGeo} {
		row := store.ExtCheck{NodeID: "nod_de1", Source: source, At: old, Verdict: countryreach.VerdictTCPBlocked, DetailJSON: string(detail)}
		got := countryExternalSignalFor(row, time.Hour, "BY", now)
		want := countryExternalSignal(row, time.Hour)
		if got.country != countryreach.TSPUCountry || got.verdict != countryreach.VerdictTCPBlocked || got.reason != "r" || !got.at.Equal(old) ||
			len(got.vantages) != 1 || got.vantages[0].vantage.At != old.Unix() || got.eyeball != externalSourceEyeball(source) ||
			got.vantages[0].eyeball != externalSourceEyeball(source) || got.path != externalSourcePath(source) ||
			got.maxAge != time.Hour || got.interval != time.Hour/3 || got.nodeID != "nod_de1" {
			t.Errorf("%s: %+v", source, got)
		}
		if got.country != want.country || got.verdict != want.verdict || got.eyeball != want.eyeball {
			t.Errorf("%s: the country-aware form differs from the old one: %+v vs %+v", source, got, want)
		}
	}
	for source, eyeball := range map[string]bool{countryreach.SourceCheburcheck: true, countryreach.SourceCheburcheckOwn: true,
		countryreach.SourceBsbord: true, countryreach.SourceBsbordTunnel: true, countryreach.SourceBsbordGeo: true,
		countryreach.SourceGlobalping: false, countryreach.SourceTSPU: false, countryreach.SourceLists: false} {
		if externalSourceEyeball(source) != eyeball {
			t.Errorf("externalSourceEyeball(%q) = %v", source, !eyeball)
		}
	}
	// the probes: by the kind of the point, in the country the owner chose
	for kind, eyeball := range map[string]bool{"home": true, "mobile": true, "dc": false, "": false} {
		if externalSourceEyeball(countryreach.SourceProbes, kind) != eyeball {
			t.Errorf("probe kind %q eyeball = %v", kind, !eyeball)
		}
	}
	probe := countryreach.Vantage{Key: "probe:prb_de1", Kind: "home", State: countryreach.VantageStateDark, At: now.Unix()}
	probeDetail, _ := json.Marshal(countryreach.ExtDetail{Vantages: []countryreach.Vantage{probe}})
	row := store.ExtCheck{NodeID: "nod_de1", Source: countryreach.SourceProbes, At: now, Verdict: countryreach.VerdictTCPBlocked, DetailJSON: string(probeDetail)}
	if got := countryExternalSignalFor(row, 3*time.Hour, "BY", now); got.country != "BY" || got.verdict != countryreach.VerdictPartial {
		t.Errorf("a lone dark probe: country %q verdict %q, want BY and partial", got.country, got.verdict)
	}
	// a probe point older than the source's age is not a point
	if got := countryExternalSignalFor(row, time.Minute, "BY", now.Add(time.Hour)); got.verdict != countryreach.VerdictUnknown || len(got.vantages) != 0 {
		t.Errorf("an expired probe point counted: %+v", got)
	}
}

// The reason a probe point is uncertain reaches the admin, and a point without a time of its own takes the row's.
func TestCountryVantageProtoCarriesTheReasonAndTheTimeOfThePoint(t *testing.T) {
	row := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	got := countryVantageProto("probes", row, countryreach.Vantage{Key: "probe:prb_x", State: "uncertain", Why: "no_control", At: row.Add(-time.Hour).Unix()})
	if got.Why != "no_control" || got.AtUnix != row.Add(-time.Hour).Unix() {
		t.Fatalf("proto = %+v", got)
	}
	if got := countryVantageProto("bsbord", row, countryreach.Vantage{Key: "k", State: "dark"}); got.Why != "" || got.AtUnix != row.Unix() {
		t.Fatalf("other source = %+v", got)
	}
}

// The name the owner gave a probe and the port its reason comes from travel in fields of their own, so the admin does not have to
// take them out of a string; the name of the point in an alert names the probe without a word of any language.
func TestCountryVantageProtoCarriesTheProbeNameAndThePort(t *testing.T) {
	row := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	v := countryreach.Vantage{Key: "probe:prb_x", Network: "ExampleNet · Springfield", Region: "Springfield", ASN: "64496", Kind: "home", State: "dark",
		Why: "tls_reset", ProbeName: "office", Port: 443}
	got := countryVantageProto("probes", row, v)
	if got.ProbeName != "office" || got.Port != 443 || got.Kind != "home" || got.Network != "ExampleNet · Springfield" || got.Why != "tls_reset" {
		t.Fatalf("proto = %+v", got)
	}
	if other := countryVantageProto("bsbord", row, countryreach.Vantage{Key: "k", State: "dark"}); other.ProbeName != "" || other.Port != 0 {
		t.Fatalf("another source = %+v", other)
	}
	if name := countryVantageName("probes", v); name != "ExampleNet · Springfield · “office” (AS64496, Springfield, probes)" {
		t.Fatalf("alert name = %q", name)
	}
	if name := countryVantageName("cheburcheck", countryreach.Vantage{Key: "k", Network: "Tele", ASN: "8359", Region: "Moscow"}); name != "Tele (AS8359, Moscow, cheburcheck)" {
		t.Fatalf("a point of another source = %q", name)
	}
}
