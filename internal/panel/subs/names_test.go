package subs

import (
	"net/url"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
)

func TestFlagEmoji(t *testing.T) {
	for cc, want := range map[string]string{"DE": "\U0001F1E9\U0001F1EA", "de": "\U0001F1E9\U0001F1EA", " nl ": "\U0001F1F3\U0001F1F1", "": "", "D": "", "DEU": "", "1A": "", "д": ""} {
		if got := flagEmoji(cc); got != want {
			t.Errorf("flagEmoji(%q) = %q, want %q", cc, got, want)
		}
	}
}

func TestCountryName(t *testing.T) {
	for _, c := range []struct{ cc, lang, want string }{
		{"DE", "en", "Germany"}, {"DE", "ru", "Германия"}, {"nl", "ru", "Нидерланды"}, {"ZZ", "en", "ZZ"}, {"x", "en", "X"}, {"", "en", ""},
	} {
		if got := access.CountryName(c.cc, c.lang); got != c.want {
			t.Errorf("CountryName(%q, %q) = %q, want %q", c.cc, c.lang, got, c.want)
		}
	}
}

func srv(node, cc, profile string) access.SubServer {
	return access.SubServer{URI: "hysteria2://x@h:443/#old", Node: node, CountryCode: cc, Profile: profile}
}

func TestRemarks(t *testing.T) {
	de, nl := "\U0001F1E9\U0001F1EA", "\U0001F1F3\U0001F1F1"
	for _, c := range []struct {
		name     string
		servers  []access.SubServer
		template string
		lang     string
		want     []string
	}{
		{"default template is flag and country", []access.SubServer{srv("de1", "DE", "p"), srv("nl1", "NL", "p")}, "", "ru", []string{de + " Германия", nl + " Нидерланды"}},
		{"no country: the node, no stray space", []access.SubServer{srv("de1", "", "p")}, "", "en", []string{"de1"}},
		{"no country with the node template", []access.SubServer{srv("de1", "", "p")}, "{flag} {node}", "en", []string{"de1"}},
		{"country name follows the language", []access.SubServer{srv("de1", "DE", "p")}, "{country} {node}", "ru", []string{"Германия de1"}},
		{"profile placeholder", []access.SubServer{srv("de1", "DE", "hy2")}, "{flag} {node} ({profile})", "en", []string{de + " de1 (hy2)"}},
		// two servers in one country (two nodes, two profiles, a profile and its WARP twin) read apart by a number, never
		// by the panel's profile names
		{"one country twice gets a number", []access.SubServer{srv("de1", "DE", "fast"), srv("de2", "DE", "safe"), srv("nl1", "NL", "fast")},
			"", "ru", []string{de + " Германия", de + " Германия 2", nl + " Нидерланды"}},
		{"a profile and its WARP twin", []access.SubServer{srv("de1", "DE", "files"), srv("de1", "DE", "files · WARP"), srv("nl1", "NL", "files")},
			"{flag} {node}", "en", []string{de + " de1", de + " de1 2", nl + " nl1"}},
		{"three times", []access.SubServer{srv("de1", "DE", "p"), srv("de1", "DE", "p"), srv("de1", "DE", "q")},
			"{node}", "en", []string{"de1", "de1 2", "de1 3"}},
		{"a number that is taken by a real name is skipped", []access.SubServer{srv("x", "", "p"), srv("x 2", "", "p"), srv("x", "", "p")},
			"{node}", "en", []string{"x", "x 2", "x 3"}},
		{"template that names the profile needs no number", []access.SubServer{srv("de1", "DE", "a"), srv("de1", "DE", "b")},
			"{node} {profile}", "en", []string{"de1 a", "de1 b"}},
		{"empty template result falls back to the node", []access.SubServer{srv("de1", "", "p")}, "{flag}", "en", []string{"de1"}},
		{"control characters and runs of spaces are cleaned", []access.SubServer{srv("de\n1\t  x", "DE", "p")}, "{node}", "en", []string{"de 1 x"}},
		{"a node name cannot smuggle a placeholder", []access.SubServer{srv("{profile}", "DE", "SECRET")}, "{node}", "en", []string{"{profile}"}},
	} {
		got := remarks(c.servers, c.template, c.lang)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// A subscription that does not work says why in the words of the user page, in the instance language.
func TestStateNote(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sep29, oct10, jan5 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		v          access.SubView
		lang, want string
	}{
		{access.SubView{Status: access.StatusExpired, Expires: sep29}, "ru", "Подписка закончилась 29 сентября. Напишите — продлим"},
		{access.SubView{Status: access.StatusExpired, Expires: sep29}, "en", "Subscription ended on 29 September. Message us to renew"},
		{access.SubView{Status: access.StatusExpired}, "ru", "Подписка закончилась. Напишите — продлим"},
		{access.SubView{Status: access.StatusLimited, QuotaReset: access.ResetMonth, NextReset: oct10}, "ru", "Трафик закончился, обнулится 10 октября"},
		{access.SubView{Status: access.StatusLimited, QuotaReset: access.ResetMonth, NextReset: jan5}, "ru", "Трафик закончился, обнулится 5 января 2027"},
		{access.SubView{Status: access.StatusLimited, QuotaReset: access.ResetNone}, "ru", "Трафик закончился. Напишите — увеличим"},
		{access.SubView{Status: access.StatusDisabled}, "ru", "Доступ приостановлен"},
		{access.SubView{Status: access.StatusDisabled}, "en", "Access paused"},
		{access.SubView{Status: access.StatusActive}, "ru", ""},
	} {
		if got := stateNote(c.v, c.lang, now); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.v.Status, c.lang, got, c.want)
		}
	}
}

