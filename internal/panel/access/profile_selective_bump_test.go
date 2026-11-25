package access

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/statehash"
)

func TestProfilePortUpdateBumpsOnlyChangedInbounds(t *testing.T) {
	f := newRelayFix(t)
	p := f.vlessWith("public", `{"port":19443,"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
	in := f.inbound(p.Id, "nod_de1")
	fixed := f.inbound(p.Id, "nod_fi1")
	must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: fixed.Id, PortOverride: new(uint32(24443))})))
	before, fixedBefore := f.specOf(in.Id), f.specOf(fixed.Id)
	settings := `{"port":20443}`
	dry := must(f.s.UpdateProfile(f.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings, DryRun: true}))).Msg
	if dry.Impact.InboundsRestarted != 1 {
		t.Fatalf("impact: %+v", dry.Impact)
	}
	must(f.s.UpdateProfile(f.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings})))
	after, fixedAfter := f.specOf(in.Id), f.specOf(fixed.Id)
	if after.Version != before.Version+1 || after.Listen.Port != 20443 {
		t.Fatalf("server port not applied: %+v", after)
	}
	if fixedAfter.Version != fixedBefore.Version || statehash.Spec(fixedAfter) != statehash.Spec(fixedBefore) {
		t.Fatal("node override restarted an unaffected inbound")
	}
}

func TestProfileTunnelOnlyEditBumpsInbound(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.awgProfile("tunnel", `{"mtu":1280}`)
	id := e.inbound(p.Id, "nod_de1").Id
	before := e.specOf(id)
	settings := `{"mtu":1281}`
	update := &adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings, DryRun: true}
	if got := must(e.s.UpdateProfile(e.ctx, req(update))).Msg.Impact.InboundsRestarted; got != 1 {
		t.Errorf("preview restarted %d inbounds, want 1", got)
	}
	update.DryRun = false
	must(e.s.UpdateProfile(e.ctx, req(update)))
	after := e.specOf(id)
	if after.Tunnel.MTU != 1281 || after.Version != before.Version+1 {
		t.Errorf("tunnel MTU %d; version %d -> %d", after.Tunnel.MTU, before.Version, after.Version)
	}
}
