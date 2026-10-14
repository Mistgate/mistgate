package countryreach

import (
	"slices"
	"testing"
)

// TestCombine walks every Globalping verdict (and none) against a list hit, a list miss and no list.
func TestCombine(t *testing.T) {
	hit := &Listing{ListedBy: []string{"antifilter_ipsum"}, CDNProvider: "ExampleHost", CDNRegion: "eu"}
	miss := &Listing{CDNProvider: "ExampleHost"} // checked, not listed, but in a hosting range
	type want struct {
		verdict, confidence string
		listedBy            bool
	}
	lists := []struct {
		name string
		l    *Listing
	}{{"hit", hit}, {"miss", miss}, {"none", nil}}
	cases := []struct {
		name string
		live *Live
		want [3]want // against hit, miss, none
	}{
		{"no round", nil, [3]want{{VerdictListed, ConfidenceListed, true}, {VerdictUnknown, "", false}, {VerdictUnknown, "", false}}},
		{"unknown", &Live{Verdict: VerdictUnknown, Eyeball: true}, [3]want{{VerdictListed, ConfidenceListed, true}, {VerdictUnknown, "", false}, {VerdictUnknown, "", false}}},
		{"ok eyeball", &Live{Verdict: VerdictOK, Eyeball: true}, [3]want{{VerdictOK, ConfidenceEyeball, true}, {VerdictOK, ConfidenceEyeball, false}, {VerdictOK, ConfidenceEyeball, false}}},
		{"ok dc", &Live{Verdict: VerdictOK}, [3]want{{VerdictOK, ConfidenceDCOnly, true}, {VerdictOK, ConfidenceDCOnly, false}, {VerdictOK, ConfidenceDCOnly, false}}},
		{"tcp_blocked eyeball", &Live{Verdict: VerdictTCPBlocked, Eyeball: true}, [3]want{{VerdictTCPBlocked, ConfidenceEyeball, true}, {VerdictTCPBlocked, ConfidenceEyeball, false}, {VerdictTCPBlocked, ConfidenceEyeball, false}}},
		{"tcp_blocked dc", &Live{Verdict: VerdictTCPBlocked}, [3]want{{VerdictTCPBlocked, ConfidenceDCOnly, true}, {VerdictTCPBlocked, ConfidenceDCOnly, false}, {VerdictTCPBlocked, ConfidenceDCOnly, false}}},
		{"blocked eyeball", &Live{Verdict: VerdictBlocked, Eyeball: true}, [3]want{{VerdictBlocked, ConfidenceEyeball, true}, {VerdictBlocked, ConfidenceEyeball, false}, {VerdictBlocked, ConfidenceEyeball, false}}},
		{"blocked dc", &Live{Verdict: VerdictBlocked}, [3]want{{VerdictBlocked, ConfidenceDCOnly, true}, {VerdictBlocked, ConfidenceDCOnly, false}, {VerdictBlocked, ConfidenceDCOnly, false}}},
	}
	for _, c := range cases {
		for i, l := range lists {
			got := Combine(c.live, l.l)
			w := c.want[i]
			if got.Verdict != w.verdict || got.Confidence != w.confidence || (len(got.ListedBy) > 0) != w.listedBy {
				t.Errorf("%s x list %s: got %+v, want %+v", c.name, l.name, got, w)
			}
			// the CDN range is context: it comes along with any verdict as long as the lists were checked
			if l.l != nil && (got.CDNProvider != l.l.CDNProvider || got.CDNRegion != l.l.CDNRegion) {
				t.Errorf("%s x list %s: CDN context lost: %+v", c.name, l.name, got)
			}
			if l.l == nil && (got.CDNProvider != "" || got.ListedBy != nil) {
				t.Errorf("%s x no list: context from nowhere: %+v", c.name, got)
			}
		}
	}
	if got := Combine(&Live{Verdict: VerdictOK}, hit); !slices.Equal(got.ListedBy, []string{"antifilter_ipsum"}) {
		t.Errorf("a live ok keeps the listing as detail: %+v", got)
	}
}
