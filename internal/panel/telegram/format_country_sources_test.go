package telegram

import (
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The alert says which sources saw the block. A source of countryreach.Sources that can raise the alert and has no name here would
// be left out of "Seen by" without a word (the own probes were); the two that never raise one are the block lists, whose verdict
// never opens an alert, and the kl!ck reports, which are no verdict at all.
func TestEverySourceThatRaisesTheCountryAlertIsNamedInThreeLanguages(t *testing.T) {
	for _, src := range countryreach.Sources {
		if src.ID == countryreach.SourceLists || src.ID == countryreach.SourceKlick {
			continue
		}
		names, ok := countrySourceNames[src.ID]
		if !ok {
			t.Errorf("%s has no name in the alert", src.ID)
			continue
		}
		for i, lang := range []L{"en", "ru", "zh"} {
			if strings.TrimSpace(names[i]) == "" {
				t.Errorf("%s has no %s name", src.ID, lang)
			}
		}
		a := store.HealthAlert{Kind: "country_blocked", Severity: 2, NodeID: "de1", Subject: "partial",
			TitleKey: "health.alert.country_blocked.title", WhyKey: "health.alert.country_blocked.why.partial",
			Params: map[string]string{"node": "de1", "country": "RU", "verdict": "partial", "confidence": "eyeball",
				"sources": src.ID, "networks": "ExampleNet"}}
		for i, lang := range []L{"en", "ru", "zh"} {
			if got := alertReason(lang, a); !strings.Contains(got, names[i]) {
				t.Errorf("%s alert in %s does not name it: %q", src.ID, lang, got)
			}
		}
	}
	for id := range countrySourceNames {
		if _, ok := countryreach.Info(id); !ok {
			t.Errorf("%s is named in the alert but is no source", id)
		}
	}
}

func TestOwnProbesAreNamedAmongTheSourcesThatSawTheBlock(t *testing.T) {
	a := store.HealthAlert{Kind: "country_blocked", Severity: 2, NodeID: "de1", Subject: "partial",
		TitleKey: "health.alert.country_blocked.title", WhyKey: "health.alert.country_blocked.why.partial",
		Params: map[string]string{"node": "de1", "country": "RU", "verdict": "partial", "confidence": "eyeball",
			"sources": "globalping,bsbord,probes", "networks": "ExampleNet"}}
	for lang, want := range map[L]string{"en": "Seen by: Globalping, bsbord, your own probes.", "ru": "Видели: Globalping, bsbord, ваши зонды.",
		"zh": "发现方：Globalping、bsbord、您自己的探测点。"} {
		if got := alertReason(lang, a); !strings.Contains(got, want) {
			t.Errorf("%s: %q lacks %q", lang, got, want)
		}
	}
}
