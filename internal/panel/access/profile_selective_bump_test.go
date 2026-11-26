package access

import (
	"strings"
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

func TestProfileClientFingerprintDoesNotRestartInbound(t *testing.T) {
	f := newRelayFix(t)
	p := f.vlessProfile("fingerprint")
	in := f.inbound(p.Id, "nod_de1")
	before := f.specOf(in.Id)
	settings := `{"reality":{"fingerprint":"firefox"}}`
	change := &adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings, DryRun: true}
	for _, dry := range []bool{true, false} {
		change.DryRun = dry
		if got := must(f.s.UpdateProfile(f.ctx, req(change))).Msg.Impact.InboundsRestarted; got != 0 {
			t.Fatalf("dry=%v: restarted %d", dry, got)
		}
	}
	after := f.specOf(in.Id)
	if after.Version != before.Version || statehash.Spec(after) != statehash.Spec(before) {
		t.Fatal("client fingerprint changed server version or configuration")
	}
	if got := must(f.st.Access().Profile(f.ctx, p.Id)); got.Version != p.Version+1 || !strings.Contains(got.SettingsJSON, "firefox") {
		t.Fatalf("client setting was not saved: version %d", got.Version)
	}
}

func TestProfileEditIgnoresDisabledAndRetiredInbounds(t *testing.T) {
	f := newRelayFix(t)
	p := f.vlessProfile("mixed")
	active := f.inbound(p.Id, "nod_de1")
	disabled := f.inbound(p.Id, "nod_fi1")
	retired := f.inbound(p.Id, "nod_nl1")
	must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: disabled.Id, Enabled: new(false)})))
	f.sql(`UPDATE node SET state='retired' WHERE id='nod_nl1'`)
	version := func(id string) uint64 { return must(f.st.Access().Inbound(f.ctx, id)).SpecVersion }
	a, b, c := version(active.Id), version(disabled.Id), version(retired.Id)
	settings := `{"port":24443}`
	change := &adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings}
	for _, dry := range []bool{true, false} {
		change.DryRun = dry
		if got := must(f.s.UpdateProfile(f.ctx, req(change))).Msg.Impact.InboundsRestarted; got != 1 {
			t.Fatalf("dry=%v: restarted %d", dry, got)
		}
	}
	if version(active.Id) != a+1 || version(disabled.Id) != b || version(retired.Id) != c {
		t.Fatal("only the active inbound should have changed")
	}
}
