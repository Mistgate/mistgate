package countryreach

import "testing"

func TestJudgeVantages(t *testing.T) {
	cases := []struct {
		name     string
		vantages []Vantage
		minValid int
		full     string
		verdict  string
		reason   string
	}{
		{"public source requires two valid probes", []Vantage{{State: VantageStateReach}}, 2, VerdictTCPBlocked, VerdictUnknown, ReasonTooFewProbes},
		{"one valid probe is enough", []Vantage{{State: VantageStateReach}}, 1, VerdictBlocked, VerdictOK, ""},
		{"two reaches are ok", []Vantage{{State: VantageStateReach}, {State: VantageStateReach}}, 2, VerdictBlocked, VerdictOK, ""},
		{"all dark uses the source full word", []Vantage{{State: VantageStateDark}, {State: VantageStateDark}}, 2, VerdictTCPBlocked, VerdictTCPBlocked, ""},
		{"bsbord full word", []Vantage{{State: VantageStateDark}}, 1, VerdictBlocked, VerdictBlocked, ""},
		{"dark and reach is partial", []Vantage{{State: VantageStateDark}, {State: VantageStateReach}}, 2, VerdictBlocked, VerdictPartial, ""},
		{"uncertain only is too few", []Vantage{{State: VantageStateUncertain}}, 1, VerdictBlocked, VerdictUnknown, ReasonTooFewProbes},
		{"unreached whitelist is never dark", []Vantage{{State: VantageStateWhitelist}}, 1, VerdictBlocked, VerdictUnknown, ReasonTooFewProbes},
		{"reached whitelist counts as reach", []Vantage{{State: VantageStateWhitelist, ICMPOK: true}, {State: VantageStateDark}}, 2, VerdictBlocked, VerdictPartial, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict, reason := JudgeVantages(tc.vantages, tc.minValid, tc.full)
			if verdict != tc.verdict || reason != tc.reason {
				t.Fatalf("JudgeVantages() = %q/%q, want %q/%q", verdict, reason, tc.verdict, tc.reason)
			}
		})
	}
}
