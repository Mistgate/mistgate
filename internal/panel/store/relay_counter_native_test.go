//go:build !js

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRelayTodayBytesPersistAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "relay.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	f := relayCounterFixture(t, st, "restart")
	day := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC).Unix()
	u := f.updates[0]
	u.RelayCounters = RelayCounters{Bytes: 123, Packets: 1, CountedUnix: day + 100}
	if err := st.SetRelayCounters(ctx, f.entryID, []RelayCounterUpdate{u}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if got := relayCounterToday(t, st, f.entryID, day); got != 123 {
		t.Fatalf("restored relay today bytes = %d, want 123", got)
	}
}
