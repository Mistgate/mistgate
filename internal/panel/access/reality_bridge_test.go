package access

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The exit verifies the entry of a reverse relay by the name and pin of the entry's own site on 443. A VLESS server there that shows another
// site takes that away: the panel says so before the change is saved (bridge_site_lost), and says nothing where the bridge has a port of
// its own, where no relay may use the bridge, or where the change leaves the node's site as it was.
func TestAnotherSiteOn443WarnsOfTheBridgeOfARelay(t *testing.T) {
	f := newRelayFix(t)
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1 vless-target/1' WHERE id = 'nod_fi1'`)
	self := f.inbound(f.vlessSelfProfile("self site").Id, "nod_fi1")
	check := func(target string) []*adminv1.StatusReason {
		t.Helper()
		r, err := f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: self.Id, RealityTargetOverride: &target, ValidateOnly: true}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.Warnings
	}
	has := func(ws []*adminv1.StatusReason) *adminv1.StatusReason {
		for _, w := range ws {
			if w.Code == "bridge_site_lost" {
				return w
			}
		}
		return nil
	}

	// no relay through the node: nothing rides its site
	if w := has(check("shop.example.com:443")); w != nil {
		t.Fatalf("no relay: %+v", w)
	}
	if err := f.st.CreateRelay(f.ctx, store.Relay{ID: "rly_x", InboundID: f.server.Id, ViaNodeID: "nod_fi1", Network: "tcp", Port: 4443}, f.clock); err != nil {
		t.Fatal(err)
	}
	w := has(check("shop.example.com:443"))
	if w == nil || w.Params["node"] != "fi1" || w.Params["port"] != "443" {
		t.Fatalf("with a relay through the node: %+v", w)
	}
	// the site the node had stays: nothing is lost
	if w := has(check("")); w != nil {
		t.Fatalf("own site kept: %+v", w)
	}
	// a bridge of its own port does not need the site
	f.sql(`UPDATE node SET bridge_port = 8443 WHERE id = 'nod_fi1'`)
	if w := has(check("shop.example.com:443")); w != nil {
		t.Fatalf("separate bridge port: %+v", w)
	}
	// a relay that only forwards (DNAT) does not use the bridge
	f.sql(`UPDATE node SET bridge_port = 0 WHERE id = 'nod_fi1'`)
	f.sql(`UPDATE relay SET mode = 'dnat' WHERE id = 'rly_x'`)
	if w := has(check("shop.example.com:443")); w != nil {
		t.Fatalf("a DNAT relay: %+v", w)
	}
	// the add-profile check says the same for a profile that shows another site on the node
	f.sql(`UPDATE relay SET mode = 'auto' WHERE id = 'rly_x'`)
	f.sql(`DELETE FROM inbound WHERE id = ?`, self.Id)
	checked, err := f.check(&adminv1.CreateInboundRequest{ProfileId: f.vlessProfile("other site").Id, NodeId: "nod_fi1"})
	if err != nil || has(checked.Warnings) == nil {
		t.Fatalf("adding a profile with another site: %+v, %v", checked, err)
	}
}
