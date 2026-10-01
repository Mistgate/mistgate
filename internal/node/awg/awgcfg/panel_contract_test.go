package awgcfg

import (
	"os"
	"path/filepath"
	"testing"
)

// The panel plugin writes the node's settings_json; its golden files are the exact bytes a node receives. They
// must parse here and pass the node's own validator: a flat document once made every awg inbound fail with
// /obfuscation/h1: required while all unit tests of both sides were green.
func TestPanelNodeSettingsGoldenAreAcceptedByTheNode(t *testing.T) {
	for _, name := range []string{"node_settings_31.json", "node_settings_20.json"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "panel", "protocols", "awg", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		s, err := ParseSettings(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.PrivateKey == "" || s.Obfuscation.H1.IsZero() || s.Obfuscation.H4.IsZero() || s.Obfuscation.Jc == 0 {
			t.Errorf("%s: the node read an empty obfuscation block: %+v", name, s.Obfuscation)
		}
		if r := Validate(s, Options{MTU: 1280}); !r.OK() {
			t.Errorf("%s: %v", name, r.Err())
		}
		// and the node's own canonical form is what the panel sends, byte for byte
		again, err := s.NodeJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(again)+"\n" != string(raw) {
			t.Errorf("%s: panel bytes differ from awgcfg.Settings.NodeJSON():\n%s\n%s", name, raw, again)
		}
	}
}
