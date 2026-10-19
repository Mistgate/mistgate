package subs_test

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The ping headers go to Happ only, and only when the owner picked something other than the app's default.
func TestHappPingHeaders(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(nil)
	_, tok := m.newUser("alice", nil)
	ping := func(ua string) [3]string {
		hd := fetch(h, "/"+tok, ua).Header()
		return [3]string{hd.Get("Ping-Type"), hd.Get("Ping-Result"), hd.Get("Subscription-Ping-Onopen-Enabled")}
	}
	if got := ping(happUA); got != [3]string{} {
		t.Fatalf("the app's default: %q, want no ping headers", got)
	}

	set := subsettings.Defaults()
	set.HappPing = adminv1.HappPing_HAPP_PING_TCP
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if got := ping(happUA); got != [3]string{"tcp", "time", "0"} {
		t.Errorf("TCP: %q", got)
	}
	for _, ua := range []string{v2rayNGUA, sfaUA, "clash-verge/v2.2.3"} {
		if got := ping(ua); got != [3]string{} {
			t.Errorf("%s got Happ's ping headers: %q", ua, got)
		}
	}
	set.HappPingOnOpen = true
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if got := ping(happUA); got != [3]string{"tcp", "time", "1"} {
		t.Errorf("TCP, ping on open: %q", got)
	}
	// on open means nothing without a ping type: the app's default sends nothing at all
	set.HappPing = adminv1.HappPing_HAPP_PING_UNSPECIFIED
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if got := ping(happUA); got != [3]string{} {
		t.Errorf("the app's default with on open: %q", got)
	}

	set.HappPing = 7
	if _, err := cache.Update(m.ctx, set); err == nil {
		t.Error("an unknown ping value was stored")
	}
}
