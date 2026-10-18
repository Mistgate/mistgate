package subsettings

import (
	"context"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// One subscription request whose context ends while the cache refreshes (the app gave up on a slow panel)
// makes Load fail; the cache stores Defaults() for its whole TTL, so every fetch of the next minute is rendered with
// the default rules, name template, update interval and title instead of the owner's.
func TestCanceledFetchDoesNotCacheTheDefaults(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	c := NewCache(st, nil)
	s := Defaults()
	s.Title = "Owner Title"
	s.ServerNameTemplate = "{country} {node}"
	s.UpdateIntervalHours = 3
	s.Rules = append([]*adminv1.ServeRule{{UaContains: "Happ", Format: adminv1.SubFormat_SUB_FORMAT_XRAY_JSON}}, s.Rules...)
	if _, err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	c.Invalidate() // the TTL ran out

	gone, cancel := context.WithCancel(ctx)
	cancel() // the client of this request disconnected
	_ = c.Get(gone)

	got := c.Get(ctx) // the next person's fetch, a second later
	if got.GetTitle() != "Owner Title" || got.GetServerNameTemplate() != "{country} {node}" || len(got.Rules) != len(s.Rules) {
		t.Errorf("after one canceled fetch everyone gets the defaults: title %q, template %q, %d rules (want %d)",
			got.GetTitle(), got.GetServerNameTemplate(), len(got.Rules), len(s.Rules))
	}
}
