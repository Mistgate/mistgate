package access

import (
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The AWG server key belongs to (profile, node), not to the inbound: removing the profile from a node and adding it
// back keeps it, so the configs users already imported go on working.

func serverPubOfConf(t *testing.T, conf string) string {
	t.Helper()
	for _, l := range strings.Split(conf, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "PublicKey = "); ok {
			return v
		}
	}
	t.Fatalf("no peer PublicKey in:\n%s", conf)
	return ""
}

func (f *awgFixture) deviceConfs(deviceID string) []*adminv1.DeviceConfig {
	f.e.t.Helper()
	return must(f.e.s.GetDeviceConfigs(f.e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: deviceID}))).Msg.Configs
}

func (f *awgFixture) dropInbound(id string) {
	f.e.t.Helper()
	must(f.e.s.DeleteInbound(f.e.ctx, req(&adminv1.DeleteInboundRequest{InboundId: id})))
}

func TestAWGReAddKeepsServerKey(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	d := f.add(f.user, "phone").Device
	before := f.deviceConfs(d.Id)
	if len(before) != 1 {
		t.Fatalf("configs = %d", len(before))
	}
	pubBefore := serverPubOfConf(t, before[0].Conf)
	var staleBefore int64
	if err := e.st.R.QueryRow(`SELECT critical_epoch FROM profile WHERE id = ?`, f.profile).Scan(&staleBefore); err != nil {
		t.Fatal(err)
	}

	f.dropInbound(f.inbound)
	if n := e.count(`SELECT count(*) FROM awg_retained_key WHERE profile_id = ? AND node_id = ?`, f.profile, f.nodeID); n != 1 {
		t.Fatalf("retained keys after removing the inbound = %d", n)
	}
	// The parked blob is bound to the pair: it does not open under the gone inbound's id.
	var enc []byte
	if err := e.st.R.QueryRow(`SELECT state_enc FROM awg_retained_key`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.vault.Open(enc, f.inbound); err == nil {
		t.Error("the retained key opens under the old inbound id")
	}

	in := e.inbound(f.profile, f.nodeID)
	if in.Id == f.inbound {
		t.Fatal("same inbound id")
	}
	if n := e.count(`SELECT count(*) FROM awg_retained_key`); n != 0 {
		t.Errorf("retained keys after re-adding = %d", n)
	}
	after := f.deviceConfs(d.Id)
	if len(after) != 1 || serverPubOfConf(t, after[0].Conf) != pubBefore {
		t.Fatalf("the server public key changed after remove + re-add")
	}
	if got := f.user_(f.user).Devices[0]; got.Stale {
		t.Error("a remove + re-add made the device stale")
	}
	// The node gets the same private key under the new inbound id, and the peer of the device is still there.
	var priv []byte
	if err := e.st.R.QueryRow(`SELECT plugin_state_enc FROM inbound WHERE id = ?`, in.Id).Scan(&priv); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.vault.Open(priv, in.Id); err != nil {
		t.Errorf("the re-added inbound's key does not open under its own id: %v", err)
	}
	if len(f.desired()) != 1 {
		t.Errorf("desired creds = %d", len(f.desired()))
	}

	// A second round trip keeps it as well, and a new device sees the same server key.
	f.dropInbound(in.Id)
	e.inbound(f.profile, f.nodeID)
	if c := f.deviceConfs(f.add(f.user, "tablet").Device.Id); serverPubOfConf(t, c[0].Conf) != pubBefore {
		t.Error("the key changed on the second round trip")
	}
}

func TestAWGReAddOnAnotherNodeGetsNewKey(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	d := f.add(f.user, "phone").Device
	pubA := serverPubOfConf(t, f.deviceConfs(d.Id)[0].Conf)
	f.dropInbound(f.inbound)

	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	e.inbound(f.profile, "nod_nl1")
	cfgs := f.deviceConfs(d.Id)
	if len(cfgs) != 1 || serverPubOfConf(t, cfgs[0].Conf) == pubA {
		t.Fatal("another node reused the retained key of the first")
	}
	if n := e.count(`SELECT count(*) FROM awg_retained_key WHERE node_id = ?`, f.nodeID); n != 1 {
		t.Errorf("the first node's retained key = %d (another node's add must leave it)", n)
	}
	// Back on the first node: its own key returns.
	e.inbound(f.profile, f.nodeID)
	for _, c := range f.deviceConfs(d.Id) {
		if c.NodeName == "de1" && serverPubOfConf(t, c.Conf) != pubA {
			t.Error("the first node did not get its key back")
		}
	}
}

func TestAWGRetainedKeyDroppedWithProfileOrNode(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	// the profile: delete its inbound, then it
	f.dropInbound(f.inbound)
	must(e.s.DeleteProfile(e.ctx, req(&adminv1.DeleteProfileRequest{ProfileId: f.profile})))
	if n := e.count(`SELECT count(*) FROM awg_retained_key`); n != 0 {
		t.Errorf("retained keys after deleting the profile = %d", n)
	}
	// the node: retiring it
	p2 := e.awgProfile("awg-two", "")
	in := e.inbound(p2.Id, f.nodeID)
	f.dropInbound(in.Id)
	if n := e.count(`SELECT count(*) FROM awg_retained_key`); n != 1 {
		t.Fatalf("retained keys = %d", n)
	}
	if err := e.st.RetireNode(e.ctx, f.nodeID, e.s.now()); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM awg_retained_key`); n != 0 {
		t.Errorf("retained keys after retiring the node = %d", n)
	}
	// hysteria2 parks nothing
	e.node("nod_x", "x", "x.example.com", "active")
	hy := e.profile("hy", "")
	hin := e.inbound(hy.Id, "nod_x")
	f.dropInbound(hin.Id)
	if n := e.count(`SELECT count(*) FROM awg_retained_key`); n != 0 {
		t.Errorf("a hysteria2 inbound parked a key: %d", n)
	}
}
