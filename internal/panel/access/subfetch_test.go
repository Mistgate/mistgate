package access

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A subscription link is open to anyone who holds it, so repeated fetches must not write: the single writer
// connection is shared with stats ingestion and every admin call. The writer is occupied here; a fetch must
// still be answered at once, from a device that is known and holds all its credentials: the shared device and the
// ones that report a HWID alike.
func TestSubscriptionFetchIsReadOnly(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.userBare("sub", f.group, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	hints := []DeviceHint{
		NewDeviceHint("0f8fad5b-d9cb-469f-a165-70867728950e", "iOS", "18.1", "iPhone 15", "Happ/3.4.2"),
		NewDeviceHint("7c9e6679-7425-40de-944b-e07fc1f90ae7", "Android", "15", "Pixel 9", "Happ/3.4.2"),
		{}, // an app without a HWID: the shared device, made after the two (the first HWID would have taken an existing one over)
	}
	for _, hint := range hints { // the first fetch of each makes its device and credentials
		must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Device: hint}))
	}

	w, err := e.st.W.Conn(e.ctx) // "somebody else" holds the only writer connection
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 3; i++ {
		for _, hint := range hints {
			ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
			start := time.Now()
			v, err := e.s.SubscriptionWith(ctx, token, SubOptions{Device: hint})
			cancel()
			if err != nil || len(v.Lines) != 1 {
				t.Fatalf("fetch %d of %q with a busy writer: %v, %d lines", i, hint.HWID, err, len(v.Lines))
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("fetch %d of %q waited %v for the writer", i, hint.HWID, d)
			}
		}
	}
	w.Close()

	if n := len(must(e.st.Access().Devices(e.ctx, r.User.Id))); n != len(hints) {
		t.Errorf("repeated fetches left %d devices, want %d", n, len(hints))
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

// The page, the owner's preview and the page's own calls only look: a person whose app has not fetched yet has no device
// and gets none from them, not even after a revoke, yet the page lists their servers. The app's own fetch makes the device.
func TestPageNeverCreatesADevice(t *testing.T) {
	f := newFixture(t)
	e := f.e
	touches := make(chan bool, 2)
	e.s.SetTouchHookForTest(func(started bool) { touches <- started })
	r := e.userBare("sub", f.group, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	devices := func() int { return e.count(`SELECT count(*) FROM device WHERE user_id = ?`, r.User.Id) }
	credentials := func() int { return e.count(`SELECT count(*) FROM device_credential WHERE user_id = ?`, r.User.Id) }

	notified := e.notify.n.Load()
	hint := NewDeviceHint("0f8fad5b-d9cb-469f-a165-70867728950e", "", "", "", "")
	for name, view := range map[string]func() (SubView, error){
		"page": func() (SubView, error) { return e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true}) },
		"hwid": func() (SubView, error) {
			return e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true, Device: hint})
		},
		"preview": func() (SubView, error) { v, _, err := e.s.PreviewSubscription(e.ctx, r.User.Id); return v, err },
	} {
		v, err := view()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(v.Devices) != 0 || v.DevicesUsed != 0 || len(v.Nodes) != 1 || len(v.Servers) != 1 || len(v.Nodes[0].Conns) != 1 {
			t.Errorf("%s view = devices %+v used %d, nodes %+v, servers %d; want no device and the person's one server", name, v.Devices, v.DevicesUsed, v.Nodes, len(v.Servers))
		}
		if devices() != 0 || credentials() != 0 {
			t.Fatalf("%s made %d devices and %d credentials", name, devices(), credentials())
		}
	}
	if e.notify.n.Load() != notified {
		t.Error("a view that writes nothing notified the nodes")
	}
	assertNoTouchStarted(t, touches)

	// The app's fetch makes the device and marks it fetched.
	must(e.s.Subscription(e.ctx, token))
	if devices() != 1 || credentials() == 0 {
		t.Fatalf("the app's fetch made %d devices and %d credentials, want 1 device with its credentials", devices(), credentials())
	}
	var seen int64
	if err := e.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, r.User.Id).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != e.clock.Unix() {
		t.Errorf("app fetch last_seen_at = %d, want %d", seen, e.clock.Unix())
	}
	// Revoked, it is not made again by a page either.
	e.sql(`UPDATE device SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, e.clock.Unix(), r.User.Id)
	must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true}))
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, r.User.Id); n != 0 {
		t.Fatalf("a page view recreated the revoked device (%d live)", n)
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

func TestExpiredSubscriptionFetchTouchesImplicitDeviceOncePerHour(t *testing.T) {
	f := newFixture(t)
	e := f.e
	touches := make(chan bool, 4)
	e.s.SetTouchHookForTest(func(started bool) { touches <- started })
	r := e.user("sub", f.group, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	must(e.s.Subscription(e.ctx, token)) // establish the active subscription before expiry
	var credentialsBefore int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM device_credential WHERE user_id = ? AND revoked_at IS NULL`, r.User.Id).Scan(&credentialsBefore); err != nil {
		t.Fatal(err)
	}
	e.sql(`UPDATE device SET last_seen_at = ? WHERE user_id = ?`, e.clock.Add(-2*time.Hour).Unix(), r.User.Id)
	e.sql(`UPDATE user SET expires_at = ? WHERE id = ?`, e.clock.Add(-time.Minute).Unix(), r.User.Id)

	view := must(e.s.Subscription(e.ctx, token))
	if view.Status != StatusExpired || len(view.Lines) != 0 {
		t.Fatalf("expired subscription = status %q with %d lines, want no lines", view.Status, len(view.Lines))
	}
	waitTouchEvent(t, touches, true)
	waitTouchEvent(t, touches, false)

	var seen int64
	if err := e.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, r.User.Id).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != e.clock.Unix() {
		t.Fatalf("last_seen_at = %d, want %d", seen, e.clock.Unix())
	}

	view = must(e.s.Subscription(e.ctx, token))
	if view.Status != StatusExpired || len(view.Lines) != 0 {
		t.Fatalf("second expired subscription = status %q with %d lines, want no lines", view.Status, len(view.Lines))
	}
	assertNoTouchStarted(t, touches)
	var credentials int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM device_credential WHERE user_id = ? AND revoked_at IS NULL`, r.User.Id).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if credentials != credentialsBefore {
		t.Errorf("expired fetch left %d live credentials, want the existing %d", credentials, credentialsBefore)
	}
}

func TestExpiredSubscriptionFetchWithoutImplicitDeviceDoesNotCreateOne(t *testing.T) {
	f := newFixture(t)
	e := f.e
	touches := make(chan bool, 2)
	e.s.SetTouchHookForTest(func(started bool) { touches <- started })
	r := e.user("sub", f.group, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	// User creation normally provisions the implicit device. Remove it to model a user whose implicit device is gone.
	e.sql(`DELETE FROM device_credential WHERE user_id = ?`, r.User.Id)
	e.sql(`DELETE FROM device WHERE user_id = ?`, r.User.Id)
	e.sql(`UPDATE user SET expires_at = ? WHERE id = ?`, e.clock.Add(-time.Minute).Unix(), r.User.Id)

	view := must(e.s.Subscription(e.ctx, token))
	if view.Status != StatusExpired || len(view.Lines) != 0 {
		t.Fatalf("expired subscription = status %q with %d lines, want no lines", view.Status, len(view.Lines))
	}
	assertNoTouchStarted(t, touches)
	var devices, credentials int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM device WHERE user_id = ?`, r.User.Id).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if err := e.st.R.QueryRow(`SELECT count(*) FROM device_credential WHERE user_id = ?`, r.User.Id).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if devices != 0 || credentials != 0 {
		t.Errorf("expired fetch created %d devices and %d credentials, want none", devices, credentials)
	}
}

func TestDefaultAfterResponseRunnerDoesNotWaitForWork(t *testing.T) {
	e := newEnv(t)
	started, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		e.s.afterResponse(func() {
			close(started)
			<-release
		})
		close(returned)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("default runner did not start its work")
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("default runner waited for its work")
	}
	close(release)
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
