//go:build js && wasm

package store

import (
	"context"
	"syscall/js"
	"testing"
	"time"
)

func TestD1NodeInSync(t *testing.T) {
	ctx := context.Background()
	st := openD1Store(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, created_at) VALUES ('nod_sync', 'node-sync', '203.0.113.10', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.NodeHello(ctx, "nod_sync", 3, HelloInfo{Instance: "i"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.NodeApplied(ctx, "nod_sync", 3, true, 5, "wrong", nil, now); err != nil {
		t.Fatal(err)
	}
	var err error
	calls := countD1Queries(t, js.Global().Get("__d1"), "NodeInSync", func() { err = st.NodeInSync(ctx, "nod_sync", 3, 6, "right") })
	if err != nil || calls.queries != 1 || calls.batches != 1 {
		t.Fatalf("NodeInSync: %v, %d calls %d batches", err, calls.queries, calls.batches)
	}
	n, err := st.Node(ctx, "nod_sync")
	if err != nil || n.AppliedRevision != 6 || n.AppliedHash != "right" {
		t.Fatalf("node %+v %v", n, err)
	}
	rows, err := st.NodeLive(ctx, now, "nod_sync", false)
	if err != nil || len(rows) != 1 || rows[0].Drift {
		t.Fatalf("live %+v %v", rows, err)
	}
	// another session's row is left alone
	if err := st.NodeApplied(ctx, "nod_sync", 3, true, 6, "right", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := st.NodeInSync(ctx, "nod_sync", 2, 6, "right"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.NodeLive(ctx, now, "nod_sync", false); !rows[0].Drift {
		t.Fatalf("an older session cleared the live drift: %+v", rows)
	}
}
