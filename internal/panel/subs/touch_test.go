package subs_test

import (
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/subs"
)

// A view an app's fetch left in the cache has no page data (the DNS of the servers): the page, opened right after, must
// still get it.
func TestPageViewAfterAnAppFetchHasItsData(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.MinInterval = 10 * time.Minute })
	_, tok := r.user("alice", nil)
	fetch(h, "/"+tok, happUA)
	d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if obj(obj(d["dns"])["link"])["effective"] == "" {
		t.Errorf("the page after an app's fetch has no DNS data: %v", d["dns"])
	}
}

// Only an app's fetch says "the app received the subscription" (the device's last_seen_at): opening the page in a browser
// and the page's own calls must not, and a view they left in the cache must not stand in for an app's fetch either.
func TestPageViewAndCallsDoNotTouchTheDevice(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.MinInterval = 10 * time.Minute })
	uid, tok := r.user("alice", nil)
	if _, err := r.svc.Subscription(r.ctx, tok); err != nil { // the first fetch makes the implicit device
		t.Fatal(err)
	}
	seen := func() int64 {
		var n int64
		r.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ?`, uid).Scan(&n)
		return n
	}
	stale := time.Now().Add(-2 * time.Hour).Unix()
	if _, err := r.st.W.Exec(`UPDATE device SET last_seen_at = ? WHERE user_id = ?`, stale, uid); err != nil {
		t.Fatal(err)
	}

	if rec := fetch(h, "/"+tok, chrome); rec.Code != 200 {
		t.Fatalf("page: status %d", rec.Code)
	}
	call{h, t, tok}.post("/devices/dev_x/rename", `{"label":"x"}`)
	call{h, t, tok}.post("/dns", `{}`)
	time.Sleep(300 * time.Millisecond) // a touch is written off the request path
	if got := seen(); got != stale {
		t.Fatalf("the page view or its calls moved last_seen_at: %d, was %d", got, stale)
	}

	// The app's fetch comes within MinInterval of the page view: it still counts.
	fetch(h, "/"+tok, happUA)
	deadline := time.Now().Add(5 * time.Second)
	for seen() == stale {
		if time.Now().After(deadline) {
			t.Fatal("an app fetch did not touch the device")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
