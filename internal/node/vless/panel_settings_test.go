package vless

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The panel plugin builds the settings, this package parses them (the second line of defence). Every combination the
// panel can build must be one the node accepts, with the same meaning.
func TestPanelBuildInboundSettingsAcceptedByNodeParser(t *testing.T) {
	panel := panelvless.New()
	init, err := panel.InitInbound(protocols.InboundInitInput{InboundID: "inb_1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*panelvless.Settings)
		check  func(t *testing.T, got settings)
	}{
		{"xhttp defaults", func(s *panelvless.Settings) {}, func(t *testing.T, got settings) {
			if got.transport != "xhttp" || got.flow != "" || got.xhttp.mode != "auto" || got.xhttp.xPaddingFrom != 100 || got.xhttp.xPaddingTo != 1000 {
				t.Errorf("parsed = %+v", got)
			}
		}},
		{"tcp with Vision", func(s *panelvless.Settings) { s.Transport = "tcp" }, func(t *testing.T, got settings) {
			if got.transport != "tcp" || got.flow != "xtls-rprx-vision" {
				t.Errorf("parsed = %+v", got)
			}
		}},
		{"every optional field", func(s *panelvless.Settings) {
			s.Port = 8443
			s.Reality.ServerNames = []string{"www.example.org", "cdn.example.org"}
			s.Reality.ShortIDs = []string{"ab", "0123456789abcdef"}
			s.XHTTP.Host, s.XHTTP.Mode, s.XHTTP.XPaddingBytes = "cover.example.org", "stream-up", "1-4096"
			s.Egress = "warp"
		}, func(t *testing.T, got settings) {
			if len(got.reality.serverNames) != 2 || len(got.reality.shortIDs) != 2 || got.xhttp.host != "cover.example.org" ||
				got.xhttp.mode != "stream-up" || got.xhttp.xPaddingFrom != 1 || got.xhttp.xPaddingTo != 4096 {
				t.Errorf("parsed = %+v", got)
			}
		}},
		{"IPv6 target with names", func(s *panelvless.Settings) {
			s.Reality.Target = "[2001:db8::1]:443"
			s.Reality.ServerNames = []string{"www.example.org"}
		}, func(t *testing.T, got settings) {
			if got.reality.target != "[2001:db8::1]:443" {
				t.Errorf("parsed = %+v", got)
			}
		}},
		{"the node's own site", func(s *panelvless.Settings) { s.Reality.TargetKind, s.Reality.Target = "self", "" }, func(t *testing.T, got settings) {
			if !got.reality.self || got.reality.target != "self" || len(got.reality.serverNames) != 1 || got.reality.serverNames[0] != "de1.example.com" {
				t.Errorf("parsed = %+v", got)
			}
		}},
		{"names follow the target", func(s *panelvless.Settings) { s.Reality.ServerNames = nil }, func(t *testing.T, got settings) {
			if len(got.reality.serverNames) != 1 || got.reality.serverNames[0] != "www.example.org" {
				t.Errorf("parsed = %+v", got)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defaults, err := panel.DefaultSettings()
			if err != nil {
				t.Fatal(err)
			}
			var s panelvless.Settings
			if err := json.Unmarshal(defaults, &s); err != nil {
				t.Fatal(err)
			}
			s.Reality.TargetKind, s.Reality.Target = "site", "www.example.org:443"
			tc.mutate(&s)
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if raw, err = panel.NormalizeSettings(protocols.NormalizeInput{Merged: raw, Save: true}); err != nil {
				t.Fatal(err)
			}
			if errs := panel.Validate(raw); len(errs) != 0 {
				t.Fatalf("panel refuses its own settings: %+v", errs)
			}
			spec, err := panel.BuildInbound(protocols.InboundInput{
				Profile:     protocols.ProfileView{ID: "prf_1", Settings: raw},
				Node:        protocols.NodeView{ID: "nod_de1", Name: "de1", Address: "de1.example.com"},
				SpecVersion: 1, Enabled: true, InboundID: "inb_1", PluginState: init.State, PluginPublic: init.Public,
			})
			if err != nil {
				t.Fatal(err)
			}
			spec.ID = "inb_1"
			got, err := parseSpec(spec)
			if err != nil {
				t.Fatalf("the node refuses the panel's settings %s: %v", spec.Settings, err)
			}
			if spec.Listen != (plugin.Listen{Network: "tcp", Port: uint16(s.Port)}) || got.security != "reality" ||
				base64.RawURLEncoding.EncodeToString(got.reality.privateKey) != base64.RawURLEncoding.EncodeToString(init.State) {
				t.Errorf("listen = %+v, parsed = %+v", spec.Listen, got)
			}
			tc.check(t, got)
		})
	}
}
