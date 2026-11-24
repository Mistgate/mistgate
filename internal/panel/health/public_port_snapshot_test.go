package health

import (
	"encoding/json"
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
		s.ClientPort = 443
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
	p, err := c.st.Access().Profile(c.ctx, before.in.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err = json.Unmarshal([]byte(p.SettingsJSON), &settings); err != nil {
		t.Fatal(err)
	}
	settings["client_port"] = 8443
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	p.SettingsJSON = string(raw)
	if _, err = c.st.Access().UpdateProfile(c.ctx, p, p.Version, nil, false, c.clock.Now()); err != nil {
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
		t.Fatal("client-only profile update changed node spec")
	}
	if after.in.SpecVersion != before.in.SpecVersion {
		t.Fatal("fixture unexpectedly bumped spec")
	}
	if relayTargetKey(fresh.relayTargets[relayTargetID("rly_public")]) == beforeKey {
		t.Fatal("relay evidence key failed to reflect profile update")
	}
	t.Logf("after TTL: client_port=%d spec_version=%d profile_version=%d -> %d, node spec unchanged, relay key refreshed", params.Port, after.in.SpecVersion, before.in.ProfileVersion, after.in.ProfileVersion)
}
