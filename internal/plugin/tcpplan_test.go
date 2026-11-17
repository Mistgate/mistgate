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
