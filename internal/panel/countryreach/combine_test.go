package countryreach

import (
	"slices"
	"testing"
)

// TestCombine walks every Globalping state against every state of the TSPU probe, and each of those pairs against a list hit, a
// list miss and no list.
func TestCombine(t *testing.T) {
	hit := &Listing{ListedBy: []string{"antifilter_ipsum"}, CDNProvider: "ExampleHost", CDNRegion: "eu"}
	miss := &Listing{CDNProvider: "ExampleHost"} // checked, not listed, but in a hosting range
	lists := []struct {
		name string
		l    *Listing
	}{{"hit", hit}, {"miss", miss}, {"none", nil}}

	gp := map[string]*Live{
		"gp-none": nil, "gp-unknown": {Verdict: VerdictUnknown, Eyeball: true},
		"gp-ok-eye": {Verdict: VerdictOK, Eyeball: true}, "gp-ok-dc": {Verdict: VerdictOK},
		"gp-tcp-eye": {Verdict: VerdictTCPBlocked, Eyeball: true}, "gp-tcp-dc": {Verdict: VerdictTCPBlocked},
		"gp-blocked-eye": {Verdict: VerdictBlocked, Eyeball: true}, "gp-blocked-dc": {Verdict: VerdictBlocked},
	}
	tspu := map[string]*TSPU{
		"tspu-none": nil, "tspu-unknown": {Verdict: VerdictUnknown}, "tspu-ok": {Verdict: VerdictOK}, "tspu-tcp": {Verdict: VerdictTCPBlocked},
	}
	type want struct {
		verdict, confidence string
		sources             []string
	}
	g, ts := []string{SourceGlobalping}, []string{SourceTSPU}
	both := []string{SourceGlobalping, SourceTSPU}
	// the answer for the live sources alone ("lists" is filled in below: it depends on the list)
	none := want{}
	cases := []struct {
		gp, tspu string
		want
	}{
		// no Globalping verdict (none, or an unknown round): the probe node alone, or nothing
		{"gp-none", "tspu-none", none}, {"gp-none", "tspu-unknown", none}, {"gp-none", "tspu-ok", want{VerdictOK, ConfidenceDCOnly, ts}}, {"gp-none", "tspu-tcp", want{VerdictTCPBlocked, ConfidenceDCOnly, ts}},
		{"gp-unknown", "tspu-none", none}, {"gp-unknown", "tspu-unknown", none}, {"gp-unknown", "tspu-ok", want{VerdictOK, ConfidenceDCOnly, ts}}, {"gp-unknown", "tspu-tcp", want{VerdictTCPBlocked, ConfidenceDCOnly, ts}},
		// Globalping alone (the probe node has nothing or is unknown): its own confidence
		{"gp-ok-eye", "tspu-none", want{VerdictOK, ConfidenceEyeball, g}}, {"gp-ok-eye", "tspu-unknown", want{VerdictOK, ConfidenceEyeball, g}},
		{"gp-ok-dc", "tspu-none", want{VerdictOK, ConfidenceDCOnly, g}}, {"gp-ok-dc", "tspu-unknown", want{VerdictOK, ConfidenceDCOnly, g}},
		{"gp-tcp-eye", "tspu-none", want{VerdictTCPBlocked, ConfidenceEyeball, g}}, {"gp-tcp-eye", "tspu-unknown", want{VerdictTCPBlocked, ConfidenceEyeball, g}},
		{"gp-tcp-dc", "tspu-none", want{VerdictTCPBlocked, ConfidenceDCOnly, g}}, {"gp-tcp-dc", "tspu-unknown", want{VerdictTCPBlocked, ConfidenceDCOnly, g}},
		{"gp-blocked-eye", "tspu-none", want{VerdictBlocked, ConfidenceEyeball, g}}, {"gp-blocked-eye", "tspu-unknown", want{VerdictBlocked, ConfidenceEyeball, g}},
		{"gp-blocked-dc", "tspu-none", want{VerdictBlocked, ConfidenceDCOnly, g}}, {"gp-blocked-dc", "tspu-unknown", want{VerdictBlocked, ConfidenceDCOnly, g}},
		// both live and they agree: the eyeball view outranks the data-centre one
		{"gp-ok-eye", "tspu-ok", want{VerdictOK, ConfidenceEyeball, both}}, {"gp-ok-dc", "tspu-ok", want{VerdictOK, ConfidenceDCOnly, both}},
		{"gp-tcp-eye", "tspu-tcp", want{VerdictTCPBlocked, ConfidenceEyeball, both}}, {"gp-tcp-dc", "tspu-tcp", want{VerdictTCPBlocked, ConfidenceDCOnly, both}},
		// both live and they differ: the worse verdict, flagged as a conflict (whatever the confidence of either)
		{"gp-ok-eye", "tspu-tcp", want{VerdictTCPBlocked, ConfidenceConflict, both}}, {"gp-ok-dc", "tspu-tcp", want{VerdictTCPBlocked, ConfidenceConflict, both}},
		{"gp-tcp-eye", "tspu-ok", want{VerdictTCPBlocked, ConfidenceConflict, both}}, {"gp-tcp-dc", "tspu-ok", want{VerdictTCPBlocked, ConfidenceConflict, both}},
		{"gp-blocked-eye", "tspu-ok", want{VerdictBlocked, ConfidenceConflict, both}}, {"gp-blocked-dc", "tspu-ok", want{VerdictBlocked, ConfidenceConflict, both}},
		{"gp-blocked-eye", "tspu-tcp", want{VerdictBlocked, ConfidenceConflict, both}}, {"gp-blocked-dc", "tspu-tcp", want{VerdictBlocked, ConfidenceConflict, both}},
	}
	if len(cases) != len(gp)*len(tspu) {
		t.Fatalf("%d cases for %d x %d combinations", len(cases), len(gp), len(tspu))
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.gp+"/"+c.tspu] {
			t.Fatalf("%s x %s twice", c.gp, c.tspu)
		}
		seen[c.gp+"/"+c.tspu] = true
		for _, l := range lists {
			name := c.gp + " x " + c.tspu + " x list " + l.name
			got := Combine(gp[c.gp], tspu[c.tspu], l.l)
			w := c.want
			if w.verdict == "" { // no live verdict: the lists decide
				w = want{VerdictUnknown, "", nil}
				if l.l == hit {
					w = want{VerdictListed, ConfidenceListed, []string{SourceLists}}
				}
			}
			if got.Verdict != w.verdict || got.Confidence != w.confidence || !slices.Equal(got.Sources, w.sources) {
				t.Errorf("%s: got %+v, want %+v", name, got, w)
			}
			// the list detail comes along with any verdict as long as the lists were checked, and from nowhere otherwise
			if l.l != nil && (got.CDNProvider != l.l.CDNProvider || got.CDNRegion != l.l.CDNRegion || !slices.Equal(got.ListedBy, l.l.ListedBy)) {
				t.Errorf("%s: list detail lost: %+v", name, got)
			}
			if l.l == nil && (got.CDNProvider != "" || got.ListedBy != nil) {
				t.Errorf("%s: detail from nowhere: %+v", name, got)
			}
		}
	}
}

