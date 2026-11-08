// Package bench is the engine benchmark suite: Hysteria2, VLESS (REALITY over TCP and over XHTTP) and AmneziaWG, each between
// two network namespaces joined by a veth pair (with netem variants), measured for throughput, CPU per gigabyte, latency under
// load, memory and handshakes per second. The suite itself is in the *_test.go files behind the build tag "bench" and runs only
// on Linux, as root, with MISTGATE_BENCH=1 (see docs/en/reference/benchmarks.md and scripts/bench.sh). This file is the part
// that needs none of that: the report format and the comparison against the committed baseline, which is plain Go and is tested
// on every OS.
package bench

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion is the version of the report format. A baseline of another version is not compared.
const SchemaVersion = 1

// DefaultThreshold is how much worse than the baseline a metric may get before the run fails: 15 percent.
const DefaultThreshold = 0.15

// ThresholdEnv names the environment variable that overrides the threshold: "20" or "20%" or "0.2" all mean 20 percent.
const ThresholdEnv = "MISTGATE_BENCH_THRESHOLD"

// The engines and the network variants of the suite; a case is "<engine>/<variant>".
var (
	engines  = []string{"hysteria2", "vless-tcp", "vless-xhttp", "awg"}
	variants = []string{"clean", "rtt50-loss1", "mtu1280"}
)

// Metrics is what one case measured. A zero value is "not measured" (a case that could not measure it) and is never compared.
type Metrics struct {
	// Throughput of 1 and of 8 parallel streams, Mbit/s, client to internet direction as seen by the client (a download).
	Tput1Mbit float64 `json:"tput_1_mbit_s,omitempty"`
	Tput8Mbit float64 `json:"tput_8_mbit_s,omitempty"`
	// CPUSecPerGB is the CPU time the engine process used (user plus system) per gigabyte it moved in both throughput phases.
	CPUSecPerGB float64 `json:"cpu_s_per_gb,omitempty"`
	// P50MS and P99MS are the latency of a small request while 8 streams are loading the link.
	P50MS float64 `json:"p50_ms,omitempty"`
	P99MS float64 `json:"p99_ms,omitempty"`
	// RSSMB is the engine process's peak resident memory after the run.
	RSSMB float64 `json:"rss_mb,omitempty"`
	// HandshakesPerS is how many new sessions per second a client can complete. For AmneziaWG it is a re-handshake of one peer
	// with one ping, paced at WireGuard's own flood limit (one initiation per 20 ms), so it tops out near 30 and mostly shows
	// that every one of them succeeds.
	HandshakesPerS float64 `json:"handshakes_per_s,omitempty"`
}

// Report is the JSON the suite writes.
type Report struct {
	Schema int    `json:"schema"`
	When   string `json:"when,omitempty"`
	Host   string `json:"host,omitempty"` // kernel, CPU count and Go version: a baseline only means something on the same kind of machine
	// Cases is keyed "<engine>/<variant>": hysteria2/clean, vless-tcp/rtt50-loss1, awg/mtu1280 and so on.
	Cases map[string]Metrics `json:"cases"`
}

// metric describes one figure: which way is better, and the smallest change that counts at all (a latency of 0.4 ms against
// 0.3 ms is +33 percent and means nothing).
type metric struct {
	name         string
	higherBetter bool
	minDelta     float64
	get          func(Metrics) float64
}

var metrics = []metric{
	{"tput_1_mbit_s", true, 5, func(m Metrics) float64 { return m.Tput1Mbit }},
	{"tput_8_mbit_s", true, 5, func(m Metrics) float64 { return m.Tput8Mbit }},
	{"cpu_s_per_gb", false, 0.3, func(m Metrics) float64 { return m.CPUSecPerGB }},
	{"p50_ms", false, 2, func(m Metrics) float64 { return m.P50MS }},
	{"p99_ms", false, 5, func(m Metrics) float64 { return m.P99MS }},
	{"rss_mb", false, 8, func(m Metrics) float64 { return m.RSSMB }},
	{"handshakes_per_s", true, 2, func(m Metrics) float64 { return m.HandshakesPerS }},
}

// selectBenchmarkNames reads an explicit engine or variant list. Unknown names must stop the run instead of producing an empty
// report that looks successful.
func selectBenchmarkNames(all []string, requested string) ([]string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return slices.Clone(all), nil
	}
	var selected []string
	for _, name := range strings.Split(requested, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(all, name) {
			return nil, fmt.Errorf("unknown benchmark name %q; choose from %s", name, strings.Join(all, ", "))
		}
		selected = append(selected, name)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no benchmark names were requested")
	}
	return selected, nil
}

// handshakeRate rejects a run where every fresh session attempt failed, so a zero metric cannot pass comparison as unmeasured.
func handshakeRate(successes int64, elapsed time.Duration) (float64, error) {
	if successes <= 0 {
		return 0, fmt.Errorf("no successful handshakes")
	}
	if elapsed <= 0 {
		return 0, fmt.Errorf("handshake duration is not positive")
	}
	return float64(successes) / elapsed.Seconds(), nil
}

