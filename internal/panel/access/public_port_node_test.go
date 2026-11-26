package access

import (
	"net/url"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

func TestPublicPortBelongsToOneInbound(t *testing.T) {
	f := newRelayFix(t)
	must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: f.server.Id, Enabled: new(false)}))) // leave public TCP 443 free
	p := f.vlessWith("shared", `{"port":19443,"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
	a := f.inbound(p.Id, "nod_de1")
	b := f.inbound(p.Id, "nod_fi1")
	before := f.specOf(a.Id)
	tok := f.tokenOf(f.user("alice", f.group("public", p.Id), nil).User.Id)
	port := uint32(443)
	update := func(request *adminv1.UpdateInboundRequest) *adminv1.Inbound {
		return must(f.s.UpdateInbound(f.ctx, req(request))).Msg.Inbound
	}
	update(&adminv1.UpdateInboundRequest{InboundId: a.Id, PublicPort: &port, ValidateOnly: true})
	assertPorts := func(want string) {
		t.Helper()
		got := map[string]string{}
		for _, srv := range f.twinView(tok, plugin.FormatURIList).Servers {
			u, err := url.Parse(srv.URI)
			if err != nil {
				t.Fatal(err)
			}
			got[srv.InboundID] = u.Port()
		}
		if got[a.Id] != want || got[b.Id] != "19443" {
			t.Fatalf("subscription ports %v; expected %s and 19443", got, want)
		}
	}
	assertPorts("19443")
	f.resetNotify()
	out := update(&adminv1.UpdateInboundRequest{InboundId: a.Id, PublicPort: &port})
	if out.Port != 19443 || out.PublicPort != 443 || f.notify.n.Load() != 1 {
		t.Fatalf("bad inbound or missing refresh: %v", out)
	}
	update(&adminv1.UpdateInboundRequest{InboundId: a.Id, Enabled: new(true)})
	assertPorts("443")
	if after := f.specOf(a.Id); before.Version != after.Version || statehash.Spec(before) != statehash.Spec(after) {
		t.Fatal("public edit changed the listener")
	}
	port = 0
	update(&adminv1.UpdateInboundRequest{InboundId: a.Id, PublicPort: &port})
	assertPorts("19443")
}

func TestRelayUsesPublicExitPort(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		exitPublic, entryListen, entryPublic uint32
		reuse                                bool
	}{
		{"public port free despite listener collision", 443, 19443, 0, true},
		{"public port occupied by listener", 443, 443, 0, false},
		{"public port reserved by another forward", 443, 20443, 443, false},
		{"public port not allowed for relay listener", 81, 20443, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRelayFix(t)
			must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: f.server.Id, Enabled: new(false)}))) // leave public TCP 443 free
			p := f.vlessWith("shared", `{"port":19443,"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
			create := func(node string, listen, public uint32) *adminv1.Inbound {
				t.Helper()
				return must(f.s.CreateInbound(f.ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: node, PortOverride: listen, PublicPort: public}))).Msg.Inbound
			}
			exit := create("nod_de1", 0, tc.exitPublic)
			create("nod_fi1", tc.entryListen, tc.entryPublic)
			candidates := must(f.s.ListRelayCandidates(f.ctx, req(&adminv1.ListRelayCandidatesRequest{ViaNodeId: "nod_fi1"}))).Msg.Candidates
			var shown uint32
			for _, c := range candidates {
				if c.InboundId == exit.Id {
					shown = c.Port
				}
			}
			if shown != tc.exitPublic {
				t.Errorf("wizard exit port = %d, want %d", shown, tc.exitPublic)
			}
			r, err := f.create(exit.Id, "nod_fi1", 0)
			if err != nil {
				t.Fatal(err)
			}
			if r.ExitPort != tc.exitPublic || !relayPortOK(r.Port) || r.Port == tc.entryListen || r.Port == tc.entryPublic || (r.Port == tc.exitPublic) != tc.reuse {
				t.Errorf("relay entry=%d exit=%d, want usable entry (reuse=%v) and exit=%d", r.Port, r.ExitPort, tc.reuse, tc.exitPublic)
			}
		})
	}
}

func TestForwardConflictsWithSiteInEitherOrder(t *testing.T) {
	f := newRelayFix(t)
	p := f.vlessWith("forward", `{"port":19443,"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
	id := f.inbound(p.Id, "nod_de1").Id
	update := func(id string, port *uint32, enabled *bool, dry bool) error {
		_, err := f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: id, PublicPort: port, Enabled: enabled, ValidateOnly: dry}))
		return err
	}
	port := uint32(443)
	for _, dry := range []bool{true, false} {
		wantMsg(t, update(id, &port, nil, dry), "already_exists: public_port_taken: node=de1&port=443&profile=files")
	}
	if err := update(f.server.Id, nil, new(false), false); err != nil {
		t.Fatal(err)
	}
	if err := update(id, &port, nil, false); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		wantMsg(t, update(f.server.Id, nil, new(true), dry), "already_exists: public_port_taken: node=de1&port=443&profile=forward")
	}
	if err := update(id, new(uint32), nil, false); err != nil {
		t.Fatal(err)
	}
	if err := update(f.server.Id, nil, new(true), false); err != nil {
		t.Fatal(err)
	}
}

func TestForwardConflictsWithRelayOnCreateAndMove(t *testing.T) {
	f := newRelayFix(t)
	p := f.vlessWith("forward", `{"port":19443,"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
	exit := f.inbound(p.Id, "nod_nl1")
	entry := f.inbound(p.Id, "nod_fi1")
	r := must(f.create(exit.Id, "nod_fi1", 8443))
	port := uint32(8443)
	_, err := f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: entry.Id, PublicPort: &port}))
	if err == nil || !strings.Contains(err.Error(), "public_port_taken") {
		t.Fatalf("forward onto relay port: %v", err)
	}
	port = 9443
	must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: entry.Id, PublicPort: &port})))
	_, err = f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Port: &port})
	wantMsg(t, err, "already_exists: public_port_taken: node=fi1&port=9443&profile=forward")
	second := f.inbound(p.Id, "nod_de1")
	_, err = f.create(second.Id, "nod_fi1", port)
	wantMsg(t, err, "already_exists: public_port_taken: node=fi1&port=9443&profile=forward")
}
