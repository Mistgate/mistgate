package vless

import (
	"net/url"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

func tlsRenderInput(t *testing.T, format plugin.ClientFormat, transport, cdnHost string) protocols.RenderInput {
	t.Helper()
	fields := map[string]any{
		"security": "tls", "transport": transport, "tls_mode": "acme_domain", "sni": "origin.example.com",
	}
	if cdnHost != "" {
		fields["cdn"] = map[string]any{"host": cdnHost, "port": 443}
	}
	if transport == "ws" {
		fields["ws"] = map[string]any{"path": "/ws", "host": ""}
	}
	in := protocols.RenderInput{
		Format: format, Settings: editSettingsJSON(t, validSettings(t, nil), fields), Inbound: vlessInbound(),
		DeviceID: "dev_123", Secret: "8a3b5c2d-1e4f-4a6b-8c0d-123456789abc",
	}
	in.Inbound.TLSServerName = "origin.example.com"
	in.Inbound.TLSMode = plugin.TLSAcmeDomain
	return in
}

func TestRenderTLSURIEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, transport, cdnHost, host, sni, typ string
		port                                     string
	}{
		{name: "direct XHTTP", transport: "xhttp", host: "origin.example.com", sni: "origin.example.com", typ: "xhttp", port: "443"},
		{name: "CDN XHTTP", transport: "xhttp", cdnHost: "cdn.example.org", host: "cdn.example.org", sni: "cdn.example.org", typ: "xhttp", port: "443"},
		{name: "CDN WebSocket", transport: "ws", cdnHost: "cdn.example.org", host: "cdn.example.org", sni: "cdn.example.org", typ: "ws", port: "443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tlsRenderInput(t, plugin.FormatURIList, tc.transport, tc.cdnHost)
			in.Inbound.Port = 8443
			frag, ok := New().Render(in)
			if !ok {
				t.Fatal("TLS URI render refused")
			}
			u, err := url.Parse(string(frag.Data))
			if err != nil {
				t.Fatal(err)
			}
			wantHost := "de1.example.com"
			if tc.cdnHost != "" {
				wantHost = tc.cdnHost
			}
			wantPort := "8443"
			if tc.cdnHost != "" {
				wantPort = tc.port
			}
			q := u.Query()
			if u.Hostname() != wantHost || u.Port() != wantPort || q.Get("security") != "tls" || q.Get("sni") != tc.sni || q.Get("type") != tc.typ || q.Has("pbk") || q.Has("sid") || q.Has("spx") || q.Has("flow") {
				t.Fatalf("TLS URI = %s", frag.Data)
			}
			if tc.host != "" && q.Get("host") != tc.host {
				t.Fatalf("Host = %q, URI %s", q.Get("host"), frag.Data)
			}
			if tc.cdnHost != "" && tc.transport == "xhttp" && q.Get("mode") != "packet-up" {
				t.Fatalf("CDN XHTTP mode = %q, URI %s", q.Get("mode"), frag.Data)
			}
		})
	}
}

func TestRenderTLSMihomoTransportAndCDN(t *testing.T) {
	for _, tc := range []struct{ transport, cdnHost string }{
		{transport: "xhttp"},
		{transport: "xhttp", cdnHost: "cdn.example.org"},
		{transport: "ws", cdnHost: "cdn.example.org"},
	} {
		in := tlsRenderInput(t, plugin.FormatMihomo, tc.transport, tc.cdnHost)
		in.Inbound.Port = 8443
		frag, ok := New().Render(in)
		if !ok {
			t.Fatalf("TLS Mihomo render refused for %+v", tc)
		}
		var proxies []map[string]any
		if err := yaml.Unmarshal(frag.Data, &proxies); err != nil || len(proxies) != 1 {
			t.Fatalf("Mihomo YAML: %v: %s", err, frag.Data)
		}
		proxy := proxies[0]
		wantHost := "de1.example.com"
		wantPort := 8443
		if tc.cdnHost != "" {
			wantHost, wantPort = tc.cdnHost, 443
		}
		if proxy["server"] != wantHost || proxy["port"] != wantPort || proxy["servername"] != map[bool]string{true: tc.cdnHost, false: "origin.example.com"}[tc.cdnHost != ""] || proxy["tls"] != true || proxy["reality-opts"] != nil {
			t.Fatalf("TLS Mihomo fields = %#v", proxy)
		}
		if tc.transport == "ws" {
			if proxy["network"] != "ws" || proxy["ws-opts"] == nil || proxy["xhttp-opts"] != nil || proxy["flow"] != nil {
				t.Fatalf("WebSocket Mihomo fields = %#v", proxy)
			}
		} else if proxy["network"] != "xhttp" || proxy["xhttp-opts"] == nil || proxy["ws-opts"] != nil || proxy["flow"] != nil {
			t.Fatalf("XHTTP Mihomo fields = %#v", proxy)
		}
	}
}
