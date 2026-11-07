package doctor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPercentileIsNearestRank(t *testing.T) {
	ms := func(v ...int) []time.Duration {
		out := make([]time.Duration, len(v))
		for i, x := range v {
			out[i] = time.Duration(x) * time.Millisecond
		}
		return out
	}
	for _, tc := range []struct {
		lat  []time.Duration
		pct  int
		want time.Duration
	}{
		{nil, 95, 0},
		{ms(7), 50, 7 * time.Millisecond},
		{ms(7), 95, 7 * time.Millisecond},
		{ms(10, 20, 30, 40), 50, 20 * time.Millisecond},
		{ms(10, 20, 30, 40), 95, 40 * time.Millisecond},
		{ms(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20), 95, 19 * time.Millisecond},
	} {
		if got := percentile(tc.lat, tc.pct); got != tc.want {
			t.Errorf("percentile(%v, %d) = %v, want %v", tc.lat, tc.pct, got, tc.want)
		}
	}
}

// The resolver check records p50 and p95 of the control domains' lookups and how many failed, for the panel's history.
func TestResolverRecordsTimings(t *testing.T) {
	f := newFake(t)
	delays := map[string]time.Duration{"www.cloudflare.com": 5 * time.Millisecond, "www.gstatic.com": 10 * time.Millisecond, "www.google.com": 60 * time.Millisecond}
	timed := func(e *Env) {
		e.Now = time.Now // the lookups are timed with the clock of the Env
		e.Lookup = func(_ context.Context, h string) error {
			time.Sleep(delays[h])
			return nil
		}
		e.LookupVia = func(context.Context, string, string) error { return nil }
	}
	r := run(t, f.doctor(timed), CheckResolver)
	want(t, r, OK, "")
	p50, p95 := r.Params["p50_ms"], r.Params["p95_ms"]
	if p50 == "" || p95 == "" || r.Params["median_ms"] != p50 || r.Params["failed_count"] != "0" || r.Params["domains"] != "3" {
		t.Fatalf("params = %v", r.Params)
	}
	a, b := atoi(t, p50), atoi(t, p95)
	if a < 8 || a > 40 || b < 55 || b < a {
		t.Errorf("p50 = %d, p95 = %d: want the middle and the slowest lookup", a, b)
	}

	// a failed lookup is counted and left out of the timings
	r = run(t, f.doctor(func(e *Env) {
		timed(e)
		inner := e.Lookup
		e.Lookup = func(c context.Context, h string) error {
			if h == "www.google.com" {
				return errors.New("SERVFAIL")
			}
			return inner(c, h)
		}
	}), CheckResolver)
	if r.Params["failed_count"] != "1" || atoi(t, r.Params["p95_ms"]) >= 55 {
		t.Errorf("one failure: %v", r.Params)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("%q is not a number", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}
