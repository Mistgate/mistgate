package countryreach

import (
	"slices"
	"testing"
	"time"
)

// TestCombine covers the source-list rules, including the path-carrier exception and source ordering.
func TestCombine(t *testing.T) {
	at := time.Unix(1700000000, 0)
	hit := &Listing{ListedBy: []string{"antifilter_ipsum"}, CDNProvider: "ExampleHost", CDNRegion: "eu"}
	cases := []struct {
		name       string
		live       []Live
		list       *Listing
		verdict    string
		confidence string
		sources    []string
	}{
		{"GP, cheburcheck and bsbord", []Live{
			{Source: SourceBsbord, Verdict: VerdictPartial, Eyeball: true, At: at},
			{Source: SourceCheburcheck, Verdict: VerdictOK, Eyeball: true, Path: true, At: at},
			{Source: SourceGlobalping, Verdict: VerdictOK, At: at},
		}, nil, VerdictPartial, ConfidenceEyeball, []string{SourceGlobalping, SourceCheburcheck, SourceBsbord}},
		{"GP ok and narrow bsbord block", []Live{{Source: SourceGlobalping, Verdict: VerdictOK}, {Source: SourceBsbord, Verdict: VerdictBlocked, Eyeball: true}}, nil,
			VerdictPartial, ConfidenceEyeball, []string{SourceGlobalping, SourceBsbord}},
		{"bsbord full block alone", []Live{{Source: SourceBsbord, Verdict: VerdictBlocked, Eyeball: true}}, nil,
			VerdictBlocked, ConfidenceEyeball, []string{SourceBsbord}},
		{"GP block and path reach", []Live{{Source: SourceCheburcheck, Verdict: VerdictOK, Eyeball: true, Path: true}, {Source: SourceGlobalping, Verdict: VerdictBlocked}}, nil,
			VerdictBlocked, ConfidenceConflict, []string{SourceGlobalping, SourceCheburcheck}},
		{"GP block and bsbord reach", []Live{{Source: SourceGlobalping, Verdict: VerdictBlocked, Eyeball: true}, {Source: SourceBsbord, Verdict: VerdictOK, Eyeball: true}}, nil,
			VerdictPartial, ConfidenceEyeball, []string{SourceGlobalping, SourceBsbord}},
		{"GP tcp block and TSPU reach", []Live{{Source: SourceTSPU, Verdict: VerdictOK, Path: true}, {Source: SourceGlobalping, Verdict: VerdictTCPBlocked}}, nil,
			VerdictTCPBlocked, ConfidenceConflict, []string{SourceGlobalping, SourceTSPU}},
		{"GP reach and TSPU tcp block", []Live{{Source: SourceTSPU, Verdict: VerdictTCPBlocked, Path: true}, {Source: SourceGlobalping, Verdict: VerdictOK}}, nil,
			VerdictPartial, ConfidenceDCOnly, []string{SourceGlobalping, SourceTSPU}},
		{"GP reach and cheburcheck tcp block", []Live{{Source: SourceCheburcheck, Verdict: VerdictTCPBlocked, Eyeball: true, Path: true}, {Source: SourceGlobalping, Verdict: VerdictOK}}, nil,
			VerdictPartial, ConfidenceEyeball, []string{SourceGlobalping, SourceCheburcheck}},
		{"two path-only tcp blocks", []Live{{Source: SourceCheburcheck, Verdict: VerdictTCPBlocked, Eyeball: true, Path: true}, {Source: SourceTSPU, Verdict: VerdictTCPBlocked, Path: true}}, nil,
			VerdictTCPBlocked, ConfidenceEyeball, []string{SourceTSPU, SourceCheburcheck}},
		{"all sources ok", []Live{{Source: SourceBsbord, Verdict: VerdictOK, Eyeball: true}, {Source: SourceGlobalping, Verdict: VerdictOK}}, nil,
			VerdictOK, ConfidenceEyeball, []string{SourceGlobalping, SourceBsbord}},
		{"unknown rows do not count", []Live{{Source: SourceBsbord, Verdict: VerdictUnknown}}, nil,
			VerdictUnknown, "", nil},
		{"unknown rows with list hit", []Live{{Source: SourceBsbord, Verdict: VerdictUnknown}}, hit,
			VerdictListed, ConfidenceListed, []string{SourceLists}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Combine(tc.live, tc.list)
			if got.Verdict != tc.verdict || got.Confidence != tc.confidence || !slices.Equal(got.Sources, tc.sources) {
				t.Fatalf("Combine() = %+v, want verdict %q confidence %q sources %v", got, tc.verdict, tc.confidence, tc.sources)
			}
			if len(tc.live) == 3 && (got.SourceVerdicts[0].Source != SourceGlobalping || got.SourceVerdicts[1].Source != SourceCheburcheck || got.SourceVerdicts[2].Source != SourceBsbord) {
				t.Errorf("source verdict order = %+v", got.SourceVerdicts)
			}
			if tc.list != nil && (got.CDNProvider != tc.list.CDNProvider || got.CDNRegion != tc.list.CDNRegion || !slices.Equal(got.ListedBy, tc.list.ListedBy)) {
				t.Errorf("list detail lost: %+v", got)
			}
		})
	}
}

