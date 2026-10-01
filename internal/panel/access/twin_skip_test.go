package access

import (
	"slices"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// A node the owner unticked in the twin dialog (no WARP there: the WARP twin would never start) gets no copy; the
// other nodes of the profile do.
func TestTwinSkipsUntickedNodes(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_fi1", "fi1", "fi1.example.com", "active")
	p := e.profile("files", "")
	e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_fi1")
	r, err := e.s.TwinProfile(e.ctx, req(&adminv1.TwinProfileRequest{ProfileId: p.Id, Egress: "warp", SkipNodeIds: []string{"nod_fi1"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Msg.NodeIds, []string{"nod_de1"}) || r.Msg.Profile == nil || r.Msg.Profile.NodeCount != 1 {
		t.Fatalf("twin = %+v", r.Msg)
	}
	var on []string
	for _, in := range must(e.st.Access().InboundsOfProfile(e.ctx, r.Msg.Profile.Id)) {
		on = append(on, in.NodeID)
	}
	if !slices.Equal(on, []string{"nod_de1"}) {
		t.Fatalf("the twin runs on %v", on)
	}
}
