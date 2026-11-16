package access

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (e *env) removeFrom(profileID string, delete bool, name string, nodeIDs ...string) (*adminv1.RemoveProfileFromNodesResponse, error) {
	r, err := e.s.RemoveProfileFromNodes(e.ctx, req(&adminv1.RemoveProfileFromNodesRequest{ProfileId: profileID, NodeIds: nodeIDs, DeleteProfile: delete, ConfirmName: name}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (e *env) audited(action string) []store.AuditRow {
	e.t.Helper()
	return must(e.st.ListAuditQuery(e.ctx, store.AuditQuery{Actions: []string{action}, Limit: 50}))
}

func (e *env) nodeIDsOf(profileID string) []string {
	e.t.Helper()
	var out []string
	for _, in := range must(e.st.Access().InboundsOfProfile(e.ctx, profileID)) {
		out = append(out, in.NodeID)
	}
	return out
}

// Taking a profile off some nodes keeps it and its groups; taking it off all of them with delete_profile deletes it in the
// same step. A node that is not connected is no obstacle: the desired state is read from the panel, so it drops the profile
// when it connects again.
func TestRemoveProfileFromNodesAndDelete(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"de1", "nl1", "fi1"} {
		e.node("nod_"+n, n, n+".example.com", "active") // none of them is online: e.online is empty
	}
	p := e.profile("main", "")
	g := e.group("friends", p.Id)
	for _, n := range []string{"de1", "nl1", "fi1"} {
		e.inbound(p.Id, "nod_"+n)
	}
	e.resetNotify()

	// Some of the nodes: the profile stays, the others keep it, the group still has it.
	r, err := e.removeFrom(p.Id, false, "", "nod_de1", "nod_nl1")
	if err != nil || r.Removed != 2 || r.ProfileDeleted {
		t.Fatalf("partial: %+v %v", r, err)
	}
	if got := e.nodeIDsOf(p.Id); len(got) != 1 || got[0] != "nod_fi1" {
		t.Fatalf("left on %v", got)
	}
	if e.notify.n.Load() == 0 {
		t.Error("the nodes were not told")
	}
	if ins := must(e.s.Desired(e.ctx, "nod_de1")); len(ins) != 0 {
		t.Errorf("the offline node still wants %d inbounds when it connects", len(ins))
	}
	var tomb int
	e.st.R.QueryRow(`SELECT count(*) FROM node_inbound_stats_tombstone`).Scan(&tomb)
	if tomb != 2 {
		t.Errorf("tombstones = %d", tomb)
	}
	rows := e.audited("inbounds_remove")
	if len(rows) != 1 || !strings.Contains(rows[0].Params, `"count":2`) || !strings.Contains(rows[0].Params, `"nodes":"de1, nl1"`) {
		t.Fatalf("audit: %+v", rows)
	}
	if gs := must(e.s.ListGroups(e.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups; len(gs) != 1 || gs[0].Id != g || len(gs[0].ProfileIds) != 1 {
		t.Fatalf("groups: %+v", gs)
	}

	// A repeated call: nodes that no longer carry it are skipped, nothing is refused.
	if r, err = e.removeFrom(p.Id, false, "", "nod_de1", "nod_nl1"); err != nil || r.Removed != 0 {
		t.Fatalf("repeat: %+v %v", r, err)
	}

	// Deleting needs the name typed, and every node the profile is on in the list: else nothing changes.
	_, err = e.removeFrom(p.Id, true, "mian", "nod_fi1")
	wantCode(t, err, connect.CodeInvalidArgument)
	e.node("nod_se1", "se1", "se1.example.com", "active")
	e.inbound(p.Id, "nod_se1")
	_, err = e.removeFrom(p.Id, true, "main", "nod_fi1")
	wantMsg(t, err, "failed_precondition: profile_deployed: nodes=se1")
	if got := e.nodeIDsOf(p.Id); len(got) != 2 {
		t.Fatalf("a refused delete removed inbounds: %v", got)
	}
	_, err = e.removeFrom(p.Id, false, "")
	wantCode(t, err, connect.CodeInvalidArgument)

	// Everything at once: the profile goes with its inbounds, and the group loses it.
	if r, err = e.removeFrom(p.Id, true, "main", "nod_fi1", "nod_se1"); err != nil || r.Removed != 2 || !r.ProfileDeleted {
		t.Fatalf("delete: %+v %v", r, err)
	}
	_, err = e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: p.Id}))
	wantCode(t, err, connect.CodeNotFound)
	if gs := must(e.s.ListGroups(e.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups; len(gs) != 1 || len(gs[0].ProfileIds) != 0 {
		t.Fatalf("groups: %+v", gs)
	}
	if rows = e.audited("profile_delete"); len(rows) != 1 || !strings.Contains(rows[0].Params, `"nodes":2`) {
		t.Fatalf("audit: %+v", rows)
	}

	// And once more: the profile is gone.
	_, err = e.removeFrom(p.Id, true, "main", "nod_fi1")
	wantCode(t, err, connect.CodeNotFound)
}

// A retired node keeps its inbound rows; they go like the others, and the relays through them are named in the audit log.
func TestRemoveProfileFromNodesTakesRelaysAndRetiredNodes(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.invalid", "active")
	e.node("nod_msk1", "msk1", "msk1.example.invalid", "active")
	e.node("nod_old", "old1", "old1.example.invalid", "active")
	e.sql(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id = 'nod_msk1'`)
	p := e.profile("main", "")
	in := e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_old")
	must(e.s.CreateRelay(e.ctx, req(&adminv1.CreateRelayRequest{InboundId: in.Id, ViaNodeId: "nod_msk1"})))
	e.sql(`UPDATE node SET state = 'retired' WHERE id = 'nod_old'`)

	r, err := e.removeFrom(p.Id, true, "main", "nod_de1", "nod_old")
	if err != nil || r.Removed != 2 || r.RelaysRemoved != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if got := must(e.s.ListRelays(e.ctx, req(&adminv1.ListRelaysRequest{}))).Msg.Relays; len(got) != 0 {
		t.Fatalf("relays left: %+v", got)
	}
	if rows := e.audited("relay.delete"); len(rows) != 1 || !strings.Contains(rows[0].Params, `"reason":"inbound_removed"`) {
		t.Fatalf("audit: %+v", rows)
	}
}

// The inbounds of an AmneziaWG profile keep their server key for the pair when only the node list shrinks (adding it back
// brings the same key); with the profile deleted the key goes with it.
func TestRemoveProfileFromNodesRetainsKeyOnlyWhenProfileStays(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	p := e.awgProfile("awg main", "")
	e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_nl1")
	if _, err := e.st.Access().RetainedKey(e.ctx, p.Id, "nod_de1"); err == nil {
		t.Fatal("a key is retained before anything was removed")
	}
	if _, err := e.removeFrom(p.Id, false, "", "nod_de1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Access().RetainedKey(e.ctx, p.Id, "nod_de1"); err != nil {
		t.Fatalf("the server key was not kept: %v", err)
	}
	if _, err := e.removeFrom(p.Id, true, "awg main", "nod_nl1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Access().RetainedKey(e.ctx, p.Id, "nod_de1"); err != store.ErrNotFound {
		t.Fatalf("a key outlived its profile: %v", err)
	}
}
