package vless

import (
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
)

func TestPublicPortRejectsLoopbackViaPublicEndpoint(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "profile_target", true: "inbound_override"}[override], func(t *testing.T) {
			raw := validSettings(t, func(s *Settings) {
				s.Port, s.ClientPort = 19443, 443
				if !override {
					s.Reality.Target = "node.example.com:443"
					s.Reality.ServerNames = []string{"node.example.com"}
				}
			})
			in := protocols.InboundInput{Profile: protocols.ProfileView{Settings: raw}, Node: protocols.NodeView{Address: "node.example.com"}, PluginState: bytes32()}
			if override {
				in.RealityTargetOverride = "node.example.com:443"
			}
			_, err := New().BuildInbound(in)
			if err == nil {
				t.Fatal("accepted REALITY target pointing at the same inbound's public endpoint; 443 forwards to 19443")
			}
		})
	}
}
func TestPublicPortAllowsEmptyCDNObject(t *testing.T) {
	raw := validSettings(t, func(s *Settings) {
		s.Port, s.ClientPort = 19443, 443
		s.Security, s.Transport, s.TLSMode, s.SNI = "tls", "ws", "acme_domain", "origin.example.org"
		s.Reality = RealitySettings{}
		s.WS = &WSSettings{Path: "/proxy"}
		s.CDN = &CDNSettings{}
	})
	if errs := New().Validate(raw); len(errs) != 0 {
		t.Fatalf("empty CDN host is no CDN; public TCP port should remain valid: %+v", errs)
	}
}
