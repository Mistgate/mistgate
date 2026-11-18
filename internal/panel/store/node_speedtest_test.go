//go:build !js

package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestSpeedTestTimesAreKeptForTheReportWindow(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_de1', 'de1', '192.0.2.10', 'active', 1)`)
	now := time.Unix(1_800_000_000, 0)
	old := now.Add(-(HostMaxHours + 2) * time.Hour)
	for _, at := range []time.Time{old, now.Add(-time.Hour), now.Add(-time.Hour), now} {
		if err := s.NoteSpeedTest(ctx, "nod_de1", at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SpeedTestTimes(ctx, "nod_de1", old)
	if err != nil {
		t.Fatal(err)
	}
	// the one older than the window went when a newer one was written; the same second is one row
	if want := []int64{now.Add(-time.Hour).Unix(), now.Unix()}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
