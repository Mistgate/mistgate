package dns

import (
	"strings"
	"testing"
	"unicode"
)

// Every built-in preset has its words on the Persian and the Turkish page: a Persian name in Persian letters (a bare brand
// stays a brand), a Turkish one, and both descriptions, shown while the stored English one is still the stock one.
func TestPersianAndTurkishBuiltInNamesAreTranslated(t *testing.T) {
	persian := func(s string) bool {
		return strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Arabic, r) })
	}
	for id, names := range builtinNames {
		p := Preset{ID: id, Name: names.ru, Builtin: true}
		brandOnly := names.en == names.zh // "Cloudflare + Google", "Yandex DNS"
		if got := p.NameIn("fa"); got != names.fa || brandOnly == persian(got) {
			t.Errorf("%s: Persian name %q is not a translation of %q", id, got, names.en)
		}
		if got := p.NameIn("tr"); got != names.tr || got == "" || (!brandOnly && got == names.en) {
			t.Errorf("%s: Turkish name %q is not a translation of %q", id, got, names.en)
		}
		d, ok := builtinDescriptions[id]
		if !ok || !persian(d.fa) || d.tr == "" || d.tr == d.en {
			t.Errorf("%s: no Persian or Turkish description (%+v)", id, d)
		}
		p.Description = names.ru + "\n" + d.en
		if p.DescriptionIn("fa") != d.fa || p.DescriptionIn("tr") != d.tr {
			t.Errorf("%s: DescriptionIn(fa, tr) = %q, %q", id, p.DescriptionIn("fa"), p.DescriptionIn("tr"))
		}
		p.Description = names.ru + "\nThe owner's own words"
		if got := p.DescriptionIn("fa"); got != "The owner's own words" {
			t.Errorf("%s: an owner's description is shown as it is, got %q", id, got)
		}
	}
}
