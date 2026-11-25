package subs_test

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The owner's switch "Russian services direct" (UserPageOptions.allow_ru_direct_key, off by default): the page tells whether it is
// offered, the endpoint refuses the route while it is off, and a key made with it holds the long AllowedIPs and says its route.
func TestRUDirectKeyOnThePage(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(nil)
	_, tok := m.newUser("alice", nil)
	c := call{h, t, tok}
	body := func(route string) string {
		b, _ := json.Marshal(map[string]string{"profile_id": m.awg, "platform": "ios", "label": "phone", "route": route})
		return string(b)
	}
	if a := amneziaOf(t, h, tok); a["ru_direct"] != false {
		t.Fatalf("offered by default: %v", a["ru_direct"])
	}
	if rec := c.post("/devices", body("ru-direct")); rec.Code != 403 || decodeAnswer(t, rec).Error != "ru_direct_disabled" {
		t.Errorf("switched off: %d %s", rec.Code, rec.Body.String())
	}
	set := subsettings.Defaults()
	set.UserPage.AllowRuDirectKey = proto.Bool(true)
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if a := amneziaOf(t, h, tok); a["ru_direct"] != true {
		t.Fatalf("not offered once on: %v", a["ru_direct"])
	}
	rec := c.post("/devices", body("ru-direct"))
	a := decodeAnswer(t, rec)
	if rec.Code != 200 || a.Device["route"] != "ru-direct" || len(a.Configs) == 0 {
		t.Fatalf("direct key: %d %s", rec.Code, rec.Body.String())
	}
	conf := a.Configs[0]["conf"].(string)
	if n := len(strings.Split(lineValue(conf, "AllowedIPs"), ", ")); n < 1000 || n > 1500 || strings.Contains(lineValue(conf, "Address"), ":") || a.Configs[0]["qr"] != nil {
		t.Errorf("direct key: %d AllowedIPs entries, Address %q, qr %v", n, lineValue(conf, "Address"), a.Configs[0]["qr"])
	}
	if rec := c.post("/devices", body("")); rec.Code != 200 || decodeAnswer(t, rec).Device["route"] != "" {
		t.Errorf("an ordinary key next to it: %d", rec.Code)
	}
	if rec := c.post("/devices", body("elsewhere")); rec.Code != 400 {
		t.Errorf("an unknown route: %d %s", rec.Code, rec.Body.String())
	}
}
