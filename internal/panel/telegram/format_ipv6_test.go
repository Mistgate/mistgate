package telegram

import (
	"maps"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The IPv6 side of the country alerts (step 4 of the IPv6 plan): an IPv4 alert whose node the IPv6 rounds reach says so in its
// own messages, and the IPv6 alert's subjects are read like the IPv4 ones.

func countryAlert(subject string, params map[string]string) store.HealthAlert {
	p := map[string]string{"node": "de1", "country": "RU", "verdict": "tcp_blocked", "confidence": "eyeball", "sources": "globalping",
		"networks": "Rostelecom (AS12389, Moscow)", "ports": "443"}
	maps.Copy(p, params)
	return store.HealthAlert{Kind: "country_blocked", Severity: 2, NodeID: "de1", Subject: subject, Params: p,
		TitleKey: "health.alert.country_blocked.title", WhyKey: "health.alert.country_blocked.why." + p["verdict"]}
}

func TestCountryAlertSaysTheNodeIsReachableOverIPv6(t *testing.T) {
	for _, tc := range []struct {
		lang L
		line string
	}{{"en", "Reachable over IPv6."}, {"ru", "По IPv6 доступна."}, {"zh", "通过 IPv6 可以访问。"}} {
		without := alertReason(tc.lang, countryAlert("", nil))
		if strings.Contains(without, "IPv6") {
			t.Errorf("%s: an alert without the parameter speaks of IPv6: %q", tc.lang, without)
		}
		// an unknown, blocked or partial IPv6 says nothing in the message
		for _, word := range []string{"unknown", "blocked", "partial", ""} {
			if got := alertReason(tc.lang, countryAlert("", map[string]string{"ipv6": word})); got != without {
				t.Errorf("%s: ipv6=%q changed the text: %q", tc.lang, word, got)
			}
		}
		with := alertReason(tc.lang, countryAlert("", map[string]string{"ipv6": "open"}))
		if with != without+"\n"+tc.line {
			t.Errorf("%s: with the parameter %q, without %q, want the line %q appended", tc.lang, with, without, tc.line)
		}
		// the same on the partial alert
		partial := map[string]string{"verdict": "partial"}
		pWithout := alertReason(tc.lang, countryAlert("partial", partial))
		partial["ipv6"] = "open"
		if pWith := alertReason(tc.lang, countryAlert("partial", partial)); pWith != pWithout+"\n"+tc.line {
			t.Errorf("%s partial: %q, want %q + the line", tc.lang, pWith, pWithout)
		}
		// the opened message carries it once, the resolved one (which says no reason) not at all, and no extra message exists
		a := countryAlert("", map[string]string{"ipv6": "open"})
		if opened := alertOpened(tc.lang, a, "de1", ""); strings.Count(opened, tc.line) != 1 {
			t.Errorf("%s opened message %q", tc.lang, opened)
		}
		if resolved := alertResolved(tc.lang, a, "de1"); strings.Contains(resolved, tc.line) {
			t.Errorf("%s resolved message %q", tc.lang, resolved)
		}
	}
}

// The legacy subjects read as before; the IPv6 ones are the same wording with a mark.
func TestCountryAlertSubjectsOfBothFamilies(t *testing.T) {
	for _, lang := range []L{"en", "ru", "zh"} {
		full4, partial4 := countryAlert("", nil), countryAlert("partial", map[string]string{"verdict": "partial"})
		full6, partial6 := countryAlert("ipv6", map[string]string{"family": "6"}), countryAlert("ipv6/partial", map[string]string{"family": "6", "verdict": "partial"})
		if alertReason(lang, full6) != alertReason(lang, full4) || alertReason(lang, partial6) != alertReason(lang, partial4) {
			t.Errorf("%s: the reason depends on the family:\n%q\n%q", lang, alertReason(lang, full6), alertReason(lang, full4))
		}
		if alertReason(lang, partial6) == alertReason(lang, full6) {
			t.Errorf("%s: the IPv6 partial alert reads as the full one", lang)
		}
		for _, pair := range [][2]store.HealthAlert{{full4, full6}, {partial4, partial6}} {
			t4, t6 := alertTitle(lang, pair[0]), alertTitle(lang, pair[1])
			if strings.Contains(t4, "IPv6") || t6 != t4+" (IPv6)" {
				t.Errorf("%s titles: %q and %q", lang, t4, t6)
			}
		}
	}
	if got := alertTitle("en", countryAlert("partial", map[string]string{"verdict": "partial"})); got != "Node is unreachable from some networks in Russia" {
		t.Errorf("legacy partial title %q", got)
	}
	if got := alertTitle("en", countryAlert("", nil)); got != "Node is unreachable from Russia" {
		t.Errorf("legacy title %q", got)
	}
}
