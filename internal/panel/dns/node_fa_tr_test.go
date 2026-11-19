package dns

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
	"unicode"
)

var updateBuiltinNames = flag.Bool("update-builtin-names", false, "rewrite testdata/builtin_names.json")

// The names of the built-in presets are written twice, here for the user page and in the admin (web/src/screens/subscriptions/model.ts
// builtinNames). Both sides are pinned to this one file: the web test web/src/screens/subscriptions/model.test.ts reads it too.
// A name changed here fails until the file is refreshed (go test ./internal/panel/dns -run BuiltinNamesFile -update-builtin-names),
// and then the web test fails until the admin says the same.
func TestBuiltinNamesFileIsTheOneListOfNames(t *testing.T) {
	type names struct {
		Ru string `json:"ru"`
		En string `json:"en"`
		Zh string `json:"zh"`
		Fa string `json:"fa"`
		Tr string `json:"tr"`
	}
	all := map[string]names{}
	for id, n := range builtinNames {
		all[id] = names{n.ru, n.en, n.zh, n.fa, n.tr}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(all); err != nil {
		t.Fatal(err)
	}
	const path = "testdata/builtin_names.json"
	if *updateBuiltinNames {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(bytes.ReplaceAll(have, []byte("\r\n"), []byte("\n")), buf.Bytes()) {
		t.Fatalf("%s is out of date (%v): refresh it with `go test ./internal/panel/dns -run BuiltinNamesFile -update-builtin-names` and give the admin the same words in web/src/screens/subscriptions/model.ts", path, err)
	}
}

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
