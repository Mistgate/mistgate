package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
)

// The desired state of a node built through the real Hysteria2 plugin: profile secrets split the way the
// access module stores them come back merged, credentials carry the plugin's verifier.
func TestDesiredStateWithHysteria2Plugin(t *testing.T) {
	e := newEnvWith(t, builtin.Registry())
	a := e.enroll("de1")
	hy2, ok := e.f.reg.Get("hysteria2")
	if !ok {
		t.Fatal("hysteria2 is not registered")
	}
	settings, err := hy2.DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	ptrs, err := protocols.FlaggedPointers(hy2.SettingsSchema(), "x-secret")
	if err != nil {
		t.Fatal(err)
	}
	pub, secrets, err := protocols.SplitSecrets(settings, ptrs)
	if err != nil || len(secrets) == 0 {
		t.Fatalf("split secrets: %v %v", secrets, err)
	}
	if strings.Contains(string(pub), secrets[ptrs[0]]) {
		t.Fatal("the public settings still hold the secret")
	}
	raw, _ := json.Marshal(secrets)

	now := time.Now().Unix()
	e.exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_1', 'g1', ?)`, now)
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, secrets_enc, version, created_at, updated_at) VALUES ('prf_h', 'hysteria2', 'hy', ?, ?, 1, ?, ?)`,
		string(pub), e.v.Seal(raw, "prf_h"), now, now)
	e.exec(`INSERT INTO user_group_profile (group_id, profile_id) VALUES ('grp_1', 'prf_h')`)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, created_at, updated_at) VALUES ('inb_h', 'prf_h', ?, 1, ?, ?)`, a.nodeID, now, now)
	e.exec(`INSERT INTO user (id, name, group_id, status, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_u', 'u', 'grp_1', 'active', ?, ?, x'00', ?)`, now, sha("u"), now)
	e.exec(`INSERT INTO device (id, user_id, first_seen_at, last_seen_at, created_at) VALUES ('dev_u', 'usr_u', ?, ?, ?)`, now, now, now)
	issued, err := hy2.IssueCredential(protocols.IssueInput{UserID: "usr_u", DeviceID: "dev_u"})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at) VALUES ('crd_u', 'dev_u', 'usr_u', 'hysteria2', x'00', ?, ?)`,
		string(issued.NodeData), now)

	c := a.open()
	c.send(0, hello("inst1", 0, ""))
	ds := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetDesiredState() != nil }).GetDesiredState()
	if len(ds.Inbounds) != 1 {
		t.Fatalf("desired state %v", ds)
	}
	in := ds.Inbounds[0]
	if in.InboundId != "inb_h" || in.Spec.Protocol != "hysteria2" || in.Spec.Listen.Network != "udp" || in.Spec.Listen.Port == 0 || in.Spec.Tls.ServerName != "de1.example.com" {
		t.Errorf("spec %v", in.Spec)
	}
	for _, s := range secrets {
		if !strings.Contains(in.Spec.SettingsJson, s) {
			t.Errorf("secret %q is missing from the node settings %s", s, in.Spec.SettingsJson)
		}
	}
	if len(in.Creds) != 1 || in.Creds[0].CredId != "crd_u" || in.Creds[0].DataJson != string(issued.NodeData) {
		t.Errorf("credentials %v", in.Creds)
	}
	m := newModel()
	m.apply(ds)
	if m.hash() != ds.StateHash {
		t.Error("state hash of the real-plugin state")
	}
}
