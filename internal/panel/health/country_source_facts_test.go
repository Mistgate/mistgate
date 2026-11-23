//go:build !js

package health

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// What each source of the country check is, as the verdict and the alert read it: a path carrier shows the way past the TSPU but
// cannot prove the node answers; an eyeball source looks from home or mobile networks; a broad source may make a full block of the
// alert (the others only a partial one).
func TestCountrySourceFacts(t *testing.T) {
	for _, c := range []struct {
		source               string
		path, eyeball, broad bool
	}{
		{countryreach.SourceGlobalping, false, false, true},
		{countryreach.SourceTSPU, false, false, true},
		{countryreach.SourceCheburcheck, true, true, true},
		{countryreach.SourceCheburcheckOwn, true, true, false},
		{countryreach.SourceBsbord, false, true, false},
		{countryreach.SourceBsbordTunnel, false, true, false},
		{countryreach.SourceBsbordGeo, false, true, false},
		{countryreach.SourceLists, false, false, false},
		{countryreach.SourceProbes, false, false, false}, // its eyeball is the kind of the point
		{"somewhere_else", false, false, false},
	} {
		if got := externalSourcePath(c.source); got != c.path {
			t.Errorf("%s: path = %v", c.source, got)
		}
		if got := externalSourceEyeball(c.source); got != c.eyeball {
			t.Errorf("%s: eyeball = %v", c.source, got)
		}
		live := []countryreach.Live{{Source: c.source, Verdict: countryreach.VerdictBlocked}}
		if got := countryAlertHasBroadBlock(live); got != c.broad {
			t.Errorf("%s: broad = %v", c.source, got)
		}
	}
}

// "Check now" tells of its sources in one order, which the page and the proto document; a press that is too early tells of the same
// ones, and a source that is off reads off.
func TestCountryNowSourceOrder(t *testing.T) {
	want := []string{"globalping", "tspu", "cheburcheck", "cheburcheck_own", "probes", "bsbord", "lists", "klick"}
	n := newNowEnv(t)
	v := n.s.RunCountryNow(n.ctx, "n1")
	n.wait()
	var got []string
	for _, s := range v.Sources {
		got = append(got, s.Source)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	// klick is read again whatever else is off; bsbord listed but off reads off, not schedule_only
	n.s.cfg.External = func(context.Context) []ExternalState {
		return []ExternalState{{Source: countryreach.SourceBsbord, On: false, MaxAge: time.Hour}}
	}
	n.clock.Advance(11 * time.Minute)
	v = n.s.RunCountryNow(n.ctx, "n1")
	n.wait()
	if st, why := stateOf(v, countryreach.SourceBsbord); st != "skipped" || why != "off" {
		t.Fatalf("bsbord switched off = %q %q", st, why)
	}
	if st, why := stateOf(v, "klick"); st != "reread" || why != "" {
		t.Fatalf("klick = %q %q", st, why)
	}
	// a press that is too early names the same sources in the same order
	n.clock.Advance(time.Minute)
	v = n.s.RunCountryNow(n.ctx, "n1")
	got = nil
	for _, s := range v.Sources {
		got = append(got, s.Source)
	}
	if !slices.Equal(got, want) || v.RetryAfterS == 0 {
		t.Fatalf("too early: sources = %v, retry %d", got, v.RetryAfterS)
	}
}
