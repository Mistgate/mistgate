package health

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// setLegacyRelayMonitor stores a passing monitor under the key the previous release computed: it carried the profile revision
// and was built from the exit's own listen port.
func setLegacyRelayMonitor(t *testing.T, c *countryEnv, id string) {
	t.Helper()
	c.s.invalidateSnapshot()
	sn := c.mustSnapshot()
	rt := sn.relayTargets[relayTargetID(id)]
	if rt == nil || rt.relay == nil {
		t.Fatalf("missing relay target for %s", id)
	}
	exit := sn.targets[rt.in.ID]
	link := rt.relay.link
	legacy := strings.Join([]string{link.ID, link.ViaNodeID, rt.relay.entry.Address, rt.node.Address, rt.in.ID, rt.in.ProfileID, rt.in.Protocol,
		strconv.FormatUint(uint64(rt.in.ProfileVersion), 10), strconv.FormatUint(rt.in.SpecVersion, 10), rt.in.TLSNameOverride, rt.in.RealityTargetOverride,
		link.Network, strconv.Itoa(int(link.Port)), strconv.Itoa(int(exit.spec.Listen.Port))}, "\x00")
	sum := sha256.Sum256([]byte(legacy))
	key := hex.EncodeToString(sum[:])
	if key == rt.relay.key {
		t.Fatal("the legacy key must differ from the current one")
	}
	if err := c.st.SetRelayMonitorCheck(c.ctx, id, store.RelayMonitorCheck{TargetKey: key, AtUnix: c.clock.Now().Unix(), OK: true}); err != nil {
		t.Fatal(err)
	}
	c.s.invalidateSnapshot()
}

func TestCountryBlockedStaysInfoWithLegacyKeyedRelayMonitors(t *testing.T) {
	c := newCountryEnv(t)
	f := newCountryRelayFixture(t, c)
	addHealthyCountryRelay(t, c, "rly_vless", f.vless, "tcp", 8443)
	addHealthyCountryRelay(t, c, "rly_hy", f.hysteria2, "udp", 8443)
	var transitions []Transition
	c.s.cfg.OnTransition = func(tr Transition) { transitions = append(transitions, tr) }
	opened := openCountryRelayAlert(t, c)
	if opened.Severity != sevInfo || len(transitions) != 1 {
		t.Fatalf("setup: alert = %+v, transitions = %+v", opened, transitions)
	}

	// the panel is upgraded: both monitors were written by the previous release
	setLegacyRelayMonitor(t, c, "rly_vless")
	setLegacyRelayMonitor(t, c, "rly_hy")
	c.evaluate()

	a := c.active()["country_blocked/de1/"]
	if a.ID != opened.ID || a.Severity != sevInfo || a.WhyKey != "health.alert.country_blocked.why.reached_through_relays" {
		t.Fatalf("alert after the upgrade = %+v", a)
	}
	if len(transitions) != 1 {
		t.Fatalf("the upgrade announced a change: %+v", transitions)
	}
}
