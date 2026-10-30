//go:build !js

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestIPv6DoctorsReadsOnlyTheIPv6Check(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"nod_a", "nod_b"} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, 'x.example.com', 'active', 1)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	rows := []DoctorRow{
		{NodeID: "nod_a", CheckID: "ipv6", Status: 1, Params: map[string]string{"ipv6": "yes", "connect": "ok", "src": "2001:db8::5"}},
		{NodeID: "nod_a", CheckID: "disk", Status: 1, Params: map[string]string{"ipv6": "no"}},
	}
	if err := st.PutDoctor(ctx, "nod_a", rows, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := st.IPv6Doctors(ctx)
	if err != nil || len(got) != 1 || got["nod_a"].State != IPv6OK || got["nod_a"].Src != "2001:db8::5" {
		t.Fatalf("IPv6Doctors = %+v, %v", got, err)
	}
	if d, err := st.IPv6Doctor(ctx, "nod_b"); err != nil || d.State != IPv6Unknown {
		t.Errorf("a node without a report: %+v, %v", d, err)
	}
	if d, err := st.IPv6Doctor(ctx, "nod_a"); err != nil || d.State != IPv6OK {
		t.Errorf("IPv6Doctor = %+v, %v", d, err)
	}
}