// MedianOf is the metric-by-metric median of several runs of one case (a metric a run did not measure is left out of its own
// median; an even count takes the mean of the middle two). One run of a throughput test on a shared machine can be off by a
// third, so a baseline is best made from three or five.
func MedianOf(runs []Metrics) Metrics {
	var out Metrics
	set := []struct {
		get func(Metrics) float64
		put func(*Metrics, float64)
	}{
		{func(m Metrics) float64 { return m.Tput1Mbit }, func(m *Metrics, v float64) { m.Tput1Mbit = v }},
		{func(m Metrics) float64 { return m.Tput8Mbit }, func(m *Metrics, v float64) { m.Tput8Mbit = v }},
		{func(m Metrics) float64 { return m.CPUSecPerGB }, func(m *Metrics, v float64) { m.CPUSecPerGB = v }},
		{func(m Metrics) float64 { return m.P50MS }, func(m *Metrics, v float64) { m.P50MS = v }},
		{func(m Metrics) float64 { return m.P99MS }, func(m *Metrics, v float64) { m.P99MS = v }},
		{func(m Metrics) float64 { return m.RSSMB }, func(m *Metrics, v float64) { m.RSSMB = v }},
		{func(m Metrics) float64 { return m.HandshakesPerS }, func(m *Metrics, v float64) { m.HandshakesPerS = v }},
	}
	for _, s := range set {
		var xs []float64
		for _, r := range runs {
			if v := s.get(r); v > 0 {
				xs = append(xs, v)
			}
		}
		if len(xs) == 0 {
			continue
		}
		sort.Float64s(xs)
		mid := len(xs) / 2
		v := xs[mid]
		if len(xs)%2 == 0 {
			v = (xs[mid-1] + xs[mid]) / 2
		}
		s.put(&out, math.Round(v*100)/100)
	}
	return out
}

// Finding is one metric of one case that got worse than the threshold allows.
type Finding struct {
	Case, Metric   string
	Baseline, Got  float64
	ChangePct      float64 // positive = worse
	HigherIsBetter bool
}

func (f Finding) String() string {
	return fmt.Sprintf("%s %s: %.2f against a baseline of %.2f (%.1f%% worse)", f.Case, f.Metric, f.Got, f.Baseline, f.ChangePct)
}

// Result is the outcome of a comparison.
type Result struct {
	Regressions []Finding
	// Notes are things worth saying that do not fail the run: a case without a baseline, a baseline case that was not run.
	Notes []string
}

// Compare checks got against baseline. A metric fails when it is worse than the baseline by more than threshold (a fraction:
// 0.15 is 15 percent) and by more than its minimum absolute change. Better is never a failure. A case or a metric missing on one
// side is a note, not a failure, so a new engine does not need a baseline first.
func Compare(baseline, got Report, threshold float64) Result {
	var res Result
	if threshold <= 0 || math.IsNaN(threshold) {
		threshold = DefaultThreshold
	}
	if baseline.Schema != got.Schema {
		res.Notes = append(res.Notes, fmt.Sprintf("the baseline is of schema %d and the report of schema %d: nothing compared", baseline.Schema, got.Schema))
		return res
	}
	for _, name := range sortedKeys(got.Cases) {
		base, ok := baseline.Cases[name]
		if !ok {
			res.Notes = append(res.Notes, name+": no baseline")
			continue
		}
		cur := got.Cases[name]
		for _, m := range metrics {
			b, c := m.get(base), m.get(cur)
			if b <= 0 || c <= 0 || math.IsNaN(b) || math.IsNaN(c) {
				continue // not measured on one side
			}
			worse := (b - c) / b // higher is better: a drop is positive
			delta := b - c
			if !m.higherBetter {
				worse, delta = (c-b)/b, c-b
			}
			if worse > threshold && delta > m.minDelta {
				res.Regressions = append(res.Regressions, Finding{Case: name, Metric: m.name, Baseline: b, Got: c, ChangePct: worse * 100, HigherIsBetter: m.higherBetter})
			}
		}
	}
	for _, name := range sortedKeys(baseline.Cases) {
		if _, ok := got.Cases[name]; !ok {
			res.Notes = append(res.Notes, name+": in the baseline, not run")
		}
	}
	return res
}

// Failed says whether the comparison found a regression.
func (r Result) Failed() bool { return len(r.Regressions) > 0 }

// Summary is the comparison as text, one line per finding and note.
func (r Result) Summary() string {
	var b strings.Builder
	for _, f := range r.Regressions {
		b.WriteString("REGRESSION " + f.String() + "\n")
	}
	for _, n := range r.Notes {
		b.WriteString("note: " + n + "\n")
	}
	if len(r.Regressions) == 0 {
		b.WriteString("no regression against the baseline\n")
	}
	return b.String()
}

// ParseThreshold reads a threshold given as "15", "15%" or "0.15" (everything above 1 is a percentage). An empty string is the
// default. A value that is not a positive number is an error: a typo must not silently turn the check off.
func ParseThreshold(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return DefaultThreshold, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, fmt.Errorf("%s=%q is not a positive number (15, 15%% or 0.15)", ThresholdEnv, s)
	}
	if v > 1 {
		v /= 100
	}
	return v, nil
}

// ThresholdFromEnv is ParseThreshold of the environment variable.
func ThresholdFromEnv() (float64, error) { return ParseThreshold(os.Getenv(ThresholdEnv)) }

// LoadReport reads a report (or the baseline) from a file.
func LoadReport(path string) (Report, error) {
	var r Report
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Marshal is the report as indented JSON with a final newline.
func (r Report) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
