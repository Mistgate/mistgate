//go:build !js

package store

import (
	"context"
	"testing"
)

// The panel's time zone is read the same way by every reader: a missing, malformed or out-of-range value is UTC, a good one
// comes back as stored.
func TestPanelTimezoneReadsOnlyUsableValues(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if n, err := s.PanelTimezone(ctx); err != nil || n != 0 {
		t.Fatalf("nothing stored: %d, %v", n, err)
	}
	for stored, want := range map[string]int{"180": 180, "-300": -300, "840": 840, "-720": -720, "845": 0, "841": 0, "-735": 0, "x": 0, "": 0, "99999999999": 0} {
		if err := s.SetSettings(ctx, map[string]string{SettingPanelTimezone: stored}); err != nil {
			t.Fatal(err)
		}
		if n, err := s.PanelTimezone(ctx); err != nil || n != want {
			t.Errorf("stored %q: %d, %v; want %d", stored, n, err, want)
		}
	}
	if err := s.SetPanelTimezone(ctx, 330, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PanelTimezone(ctx); err != nil || n != 330 {
		t.Errorf("after SetPanelTimezone: %d, %v", n, err)
	}
	for _, m := range []int{-720, 840, 0, 345} {
		if !ValidTimezoneOffset(m) {
			t.Errorf("%d must be valid", m)
		}
	}
	for _, m := range []int{-735, 855, 7, 1000} {
		if ValidTimezoneOffset(m) {
			t.Errorf("%d must be invalid", m)
		}
	}
}
