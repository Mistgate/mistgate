package access

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/protocols/trusttunnel"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

func registerTrustTunnelForTest(t *testing.T, e *env) {
	t.Helper()
	plugins := builtin.Registry().List()
	plugins = append(plugins, trusttunnel.New())
	r, err := protocols.NewRegistry(plugins...)
	if err != nil {
		t.Fatal(err)
	}
	e.s.reg = r
}

func TestTrustTunnelCreateInboundDomainErrorAndSNI(t *testing.T) {
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	e.node("nod_ip", "de1", "192.0.2.4", "active")
	p := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "trusttunnel", Name: "tt"}))).Msg.Profile
	_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_ip"}))
	if err == nil || !strings.Contains(err.Error(), "acme_needs_domain") {
		t.Fatalf("CreateInbound without a domain = %v, want acme_needs_domain", err)
	}

	withSNI := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "trusttunnel", Name: "tt with SNI", SettingsJson: `{"port":443,"h3":false,"sni":"de1.example.com","tls_mode":"acme_domain","egress":"direct"}`,
	}))).Msg.Profile
	created := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: withSNI.Id, NodeId: "nod_ip"}))).Msg.Inbound
	if created.TlsServerName != "de1.example.com" {
		t.Fatalf("effective SNI = %q", created.TlsServerName)
	}
}

func TestTrustTunnelSideUDPPortConflictsWithHysteria2(t *testing.T) {
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	hy2 := e.profile("hy2", `{"port":443}`)
	e.inbound(hy2.Id, "nod_de1")
	tt := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "trusttunnel", Name: "tt h3", SettingsJson: `{"port":443,"h3":true,"tls_mode":"acme_domain","egress":"direct"}`,
	}))).Msg.Profile
	_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: tt.Id, NodeId: "nod_de1"}))
	if err == nil || !strings.Contains(err.Error(), "port_taken") || !strings.Contains(err.Error(), "profile=hy2") || strings.Contains(err.Error(), "free=443") {
		t.Fatalf("TrustTunnel UDP side-port conflict = %v, want hy2 conflict and a different free hint", err)
	}
}

// The other order: the server that holds UDP 443 through TrustTunnel's HTTP/3 is there first, Hysteria2 asks for it afterwards.
func TestHysteria2ConflictsWithTrustTunnelSideUDPPort(t *testing.T) {
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	tt := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "trusttunnel", Name: "ttdual", SettingsJson: `{"port":443,"h3":true,"tls_mode":"acme_domain","egress":"direct"}`,
	}))).Msg.Profile
	e.inbound(tt.Id, "nod_de1")
	hy2 := e.profile("hy2", `{"port":443}`)
	_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: hy2.Id, NodeId: "nod_de1"}))
	if err == nil || !strings.Contains(err.Error(), "port_taken") || !strings.Contains(err.Error(), "profile=ttdual") || strings.Contains(err.Error(), "free=443") {
		t.Fatalf("Hysteria2 on the UDP port of TrustTunnel's HTTP/3 = %v, want port_taken naming it and a different free port", err)
	}
}

// Without HTTP/3 TrustTunnel takes TCP only: Hysteria2 keeps UDP 443 beside it.
func TestTrustTunnelWithoutHTTP3LeavesTheUDPPortFree(t *testing.T) {
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	tt := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "trusttunnel", Name: "tt h2", SettingsJson: `{"port":443,"h3":false,"tls_mode":"acme_domain","egress":"direct"}`,
	}))).Msg.Profile
	e.inbound(tt.Id, "nod_de1")
	e.inbound(e.profile("hy2", `{"port":443}`).Id, "nod_de1")
}

