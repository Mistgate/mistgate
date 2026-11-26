package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func profileSelectiveBumpProof(t *testing.T, st *Store) {
	ctx := context.Background()
	relayFixture(t, st)
	p, err := st.Access().Profile(ctx, "prf_hy")
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "changed profile"
	changes := &ProfileInboundChanges{Versions: map[string]uint64{"inb_de1_hy": 1, "inb_nl1_hy": 1}, Changed: []string{"inb_de1_hy", "inb_de1_hy", "inb_de1_vl"}}
	changed, err := st.Access().UpdateProfile(ctx, p, p.Version, changes, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	version := func(id string) uint64 {
		t.Helper()
		row, err := st.FleetInbound(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return row.SpecVersion
	}
	if version("inb_de1_hy") != 2 || version("inb_nl1_hy") != 1 || version("inb_de1_vl") != 1 {
		t.Fatal("bumped an unlisted inbound or one of another profile")
	}
	_, err = st.Access().UpdateProfile(ctx, p, p.Version, changes, false, time.Now())
	if !errors.Is(err, ErrAccessVersion) || version("inb_nl1_hy") != 1 {
		t.Fatalf("stale update modified inbounds: %v", err)
	}
	if _, err = st.Access().UpdateProfile(ctx, changed, changed.Version, nil, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if version("inb_de1_hy") != 2 {
		t.Fatal("client-only update changed inbound version")
	}
	// A node changes its port override after the panel computed the impacted IDs.
	relayExec(t, st, `UPDATE inbound SET port_override = 8443, spec_version = spec_version + 1 WHERE id = 'inb_nl1_hy'`)
	current, err := st.Access().Profile(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	changes.Versions["inb_de1_hy"] = 2
	assertStale := func() {
		t.Helper()
		_, err := st.Access().UpdateProfile(ctx, current, current.Version, changes, false, time.Now())
		actual, readErr := st.Access().Profile(ctx, p.ID)
		if !errors.Is(err, ErrAccessVersion) || readErr != nil || actual.Version != current.Version || version("inb_de1_hy") != 2 {
			t.Fatalf("stale plan changed the profile or its nodes: %v, read %v, version %d", err, readErr, actual.Version)
		}
	}
	assertStale()
	changes.Versions["inb_nl1_hy"] = 2
	relayExec(t, st, `UPDATE inbound SET enabled = 0 WHERE id = 'inb_nl1_hy'`)
	assertStale()
	delete(changes.Versions, "inb_nl1_hy")
	relayExec(t, st, `UPDATE inbound SET enabled = 1 WHERE id = 'inb_nl1_hy'`)
	assertStale()
}
