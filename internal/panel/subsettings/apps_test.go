package subsettings

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func app(p adminv1.Platform, k adminv1.App, name string) *adminv1.PlatformApp {
	return &adminv1.PlatformApp{Platform: p, Kind: k, Name: name, DownloadUrl: "https://example.com/dl"}
}

// Several apps may share a platform and a kind: the page lists one card each, in this order.
func TestSeveralAppsOnOnePlatformAndKind(t *testing.T) {
	s := Defaults()
	s.Apps = append(s.Apps,
		app(adminv1.Platform_PLATFORM_WINDOWS, adminv1.App_APP_HAPP, "Example Client"),
		app(adminv1.Platform_PLATFORM_WINDOWS, adminv1.App_APP_HAPP, "Another Client"),
		app(adminv1.Platform_PLATFORM_WINDOWS, adminv1.App_APP_AMNEZIA, "AmneziaWG"),
	)
	s.Apps[len(s.Apps)-3].Description = "Hysteria2 и AmneziaWG в одном приложении"
	s.Apps[len(s.Apps)-3].Recommended = true
	if err := Validate(s); err != nil {
		t.Fatalf("several apps per platform and kind were refused: %v", err)
	}
	ctx := context.Background()
	st := openStore(t)
	c := NewCache(st, nil)
	if _, err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	var win []string
	for _, a := range got.Apps {
		if a.Platform == adminv1.Platform_PLATFORM_WINDOWS && a.Kind == adminv1.App_APP_HAPP {
			win = append(win, a.Name)
		}
	}
	if strings.Join(win, ",") != "Happ,Example Client,Another Client" {
		t.Errorf("order or content lost on save: %v", win)
	}
	if a := got.Apps[len(s.Apps)-3]; a.Description != "Hysteria2 и AmneziaWG в одном приложении" || !a.Recommended {
		t.Errorf("description or recommended lost: %+v", a)
	}
}

// A fresh install: Happ leads every platform it is on, AmneziaVPN comes from the store on a phone and from the site on a
// computer, and servers are named by country.
func TestDefaultsRecommendHappAndLinkTheStores(t *testing.T) {
	s := Defaults()
	if err := Validate(s); err != nil {
		t.Fatal(err)
	}
	amnezia := map[adminv1.Platform]string{}
	for _, a := range s.Apps {
		switch a.Kind {
		case adminv1.App_APP_HAPP:
			if !a.Recommended {
				t.Errorf("Happ on %v is not recommended", a.Platform)
			}
		case adminv1.App_APP_AMNEZIA:
			if a.Recommended {
				t.Errorf("AmneziaVPN on %v is recommended", a.Platform)
			}
			amnezia[a.Platform] = a.DownloadUrl
		}
	}
	if amnezia[adminv1.Platform_PLATFORM_IOS] != "https://apps.apple.com/us/app/amneziavpn/id1600529900" ||
		amnezia[adminv1.Platform_PLATFORM_ANDROID] != "https://play.google.com/store/apps/details?id=org.amnezia.vpn" ||
		amnezia[adminv1.Platform_PLATFORM_WINDOWS] != "https://amnezia.org/downloads" || amnezia[adminv1.Platform_PLATFORM_LINUX] != "https://amnezia.org/downloads" {
		t.Errorf("AmneziaVPN links = %v", amnezia)
	}
	if s.ServerNameTemplate != DefaultNameTemplate {
		t.Errorf("template = %q", s.ServerNameTemplate)
	}
}

func TestDescriptionRules(t *testing.T) {
	mk := func(desc string) *adminv1.SubscriptionSettings {
		s := Defaults()
		s.Apps[0].Description = desc
		return s
	}
	for name, desc := range map[string]string{
		"empty": "", "plain": "Hysteria2 и AmneziaWG", "80 runes": strings.Repeat("я", 80),
		// markup is not rejected: the page shows every description as text, so it is just characters
		"markup": "<b>bold</b> & <img src=x onerror=alert(1)>",
	} {
		if err := Validate(mk(desc)); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	for name, desc := range map[string]string{
		"81 runes": strings.Repeat("я", 81), "newline": "two\nlines", "control": "a\x07b", "tab": "a\tb", "bad utf8": "a\xffb",
	} {
		if err := Validate(mk(desc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	s := mk("  padded  ")
	Normalize(s)
	if s.Apps[0].Description != "padded" {
		t.Errorf("not trimmed: %q", s.Apps[0].Description)
	}
}

// The add link of an app is its own scheme (happ://, an instance's own desktop client's) or a web link, with the known
// placeholders; never a scheme that runs or embeds content.
func TestAddLinkTemplateRules(t *testing.T) {
	for tmpl, ok := range map[string]bool{
		"":                 true,
		"happ://add/{url}": true,
		"myclient://add?url={url_enc}&name={name_enc}": true,
		"my-client+v2.x://import/{url_enc}":            true,
		"https://example.com/import?u={url_enc}":       true,
		"javascript:alert(1)":                          false,
		"JavaScript://x/{url}":                         false,
		"data:text/html,{url}":                         false,
		"vbscript:x":                                   false,
		"file:///{url}":                                false,
		"{url}":                                        false,
		"x://add/{url}":                                false, // a one-letter scheme reads as a Windows drive
		"myclient://add?u={token}":                     false,
		"myclient://add/{url":                          false,
		"myclient://add {url}":                         false,
	} {
		s := Defaults()
		s.Apps[0].AddLinkTemplate = tmpl
		if err := Validate(s); (err == nil) != ok {
			t.Errorf("add_link_template %q: %v, want ok=%v", tmpl, err, ok)
		}
	}
}

// Documents stored before description and recommended existed load unchanged.
func TestOldAppsDocumentLoadsUnchanged(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	old := `{"title":"T","apps":[{"platform":"PLATFORM_IOS","kind":"APP_HAPP","name":"Happ","download_url":"https://example.com/h","add_link_template":"happ://add/{url}"}],"user_page":{"show_qr":true}}`
	if err := st.SetSettings(ctx, map[string]string{Key: old}); err != nil {
		t.Fatal(err)
	}
	s, err := Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(s); err != nil {
		t.Fatalf("the old document no longer validates: %v", err)
	}
	if a := s.Apps[0]; a.Name != "Happ" || a.Description != "" || a.Recommended || a.AddLinkTemplate != "happ://add/{url}" {
		t.Errorf("app changed on load: %+v", a)
	}
	if !PagePassword(s) || !SelfService(s) {
		t.Error("the keys a document lacks must read as on")
	}
}

func TestPagePasswordSettingSurvivesASave(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	c := NewCache(st, nil)
	in := Defaults()
	in.UserPage.RequirePagePassword = proto.Bool(false)
	if _, err := c.Update(ctx, in); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load(ctx, st); PagePassword(s) {
		t.Error("false did not survive a save")
	}
	in.UserPage.RequirePagePassword = nil // the admin UI of an older build sends no key
	if _, err := c.Update(ctx, in); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load(ctx, st); !PagePassword(s) {
		t.Error("an absent key must read as on")
	}
}
