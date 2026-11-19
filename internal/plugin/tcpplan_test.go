package plugin

import (
	"reflect"
	"testing"
)

func TestTCPPortOwners(t *testing.T) {
	tcp := func(id, protocol string, port uint16, settings string) InboundSpec {
		return InboundSpec{ID: id, Protocol: protocol, Listen: Listen{Network: "tcp", Port: port}, Settings: []byte(settings)}
	}
	hy2 := func(id, settings string) InboundSpec {
		return InboundSpec{ID: id, Protocol: "hysteria2", Listen: Listen{Network: "udp", Port: 443}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: "de1.example.com"}, Settings: []byte(settings)}
	}
	for _, tt := range []struct {
		name  string
		specs []InboundSpec
		want  map[uint16]TCPOwner
	}{
		{"nothing", nil, map[uint16]TCPOwner{}},
		{"a Hysteria2 site alone", []InboundSpec{hy2("h1", `{}`)}, map[uint16]TCPOwner{443: {ID: "h1", Protocol: "hysteria2", Site: true}}},
		{"a Hysteria2 site on its own port", []InboundSpec{hy2("h1", `{"masquerade":{"tcp_port":8443}}`)}, map[uint16]TCPOwner{8443: {ID: "h1", Protocol: "hysteria2", Site: true}}},
		{"a Hysteria2 site turned off", []InboundSpec{hy2("h1", `{"masquerade":{"tcp_port":0}}`)}, map[uint16]TCPOwner{}},
		{"a Hysteria2 inbound without TLS has no site", []InboundSpec{func() InboundSpec { s := hy2("h1", `{}`); s.TLS = TLS{}; return s }()}, map[uint16]TCPOwner{}},
		{"REALITY self holds the port the site wanted", []InboundSpec{hy2("a", `{}`), tcp("b", "vless", 443, `{"security":"reality","reality":{"target":"self"}}`)},
			map[uint16]TCPOwner{443: {ID: "b", Protocol: "vless", Site: true}}},
		{"REALITY with another site", []InboundSpec{hy2("a", `{}`), tcp("b", "vless", 443, `{"security":"reality","reality":{"target":"www.example.com:443"}}`)},
			map[uint16]TCPOwner{443: {ID: "b", Protocol: "vless"}}},
		{"VLESS TLS", []InboundSpec{tcp("b", "vless", 443, `{"security":"tls"}`)}, map[uint16]TCPOwner{443: {ID: "b", Protocol: "vless", Site: true}}},
		{"TrustTunnel", []InboundSpec{hy2("a", `{}`), tcp("b", "trusttunnel", 443, `{}`)}, map[uint16]TCPOwner{443: {ID: "b", Protocol: "trusttunnel", Site: true}}},
		{"MTProto decoy", []InboundSpec{hy2("a", `{}`), tcp("b", "mtproto", 443, `{"fallback":"decoy"}`)}, map[uint16]TCPOwner{443: {ID: "b", Protocol: "mtproto", Site: true}}},
		{"MTProto without a fallback setting is the decoy", []InboundSpec{tcp("b", "mtproto", 443, `{}`)}, map[uint16]TCPOwner{443: {ID: "b", Protocol: "mtproto", Site: true}}},
		{"MTProto with another site still holds the port", []InboundSpec{hy2("a", `{}`), tcp("b", "mtproto", 443, `{"fallback":"site"}`)}, map[uint16]TCPOwner{443: {ID: "b", Protocol: "mtproto"}}},
		{"MTProto with a broken setting", []InboundSpec{tcp("b", "mtproto", 443, `{`)}, map[uint16]TCPOwner{443: {ID: "b", Protocol: "mtproto"}}},
		{"the lowest id holds the port", []InboundSpec{tcp("z", "trusttunnel", 443, `{}`), tcp("m", "mtproto", 443, `{"fallback":"site"}`)}, map[uint16]TCPOwner{443: {ID: "m", Protocol: "mtproto"}}},
		{"two Hysteria2 sites", []InboundSpec{hy2("z", `{}`), hy2("a", `{}`)}, map[uint16]TCPOwner{443: {ID: "a", Protocol: "hysteria2", Site: true}}},
		{"UDP and port 0 are no TCP port", []InboundSpec{{ID: "a", Protocol: "awg", Listen: Listen{Network: "udp", Port: 443}}, tcp("b", "trusttunnel", 0, `{}`)}, map[uint16]TCPOwner{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := TCPPortOwners(tt.specs); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestTCPSharePlan(t *testing.T) {
	tt := func(id string, port uint16, name string) InboundSpec {
		return InboundSpec{ID: id, Protocol: "trusttunnel", Listen: Listen{Network: "tcp", Port: port}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: name}, Settings: []byte(`{}`)}
	}
	mt := func(id string, port uint16, settings string) InboundSpec {
		return InboundSpec{ID: id, Protocol: "mtproto", Listen: Listen{Network: "tcp", Port: port}, Settings: []byte(settings)}
	}
	vl := func(id string, port uint16, name, settings string) InboundSpec {
		return InboundSpec{ID: id, Protocol: "vless", Listen: Listen{Network: "tcp", Port: port}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: name}, Settings: []byte(settings)}
	}
	hy2 := InboundSpec{ID: "a", Protocol: "hysteria2", Listen: Listen{Network: "udp", Port: 443}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: "de1.example.com"}, Settings: []byte(`{}`)}
	realityOther := `{"security":"reality","reality":{"target":"www.example.org:443","server_names":["Www.Example.org."]}}`
	realitySelf := `{"security":"reality","reality":{"target":"self","server_names":["self.example.com"]}}`
	route := func(id, protocol string, site, reality bool, names ...string) TCPRoute {
		return TCPRoute{ID: id, Protocol: protocol, Names: names, Site: site, Reality: reality}
	}
	// the layouts the agent puts a front on; on the others the port keeps its old rule
	fronts := map[string]bool{
		"TrustTunnel and MTProto, the lower id is the default":                         true,
		"names are normalized and repeated ones kept once":                             true,
		"REALITY with another site holds all its server names and is no default":       true,
		"REALITY self is a site; its name is the server name, not the node's TLS name": true,
		"VLESS TLS answers to its name and its CDN host":                               true,
		"a shadowed service holds none of its names, so a third may take one":          true,
	}
	for _, tc := range []struct {
		name  string
		specs []InboundSpec
		want  map[uint16]TCPPortShare
	}{
		{"nothing", nil, map[uint16]TCPPortShare{}},
		{"one service is no share", []InboundSpec{tt("a", 443, "tt.example.com")}, map[uint16]TCPPortShare{}},
		{"a Hysteria2 site and one TCP service is one owner", []InboundSpec{hy2, mt("b", 443, `{"domains":["mt.example.com"]}`)}, map[uint16]TCPPortShare{}},
		{"two services on different ports", []InboundSpec{tt("a", 443, "tt.example.com"), tt("b", 8443, "tt2.example.com")}, map[uint16]TCPPortShare{}},
		{"UDP is no TCP port", []InboundSpec{{ID: "a", Protocol: "awg", Listen: Listen{Network: "udp", Port: 443}}, {ID: "b", Protocol: "awg", Listen: Listen{Network: "udp", Port: 443}}}, map[uint16]TCPPortShare{}},
		{"TrustTunnel and MTProto, the lower id is the default", []InboundSpec{mt("b", 443, `{"domains":["mt.example.com"]}`), tt("a", 443, "tt.example.com"), hy2},
			map[uint16]TCPPortShare{443: {Default: "a", Routes: []TCPRoute{route("a", "trusttunnel", true, false, "tt.example.com"), route("b", "mtproto", true, false, "mt.example.com")}}}},
		{"names are normalized and repeated ones kept once", []InboundSpec{tt("a", 443, "TT.Example.com."), mt("b", 443, `{"domains":[" MT.example.com","mt.example.com.","*.example.com","192.0.2.7","bücher.example"]}`)},
			map[uint16]TCPPortShare{443: {Default: "a", Routes: []TCPRoute{route("a", "trusttunnel", true, false, "tt.example.com"), route("b", "mtproto", true, false, "mt.example.com")}}}},
		{"REALITY with another site holds all its server names and is no default", []InboundSpec{vl("a", 443, "", realityOther), mt("b", 443, `{"domains":["mt.example.com"]}`)},
			map[uint16]TCPPortShare{443: {Default: "b", Routes: []TCPRoute{route("a", "vless", false, true, "www.example.org"), route("b", "mtproto", true, false, "mt.example.com")}}}},
		{"REALITY self is a site; its name is the server name, not the node's TLS name", []InboundSpec{vl("a", 443, "other.example.com", realitySelf), tt("b", 443, "tt.example.com")},
			map[uint16]TCPPortShare{443: {Default: "a", Routes: []TCPRoute{route("a", "vless", true, true, "self.example.com"), route("b", "trusttunnel", true, false, "tt.example.com")}}}},
		{"VLESS TLS answers to its name and its CDN host", []InboundSpec{vl("a", 443, "Origin.example.com", `{"security":"tls","transport":"ws","cdn":{"host":"cdn.example.com"}}`), tt("b", 443, "tt.example.com")},
			map[uint16]TCPPortShare{443: {Default: "a", Routes: []TCPRoute{route("a", "vless", true, false, "origin.example.com", "cdn.example.com"), route("b", "trusttunnel", true, false, "tt.example.com")}}}},
		{"no site on the port: no front, the old rule", []InboundSpec{vl("a", 443, "", realityOther), mt("b", 443, `{"domains":["mt.example.com"],"fallback":"site"}`)},
			map[uint16]TCPPortShare{443: {Routes: []TCPRoute{route("a", "vless", false, true, "www.example.org"), route("b", "mtproto", false, false, "mt.example.com")}}}},
		{"the lower id holds a name; the other is shadowed and the port is no share any more", []InboundSpec{tt("a", 443, "x.example.com"), mt("b", 443, `{"domains":["X.example.com"]}`)},
			map[uint16]TCPPortShare{443: {Default: "a", Routes: []TCPRoute{route("a", "trusttunnel", true, false, "x.example.com")}, Shadowed: map[string]TCPShadow{"b": {Name: "x.example.com", By: "a"}}}}},
		{"a shadowed service holds none of its names, so a third may take one", []InboundSpec{tt("a", 443, "x.example.com"), mt("b", 443, `{"domains":["x.example.com","y.example.com"]}`), tt("c", 443, "y.example.com")},
			map[uint16]TCPPortShare{443: {Default: "a", Routes: []TCPRoute{route("a", "trusttunnel", true, false, "x.example.com"), route("c", "trusttunnel", true, false, "y.example.com")}, Shadowed: map[string]TCPShadow{"b": {Name: "x.example.com", By: "a"}}}}},
		{"a service with no name keeps the whole port to the old rule", []InboundSpec{tt("a", 443, "tt.example.com"), mt("b", 443, `{"domains":["192.0.2.7"]}`)}, map[uint16]TCPPortShare{}},
		{"a TrustTunnel without a name", []InboundSpec{tt("a", 443, "tt.example.com"), tt("b", 443, "")}, map[uint16]TCPPortShare{}},
		{"a protocol that cannot share", []InboundSpec{tt("a", 443, "tt.example.com"), {ID: "b", Protocol: "fake", Listen: Listen{Network: "tcp", Port: 443}}}, map[uint16]TCPPortShare{}},
		{"broken settings", []InboundSpec{tt("a", 443, "tt.example.com"), mt("b", 443, `{`)}, map[uint16]TCPPortShare{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := TCPSharePlan(tc.specs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
			if front := got[443].Front(); front != fronts[tc.name] {
				t.Fatalf("Front() = %v, want %v", front, fronts[tc.name])
			}
		})
	}
}

func TestTCPShareProblems(t *testing.T) {
	tt := func(id, name string) InboundSpec {
		return InboundSpec{ID: id, Protocol: "trusttunnel", Listen: Listen{Network: "tcp", Port: 443}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: name}, Settings: []byte(`{}`)}
	}
	mt := func(id, settings string) InboundSpec {
		return InboundSpec{ID: id, Protocol: "mtproto", Listen: Listen{Network: "tcp", Port: 443}, Settings: []byte(settings)}
	}
	realityOther := InboundSpec{ID: "r", Protocol: "vless", Listen: Listen{Network: "tcp", Port: 443}, Settings: []byte(`{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`)}
	realityNoName := InboundSpec{ID: "r", Protocol: "vless", Listen: Listen{Network: "tcp", Port: 443}, Settings: []byte(`{"security":"reality","reality":{"target":"192.0.2.9:443"}}`)}
	other := tt("z", "z.example.com")
	other.Listen.Port = 8443
	for _, tc := range []struct {
		name  string
		specs []InboundSpec
		want  []TCPShareProblem
	}{
		{"one service is alone, whatever it is", []InboundSpec{realityNoName}, nil},
		{"two that can share", []InboundSpec{tt("a", "a.example.com"), mt("b", `{"domains":["b.example.com"]}`)}, nil},
		{"another port is not looked at", []InboundSpec{tt("a", "a.example.com"), other}, nil},
		{"the same name: the higher id loses it", []InboundSpec{mt("b", `{"domains":["A.example.com"]}`), tt("a", "a.example.com")},
			[]TCPShareProblem{{Code: SNITaken, ID: "b", Name: "a.example.com", By: "a"}}},
		{"no name to tell it by", []InboundSpec{tt("a", "a.example.com"), realityNoName}, []TCPShareProblem{{Code: ShareUnsupported, ID: "r"}}},
		{"a protocol that cannot share", []InboundSpec{tt("a", "a.example.com"), {ID: "f", Protocol: "fake", Listen: Listen{Network: "tcp", Port: 443}}}, []TCPShareProblem{{Code: ShareUnsupported, ID: "f"}}},
		{"nobody serves the node's own site", []InboundSpec{realityOther, mt("b", `{"domains":["b.example.com"],"fallback":"site"}`)}, []TCPShareProblem{{Code: ShareNeedsSite}}},
		{"a site and a REALITY of another site", []InboundSpec{realityOther, tt("t", "t.example.com")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TCPShareProblems(tc.specs, 443); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// A shared port is held by its default route, not by the lowest id: that is what answers a stranger.
func TestTCPPortOwnersOfASharedPort(t *testing.T) {
	realityOther := InboundSpec{ID: "a", Protocol: "vless", Listen: Listen{Network: "tcp", Port: 443}, Settings: []byte(`{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`)}
	tt := InboundSpec{ID: "b", Protocol: "trusttunnel", Listen: Listen{Network: "tcp", Port: 443}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: "t.example.com"}, Settings: []byte(`{}`)}
	hy2 := InboundSpec{ID: "c", Protocol: "hysteria2", Listen: Listen{Network: "udp", Port: 443}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: "de1.example.com"}, Settings: []byte(`{}`)}
	got := TCPPortOwners([]InboundSpec{realityOther, tt, hy2})
	if want := (TCPOwner{ID: "b", Protocol: "trusttunnel", Site: true}); got[443] != want {
		t.Fatalf("owner of the shared port = %+v, want %+v", got[443], want)
	}
}

func TestRealityTarget(t *testing.T) {
	spec := func(proto, settings string) InboundSpec {
		return InboundSpec{Protocol: proto, Settings: []byte(settings)}
	}
	for _, tc := range []struct {
		name         string
		s            InboundSpec
		target, host string
		ok           bool
	}{
		{"another site", spec("vless", `{"security":"reality","reality":{"target":" shop.example.com:443 ","server_names":["Shop.example.com.","b.example.com"]}}`), "shop.example.com:443", "Shop.example.com", true},
		{"own site", spec("vless", `{"security":"reality","reality":{"target":"self","server_names":["de1.example.com"]}}`), "", "", false},
		{"tls", spec("vless", `{"security":"tls","reality":{"target":"shop.example.com:443","server_names":["shop.example.com"]}}`), "", "", false},
		{"no names", spec("vless", `{"security":"reality","reality":{"target":"shop.example.com:443"}}`), "", "", false},
		{"not vless", spec("hysteria2", `{"security":"reality","reality":{"target":"shop.example.com:443","server_names":["x"]}}`), "", "", false},
		{"broken json", spec("vless", `{`), "", "", false},
		{"empty", spec("vless", ``), "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, host, ok := RealityTarget(tc.s)
			if target != tc.target || host != tc.host || ok != tc.ok {
				t.Fatalf("RealityTarget = %q, %q, %v", target, host, ok)
			}
		})
	}
}
