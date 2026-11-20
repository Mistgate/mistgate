package plugin

import (
	"reflect"
	"testing"
)

func TestHy2SitePort(t *testing.T) {
	hy2 := func(mode TLSMode, settings string) InboundSpec {
		return InboundSpec{Protocol: "hysteria2", TLS: TLS{Mode: mode, ServerName: "de1.example.com"}, Settings: []byte(settings)}
	}
	for _, tt := range []struct {
		name string
		spec InboundSpec
		want uint16
	}{
		{"no settings", hy2(TLSAcmeDomain, ``), 443},
		{"omitted", hy2(TLSAcmeDomain, `{}`), 443},
		{"null port", hy2(TLSAcmeDomain, `{"masquerade":{"tcp_port":null}}`), 443},
		{"own port", hy2(TLSSelfSigned, `{"masquerade":{"tcp_port":8443}}`), 8443},
		{"highest port", hy2(TLSAcmeDomain, `{"masquerade":{"tcp_port":65535}}`), 65535},
		{"off", hy2(TLSAcmeDomain, `{"masquerade":{"tcp_port":0}}`), 0},
		{"negative: the node refuses it", hy2(TLSAcmeDomain, `{"masquerade":{"tcp_port":-1}}`), 0},
		{"too high: the node refuses it", hy2(TLSAcmeDomain, `{"masquerade":{"tcp_port":65536}}`), 0},
		{"unreadable port", hy2(TLSAcmeDomain, `{"masquerade":{"tcp_port":"443"}}`), 0},
		{"unreadable settings", hy2(TLSAcmeDomain, `not json`), 0},
		{"no TLS mode: no certificate", hy2(0, `{}`), 0},
		// the node listens for the site and answers 404 (internal/node/hysteria2: masqNone is the handler, not the listener)
		{"type none", hy2(TLSAcmeDomain, `{"masquerade":{"type":"none"}}`), 443},
		{"type none on its own port", hy2(TLSAcmeDomain, `{"masquerade":{"type":"none","tcp_port":8443}}`), 8443},
		{"type none, site off", hy2(TLSAcmeDomain, `{"masquerade":{"type":"none","tcp_port":0}}`), 0},
		{"another protocol", InboundSpec{Protocol: "vless", TLS: TLS{Mode: TLSAcmeDomain}, Settings: []byte(`{}`)}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Hy2SitePort(tt.spec); got != tt.want {
				t.Fatalf("Hy2SitePort = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestVLESSOf(t *testing.T) {
	vless := func(settings string) InboundSpec { return InboundSpec{Protocol: "vless", Settings: []byte(settings)} }
	v, ok := VLESSOf(vless(`{"security":"reality","transport":"xhttp","reality":{"target":" www.example.org:443 ","server_names":["Www.Example.org"]},"cdn":{"host":" CDN.Example.com. "}}`))
	want := VLESSView{Security: "reality", Transport: "xhttp", Target: "www.example.org:443", ServerNames: []string{"Www.Example.org"}, CDNHost: "cdn.example.com"}
	if !ok || !reflect.DeepEqual(v, want) {
		t.Fatalf("VLESSOf = %+v, %v; want %+v", v, ok, want)
	}
	for name, spec := range map[string]InboundSpec{
		"another protocol": {Protocol: "hysteria2", Settings: []byte(`{}`)},
		"unreadable":       vless(`not json`),
		"wrong type":       vless(`{"reality":"self"}`),
	} {
		if v, ok := VLESSOf(spec); ok || !reflect.DeepEqual(v, VLESSView{}) {
			t.Errorf("%s: VLESSOf = %+v, %v; want an empty view and false", name, v, ok)
		}
	}
	for _, settings := range []string{``, `null`, `{}`} {
		if v, ok := VLESSOf(vless(settings)); !ok || !reflect.DeepEqual(v, VLESSView{}) {
			t.Errorf("settings %q: VLESSOf = %+v, %v; want an empty view and true", settings, v, ok)
		}
	}
}

func TestVLESSViewReads(t *testing.T) {
	view := func(settings string) VLESSView {
		v, _ := VLESSOf(InboundSpec{Protocol: "vless", Settings: []byte(settings)})
		return v
	}
	for _, tt := range []struct {
		name     string
		settings string
		tls      bool
		self     bool
		host     string
		ownSite  bool
	}{
		{"TLS", `{"security":"tls"}`, true, false, "", true},
		{"REALITY, the node's own site", `{"security":"reality","reality":{"target":"self"}}`, false, true, "", true},
		{"the target is trimmed, as the node trims it", `{"security":"reality","reality":{"target":" self "}}`, false, true, "", true},
		{"REALITY, another site", `{"security":"reality","reality":{"target":"www.example.org:443"}}`, false, false, "www.example.org", false},
		{"an IPv6 target", `{"security":"reality","reality":{"target":"[2001:db8::1]:443"}}`, false, false, "2001:db8::1", false},
		{"a target with no port", `{"security":"reality","reality":{"target":"www.example.org"}}`, false, false, "", false},
		{"no security written reads as REALITY", `{"reality":{"target":"self"}}`, false, true, "", true},
		{"a security that is neither", `{"security":"foo","reality":{"target":"self"}}`, false, true, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := view(tt.settings)
			if v.TLS() != tt.tls || v.TargetSelf() != tt.self || v.TargetHost() != tt.host || v.OwnSite() != tt.ownSite {
				t.Fatalf("TLS %v, TargetSelf %v, TargetHost %q, OwnSite %v; want %v, %v, %q, %v", v.TLS(), v.TargetSelf(), v.TargetHost(), v.OwnSite(), tt.tls, tt.self, tt.host, tt.ownSite)
			}
		})
	}
}

func TestServerCDN(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec InboundSpec
		want bool
	}{
		{"a CDN host", InboundSpec{Protocol: "vless", Settings: []byte(`{"security":"tls","cdn":{"host":"cdn.example.com"}}`)}, true},
		{"a CDN host with a closing dot", InboundSpec{Protocol: "vless", Settings: []byte(`{"security":"tls","cdn":{"host":"cdn.example.com."}}`)}, true},
		{"an empty host", InboundSpec{Protocol: "vless", Settings: []byte(`{"security":"tls","cdn":{"host":""}}`)}, false},
		{"a blank host: the node refuses it", InboundSpec{Protocol: "vless", Settings: []byte(`{"security":"tls","cdn":{"host":" "}}`)}, false},
		{"no CDN", InboundSpec{Protocol: "vless", Settings: []byte(`{"security":"tls","cdn":null}`)}, false},
		{"unreadable settings", InboundSpec{Protocol: "vless", Settings: []byte(`{`)}, false},
		{"another protocol", InboundSpec{Protocol: "trusttunnel", Settings: []byte(`{"cdn":{"host":"cdn.example.com"}}`)}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServerCDN(tt.spec); got != tt.want {
				t.Fatalf("ServerCDN = %v, want %v", got, tt.want)
			}
		})
	}
}

// SharesTCPPort and tcpRoute are one list: a protocol takes part in a shared port exactly when it can have a route.
func TestSharesTCPPortIsTheProtocolsOfTCPRoute(t *testing.T) {
	for _, protocol := range []string{"vless", "trusttunnel", "mtproto", "hysteria2", "awg", ""} {
		// settings every sharing protocol reads a name from; TrustTunnel's name is its TLS name
		s := InboundSpec{ID: "x", Protocol: protocol, Listen: Listen{Network: "tcp", Port: 443}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: "de1.example.com"},
			Settings: []byte(`{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"]},"domains":["de1.example.com"]}`)}
		if _, ok := tcpRoute(s); ok != SharesTCPPort(protocol) {
			t.Errorf("%q: tcpRoute ok = %v, SharesTCPPort = %v", protocol, ok, SharesTCPPort(protocol))
		}
	}
}

func TestMTProtoWeb(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec InboundSpec
		want bool
	}{
		{"the Web Proxy", InboundSpec{Protocol: "mtproto", Settings: []byte(`{"domains":["tg.example.com"],"fallback":"decoy","mode":"web"}`)}, true},
		{"Fake TLS without a mode", InboundSpec{Protocol: "mtproto", Settings: []byte(`{"domains":["tg.example.com"],"fallback":"decoy"}`)}, false},
		{"Fake TLS, written out", InboundSpec{Protocol: "mtproto", Settings: []byte(`{"domains":["tg.example.com"],"mode":"fake_tls"}`)}, false},
		{"another mode", InboundSpec{Protocol: "mtproto", Settings: []byte(`{"mode":"Web"}`)}, false},
		{"no settings", InboundSpec{Protocol: "mtproto"}, false},
		{"unreadable settings", InboundSpec{Protocol: "mtproto", Settings: []byte(`{`)}, false},
		{"another protocol", InboundSpec{Protocol: "vless", Settings: []byte(`{"mode":"web"}`)}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := MTProtoWeb(tt.spec); got != tt.want {
				t.Fatalf("MTProtoWeb = %v, want %v", got, tt.want)
			}
		})
	}
}
