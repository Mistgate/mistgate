package plugin

import (
	"reflect"
	"testing"
)

func TestSideListens(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec InboundSpec
		want []Listen
	}{
		{"h2 only", InboundSpec{Protocol: "trusttunnel", Listen: Listen{Network: "tcp", Port: 443}, Settings: []byte(`{"h3":false}`)}, nil},
		{"h3", InboundSpec{Protocol: "trusttunnel", Listen: Listen{Network: "tcp", Port: 8443}, Settings: []byte(`{"h3":true}`)}, []Listen{{Network: "udp", Port: 8443}}},
		{"other protocol", InboundSpec{Protocol: "hysteria2", Listen: Listen{Network: "udp", Port: 443}, Settings: []byte(`{"h3":true}`)}, nil},
		{"missing settings", InboundSpec{Protocol: "trusttunnel", Listen: Listen{Network: "tcp", Port: 443}}, nil},
		{"invalid settings", InboundSpec{Protocol: "trusttunnel", Listen: Listen{Network: "tcp", Port: 443}, Settings: []byte(`not json`)}, nil},
		{"invalid port", InboundSpec{Protocol: "trusttunnel", Listen: Listen{Network: "tcp"}, Settings: []byte(`{"h3":true}`)}, nil},
		{"invalid primary network", InboundSpec{Protocol: "trusttunnel", Listen: Listen{Network: "udp", Port: 443}, Settings: []byte(`{"h3":true}`)}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := SideListens(tt.spec); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SideListens() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