func TestTrustTunnelRelayTwinUsesTCPOnlyH2Path(t *testing.T) {
	in := protocols.RenderInput{
		Format: plugin.FormatMistgate, Capabilities: []string{"trusttunnel=1"},
		Settings: json.RawMessage(`{"port":443,"h3":true,"tls_mode":"acme_domain","egress":"direct"}`),
		Inbound:  protocols.InboundView{Node: protocols.NodeView{Address: "192.0.2.10"}, Port: 443, TLSServerName: "de1.example.com", TLSMode: plugin.TLSAcmeDomain},
		Secret:   "ualice2345672345:aB234567cD234567eF234567gH234567",
	}
	spec := plugin.InboundSpec{Protocol: "trusttunnel", Listen: plugin.Listen{Network: "tcp", Port: 443},
		TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "de1.example.com"}, Settings: json.RawMessage(`{"h3":true}`)}
	link := store.RelayLink{Relay: store.Relay{Network: "tcp", Port: 8443}, ViaNodeAddress: "192.0.2.22"}
	twin := twinInput(in, store.AccessInboundFull{Node: store.AccessNode{ID: "nod_de1", Name: "de1", Address: "192.0.2.10"}}, spec, link)
	if !twin.Inbound.TCPOnly || twin.Inbound.Port != 8443 {
		t.Fatalf("relay twin inbound = %+v", twin.Inbound)
	}
	frag, ok := trusttunnel.New().Render(twin)
	if !ok {
		t.Fatal("relay twin did not render")
	}
	var path struct {
		Server    string `json:"server"`
		Transport string `json:"transport"`
		Port      uint16 `json:"port"`
	}
	if err := json.Unmarshal(frag.Data, &path); err != nil {
		t.Fatal(err)
	}
	if path.Server != "192.0.2.22" || path.Port != 8443 || path.Transport != "h2" {
		t.Fatalf("relay path = %+v, JSON %s", path, frag.Data)
	}
}

func TestTrustTunnelAppearsAsExperimentalProtocol(t *testing.T) {
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	r := must(e.s.ListProtocols(e.ctx, connect.NewRequest(&adminv1.ListProtocolsRequest{})))
	for _, p := range r.Msg.Protocols {
		if p.GetId() == "trusttunnel" {
			if !p.GetExperimental() {
				t.Fatal("TrustTunnel protocol list entry is not experimental")
			}
			if strings.Contains(p.GetDefaultSettingsJson(), "password") || strings.Contains(p.GetSettingsSchemaJson(), "username") {
				t.Fatalf("protocol metadata exposed credentials: %s / %s", p.GetDefaultSettingsJson(), p.GetSettingsSchemaJson())
			}
			return
		}
	}
	t.Fatal("TrustTunnel is absent from a registry that registered it locally")
}

// The credential of the app's device has to reach the node: the link-app switches say nothing of TrustTunnel, so the node's
// desired state takes the credentials of a Mistgate-only protocol by the Mistgate switch alone (the store drops them with it).
func TestTrustTunnelCredentialReachesTheNodeOnlyForTheAppDevice(t *testing.T) {
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	hy2 := e.profile("hy2", `{"port":8443}`)
	e.inbound(hy2.Id, "nod_de1")
	tt := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "trusttunnel", Name: "tt", SettingsJson: `{"port":443,"h3":false,"tls_mode":"acme_domain","egress":"direct"}`,
	}))).Msg.Profile
	e.inbound(tt.Id, "nod_de1")
	r := e.userBare("alice", e.group("default", hy2.Id, tt.Id), nil)
	token := e.tokenOf(r.User.Id)
	fetchAs(e, token, phone(), "") // a link device: it never gets a TrustTunnel credential
	key := sha256.Sum256([]byte("a key of the app"))
	_, err := e.s.EnrolLinkDevice(e.ctx, r.User.Id, token, tablet(), key[:])
	if err != nil {
		t.Fatal(err)
	}
	dev, _, err := e.s.ClientDeviceByKey(e.ctx, key[:]) // the row the app's requests are served from
	if err != nil {
		t.Fatal(err)
	}
	ttCreds := func() []plugin.UserCred {
		var out []plugin.UserCred
		for _, in := range must(e.s.Desired(e.ctx, "nod_de1")) {
			if in.Spec.Protocol == "trusttunnel" {
				out = append(out, in.Creds...)
			}
		}
		return out
	}
	if got := ttCreds(); len(got) != 0 {
		t.Fatalf("TrustTunnel credentials before the app announced the capability: %+v", got)
	}
	v := must(e.s.ClientSubscription(e.ctx, dev, tablet(), []string{"v=1", "trusttunnel=1"}))
	var ttServer *SubServer
	for i, srv := range v.Servers {
		if srv.Protocol == "trusttunnel" {
			if ttServer != nil {
				t.Fatalf("two TrustTunnel paths: %+v", v.Servers)
			}
			ttServer = &v.Servers[i]
		}
	}
	if ttServer == nil {
		t.Fatalf("the app's servers = %+v, want a TrustTunnel path", v.Servers)
	}
	got := ttCreds()
	if len(got) != 1 || got[0].DeviceID != dev.ID {
		t.Fatalf("TrustTunnel credentials of the node = %+v, want the app device's one", got)
	}
	var data map[string]string
	if err := json.Unmarshal(got[0].Data, &data); err != nil || len(data) != 2 || data["user"] == "" || len(data["pass_sha256"]) != 64 {
		t.Fatalf("node data = %s (%v), want user and pass_sha256 only", got[0].Data, err)
	}
	if strings.Contains(ttServer.URI, data["pass_sha256"]) {
		t.Fatal("the hash of the password is in the app's path")
	}

	e.sql(`UPDATE user SET app_mistgate_off = 1 WHERE id = ?`, r.User.Id)
	if got := ttCreds(); len(got) != 0 {
		t.Fatalf("TrustTunnel credentials with the Mistgate switch off: %+v", got)
	}
	if v := must(e.s.ClientSubscription(e.ctx, dev, tablet(), []string{"trusttunnel=1"})); len(v.Servers) != 0 {
		t.Fatalf("the switched-off app is served %d servers", len(v.Servers))
	}
}

