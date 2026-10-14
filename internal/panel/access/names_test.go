package access

import (
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/vpnkey"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// Keys are named like the page's server list: the country and the location, "Server", a number on a repeat. The panel's
// node name (de1, "ノード") never shows: the user page and the user's apps get these names.
func TestServerLabelerIsTranslatedForChinese(t *testing.T) {
	label := ServerLabeler("zh")
	if got := label("", ""); got != "服务器" {
		t.Errorf("Chinese generic server name = %q, want 服务器", got)
	}
	if got := label("", ""); got != "服务器 2" {
		t.Errorf("Chinese generic server name, second = %q, want 服务器 2", got)
	}
	if got, en := ServerLabeler("zh")("", ""), ServerLabeler("en")("", ""); got == en {
		t.Errorf("Chinese generic server name %q is the English %q", got, en)
	}
	if got := ServerLabeler("zh")("DE", "Frankfurt"); !strings.Contains(got, " · Frankfurt") || strings.HasPrefix(got, "Germany") {
		t.Errorf("Chinese country server name = %q, want a Chinese country and the location", got)
	}
}

func TestKeyNames(t *testing.T) {
	n := func(name, cc, location string) store.AccessNode {
		return store.AccessNode{Name: name, CountryCode: cc, Location: location}
	}
	for _, c := range []struct {
		name, title, brand, lang string
		nodes                    []store.AccessNode
		servers, names, files    string
	}{
		{"a country each", "Mistgate", "Mistgate", "ru", []store.AccessNode{n("de1", "DE", ""), n("fi1", "fi", "")},
			"Германия|Финляндия", "Mistgate · Германия|Mistgate · Финляндия", "mistgate-de.conf|mistgate-fi.conf"},
		{"two nodes in one country: a number, never the node", "Mistgate", "Mistgate", "en", []store.AccessNode{n("de1", "DE", ""), n("de2", "DE", "")},
			"Germany|Germany 2", "Mistgate · Germany|Mistgate · Germany 2", "mistgate-de.conf|mistgate-de-2.conf"},
		{"the location tells them apart", "Mistgate", "Mistgate", "en", []store.AccessNode{n("de1", "DE", "Frankfurt"), n("de2", "DE", "")},
			"Germany · Frankfurt|Germany", "Mistgate · Germany · Frankfurt|Mistgate · Germany", "mistgate-de.conf|mistgate-de-2.conf"},
		{"no country: the location, else Server", "Mistgate", "Mistgate", "ru", []store.AccessNode{n("ee 1", "", "Tallinn"), n("x1", "", ""), n("x2", "", "")},
			"Tallinn|Сервер|Сервер 2", "Mistgate · Tallinn|Mistgate · Сервер|Mistgate · Сервер 2", "mistgate-tallinn.conf|mistgate-awg.conf|mistgate-awg-2.conf"},
		{"the subscription title names the key, the brand the file", "Кот и туман", "Mistgate", "ru", []store.AccessNode{n("de1", "DE", "")},
			"Германия", "Кот и туман · Германия", "mistgate-de.conf"},
		{"a Cyrillic brand is spelled in Latin", "Мой ВПН", "Мой ВПН", "ru", []store.AccessNode{n("de1", "DE", "")}, "Германия", "Мой ВПН · Германия", "moy-vpn-de.conf"},
		{"nothing Latin at all", "", "日本", "en", []store.AccessNode{n("ノード", "", "")}, "Server", "Server", "vpn-awg.conf"},
		{"hostile names stay one line and a safe file", "a\nb", "x/../y", "en", []store.AccessNode{n("de1\r\nEvil = 1", "", "Evil\r\nPlace = 1")},
			"Evil Place = 1", "a b · Evil Place = 1", "x-y-evil-place-1.conf"},
	} {
		servers, names, files := keyNames(c.title, c.brand, c.lang, c.nodes)
		if strings.Join(servers, "|") != c.servers || strings.Join(names, "|") != c.names || strings.Join(files, "|") != c.files {
			t.Errorf("%s:\n servers %q, want %q\n names %q, want %q\n files %q, want %q", c.name, servers, c.servers, names, c.names, files, c.files)
		}
		for _, nd := range c.nodes {
			if all := strings.Join(append(append(servers, names...), files...), "|"); strings.Contains(all, nd.Name) {
				t.Errorf("%s: the node name %q shows in %q", c.name, nd.Name, all)
			}
		}
	}
}

// What AmneziaVPN lists and what the file is called: the subscription and the country, never "de1 · AWG 3.1".
func TestAWGKeysAreNamedByCountry(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.sql(`UPDATE node SET country_code = 'DE' WHERE id = 'nod_de1'`)
	e.node("nod_de2", "de2", "de2.example.com", "active")
	e.node("nod_fi1", "fi1", "fi1.example.com", "active")
	e.sql(`UPDATE node SET country_code = 'DE' WHERE id = 'nod_de2'`)
	e.sql(`UPDATE node SET country_code = 'FI' WHERE id = 'nod_fi1'`)
	e.inbound(f.profile, "nod_de2")
	e.inbound(f.profile, "nod_fi1")
	ru := "ru"
	must(instance.Update(e.ctx, e.st, instance.Patch{Language: &ru}))

	described := func(cfgs []*adminv1.DeviceConfig) (names, files []string) {
		for _, c := range cfgs {
			var doc struct {
				Description string `json:"description"`
			}
			if err := json.Unmarshal(must(vpnkey.Decode(c.VpnKey)), &doc); err != nil {
				t.Fatal(err)
			}
			names, files = append(names, doc.Description), append(files, c.ConfFilename)
		}
		return names, files
	}
	r := f.add(f.user, "")
	names, files := described(r.Configs)
	if strings.Join(names, "|") != "Mistgate · Германия|Mistgate · Германия 2|Mistgate · Финляндия" ||
		strings.Join(files, "|") != "mistgate-de.conf|mistgate-de-2.conf|mistgate-fi.conf" {
		t.Errorf("names %q files %q", names, files)
	}

	// The subscription's own title names the keys.
	set := subsettings.Defaults()
	set.Title = "Кот VPN"
	if _, err := subsettings.NewCache(e.st, nil).Update(e.ctx, set); err != nil {
		t.Fatal(err)
	}
	again := must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: r.Device.Id}))).Msg.Configs
	if names, _ := described(again); names[2] != "Кот VPN · Финляндия" {
		t.Errorf("with a title: %q", names)
	}
}

