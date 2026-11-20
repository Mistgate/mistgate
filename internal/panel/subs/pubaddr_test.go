package subs_test

import (
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/pubaddr"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// A public address changed while the panel runs is used at once: the links start with it, and a browser on it may post.
func TestPublicAddressChangeAtRuntime(t *testing.T) {
	addr := pubaddr.New("https://panel.example.com")
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) {
		c.BaseURLFunc = func() string { return strings.TrimRight(addr.PublicURL(), "/") + "/k3xq8" }
		c.MaxWritesPerHour = -1
	})
	_, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.DeviceLimit = 50 })
	c := call{h, t, tok}
	if rec := c.post("/devices", m.addBody(""), "Origin", "https://sub.example.com"); rec.Code != 403 {
		t.Fatalf("before the change: %d %s", rec.Code, rec.Body.String())
	}
	addr.Set("https://sub.example.com")
	rec := fetch(h, "/"+tok, mihomoUA)
	if got := rec.Header().Get("Profile-Web-Page-Url"); got != "https://sub.example.com/k3xq8/"+tok {
		t.Errorf("link after the change: %q", got)
	}
	if rec := c.post("/devices", m.addBody(""), "Origin", "https://sub.example.com"); rec.Code != 200 {
		t.Errorf("the new origin: %d %s", rec.Code, rec.Body.String())
	}
}
