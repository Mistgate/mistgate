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
	touches := make(chan bool, 2)
	r.svc.SetTouchHookForTest(func(started bool) { touches <- started })
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
	assertNoTouchStarted(t, touches)
	if got := seen(); got != stale {
		t.Fatalf("the page view or its calls moved last_seen_at: %d, was %d", got, stale)
	}

	// The app's fetch comes within MinInterval of the page view: it still counts.
	fetch(h, "/"+tok, happUA)
	waitTouchEvent(t, touches, true)
	waitTouchEvent(t, touches, false)
	if seen() == stale {
		t.Fatal("an app fetch did not touch the device")
	}
}

// The page makes no device: a person whose app has not fetched yet has none, however often the page is opened, and the page
// still lists their servers. The first fetch of an app makes the device and marks it fetched.
func TestPageNeverCreatesADevice(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.MinInterval = -1 })
	uid, tok := r.user("alice", nil)
	touches := make(chan bool, 2)
	r.svc.SetTouchHookForTest(func(started bool) { touches <- started })
	count := func(q string) (n int) {
		if err := r.st.R.QueryRow(q, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for i := 0; i < 2; i++ {
		d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
		if len(arr(d["devices"])) != 0 || d["server_count"] != float64(1) {
			t.Fatalf("page %d: devices %v, servers %v; want none and the person's one server", i, d["devices"], d["server_count"])
		}
		if n := count(`SELECT count(*) FROM device WHERE user_id = ?`); n != 0 {
			t.Fatalf("page %d made %d devices", i, n)
		}
	}
	assertNoTouchStarted(t, touches)

	fetch(h, "/"+tok, happUA)
	var seen int64
	if err := r.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, uid).Scan(&seen); err != nil || seen == 0 {
		t.Fatalf("the app's fetch left the shared device at last_seen_at %d: %v", seen, err)
	}
	second, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if devices := arr(second["devices"]); len(devices) != 1 || obj(devices[0])["last_seen_unix"] != float64(seen) {
		t.Fatalf("the page after the app's fetch: %v", second["devices"])
	}
}

func assertNoTouchStarted(t *testing.T, touches <-chan bool) {
	t.Helper()
	select {
	case started := <-touches:
		t.Fatalf("unexpected touch event: started=%t", started)
	default:
	}
}

func waitTouchEvent(t *testing.T, touches <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-touches:
		if got != want {
			t.Fatalf("touch event started=%t, want %t", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for touch event started=%t", want)
	}
}
