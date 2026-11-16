package access

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// A TrustTunnel server relayed in the bridge's mode: the bridge carries TCP only, so the twin the app gets through the entry
// is the h2 path even though the server also speaks HTTP/3 (UDP). The direct path keeps its "auto".
func TestTrustTunnelRelayInReverseModeGivesTheAppAnH2Path(t *testing.T) {
	f := newTwinFix(t)
	registerTrustTunnelForTest(t, f.env)
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1' WHERE id = 'nod_fi1'`)
	f.sql(`UPDATE node SET agent_caps = 'relay-reverse/1 tcpcheck/1' WHERE id = 'nod_de1'`)
	tt := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "trusttunnel", Name: "tt", SettingsJson: `{"port":8444,"h3":true,"tls_mode":"acme_domain","egress":"direct"}`,
	}))).Msg.Profile
	inbound := f.inbound(tt.Id, "nod_de1")
	relay := must(f.create(inbound.Id, "nod_fi1", 8443))
	if relay.Network != "tcp" {
		t.Fatalf("the relay of a TrustTunnel server is %q, want tcp", relay.Network)
	}
	f.switchOn(relay.Id)
	if err := f.st.SetRelayActiveMode(f.ctx, relay.Id, store.RelayModeReverse, "leg_failed", f.clock); err != nil {
		t.Fatal(err)
	}

	group := f.group("g", tt.Id)
	r := f.userBare("alice", group, nil)
	token := f.tokenOf(r.User.Id)
	key := sha256.Sum256([]byte("a key of the app"))
	if _, err := f.s.EnrolLinkDevice(f.ctx, r.User.Id, token, tablet(), key[:]); err != nil {
		t.Fatal(err)
	}
	dev, _, err := f.s.ClientDeviceByKey(f.ctx, key[:])
	if err != nil {
		t.Fatal(err)
	}
	v := must(f.s.ClientSubscription(f.ctx, dev, tablet(), []string{"v=1", "trusttunnel=1"}))
	transports := map[string]string{}
	for _, srv := range v.Servers {
		var path struct {
			Server    string `json:"server"`
			Port      uint16 `json:"port"`
			Transport string `json:"transport"`
		}
		if err := json.Unmarshal([]byte(srv.URI), &path); err != nil {
			t.Fatalf("path %q: %v (%s)", srv.PathKey, err, srv.URI)
		}
		transports[srv.PathKey] = path.Transport
		if srv.PathKey == "relay:nod_fi1" && (path.Server != "fi1.example.com" || path.Port != 8443) {
			t.Errorf("relay path = %+v, want the entry's address and port", path)
		}
	}
	if len(transports) != 2 || transports["direct"] != "auto" || transports["relay:nod_fi1"] != "h2" {
		t.Fatalf("transports by path = %v, want direct auto and the relay through the bridge h2", transports)
	}
}