// A conflict shows the worse verdict whichever source holds it.
func TestWorse(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{VerdictOK, VerdictPartial, VerdictPartial}, {VerdictPartial, VerdictOK, VerdictPartial},
		{VerdictOK, VerdictTCPBlocked, VerdictTCPBlocked}, {VerdictTCPBlocked, VerdictOK, VerdictTCPBlocked},
		{VerdictTCPBlocked, VerdictBlocked, VerdictBlocked}, {VerdictBlocked, VerdictOK, VerdictBlocked}, {VerdictOK, VerdictOK, VerdictOK},
	} {
		if got := worse(c.a, c.b); got != c.want {
			t.Errorf("worse(%s, %s) = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestCombinePartial(t *testing.T) {
	for _, tc := range []struct {
		name       string
		live       *Live
		tspu       *TSPU
		verdict    string
		confidence string
	}{
		{"partial and ok", &Live{Verdict: VerdictPartial, Eyeball: true}, &TSPU{Verdict: VerdictOK}, VerdictPartial, ConfidenceEyeball},
		{"partial and tcp blocked", &Live{Verdict: VerdictPartial, Eyeball: true}, &TSPU{Verdict: VerdictTCPBlocked}, VerdictTCPBlocked, ConfidenceConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Combine(tc.live, tc.tspu, nil)
			if got.Verdict != tc.verdict || got.Confidence != tc.confidence {
				t.Fatalf("Combine() = %+v, want verdict %q confidence %q", got, tc.verdict, tc.confidence)
			}
		})
	}
}

// What the probe node stored becomes a verdict of the shared scale: blocked is tcp_blocked, partial is tcp_blocked unless some
// port is open, anything else says nothing.
func TestTSPUVerdict(t *testing.T) {
	ports := func(v ...string) []TSPUPort {
		out := make([]TSPUPort, len(v))
		for i, x := range v {
			out[i] = TSPUPort{Port: uint16(443 + i), Verdict: x}
		}
		return out
	}
	for _, c := range []struct {
		stored string
		ports  []TSPUPort
		want   string
	}{
		{"open", ports("open"), VerdictOK},
		{"open", ports("refused"), VerdictOK},
		{"blocked", ports("blocked", "blocked"), VerdictTCPBlocked},
		{"partial", ports("blocked", "open"), VerdictOK},
		{"partial", ports("open", "blocked", "refused"), VerdictOK},
		{"partial", ports("blocked", "refused"), VerdictTCPBlocked},
		{"partial", ports("blocked", "timeout"), VerdictTCPBlocked},
		{"partial", nil, VerdictTCPBlocked},
		{"unknown", ports("timeout"), VerdictUnknown},
		{"", nil, VerdictUnknown},
		{"anything else", nil, VerdictUnknown},
	} {
		if got := TSPUVerdict(c.stored, c.ports); got != c.want {
			t.Errorf("TSPUVerdict(%q, %v) = %s, want %s", c.stored, c.ports, got, c.want)
		}
	}
}

// The stored verdict of a node from its ports: timeouts say nothing, a refused port is not blocked.
func TestFoldTSPU(t *testing.T) {
	p := func(v ...string) []TSPUPort {
		out := make([]TSPUPort, len(v))
		for i, x := range v {
			out[i] = TSPUPort{Port: uint16(443 + i), Verdict: x}
		}
		return out
	}
	for _, c := range []struct {
		ports []TSPUPort
		want  string
	}{
		{p("blocked"), "blocked"}, {p("blocked", "blocked"), "blocked"}, {p("blocked", "timeout"), "blocked"},
		{p("open"), "open"}, {p("open", "refused"), "open"}, {p("refused"), "open"}, {p("open", "timeout"), "open"},
		{p("blocked", "open"), "partial"}, {p("blocked", "refused"), "partial"},
		{p("timeout"), "unknown"}, {p("timeout", "timeout"), "unknown"}, {nil, "unknown"},
	} {
		if got := FoldTSPU(c.ports); got != c.want {
			t.Errorf("FoldTSPU(%v) = %s, want %s", c.ports, got, c.want)
		}
	}
	got := ParseTSPUDetail(`[{"port":443,"verdict":"blocked"},{"port":8443,"verdict":"open"}]`)
	if len(got) != 2 || got[0] != (TSPUPort{443, "blocked"}) || got[1].Port != 8443 {
		t.Errorf("ParseTSPUDetail = %v", got)
	}
	if ParseTSPUDetail("not json") != nil || ParseTSPUDetail("") != nil {
		t.Error("an unreadable detail must be no ports")
	}
}
