package access

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestControllerRejectsOldTargetEvidence(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "same_target"
		if stale {
			name = "different_target"
		}
		t.Run(name, func(t *testing.T) {
			x := newReverseControlFix(t, store.RelayModeDNAT)
			x.check(x.stage("fail", 0), x.stage("ok", 0), x.stage("fail", 0), store.RelayStage{})
			x.bridge(true)
			x.monitor(false, 2)
			l := x.relayOf(x.id)
			c, _ := l.Check()
			if stale {
				row := must(x.st.FleetInbound(x.ctx, l.InboundID))
				profile := must(x.st.Access().Profile(x.ctx, row.ProfileID))
				settings := `{"port":19443,"client_port":8443}`
				must(x.s.UpdateProfile(x.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: profile.ID, ExpectedVersion: profile.Version, SettingsJson: &settings})))
			}
			current := x.s.relayTo(x.ctx, l)
			if stale && c.To == current {
				t.Fatal("normal profile edit did not change target")
			}
			// Keep the check the real save left in storage; do not inject a mismatched result.
			saved, _ := x.relayOf(x.id).Check()
			if saved.To != c.To {
				t.Fatalf("profile save changed the check's target: %s -> %s", c.To, saved.To)
			}
			x.s.afterResponse = func(func()) {} // inspect the persisted decision before the asynchronous path proof
			x.s.ControlRelay(x.ctx, x.id)
			got := x.relayOf(x.id)
			t.Logf("current=%s checked=%s active=%s reason=%s", current, c.To, got.ActiveMode, got.ModeReason)
			want := store.RelayModeReverse
			if stale {
				want = store.RelayModeDNAT
			}
			if got.ActiveMode != want {
				t.Errorf("controller used evidence for a different target: got %s want %s", got.ActiveMode, want)
			}
		})
	}
}
func TestPublicPortSaveStillNotifies(t *testing.T) {
	f := newRelayFix(t)
	p := f.vlessWith("public", `{"port":19443,"client_port":443,"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
	f.inbound(p.Id, "nod_de1")
	f.resetNotify()
	settings := `{"client_port":8443}`
	must(f.s.UpdateProfile(f.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings})))
	if f.notify.n.Load() != 1 {
		t.Fatalf("desired-state notifications=%d want 1", f.notify.n.Load())
	}
}