// TestCombineGlobalpingTSPULists checks the complete Globalping x TSPU x lists table; the three changed pairings are explicit below.
func TestCombineGlobalpingTSPULists(t *testing.T) {
	type oldLive struct {
		verdict string
		eyeball bool
	}
	type oldTSPU struct{ verdict string }
	type listCase struct {
		name string
		row  *Listing
	}
	hit := &Listing{ListedBy: []string{"antifilter_ipsum"}, CDNProvider: "ExampleHost", CDNRegion: "eu"}
	miss := &Listing{CDNProvider: "ExampleHost", CDNRegion: "eu"}
	lists := []listCase{{"hit", hit}, {"miss", miss}, {"none", nil}}
	gps := []*oldLive{nil}
	for _, verdict := range []string{VerdictUnknown, VerdictOK, VerdictPartial, VerdictTCPBlocked, VerdictBlocked} {
		gps = append(gps, &oldLive{verdict, true}, &oldLive{verdict, false})
	}
	tspus := []*oldTSPU{nil, {VerdictUnknown}, {VerdictOK}, {VerdictTCPBlocked}}

	legacy := func(g *oldLive, ts *oldTSPU, list *Listing) Combined {
		var c Combined
		if list != nil {
			c.ListedBy, c.CDNProvider, c.CDNRegion = list.ListedBy, list.CDNProvider, list.CDNRegion
		}
		gpLive, tspuLive := g != nil && isLive(g.verdict), ts != nil && isLive(ts.verdict)
		switch {
		case gpLive && tspuLive:
			c.Verdict, c.Sources = g.verdict, []string{SourceGlobalping, SourceTSPU}
			switch {
			case g.verdict != ts.verdict:
				if (g.verdict == VerdictPartial && ts.verdict == VerdictOK) || (g.verdict == VerdictOK && ts.verdict == VerdictPartial) {
					c.Verdict = VerdictPartial
					if g.eyeball {
						c.Confidence = ConfidenceEyeball
					} else {
						c.Confidence = ConfidenceDCOnly
					}
				} else {
					c.Verdict, c.Confidence = worse(g.verdict, ts.verdict), ConfidenceConflict
				}
			case g.eyeball:
				c.Confidence = ConfidenceEyeball
			default:
				c.Confidence = ConfidenceDCOnly
			}
		case gpLive:
			c.Verdict, c.Confidence, c.Sources = g.verdict, ConfidenceDCOnly, []string{SourceGlobalping}
			if g.eyeball {
				c.Confidence = ConfidenceEyeball
			}
		case tspuLive:
			c.Verdict, c.Confidence, c.Sources = ts.verdict, ConfidenceDCOnly, []string{SourceTSPU}
		case list != nil && len(list.ListedBy) > 0:
			c.Verdict, c.Confidence, c.Sources = VerdictListed, ConfidenceListed, []string{SourceLists}
		default:
			c.Verdict = VerdictUnknown
		}
		return c
	}

	for _, gp := range gps {
		for _, tspu := range tspus {
			for _, list := range lists {
				var live []Live
				if gp != nil {
					live = append(live, Live{Source: SourceGlobalping, Verdict: gp.verdict, Eyeball: gp.eyeball})
				}
				if tspu != nil {
					live = append(live, Live{Source: SourceTSPU, Verdict: tspu.verdict, Path: true})
				}
				want := legacy(gp, tspu, list.row)
				if gp != nil && tspu != nil && tspu.verdict == VerdictTCPBlocked {
					switch gp.verdict {
					case VerdictOK:
						want.Verdict, want.Confidence = VerdictPartial, ConfidenceDCOnly
					case VerdictPartial:
						want.Verdict, want.Confidence = VerdictPartial, ConfidenceDCOnly
						if gp.eyeball {
							want.Confidence = ConfidenceEyeball
						}
					case VerdictBlocked:
						want.Verdict, want.Confidence = VerdictBlocked, ConfidenceDCOnly
						if gp.eyeball {
							want.Confidence = ConfidenceEyeball
						}
					}
				}
				got := Combine(live, list.row)
				name := "none"
				if gp != nil {
					name = gp.verdict + "/eye=" + map[bool]string{true: "true", false: "false"}[gp.eyeball]
				}
				tsName := "none"
				if tspu != nil {
					tsName = tspu.verdict
				}
				if got.Verdict != want.Verdict || got.Confidence != want.Confidence || !slices.Equal(got.Sources, want.Sources) ||
					!slices.Equal(got.ListedBy, want.ListedBy) || got.CDNProvider != want.CDNProvider || got.CDNRegion != want.CDNRegion {
					t.Errorf("GP %s + TSPU %s + list %s: got %+v, want %+v", name, tsName, list.name, got, want)
				}
			}
		}
	}
}

func TestPartialConfidenceUsesDarkSourcesOnly(t *testing.T) {
	got := Combine([]Live{
		{Source: SourceGlobalping, Verdict: VerdictOK, Eyeball: true},
		{Source: SourceTSPU, Verdict: VerdictTCPBlocked, Path: true},
	}, nil)
	if got.Verdict != VerdictPartial || got.Confidence != ConfidenceDCOnly {
		t.Fatalf("Combine() = %s/%s, want partial/dc_only", got.Verdict, got.Confidence)
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
