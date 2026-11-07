package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The comparison is plain Go: these tests run on every OS (the suite itself needs Linux, root and the "bench" tag).

func report(cases map[string]Metrics) Report { return Report{Schema: SchemaVersion, Cases: cases} }

var baselineCase = Metrics{Tput1Mbit: 400, Tput8Mbit: 800, CPUSecPerGB: 2.0, P50MS: 20, P99MS: 60, RSSMB: 100, HandshakesPerS: 50}

func TestCompareFailsOnADropOfMoreThanTheThreshold(t *testing.T) {
	base := report(map[string]Metrics{"hysteria2/clean": baselineCase})
	for name, tc := range map[string]struct {
		edit func(*Metrics)
		fail string // the metric that must be named; "" = no regression
	}{
		"same":                    {func(*Metrics) {}, ""},
		"14% slower is fine":      {func(m *Metrics) { m.Tput1Mbit = 344 }, ""},
		"16% slower fails":        {func(m *Metrics) { m.Tput1Mbit = 336 }, "tput_1_mbit_s"},
		"8 streams drop":          {func(m *Metrics) { m.Tput8Mbit = 600 }, "tput_8_mbit_s"},
		"more CPU per GB":         {func(m *Metrics) { m.CPUSecPerGB = 2.5 }, "cpu_s_per_gb"},
		"slower p99":              {func(m *Metrics) { m.P99MS = 80 }, "p99_ms"},
		"slower p50":              {func(m *Metrics) { m.P50MS = 30 }, "p50_ms"},
		"more memory":             {func(m *Metrics) { m.RSSMB = 130 }, "rss_mb"},
		"fewer handshakes":        {func(m *Metrics) { m.HandshakesPerS = 30 }, "handshakes_per_s"},
		"better is never a fail":  {func(m *Metrics) { m.Tput1Mbit, m.P99MS, m.CPUSecPerGB = 900, 5, 0.5 }, ""},
		"not measured is skipped": {func(m *Metrics) { *m = Metrics{Tput1Mbit: 400} }, ""},
	} {
		cur := baselineCase
		tc.edit(&cur)
		res := Compare(base, report(map[string]Metrics{"hysteria2/clean": cur}), DefaultThreshold)
		switch {
		case tc.fail == "" && res.Failed():
			t.Errorf("%s: failed: %s", name, res.Summary())
		case tc.fail != "" && (len(res.Regressions) != 1 || res.Regressions[0].Metric != tc.fail):
			t.Errorf("%s: regressions = %+v, want only %s", name, res.Regressions, tc.fail)
		}
	}
}

// A big change of a tiny figure is noise: a latency of 0.3 ms that reads 0.5 ms is +66% and means nothing.
func TestCompareIgnoresChangesBelowTheMinimumAbsoluteDelta(t *testing.T) {
	base := report(map[string]Metrics{"awg/clean": {P50MS: 0.3, RSSMB: 20, HandshakesPerS: 3, CPUSecPerGB: 0.1}})
	got := report(map[string]Metrics{"awg/clean": {P50MS: 0.5, RSSMB: 26, HandshakesPerS: 2, CPUSecPerGB: 0.2}})
	if res := Compare(base, got, DefaultThreshold); res.Failed() {
		t.Errorf("noise failed the run: %s", res.Summary())
	}
	got = report(map[string]Metrics{"awg/clean": {P50MS: 9, RSSMB: 80, HandshakesPerS: 1, CPUSecPerGB: 3}})
	if res := Compare(base, got, DefaultThreshold); len(res.Regressions) != 3 { // p50, rss and cpu; one handshake less is under the minimum
		t.Errorf("regressions = %+v, want p50, rss and cpu", res.Regressions)
	}
}

func TestMedianOfSeveralRuns(t *testing.T) {
	runs := []Metrics{
		{Tput1Mbit: 300, P50MS: 10, HandshakesPerS: 40},
		{Tput1Mbit: 900, P50MS: 12}, // a run that did not measure handshakes is left out of that median
		{Tput1Mbit: 320, P50MS: 11, HandshakesPerS: 50},
	}
	got := MedianOf(runs)
	if got.Tput1Mbit != 320 || got.P50MS != 11 || got.HandshakesPerS != 45 || got.RSSMB != 0 {
		t.Errorf("median = %+v", got)
	}
	if got := MedianOf(nil); got != (Metrics{}) {
		t.Errorf("median of nothing = %+v", got)
	}
	if got := MedianOf(runs[:1]); got.Tput1Mbit != 300 {
		t.Errorf("median of one = %+v", got)
	}
}

