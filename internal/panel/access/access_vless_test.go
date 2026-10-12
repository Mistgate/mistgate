package access

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

func (e *env) vlessProfile(name string) *adminv1.ProfileSummary {
	e.t.Helper()
	return must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "vless", Name: name, SettingsJson: `{"reality":{"target":"www.example.org:443"}}`,
	}))).Msg.Profile
}

func TestVLESSListPreviewAndRetainedKey(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	listed := must(e.s.ListProtocols(e.ctx, req(&adminv1.ListProtocolsRequest{}))).Msg.Protocols
	var vlessInfo *adminv1.ProtocolInfo
	for _, p := range listed {
		if p.Id == "vless" {
			vlessInfo = p
		}
	}
	if vlessInfo == nil || len(vlessInfo.Apps) != 1 || vlessInfo.Apps[0] != adminv1.App_APP_HAPP {
		t.Fatalf("VLESS protocol info = %+v", vlessInfo)
	}

	preview := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{
		Protocol: "vless", NodeId: "nod_de1", SettingsJson: `{"reality":{"target":"www.example.org:443"}}`,
	}))).Msg
	if len(preview.Errors) != 0 || !strings.HasPrefix(preview.ClientPreview, "vless://••••@de1.example.com:443?") {
		t.Fatalf("VLESS preview = %+v", preview)
	}
	// The secret in a preview is masked ("••••" is not a valid userinfo), the inbound's public key is not.
	if !strings.Contains(preview.ClientPreview, "&pbk=") || !strings.Contains(preview.ClientPreview, "&security=reality&") {
		t.Fatalf("VLESS preview omitted the inbound public key: %q", preview.ClientPreview)
	}

	profile := e.vlessProfile("vless")
	inbound := e.inbound(profile.Id, "nod_de1")
	first, err := e.st.Access().Inbound(e.ctx, inbound.Id)
	if err != nil {
		t.Fatal(err)
	}
	if first.PluginPublicJSON == "" || first.PluginPublicJSON == "{}" {
		t.Fatalf("inbound has no public key: %+v", first)
	}
	must(e.s.DeleteInbound(e.ctx, req(&adminv1.DeleteInboundRequest{InboundId: inbound.Id})))
	readded := e.inbound(profile.Id, "nod_de1")
	second, err := e.st.Access().Inbound(e.ctx, readded.Id)
	if err != nil {
		t.Fatal(err)
	}
	if first.PluginPublicJSON != second.PluginPublicJSON {
		t.Fatalf("re-added VLESS inbound changed its parked key: %s -> %s", first.PluginPublicJSON, second.PluginPublicJSON)
	}

	group := e.group("vless-only", profile.Id)
	user := e.user("alice", group, nil).User
	token := e.tokenOf(user.Id)
	sub := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true, NoPageData: true}))
	if len(sub.Lines) != 1 || !strings.HasPrefix(sub.Lines[0], "vless://") {
		t.Fatalf("VLESS subscription = %+v", sub.Lines)
	}
	uri, err := url.Parse(sub.Lines[0])
	if err != nil {
		t.Fatal(err)
	}
	var public struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(second.PluginPublicJSON), &public); err != nil {
		t.Fatal(err)
	}
	if uri.Query().Get("pbk") != public.PublicKey {
		t.Fatalf("subscription public key = %q, want inbound public key %q", uri.Query().Get("pbk"), public.PublicKey)
	}
}

func TestVLESSSubscriptionOrderWithHysteria2AndAWG(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	awgProfile := e.awgProfile("awg", "{}")
	vlessProfile := e.vlessProfile("vless")
	hy2Profile := e.profile("hy2", "")

	e.inbound(awgProfile.Id, "nod_de1")
	e.clock = e.clock.Add(time.Second)
	e.inbound(vlessProfile.Id, "nod_de1")
	e.clock = e.clock.Add(time.Second)
	e.inbound(hy2Profile.Id, "nod_de1")
	group := e.group("all-protocols", awgProfile.Id, vlessProfile.Id, hy2Profile.Id)
	user := e.user("alice", group, func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Happ: true, Amnezia: true}
	}).User
	token := e.tokenOf(user.Id)
	view := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{
		Format: plugin.FormatMihomo, NoTouch: true, NoPageData: true,
		Name: func(s SubServer) string { return s.Protocol },
	}))
	if len(view.Servers) != 3 {
		t.Fatalf("subscription servers = %+v", view.Servers)
	}
	if got := []string{view.Servers[0].Protocol, view.Servers[1].Protocol, view.Servers[2].Protocol}; !reflect.DeepEqual(got, []string{"hysteria2", "vless", "awg"}) {
		t.Fatalf("subscription protocol order = %v", got)
	}
	if len(view.Lines) != 3 || !strings.Contains(view.Lines[1], "public-key:") {
		t.Fatalf("Mihomo lines = %+v", view.Lines)
	}
	if view.Servers[0].Made != 2 || view.Servers[1].Made != 1 || view.Servers[2].Made != 0 {
		t.Fatalf("remarks creation ranks after protocol sort = %d, %d, %d", view.Servers[0].Made, view.Servers[1].Made, view.Servers[2].Made)
	}
}

