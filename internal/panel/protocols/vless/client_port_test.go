package vless

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
	"gopkg.in/yaml.v3"
)

func TestClientPortThroughTCPProxy(t *testing.T) {
	for _, host := range []string{"de1.example.com", "2001:db8::1"} {
		for _, format := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox} {
			t.Run(host+"/"+string(format), func(t *testing.T) {
				in := renderInput(t, format, "tcp", func(s *Settings) { s.Port, s.ClientPort = 19443, 443 })
				in.Inbound.Node.Address, in.Inbound.Port = host, 19443
				frag, ok := New().Render(in)
				if !ok {
					t.Fatal("render refused public port")
				}
				var gotHost string
				var port int
				switch format {
				case plugin.FormatURIList:
					u, err := url.Parse(string(frag.Data))
					if err != nil {
						t.Fatal(err)
					}
					gotHost = u.Hostname()
					if u.Port() == "443" {
						port = 443
					}
				case plugin.FormatMihomo:
					var proxies []struct {
						Server string
						Port   int
					}
					if err := yaml.Unmarshal(frag.Data, &proxies); err != nil || len(proxies) != 1 {
						t.Fatalf("invalid mihomo output: %v", err)
					}
					gotHost, port = proxies[0].Server, proxies[0].Port
				case plugin.FormatXrayJSON:
					var outbound xrayVless
					if err := json.Unmarshal(frag.Data, &outbound); err != nil || len(outbound.Settings.Vnext) != 1 {
						t.Fatalf("invalid Xray output: %v", err)
					}
					gotHost, port = outbound.Settings.Vnext[0].Address, int(outbound.Settings.Vnext[0].Port)
				case plugin.FormatSingbox:
					var outbound singboxVless
					if err := json.Unmarshal(frag.Data, &outbound); err != nil {
						t.Fatal(err)
					}
					gotHost, port = outbound.Server, int(outbound.ServerPort)
				}
				if gotHost != host || port != 443 {
					t.Fatalf("client got %s:%d, want %s:443", gotHost, port, host)
				}
			})
		}
	}
}

func TestClientPortDoesNotChangeNodeListener(t *testing.T) {
	raw := validSettings(t, func(s *Settings) { s.Port, s.ClientPort = 19443, 443 })
	spec, err := New().BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{Settings: raw}, PluginState: bytes32(), PortOverride: 20443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Listen.Port != 20443 || strings.Contains(string(spec.Settings), "client_port") {
		t.Fatal("client port leaked into the node listener or settings")
	}
	in := renderInput(t, plugin.FormatMihomo, "tcp", nil)
	in.Inbound.Port = 20443
	params, ok := ResolveClientParams(in)
	if !ok || params.Port != 20443 {
		t.Fatal("unset public port must keep the inbound port")
	}
	in.Settings = raw
	in.ClientPort = 30443
	params, ok = ResolveClientParams(in)
	if !ok || params.Port != 30443 {
		t.Fatal("a relay's entry port must override the profile public port")
	}
}

func TestClientPortValidation(t *testing.T) {
	for _, port := range []int{-1, 80, 65536} {
		errs := New().Validate(validSettings(t, func(s *Settings) { s.ClientPort = port }))
		if len(errs) != 1 || errs[0].Pointer != "/client_port" {
			t.Errorf("port %d was not rejected at its field: %+v", port, errs)
		}
	}
	raw := validSettings(t, func(s *Settings) {
		s.Security, s.Transport, s.TLSMode, s.SNI = "tls", "ws", "acme_domain", "origin.example.org"
		s.Reality = RealitySettings{}
		s.WS = &WSSettings{Path: "/proxy"}
		s.CDN, s.ClientPort = &CDNSettings{Host: "cdn.example.org", Port: 443}, 443
	})
	errs := New().Validate(raw)
	if len(errs) != 1 || errs[0].Pointer != "/client_port" {
		t.Errorf("ambiguous CDN and public ports were not rejected: %+v", errs)
	}
}
