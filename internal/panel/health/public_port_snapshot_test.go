package health

import (
	"testing"
	"time"

	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/statehash"
)

func TestSnapshotRefreshWithoutSpecBump(t *testing.T) {
	c := newCountryEnv(t)
	c.node("de1", "provider-a", true)
	c.node("fi1", "provider-b", true)
	c.vlessInboundWith("de1", 19443, func(s *panelvless.Settings) {

		s.Reality.TargetKind = "site"
		s.Reality.Target = "www.example.org:443"
		s.Reality.ServerNames = []string{"www.example.org"}
	})
	initial, err := c.s.snapshot(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var inboundID string
	for id := range initial.targets {
		inboundID = id
	}
	before := initial.targets[inboundID]
	if before == nil {
		t.Fatal("missing inbound")
	}
	if err = c.st.CreateRelay(c.ctx, store.Relay{ID: "rly_public", InboundID: inboundID, ViaNodeID: "fi1", Network: "tcp", Port: 30443}, c.clock.Now()); err != nil {
		t.Fatal(err)
	}
	on := true
	if err = c.st.UpdateRelay(c.ctx, "rly_public", store.RelayPatch{Enabled: &on}, c.clock.Now()); err != nil {
		t.Fatal(err)
	}
	c.s.invalidateSnapshot()
	initial, err = c.s.snapshot(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeKey := relayTargetKey(initial.relayTargets[relayTargetID("rly_public")])
	if beforeKey == "" {
		t.Fatal("missing relay target")
	}
	if _, err = c.st.W.ExecContext(c.ctx, "UPDATE inbound SET public_port=8443 WHERE id=?", inboundID); err != nil {
		t.Fatal(err)
	}
	c.clock.Advance(c.s.cfg.SnapshotTTL + time.Second)
	fresh, err := c.s.snapshot(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	after := fresh.targets[inboundID]
	params, ok := vlessProbeParams(Target{Node: after.node, Inbound: after.in, Spec: after.spec, Settings: after.settings})
	if !ok || params.Port != 8443 {
		t.Fatalf("fresh health client port=%d supported=%v", params.Port, ok)
	}
	if statehash.Spec(before.spec) != statehash.Spec(after.spec) {
		t.Fatal("public port update changed node spec")
	}
	if after.in.SpecVersion != before.in.SpecVersion {
		t.Fatal("fixture unexpectedly bumped spec")
	}
	if relayTargetKey(fresh.relayTargets[relayTargetID("rly_public")]) == beforeKey {
		t.Fatal("relay evidence key failed to reflect inbound public port change")
	}
}