func TestListenOverlapNetworkKeepsHysteria2AndVLESSSeparate(t *testing.T) {
	udp := plugin.Listen{Network: "udp", Port: 443}
	tcp := plugin.Listen{Network: "tcp", Port: 443}
	if listenOverlap(udp, tcp) || listenOverlap(tcp, udp) {
		t.Fatal("Hysteria2 UDP 443 and VLESS TCP 443 overlap")
	}
}

// hy2_decoy_paused: who is the pair, on which port, and when it is not one. The panel never sends masquerade.tcp_port, but
// a node may hold one (an older panel, a hand edit), and the rule is the node's: absent = 443, 0 = no site.
func TestDecoyPausedRules(t *testing.T) {
	hy2 := func(settings string) nodeListen {
		spec := plugin.InboundSpec{Protocol: "hysteria2", Listen: plugin.Listen{Network: "udp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "de1.example.com"}, Settings: []byte(settings)}
		return nodeListen{profile: "hy2", protocol: "hysteria2", enabled: true, l: spec.Listen, spec: spec}
	}
	vless := func(port uint16, target string) plugin.InboundSpec {
		return plugin.InboundSpec{Protocol: "vless", Listen: plugin.Listen{Network: "tcp", Port: port},
			Settings: []byte(`{"reality":{"target":"` + target + `"}}`)}
	}
	asNeighbour := func(spec plugin.InboundSpec, enabled bool) nodeListen {
		return nodeListen{profile: "vless", protocol: "vless", enabled: enabled, l: spec.Listen, spec: spec}
	}
	off := func(n nodeListen) nodeListen { n.enabled = false; return n }
	tests := []struct {
		name   string
		add    plugin.InboundSpec // the inbound being added
		others []nodeListen
		port   string // "" = no warning
		target string
	}{
		{"vless on 443, default site", vless(443, "www.example.org:443"), []nodeListen{hy2(`{}`)}, "443", "www.example.org"},
		{"vless on 443, site port 8443", vless(443, "www.example.org:443"), []nodeListen{hy2(`{"masquerade":{"tcp_port":8443}}`)}, "", ""},
		{"vless on 8443, site port 8443", vless(8443, "www.example.org:443"), []nodeListen{hy2(`{"masquerade":{"tcp_port":8443}}`)}, "8443", "www.example.org"},
		{"vless on 443, site off", vless(443, "www.example.org:443"), []nodeListen{hy2(`{"masquerade":{"tcp_port":0}}`)}, "", ""},
		{"vless on 8443, default site", vless(8443, "www.example.org:443"), []nodeListen{hy2(`{}`)}, "", ""},
		{"vless on 443, hysteria2 disabled", vless(443, "www.example.org:443"), []nodeListen{off(hy2(`{}`))}, "", ""},
		{"vless target is an IPv6 address", vless(443, "[2001:db8::1]:443"), []nodeListen{hy2(`{}`)}, "443", "2001:db8::1"},
		{"hysteria2 beside vless on 443", hy2(`{}`).spec, []nodeListen{asNeighbour(vless(443, "www.example.org:443"), true)}, "443", "www.example.org"},
		{"hysteria2 site 8443 beside vless on 8443", hy2(`{"masquerade":{"tcp_port":8443}}`).spec, []nodeListen{asNeighbour(vless(8443, "www.example.org:443"), true)}, "8443", "www.example.org"},
		{"hysteria2 site 8443 beside vless on 443", hy2(`{"masquerade":{"tcp_port":8443}}`).spec, []nodeListen{asNeighbour(vless(443, "www.example.org:443"), true)}, "", ""},
		{"hysteria2 without a site beside vless on 443", hy2(`{"masquerade":{"tcp_port":0}}`).spec, []nodeListen{asNeighbour(vless(443, "www.example.org:443"), true)}, "", ""},
		{"hysteria2 beside a disabled vless", hy2(`{}`).spec, []nodeListen{asNeighbour(vless(443, "www.example.org:443"), false)}, "", ""},
	}
	e := newEnv(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.s.inboundWarnings(e.ctx, "nod_de1", "de1", "added", tt.add, tt.others)
			if err != nil {
				t.Fatal(err)
			}
			if tt.port == "" {
				if len(got) != 0 {
					t.Fatalf("warnings = %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Code != "hy2_decoy_paused" {
				t.Fatalf("warnings = %+v", got)
			}
			p := got[0].Params
			if p["node"] != "de1" || p["port"] != tt.port || p["via"] != "site" || p["target"] != tt.target || p["port80"] != "unchecked" || len(p) != 7 {
				t.Errorf("params = %v", p)
			}
			// the names are the profiles', whichever of them is being added
			wantHy2, wantVLESS := "hy2", "added"
			if tt.add.Protocol == "hysteria2" {
				wantHy2, wantVLESS = "added", "vless"
			}
			if p["hy2"] != wantHy2 || p["vless"] != wantVLESS {
				t.Errorf("profile names = %q, %q", p["hy2"], p["vless"])
			}
		})
	}
}
