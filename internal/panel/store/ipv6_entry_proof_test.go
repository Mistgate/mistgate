package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// ipv6EntryProof: the IPv6 entry and its document of a node are read with the row, changed together by a compare-and-swap that
// retries on what another writer left, and never written for a retired node. Runs on SQLite and on D1.
func ipv6EntryProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_v6', 'v6', '203.0.113.10', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	n, err := st.Node(ctx, "nod_v6")
	if err != nil || n.IPv6Entry != "" || n.IPv6JSON != "{}" || len(n.IPv6().Checks) != 0 {
		t.Fatalf("a new node: %+v, %v", n, err)
	}

	check := IPv6Check{Addr: "2001:db8::5", At: 100, By: "nod_x", Sender: "de1", Pass: true,
		Verdicts: []IPv6Verdict{{Net: "tcp", Port: 443, Verdict: "ours"}, {Net: "udp", Port: 8443, Verdict: "ok"}}}
	if err := st.RecordIPv6Check(ctx, "nod_v6", check); err != nil {
		t.Fatal(err)
	}
	n, _ = st.Node(ctx, "nod_v6")
	if got, ok := n.IPv6().CheckOf("2001:db8::5"); !ok || got.By != "nod_x" || got.Sender != "de1" || !got.Pass || len(got.Verdicts) != 2 || n.IPv6Entry != "" {
		t.Fatalf("after RecordIPv6Check: %+v", n)
	}

	// the entry and the document change together; asking for what is already so writes nothing
	next, changed, err := st.UpdateIPv6(ctx, "nod_v6", func(cur IPv6Row) (IPv6Row, error) { cur.Entry = "2001:db8::5"; return cur, nil })
	if err != nil || !changed || next.Entry != "2001:db8::5" || len(next.State.Checks) != 1 {
		t.Fatalf("turn on: %+v %v %v", next, changed, err)
	}
	if _, changed, err = st.UpdateIPv6(ctx, "nod_v6", func(cur IPv6Row) (IPv6Row, error) { cur.Entry = "2001:db8::5"; return cur, nil }); err != nil || changed {
		t.Fatalf("turn on again: changed %v, %v", changed, err)
	}

	// another writer gets in between the read and the write: the function runs again, on what it left
	calls := 0
	rival := `{"checks":[{"addr":"2001:db8::9","at":5,"pass":true}]}`
	next, changed, err = st.UpdateIPv6(ctx, "nod_v6", func(cur IPv6Row) (IPv6Row, error) {
		calls++
		if calls == 1 {
			if _, err := st.W.ExecContext(ctx, `UPDATE node SET ipv6_json = ? WHERE id = 'nod_v6'`, rival); err != nil {
				t.Fatal(err)
			}
		}
		cur.Entry = ""
		return cur, nil
	})
	if err != nil || !changed || calls != 2 || next.Entry != "" || len(next.State.Checks) != 1 || next.State.Checks[0].Addr != "2001:db8::9" {
		t.Fatalf("a lost race: calls %d, %+v %v %v", calls, next, changed, err)
	}
	n, _ = st.Node(ctx, "nod_v6")
	if n.IPv6Entry != "" || n.IPv6JSON != rival {
		t.Fatalf("stored: entry %q json %s", n.IPv6Entry, n.IPv6JSON)
	}

	// a writer that never stops winning gets ErrConflict; a function's own error writes nothing
	if _, _, err = st.UpdateIPv6(ctx, "nod_v6", func(cur IPv6Row) (IPv6Row, error) {
		if _, err := st.W.ExecContext(ctx, `UPDATE node SET ipv6_entry = ? WHERE id = 'nod_v6'`, "2001:db8::"+strconv.Itoa(calls)); err != nil {
			t.Fatal(err)
		}
		calls++
		cur.Entry = "2001:db8::77"
		return cur, nil
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a rival that always wins: %v", err)
	}
	boom := errors.New("boom")
	if _, _, err = st.UpdateIPv6(ctx, "nod_v6", func(cur IPv6Row) (IPv6Row, error) { cur.Entry = "2001:db8::88"; return cur, boom }); !errors.Is(err, boom) {
		t.Fatalf("function error: %v", err)
	}
	if n, _ = st.Node(ctx, "nod_v6"); n.IPv6Entry == "2001:db8::88" || n.IPv6Entry == "2001:db8::77" {
		t.Fatalf("a refused change was written: %q", n.IPv6Entry)
	}

	if _, err := st.W.ExecContext(ctx, `UPDATE node SET state = 'retired' WHERE id = 'nod_v6'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.UpdateIPv6(ctx, "nod_v6", func(cur IPv6Row) (IPv6Row, error) { cur.Entry = "2001:db8::5"; return cur, nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("retired node: %v", err)
	}
	if _, _, err = st.UpdateIPv6(ctx, "nod_missing", func(cur IPv6Row) (IPv6Row, error) { return cur, nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing node: %v", err)
	}
}
