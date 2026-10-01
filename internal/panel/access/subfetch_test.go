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

// last_seen_at of the device is refreshed, but only when stale and never on the request path.
func TestSubscriptionFetchTouchesStaleDeviceOffTheRequestPath(t *testing.T) {
	f := newFixture(t)
	e := f.e
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
	w.Close() // the touch goes through once the writer is free

	deadline := time.Now().Add(5 * time.Second)
	for seen() != e.clock.Unix() {
		if time.Now().After(deadline) {
			t.Fatalf("last_seen_at = %d, want %d", seen(), e.clock.Unix())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
