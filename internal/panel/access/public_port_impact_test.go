package access

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/statehash"
)

func TestPublicPortUpdateImpact(t *testing.T) {
	f := newRelayFix(t)
	p := f.vlessWith("public", `{"port":19443,"client_port":443,"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
	in := f.inbound(p.Id, "nod_de1")
	u := f.user("alice", f.group("g-public", p.Id), nil).User
	f.online[u.Id] = "nod_de1"
	before := f.specOf(in.Id)
	settings := `{"client_port":8443}`
	dry := must(f.s.UpdateProfile(f.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings, DryRun: true}))).Msg
	must(f.s.UpdateProfile(f.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings})))
	after := f.specOf(in.Id)
	t.Logf("impact=%v; version=%d -> %d; hash_changed=%v; settings_changed=%v", dry.Impact, before.Version, after.Version, statehash.Spec(before) != statehash.Spec(after), string(before.Settings) != string(after.Settings))
	if dry.Impact.InboundsRestarted != 0 || before.Version != after.Version || statehash.Spec(before) != statehash.Spec(after) {
		t.Fatal("client port update restarted the inbound or changed its agent spec")
	}
	if len(dry.Impact.CriticalFields) == 0 || len(dry.Impact.AffectedUserNames) != 1 || dry.Impact.AffectedUserNames[0] != "alice" {
		t.Fatalf("client endpoint change omitted affected user: %+v", dry.Impact)
	}
}

func TestProfilePortUpdateBumpsOnlyChangedInbounds(t *testing.T) {
	f := newRelayFix(t)
	p := f.vlessWith("public", `{"port":19443,"client_port":443,"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
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
