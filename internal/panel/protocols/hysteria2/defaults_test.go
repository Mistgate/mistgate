package hysteria2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
)

// A new profile arrives with the secret already generated (never empty, never the same twice) and with the
// client-declared speed ignored.
func TestDefaultSettingsFairnessAndSecret(t *testing.T) {
	seen := map[string]bool{}
	for range 3 {
		raw, err := New().DefaultSettings()
		if err != nil {
			t.Fatal(err)
		}
		var s Settings
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if !s.IgnoreClientBandwidth {
			t.Error("ignore_client_bandwidth must default to true")
		}
		if s.Obfs.Type != "salamander" || len(s.Obfs.Password) < 8 || seen[s.Obfs.Password] {
			t.Errorf("obfs = %+v (seen before: %v)", s.Obfs, seen[s.Obfs.Password])
		}
		seen[s.Obfs.Password] = true
		if errs := New().Validate(raw); len(errs) != 0 {
			t.Errorf("the defaults do not validate: %v", errs)
		}
	}
}

func TestSchemaCarriesTheFairnessCopy(t *testing.T) {
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(New().SettingsSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	p := schema.Properties["ignore_client_bandwidth"]
	if p["default"] != true {
		t.Errorf("schema default = %v", p["default"])
	}
	for k, want := range map[string]string{
		"title":            "(always BBR)",
		"description":      "BBR shares it fairly",
		"x-title-ru":       "Не доверять заявленной скорости клиента (всегда BBR)",
		"x-description-ru": "BBR делит канал поровну",
	} {
		if s, _ := p[k].(string); !strings.Contains(s, want) {
			t.Errorf("%s = %q, want it to contain %q", k, s, want)
		}
	}
}

// The strict hop limits apply when a profile is written; a stored profile with an older range keeps building
// (the node refuses the range itself, F12) so an upgrade never turns a working inbound into a failed one.
func TestHopLimitsOnWriteNotOnBuild(t *testing.T) {
	legacy := settings(t, func(s *Settings) { s.Hop = Hop{500, 60000} })
	errs := New().Validate(legacy)
	if len(errs) == 0 || errs[0].Pointer != "/hop/from" {
		t.Fatalf("a range from 500 must be rejected on write: %v", errs)
	}
	if _, err := New().BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{ID: "prf_1", Settings: legacy},
		Node:    protocols.NodeView{ID: "nod_1", Name: "de1", Address: "de1.example.com"},
		Enabled: true,
	}); err != nil {
		t.Errorf("a stored profile with an old hop range must still build: %v", err)
	}
}