func TestCompareThresholdIsConfigurable(t *testing.T) {
	base := report(map[string]Metrics{"awg/clean": baselineCase})
	slower := baselineCase
	slower.Tput8Mbit = 650 // 18.75% worse
	if !Compare(base, report(map[string]Metrics{"awg/clean": slower}), 0.15).Failed() {
		t.Error("18.75% is not within 15%")
	}
	if Compare(base, report(map[string]Metrics{"awg/clean": slower}), 0.25).Failed() {
		t.Error("18.75% is within 25%")
	}
	if !Compare(base, report(map[string]Metrics{"awg/clean": slower}), 0).Failed() {
		t.Error("a zero threshold must fall back to the default, not turn the check off")
	}
}

func TestCompareNotesWhatIsMissingAndNeverFailsForIt(t *testing.T) {
	base := report(map[string]Metrics{"hysteria2/clean": baselineCase, "awg/clean": baselineCase})
	res := Compare(base, report(map[string]Metrics{"hysteria2/clean": baselineCase, "vless-tcp/clean": baselineCase}), DefaultThreshold)
	if res.Failed() {
		t.Fatalf("a new case or a case not run failed: %s", res.Summary())
	}
	text := res.Summary()
	if !strings.Contains(text, "vless-tcp/clean: no baseline") || !strings.Contains(text, "awg/clean: in the baseline, not run") {
		t.Errorf("notes = %q", text)
	}
	// a baseline of another schema is not compared at all
	old := base
	old.Schema = SchemaVersion + 1
	if res := Compare(old, report(map[string]Metrics{"hysteria2/clean": {Tput1Mbit: 1}}), DefaultThreshold); res.Failed() || len(res.Notes) != 1 {
		t.Errorf("a baseline of another schema: %+v", res)
	}
}

func TestParseThreshold(t *testing.T) {
	for in, want := range map[string]float64{"": DefaultThreshold, "15": 0.15, "15%": 0.15, " 20 ": 0.20, "0.25": 0.25, "100": 1.0, "5%": 0.05} {
		got, err := ParseThreshold(in)
		if err != nil || got != want {
			t.Errorf("ParseThreshold(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"abc", "0", "-5", "NaN", "Inf", "1e999"} {
		if _, err := ParseThreshold(in); err == nil {
			t.Errorf("ParseThreshold(%q) accepted", in)
		}
	}
	t.Setenv(ThresholdEnv, "30")
	if got, err := ThresholdFromEnv(); err != nil || got != 0.30 {
		t.Errorf("ThresholdFromEnv = %v, %v", got, err)
	}
}

func TestReportRoundTripsAndTheCommittedBaselineIsReadable(t *testing.T) {
	r := Report{Schema: SchemaVersion, When: "2026-11-07T00:00:00Z", Host: "test", Cases: map[string]Metrics{"awg/mtu1280": baselineCase}}
	b, err := r.Marshal()
	if err != nil || !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("Marshal = %q, %v", b, err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadReport(path)
	if err != nil || got.Cases["awg/mtu1280"] != baselineCase || got.Schema != SchemaVersion {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if _, err := LoadReport(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing file loaded")
	}
	// the baseline in the repository parses, has the current schema, and names only known engines and variants
	base, err := LoadReport("baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if base.Schema != SchemaVersion {
		t.Errorf("baseline schema %d, want %d", base.Schema, SchemaVersion)
	}
	for name := range base.Cases {
		engine, variant, ok := strings.Cut(name, "/")
		if !ok || !isKnown(engines, engine) || !isKnown(variants, variant) {
			t.Errorf("baseline case %q: unknown engine or variant", name)
		}
	}
	var raw map[string]json.RawMessage
	b, _ = os.ReadFile("baseline.json")
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
}

func isKnown(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