// Happ shows 200 characters of the announcement: the cut never splits a word and says that it cut.
func TestCutAnnounce(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"short one", 20, "short one"},
		{"  padded  ", 20, "padded"},
		{"В субботу обновляю серверы — может моргнуть", 30, "В субботу обновляю серверы…"},
		{"one two three four", 9, "one two…"},                        // the cut lands inside "three": back to the word before
		{"one two three four", 8, "one two…"},                        // the cut lands right after "two": nothing to drop
		{strings.Repeat("я", 25), 20, strings.Repeat("я", 19) + "…"}, // one long word: cut inside it, there is no boundary
		{"a, b, c, d, e, f, g, h", 10, "a, b, c…"},                   // no dangling comma before the ellipsis
	} {
		got := cutAnnounce(c.in, c.max)
		if got != c.want {
			t.Errorf("cutAnnounce(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
		if n := len([]rune(got)); n > c.max {
			t.Errorf("cutAnnounce(%q, %d) is %d runes long", c.in, c.max, n)
		}
	}
}

func TestWithRemarkPercentEncodesUTF8(t *testing.T) {
	got := withRemark("hysteria2://a@h:443/?obfs=salamander#old%20name", "\U0001F1E9\U0001F1EA de1 · p")
	if strings.Contains(got, "old") || strings.ContainsAny(got[strings.Index(got, "#"):], "🇩·") {
		t.Errorf("fragment not replaced or not encoded: %s", got)
	}
	frag := got[strings.Index(got, "#")+1:]
	if dec, err := url.PathUnescape(frag); err != nil || dec != "\U0001F1E9\U0001F1EA de1 · p" {
		t.Errorf("fragment %q decodes to %q (%v)", frag, dec, err)
	}
	if !strings.HasPrefix(got, "hysteria2://a@h:443/?obfs=salamander#") {
		t.Errorf("the link changed before the fragment: %s", got)
	}
}

func TestAddURLTemplates(t *testing.T) {
	const link = "https://sub.example.com/k3xq8/tok-en_1?a=b&c=d"
	for _, c := range []struct{ tmpl, title, want string }{
		{"happ://add/{url}", "Mist gate", "happ://add/" + link},
		{"app://import/{url_enc}", "x", "app://import/https%3A%2F%2Fsub.example.com%2Fk3xq8%2Ftok-en_1%3Fa%3Db%26c%3Dd"},
		{"app://import?url={url_enc}&name={name_enc}", "Mist gate & co/é", "app://import?url=https%3A%2F%2Fsub.example.com%2Fk3xq8%2Ftok-en_1%3Fa%3Db%26c%3Dd&name=Mist%20gate%20%26%20co%2F%C3%A9"},
		// One pass: a title that looks like a placeholder is data, not a template.
		{"app://x?name={name_enc}&u={url}", "{url}", "app://x?name=%7Burl%7D&u=" + link},
		{"", "t", ""},
	} {
		if got := addURL(c.tmpl, link, c.title); got != c.want {
			t.Errorf("addURL(%q, title %q)\n got %s\nwant %s", c.tmpl, c.title, got, c.want)
		}
	}
	if addURL("happ://add/{url}", "", "t") != "" {
		t.Error("without a subscription link there is nothing to add")
	}
}

func TestChooseFormat(t *testing.T) {
	set := &adminv1.SubscriptionSettings{Rules: []*adminv1.ServeRule{
		{UaContains: "mihomo", Format: adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML},
		{UaContains: "CURL", Format: adminv1.SubFormat_SUB_FORMAT_DECOY},
		{UaContains: "happ", Format: adminv1.SubFormat_SUB_FORMAT_BASE64_URIS},
		{UaContains: "iPhone", Format: adminv1.SubFormat_SUB_FORMAT_USER_PAGE},
	}}
	const chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"
	const iosSafari = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	const firefox = "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0"
	for _, c := range []struct {
		ua      string
		rule    int
		format  adminv1.SubFormat
		browser bool
	}{
		{"mihomo/1.19.0", 0, adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML, false},
		{"Clash.Meta mihomo", 0, adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML, false},
		{"curl/8.5.0", 1, adminv1.SubFormat_SUB_FORMAT_DECOY, false},
		{"Happ/2.1.0/ios", 2, adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, false},
		{iosSafari, 3, adminv1.SubFormat_SUB_FORMAT_USER_PAGE, false}, // a rule beats the browser fallback, first match wins
		{chrome, -1, adminv1.SubFormat_SUB_FORMAT_USER_PAGE, true},
		{firefox, -1, adminv1.SubFormat_SUB_FORMAT_USER_PAGE, true},
		{"v2rayNG/1.9.0", -1, adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, false},
		{"Mozilla/5.0 Hiddify/2.0", -1, adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, false}, // browser-shaped prefix, VPN client
		{"", -1, adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, false},
		{"Go-http-client/2.0", -1, adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, false},
	} {
		rule, format, browser := Choose(set, c.ua)
		if rule != c.rule || format != c.format || browser != c.browser {
			t.Errorf("Choose(%.40q) = %d %v %v, want %d %v %v", c.ua, rule, format, browser, c.rule, c.format, c.browser)
		}
	}
	if rule, format, _ := Choose(&adminv1.SubscriptionSettings{}, chrome); rule != -1 || format != adminv1.SubFormat_SUB_FORMAT_USER_PAGE {
		t.Errorf("no rules: %d %v", rule, format)
	}
}