// CountryName answers from a table (country_names.go) so that the edge edition's wasm does not carry the names of every
// language of the world. For the two-letter codes a node can have it must say what golang.org/x/text says; if this fails,
// run `go generate ./internal/panel/access` after a change of that library.
func TestCountryNamesAreTheLibrarys(t *testing.T) {
	libraryName := func(cc, lang string) string {
		r, err := language.ParseRegion(cc)
		if err != nil || !r.IsCountry() {
			return cc
		}
		if n := display.Regions(language.Make(lang)).Name(r); n != "" {
			return n
		}
		return cc
	}
	for a := 'A'; a <= 'Z'; a++ {
		for b := 'A'; b <= 'Z'; b++ {
			code := string([]rune{a, b})
			for _, lang := range []string{"en", "ru", "zh"} {
				if got, want := CountryName(code, lang), libraryName(code, lang); got != want {
					t.Errorf("CountryName(%q, %q) = %q, the library says %q", code, lang, got, want)
				}
				if got, want := CountryName(strings.ToLower(code)+" ", lang), libraryName(code, lang); got != want {
					t.Errorf("CountryName(%q, %q) = %q, want %q", strings.ToLower(code)+" ", lang, got, want)
				}
			}
		}
	}
	if got := CountryName("DE", "de"); got != "Germany" {
		t.Errorf("a language the panel does not have: CountryName(DE, de) = %q, want the English name", got)
	}
}
