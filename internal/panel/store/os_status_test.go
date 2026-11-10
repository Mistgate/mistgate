//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestOsStatusStoresOnlyLatestValidSnapshot(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_os_status', 'de1', '203.0.113.10', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	first := time.Unix(1700000000, 0)
	second := time.Unix(1700000300, 0)
	if err := st.PutOsStatus(ctx, "nod_os_status", `{"pending":1}`, first, first.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutOsStatus(ctx, "nod_os_status", `{"pending":2}`, second, second.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, err := st.OsStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows["nod_os_status"].StatusJSON != `{"pending":2}` || !rows["nod_os_status"].MeasuredAt.Equal(second) || !rows["nod_os_status"].ReceivedAt.Equal(second.Add(time.Minute)) {
		t.Fatalf("latest OS status = %+v", rows)
	}
	if _, err := st.OsStatus(ctx, "nod_missing"); err != ErrNotFound {
		t.Fatalf("missing status error = %v, want ErrNotFound", err)
	}
	if err := st.PutOsStatus(ctx, "nod_os_status", "not-json", second, second); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	if err := st.PutOsStatus(ctx, "nod_missing", `{"pending":1}`, second, second); err == nil {
		t.Fatal("snapshot for a missing node was accepted")
	}
}
