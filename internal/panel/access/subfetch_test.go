package access

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A subscription link is open to anyone who holds it, so repeated fetches must not write: the single writer
// connection is shared with stats ingestion and every admin call. The writer is occupied here; a fetch must
// still be answered at once.
func TestSubscriptionFetchIsReadOnly(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.user("sub", f.group, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]

	must(e.s.Subscription(e.ctx, token)) // the first fetch may create the device and its credentials

	w, err := e.st.W.Conn(e.ctx) // "somebody else" holds the only writer connection
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
		start := time.Now()
		v, err := e.s.Subscription(ctx, token)
		cancel()
		if err != nil || len(v.Lines) != 1 {
			t.Fatalf("fetch %d with a busy writer: %v, %d lines", i, err, len(v.Lines))
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("fetch %d waited %v for the writer", i, d)
		}
	}
	w.Close()

	if n := len(must(e.st.Access().Devices(e.ctx, r.User.Id))); n != 1 {
		t.Errorf("repeated fetches left %d devices, want the one implicit device", n)
	}
}

// A view for the page (SubOptions.NoTouch) is not an app's fetch: it leaves last_seen_at alone.
func TestSubscriptionNoTouchLeavesTheDeviceAlone(t *testing.T) {
	f := newFixture(t)
	e := f.e
	touches := make(chan bool, 2)
	e.s.SetTouchHookForTest(func(started bool) { touches <- started })
	r := e.user("sub", f.group, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	must(e.s.Subscription(e.ctx, token))
	stale := e.clock.Add(-2 * time.Hour).Unix()
	e.sql(`UPDATE device SET last_seen_at = ? WHERE user_id = ?`, stale, r.User.Id)

	must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true}))
	assertNoTouchStarted(t, touches)
	var seen int64
	e.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ?`, r.User.Id).Scan(&seen)
	if seen != stale {
		t.Errorf("last_seen_at = %d after a view for the page, want %d", seen, stale)
	}
}

func TestPageRecreatesImplicitDeviceWithoutClaimingFetch(t *testing.T) {
	f := newFixture(t)
	e := f.e
	touches := make(chan bool, 2)
	e.s.SetTouchHookForTest(func(started bool) { touches <- started })
	r := e.user("sub", f.group, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	e.sql(`UPDATE device SET revoked_at = ? WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, e.clock.Unix(), r.User.Id)

	v := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true}))
	if len(v.Devices) != 0 {
		t.Errorf("page view claims a fetched device: %+v", v.Devices)
	}
	var seen int64
	if err := e.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, r.User.Id).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Fatalf("page-created implicit device last_seen_at = %d, want 0", seen)
	}
	assertNoTouchStarted(t, touches)

	must(e.s.Subscription(e.ctx, token))
	waitTouchEvent(t, touches, true)
	waitTouchEvent(t, touches, false)
	if err := e.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, r.User.Id).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != e.clock.Unix() {
		t.Errorf("app fetch last_seen_at = %d, want %d", seen, e.clock.Unix())
	}
}

// last_seen_at of the device is refreshed, but only when stale and never on the request path.
func TestSubscriptionFetchTouchesStaleDeviceOffTheRequestPath(t *testing.T) {
	f := newFixture(t)
	e := f.e
	touches := make(chan bool, 2)
	e.s.SetTouchHookForTest(func(started bool) { touches <- started })
	r := e.user("sub", f.group, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	must(e.s.Subscription(e.ctx, token))

	seen := func() int64 {
		var n int64
		e.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ?`, r.User.Id).Scan(&n)
		return n
	}
	e.sql(`UPDATE device SET last_seen_at = ? WHERE user_id = ?`, e.clock.Add(-2*time.Hour).Unix(), r.User.Id)

	w, _ := e.st.W.Conn(e.ctx) // a busy writer must not delay the response
	start := time.Now()
	must(e.s.Subscription(e.ctx, token))
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the fetch waited %v for the writer", d)
	}
	waitTouchEvent(t, touches, true)
	w.Close() // the touch goes through once the writer is free
	waitTouchEvent(t, touches, false)
	if seen() != e.clock.Unix() {
		t.Fatalf("last_seen_at = %d, want %d", seen(), e.clock.Unix())
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
