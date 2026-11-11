package access

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The catalogue: what the stored client string of an app says (family, version), how versions compare, "unknown" is not "outdated",
// the fingerprint decides what a classical one means, and the owner's changes are stored as the difference from the built-in table.

func TestClientAppOfRealUserAgents(t *testing.T) {
	for ua, want := range map[string][2]string{
		"Happ/2.7.1":                                      {"happ", "2.7.1"},
		"Happ/6.0.0/ios/2609301456616":                    {"happ", "6.0.0"}, // clientOf cuts the platform and build
		"v2rayNG/1.9.45":                                  {"v2rayng", "1.9.45"},
		"SFA/1.14.3 (build 543; language en)":             {"singbox", "1.14.3"},
		"sing-box":                                        {"singbox", ""},
		"mihomo/1.19.8":                                   {"mihomo", "1.19.8"},
		"mihomo/1.19.33 klick/0.5.1":                      {"klick", "0.5.1"}, // its own token names it
		"clash.meta/v1.18.0":                              {"mihomo", "v1.18.0"},
		"FlClash/v0.8.60 clash-verge Platform/windows":    {"flclash", "v0.8.60"},
		"Shadowrocket/2086 CFNetwork/1568.100.1 Darwin/":  {"shadowrocket", "2086"},
		"NekoBox/Android/1.3.7 (Prefer ClashMeta Format)": {"nekobox", "Android"},
		"": {"", ""},
		"HiddifyNext/2.0.5 (android) like ClashMeta v2ray sing-box": {"hiddify", "2.0.5"},
	} {
		app, ver := ClientApp(clientOf(ua))
		if app != want[0] || ver != want[1] {
			t.Errorf("%q -> (%q, %q), want %v", ua, app, ver, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.19.9", "1.19.9", 0}, {"1.19.10", "1.19.9", 1}, {"1.19.8", "1.19.9", -1}, {"1.19", "1.19.0", 0}, {"2.0.16", "1.10.1", 1},
		{"v7.12.6", "7.12.6", 0}, {"3.20.0", "3.4.2", 1}, {"4.8.0-beta.1", "4.8.0", 0}, {"0.4.0", "0.10.0", -1}, {"6.0.0", "4.8.0", 1},
	} {
		a, ok1 := parseVersion(c.a)
		b, ok2 := parseVersion(c.b)
		if !ok1 || !ok2 {
			t.Fatalf("unparsed %q or %q", c.a, c.b)
		}
		if got := compareVersions(a, b); got != c.want {
			t.Errorf("compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"", "Android", "x1.2", "-1.2", "1234567890.1"} {
		if _, ok := parseVersion(bad); ok {
			t.Errorf("%q read as a version", bad)
		}
	}
}

func TestClientVerdicts(t *testing.T) {
	table := NewClientTable(nil)
	for _, c := range []struct {
		name, client, platform, fp string
		state, reason, need        string
	}{
		// the apps and versions of the plan, section 2.3
		{"Happ Android 2.7.1 (Xray 25.3.6) is below", "Happ/2.7.1", "android", "chrome", ClientBelow, ReasonVersion, "3.1.1"},
		{"Happ Android 3.1.1 with chrome", "Happ/3.1.1", "android", "chrome", ClientOK, "", ""},
		{"Happ Android 3.4.2 with firefox needs 3.20.0", "Happ/3.4.2", "android", "firefox", ClientBelow, ReasonVersion, "3.20.0"},
		{"Happ Android 3.20.0 with safari", "Happ/3.20.0", "android", "safari", ClientOK, "", ""},
		{"Happ Desktop 2.5.2 is bad for firefox", "Happ/2.5.2", "windows", "firefox", ClientBelow, ReasonVersion, "2.10.0"},
		{"Happ Desktop 2.5.2 is fine for chrome", "Happ/2.5.2", "windows", "chrome", ClientOK, "", ""},
		{"Happ iOS 3.4.2 with chrome: nothing found, not outdated", "Happ/3.4.2", "ios", "chrome", ClientUnknown, ReasonNoThreshold, ""},
		{"Happ iOS 4.8.0 works for chrome too", "Happ/4.8.0", "ios", "chrome", ClientOK, "", ""},
		{"Happ iOS 6 with firefox", "Happ/6.0.0", "ios", "firefox", ClientOK, "", ""},
		{"Happ iOS 4.2 with firefox", "Happ/4.2.0", "ios", "firefox", ClientBelow, ReasonVersion, "4.8.0"},
		{"Happ on a platform the table has no row for", "Happ/3.4.2", "other", "chrome", ClientUnknown, ReasonNoThreshold, ""},
		{"Happ with no platform", "Happ/3.4.2", "", "chrome", ClientUnknown, ReasonNoThreshold, ""},
		{"v2rayNG 1.9.45 (Xray 25.3.31) is below", "v2rayNG/1.9.45", "", "chrome", ClientBelow, ReasonVersion, "1.10.1"},
		{"v2rayNG 2.3.15", "v2rayNG/2.3.15", "android", "firefox", ClientOK, "", ""},
		{"v2rayN 7.12.6", "v2rayN/7.12.6", "windows", "chrome", ClientOK, "", ""},
		{"sing-box SFA: no version works", "SFA/1.14.3", "android", "chrome", ClientUnsupported, ReasonApp, ""},
		{"sing-box core UA without a version", "sing-box", "", "chrome", ClientUnsupported, ReasonApp, ""},
		{"NekoBox", "NekoBox/Android", "android", "chrome", ClientUnsupported, ReasonApp, ""},
		{"Mihomo 1.19.8 is below", "mihomo/1.19.8", "windows", "chrome", ClientBelow, ReasonVersion, "1.19.9"},
		{"Mihomo 1.19.9", "mihomo/1.19.9", "linux", "chrome", ClientOK, "", ""},
		{"Mihomo 1.19.33", "mihomo/1.19.33", "", "firefox", ClientOK, "", ""},
		{"kl!ck 0.5.1", "kl!ck/0.5.1", "windows", "chrome", ClientOK, "", ""},
		{"FlClash: the core is not in its User-Agent", "FlClash/v0.8.60", "windows", "chrome", ClientUnknown, ReasonNoThreshold, ""},
		{"Shadowrocket: an own implementation", "Shadowrocket/2086", "ios", "chrome", ClientUnknown, ReasonNoThreshold, ""},
		{"Streisand: Xray-based, no version found", "Streisand/1.4", "ios", "chrome", ClientUnknown, ReasonNoThreshold, ""},
		{"Hiddify: the fork's release is not verified", "HiddifyNext/2.0.5", "android", "chrome", ClientUnknown, ReasonNoThreshold, ""},
		{"an app the table never heard of", "SuperVPN/9.1", "android", "chrome", ClientUnknown, ReasonAppUnknown, ""},
		{"no client string at all", "", "android", "chrome", ClientUnknown, ReasonAppUnknown, ""},
		{"a known app that reports no version", "Happ", "android", "chrome", ClientUnknown, ReasonNoVersion, ""},
		{"an empty fingerprint is the default, chrome", "Happ/3.1.1", "android", "", ClientOK, "", ""},
		// the fingerprint of the profile: ios, edge and random are classical for every app
		{"Xray app, ios fingerprint", "Happ/6.0.0", "ios", "ios", ClientUnsupported, ReasonFingerprint, ""},
		{"Xray app, edge fingerprint", "v2rayNG/2.3.15", "android", "edge", ClientUnsupported, ReasonFingerprint, ""},
		{"Mihomo, random fingerprint", "mihomo/1.19.33", "windows", "random", ClientUnsupported, ReasonFingerprint, ""},
		{"an app of an own implementation says nothing of a classical fingerprint", "Shadowrocket/2086", "ios", "edge", ClientUnknown, ReasonNoThreshold, ""},
		{"an app that never works stays an app problem", "SFA/1.14.3", "ios", "ios", ClientUnsupported, ReasonApp, ""},
	} {
		got := table.Verdict(c.client, c.platform, c.fp)
		if got.State != c.state || got.Reason != c.reason || got.Need != c.need {
			t.Errorf("%s: %s %s %s -> %+v, want %s/%s/%s", c.name, c.client, c.platform, c.fp, got, c.state, c.reason, c.need)
		}
	}
}

// Unknown is a state of its own and is never worse than a known "below": nothing the table does not know is reported as outdated.
func TestUnknownIsNotOutdated(t *testing.T) {
	table := NewClientTable(nil)
	for _, c := range clientDefaults {
		if c.Unsupported || c.MinChrome != "" || c.MinOther != "" {
			continue
		}
		v := table.Verdict(c.App+"/0.0.1", c.Platform, "chrome")
		if v.State == ClientBelow {
			t.Errorf("%s has no known version and was called below: %+v", c.App, v)
		}
	}
	if clientSeverity(ClientUnsupported) <= clientSeverity(ClientBelow) || clientSeverity(ClientBelow) <= clientSeverity(ClientUnknown) || clientSeverity(ClientUnknown) <= clientSeverity(ClientOK) {
		t.Error("severity order is not unsupported > below > unknown > ok")
	}
}

func TestClientDefaultsAreWellFormed(t *testing.T) {
	if len(clientDefaults) == 0 {
		t.Fatal("no built-in table")
	}
	seen := map[string]bool{}
	for _, r := range clientDefaults {
		c, err := cleanClientRule(r)
		if err != nil {
			t.Errorf("%s/%s: %v", r.App, r.Platform, err)
		}
		if c.App != r.App || c.MinChrome != r.MinChrome || c.MinOther != r.MinOther {
			t.Errorf("%s/%s changes when cleaned: %+v", r.App, r.Platform, c)
		}
		if r.Title == "" || seen[r.key()] {
			t.Errorf("%s/%s: empty title or twice", r.App, r.Platform)
		}
		seen[r.key()] = true
	}
	for alias, app := range clientAliases {
		if !slices.ContainsFunc(clientDefaults, func(r ClientRule) bool { return r.App == app }) {
			t.Errorf("alias %q names %q, which the table does not have", alias, app)
		}
	}
	// the numbers of the plan are the ones in code
	for _, c := range []struct{ app, platform, chrome, other string }{
		{"happ", "android", "3.1.1", "3.20.0"}, {"happ", "windows", "0.3.6", "2.10.0"}, {"happ", "ios", "", "4.8.0"},
		{"v2rayng", "", "1.10.1", "2.0.16"}, {"v2rayn", "", "7.12.6", "7.20.0"}, {"mihomo", "", "1.19.9", "1.19.9"},
	} {
		i := slices.IndexFunc(clientDefaults, func(r ClientRule) bool { return r.App == c.app && r.Platform == c.platform })
		if i < 0 || clientDefaults[i].MinChrome != c.chrome || clientDefaults[i].MinOther != c.other {
			t.Errorf("%s/%s: %+v, want %s / %s", c.app, c.platform, clientDefaults[i], c.chrome, c.other)
		}
	}
}

func TestOwnerChangesAreStoredAsTheDifference(t *testing.T) {
	// the table as it is: nothing to store
	if got, err := ClientOverridesOf(NewClientTable(nil).Rules()); err != nil || len(got) != 0 {
		t.Fatalf("untouched table stores %v, %v", got, err)
	}
	want := NewClientTable(nil).Rules()
	for i, r := range want {
		switch {
		case r.App == "mihomo":
			want[i].MinChrome = "v1.19.14" // a change, with the "v" of a tag
		case r.App == "happ" && r.Platform == "ios":
			want = append(want[:i], want[i+1:]...) // a built-in row left out comes back by itself
		}
	}
	want = append(want, ClientRule{App: "SuperVPN", Platform: "Android", Core: "xray", MinChrome: "9.1.0"})
	got, err := ClientOverridesOf(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].App != "mihomo" || got[0].MinChrome != "1.19.14" || got[1].App != "supervpn" || got[1].Platform != "android" {
		t.Fatalf("stored %+v", got)
	}
	raw, err := MarshalClientOverrides(got)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseClientOverrides(raw)
	if err != nil || len(back) != 2 {
		t.Fatalf("round trip %q: %v, %v", raw, back, err)
	}

	table := NewClientTable(back)
	// the override changes the verdict, the title of a built-in row stays, an own row gets its app as the title
	if v := table.Verdict("mihomo/1.19.12", "windows", "chrome"); v.State != ClientBelow || v.Need != "1.19.14" || v.Title != "Mihomo core" {
		t.Errorf("overridden mihomo: %+v", v)
	}
	if v := table.Verdict("SuperVPN/9.0.0", "android", "chrome"); v.State != ClientBelow || v.Need != "9.1.0" || v.Title != "supervpn" {
		t.Errorf("own row: %+v", v)
	}
	if v := table.Verdict("SuperVPN/9.0.0", "ios", "chrome"); v.State != ClientUnknown || v.Reason != ReasonNoThreshold {
		t.Errorf("own row on another platform: %+v", v)
	}
	if v := table.Verdict("Happ/4.8.0", "ios", "firefox"); v.State != ClientOK { // the left-out row is back
		t.Errorf("restored row: %+v", v)
	}
	// a rule of the platform wins over the rule for any platform; a built-in row set to "unknown" says unknown
	custom := NewClientTable([]ClientRule{{App: "mihomo", Core: CoreMihomo}, {App: "mihomo", Platform: "android", Core: CoreMihomo, MinChrome: "1.19.20"}})
	if v := custom.Verdict("mihomo/1.19.12", "android", "chrome"); v.State != ClientBelow || v.Need != "1.19.20" {
		t.Errorf("platform row: %+v", v)
	}
	if v := custom.Verdict("mihomo/1.19.12", "windows", "chrome"); v.State != ClientUnknown {
		t.Errorf("a row set to unknown: %+v", v)
	}
}

func TestClientRulesRefuseWhatCannotBeOne(t *testing.T) {
	ok := ClientRule{App: "supervpn", Core: CoreXray, MinChrome: "1.2.3"}
	for name, r := range map[string]ClientRule{
		"empty app":            {Core: CoreXray},
		"long app":             {App: strings.Repeat("a", 41), Core: CoreXray},
		"space in app":         {App: "super vpn", Core: CoreXray},
		"app with a slash":     {App: "a/b", Core: CoreXray},
		"app starts with dash": {App: "-a", Core: CoreXray},
		"platform":             {App: "a", Platform: "other", Core: CoreXray},
		"core":                 {App: "a", Core: "v2ray"},
		"empty core":           {App: "a"},
		"version words":        {App: "a", Core: CoreXray, MinChrome: "latest"},
		"version dots":         {App: "a", Core: CoreXray, MinChrome: "1..2"},
		"version tail dot":     {App: "a", Core: CoreXray, MinOther: "1.2."},
		"version too deep":     {App: "a", Core: CoreXray, MinOther: "1.2.3.4.5"},
		"version beta":         {App: "a", Core: CoreXray, MinOther: "1.2.3-beta"},
		"never with a minimum": {App: "a", Core: CoreXray, Unsupported: true, MinOther: "1.2.3"},
	} {
		if _, err := ClientOverridesOf([]ClientRule{r}); err == nil || !errors.Is(err, ErrClientRules) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if _, err := ClientOverridesOf([]ClientRule{ok, ok}); err == nil {
		t.Error("the same app and platform twice was accepted")
	}
	var many []ClientRule
	for i := 0; i <= maxClientRules; i++ {
		many = append(many, ClientRule{App: "app" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), Core: CoreXray})
	}
	if _, err := ClientOverridesOf(many); err == nil {
		t.Error("more rows than the limit was accepted")
	}
	// the stored text is read strictly too: a bad row leaves the caller with the built-in table
	for _, raw := range []string{`{`, `[{"app":"a","core":"xray","colour":"red"}]`, `[{"app":"","core":"xray"}]`} {
		if _, err := ParseClientOverrides(raw); err == nil {
			t.Errorf("stored %q was accepted", raw)
		}
	}
	if rows, err := ParseClientOverrides(" "); err != nil || rows != nil {
		t.Errorf("empty stored value: %v, %v", rows, err)
	}
}