// The relay twin of a TrustTunnel server is the app's alone, and goes with the person's Mistgate switch.
func TestTrustTunnelRelayIsUsableOnlyInTheMistgateFormat(t *testing.T) {
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	link := store.RelayLink{Relay: store.Relay{Enabled: true, State: "active", ViaNodeID: "nod_fi1",
		CheckJSON: `{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`},
		ExitEnabled: true, ExitState: "active", ExitNodeState: "active", ViaNodeState: "active"}
	exit := store.AccessInboundFull{
		Inbound: store.AccessInbound{Enabled: true, State: "active"},
		Profile: store.AccessProfile{ID: "prf_tt", Protocol: "trusttunnel"},
		Node:    store.AccessNode{ID: "nod_de1", State: "active"},
	}
	group := store.AccessGroup{ProfileIDs: []string{"prf_tt"}}
	user := store.AccessUser{NodeIDs: []string{"nod_de1", "nod_fi1"}, AppHapp: true}
	if !e.s.relayUsableFor(link, exit, group, user, plugin.FormatMistgate) {
		t.Fatal("the app is refused the relay twin of a TrustTunnel server")
	}
	if e.s.relayUsable(link, exit, group, user) || e.s.relayUsableFor(link, exit, group, user, plugin.FormatMihomo) {
		t.Fatal("a link app is given the relay twin of a TrustTunnel server")
	}
	user.AppMistgateOff = true
	if e.s.relayUsableFor(link, exit, group, user, plugin.FormatMistgate) {
		t.Fatal("the relay twin goes to a person whose Mistgate switch is off")
	}
}

// The profile editor shows the app's path with the credential masked, and nothing of a made-up one.
func TestTrustTunnelProfilePreviewIsMasked(t *testing.T) {
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	r := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{
		Protocol: "trusttunnel", NodeId: "nod_de1", SettingsJson: `{"port":443,"h3":true,"tls_mode":"acme_domain","egress":"direct"}`,
	}))).Msg
	if len(r.Errors) != 0 || !strings.Contains(r.Summary, "HTTP/3") || r.ClientPreview == "" {
		t.Fatalf("preview = %+v", r)
	}
	var path map[string]any
	if err := json.Unmarshal([]byte(r.ClientPreview), &path); err != nil {
		t.Fatal(err)
	}
	if path["username"] != protocols.MaskedSecret || path["password"] != protocols.MaskedSecret || path["transport"] != "auto" || path["sni"] != "de1.example.com" {
		t.Fatalf("preview path = %s", r.ClientPreview)
	}
}
