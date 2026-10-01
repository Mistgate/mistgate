package access

import (
	"encoding/json"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/vpnkey"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

func TestKeyNames(t *testing.T) {
	n := func(name, cc string) store.AccessNode { return store.AccessNode{Name: name, CountryCode: cc} }
	for _, c := range []struct {
		name, title, brand, lang string
		nodes                    []store.AccessNode
		names, files             string
	}{
		{"a country each", "Mistgate", "Mistgate", "ru", []store.AccessNode{n("de1", "DE"), n("fi1", "fi")},
			"Mistgate · Германия|Mistgate · Финляндия", "mistgate-de.conf|mistgate-fi.conf"},
		{"two nodes in one country: the node tells them apart, the file a number", "Mistgate", "Mistgate", "en", []store.AccessNode{n("de1", "DE"), n("de2", "DE")},
			"Mistgate · Germany · de1|Mistgate · Germany · de2", "mistgate-de.conf|mistgate-de-2.conf"},
		{"no country: the node", "Mistgate", "Mistgate", "en", []store.AccessNode{n("ee 1", "")}, "Mistgate · ee 1", "mistgate-ee-1.conf"},
		{"the subscription title names the key, the brand the file", "Кот и туман", "Mistgate", "ru", []store.AccessNode{n("de1", "DE")},
			"Кот и туман · Германия", "mistgate-de.conf"},
		{"a Cyrillic brand is spelled in Latin", "Мой ВПН", "Мой ВПН", "ru", []store.AccessNode{n("de1", "DE")}, "Мой ВПН · Германия", "moy-vpn-de.conf"},
		{"nothing Latin at all", "", "日本", "en", []store.AccessNode{n("ノード", "")}, "ノード", "vpn-awg.conf"},
		{"hostile names stay one line and a safe file", "a\nb", "x/../y", "en", []store.AccessNode{n("de1\r\nEvil = 1", "")},
			"a b · de1 Evil = 1", "x-y-de1-evil-1.conf"},
	} {
		names, files := keyNames(c.title, c.brand, c.lang, c.nodes)
		if strings.Join(names, "|") != c.names || strings.Join(files, "|") != c.files {
			t.Errorf("%s:\n names %q, want %q\n files %q, want %q", c.name, names, c.names, files, c.files)
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
	if strings.Join(names, "|") != "Mistgate · Германия · de1|Mistgate · Германия · de2|Mistgate · Финляндия" ||
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
