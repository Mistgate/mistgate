package access

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Putting a profile on a node and taking it off are events of that node, so its event list says what happened and why a
// profile started (or went away) without the admin having to remember.
func TestInboundChangesAreNodeEvents(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.profile("p1", "")
	in := e.inbound(p.Id, "nod_de1")
	events := func() []store.EventRow {
		rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: "nod_de1", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	rows := events()
	if len(rows) != 1 {
		t.Fatalf("events: %+v", rows)
	}
	// the list joins the profile of the live inbound
	if a := rows[0]; a.Code != "profile_added" || a.Source != "admin" || a.InboundID != in.Id || a.ProfileName != "p1" || a.Protocol != "hysteria2" {
		t.Errorf("added: %+v", a)
	}

	must(e.s.DeleteInbound(e.ctx, req(&adminv1.DeleteInboundRequest{InboundId: in.Id})))
	rows = events()
	if len(rows) != 2 {
		t.Fatalf("events: %+v", rows)
	}
	// the inbound is gone, nothing to join: the event keeps the names it was written with
	if r := rows[0]; r.Code != "profile_removed" || r.Source != "admin" || r.InboundID != in.Id || r.ProfileName != "" ||
		r.Params["profile"] != "p1" || r.Params["protocol"] != "hysteria2" {
		t.Errorf("removed: %+v", r)
	}

	// a refused delete leaves no event
	if _, err := e.s.DeleteInbound(e.ctx, req(&adminv1.DeleteInboundRequest{InboundId: "inb_nope"})); err == nil {
		t.Fatal("deleted a missing inbound")
	}
	if n := len(events()); n != 2 {
		t.Errorf("a refused delete wrote an event: %d rows", n)
	}
}
