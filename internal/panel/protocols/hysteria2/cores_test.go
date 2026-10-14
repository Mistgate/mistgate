//go:build !js

package hysteria2

import (
	"encoding/hex"
	"strings"
	"testing"

	xsalamander "github.com/xtls/xray-core/transport/internet/finalmask/salamander"
	xhysteria "github.com/xtls/xray-core/transport/internet/hysteria"
	xtls "github.com/xtls/xray-core/transport/internet/tls"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/jsoncheck"
	"github.com/mistgate/mistgate/internal/plugin"
)

// checkWithCores asks the real cores: Xray's own config builder in process, and every sing-box binary found (the
// check is skipped without one: set SINGBOX_BIN, or download into ~/.cache/mistgate-tests/).
func checkWithCores(t *testing.T, format plugin.ClientFormat, outbound []byte) {
	t.Helper()
	switch format {
	case plugin.FormatXrayJSON:
		jsoncheck.XrayBuild(t, jsoncheck.XrayConfig(t, outbound))
	case plugin.FormatSingbox:
		if !jsoncheck.SingboxCheck(t, jsoncheck.SingboxConfig(t, outbound)) {
			t.Log("no sing-box binary: set SINGBOX_BIN; the sing-box check is skipped")
		}
	}
}

// What Xray's config builder makes of the fragments is what the matrix says: the auth string, the whole-certificate
// pin without allowInsecure, and Salamander as a udp mask. A key the core ignores would show here as a zero value.
func TestXrayCoreReadsTheHysteria2Fields(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	cases := []struct {
		row                string
		in                 protocols.RenderInput
		wantPin, wantSalam bool
	}{
		{"salamander", jsonIn(t, plugin.FormatXrayJSON, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil), false, true},
		{"pinned", jsonIn(t, plugin.FormatXrayJSON, inbound(plugin.TLSSelfSigned, "203.0.113.10", pin), nil), true, true},
		{"pinned_noobfs", jsonIn(t, plugin.FormatXrayJSON, inbound(plugin.TLSSelfSigned, "203.0.113.10", pin), func(s *Settings) { s.Obfs = Obfs{Type: "none"} }), true, false},
	}
	for _, c := range cases {
		t.Run(c.row, func(t *testing.T) {
			f, ok := New().Render(c.in)
			if !ok {
				t.Fatal("refused")
			}
			streams := jsoncheck.XrayStreams(t, jsoncheck.XrayConfig(t, f.Data))
			s := streams[0]
			if s.ProtocolName != "hysteria" {
				t.Errorf("transport %q", s.ProtocolName)
			}
			hy := jsoncheck.XrayTransport(t, s, "hysteria").(*xhysteria.Config)
			if hy.Version != 2 || hy.Auth != "tok-DEVICE_secret_0123456789abcdefghijklmn" {
				t.Errorf("hysteria config %v", hy)
			}
			tc := jsoncheck.XraySecurity(t, s).(*xtls.Config)
			if c.wantPin != (len(tc.PinnedPeerCertSha256) == 1 && hex.EncodeToString(tc.PinnedPeerCertSha256[0]) == pin) || tc.AllowInsecure {
				t.Errorf("tls: pins %x, allowInsecure %v", tc.PinnedPeerCertSha256, tc.AllowInsecure)
			}
			if len(tc.NextProtocol) != 1 || tc.NextProtocol[0] != "h3" {
				t.Errorf("alpn %v", tc.NextProtocol)
			}
			if c.wantSalam != (len(s.Udpmasks) == 1) {
				t.Fatalf("udp masks %v", s.Udpmasks)
			}
			if c.wantSalam {
				m, err := s.Udpmasks[0].GetInstance()
				if sal, ok := m.(*xsalamander.Config); err != nil || !ok || sal.Password != "gawrgura-pw" {
					t.Errorf("mask %v %v", m, err)
				}
			}
		})
	}
}
