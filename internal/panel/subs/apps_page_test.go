package subs_test

import (
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The page data carries every app of the settings in order, with its description (as plain text) and the recommended mark.
func TestPageDataListsSeveralAppsWithDescriptions(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.PageKey = nil })
	set := subsettings.Defaults()
	win := func(name, desc string, rec bool) *adminv1.PlatformApp {
		return &adminv1.PlatformApp{Platform: adminv1.Platform_PLATFORM_WINDOWS, Kind: adminv1.App_APP_HAPP, Name: name, DownloadUrl: "https://example.com/" + name, Description: desc, Recommended: rec}
	}
	set.Apps = []*adminv1.PlatformApp{win("First", "<b>one</b>", true), win("Second", "", false)}
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	_, tok := m.newUser("alice", nil)
	d, raw := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	apps, _ := d["apps"].([]any)
	if len(apps) != 2 {
		t.Fatalf("apps: %v", d["apps"])
	}
	first, second := apps[0].(map[string]any), apps[1].(map[string]any)
	if first["name"] != "First" || first["description"] != "<b>one</b>" || first["recommended"] != true || second["name"] != "Second" || second["recommended"] != false {
		t.Errorf("apps: %v", apps)
	}
	if got := raw; len(got) > 0 && (strings.Contains(got, "<b>") || strings.Contains(got, "</script")) {
		t.Errorf("markup got out of the data block: %.200s", got)
	}
}
