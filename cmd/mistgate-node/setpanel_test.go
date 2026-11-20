package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// set-panel on the state directory of an mTLS node (agent.json without link) saves the address as the next one and keeps
// the mTLS endpoint: the agent goes back to it if the link does not work (design/adr/0018-panel-behind-nat.md: the panel's
// address moved onto a subscription host).
func TestSetPanelOnAnMTLSNode(t *testing.T) {
	dir := t.TempDir()
	meta := []byte(`{"panel":"203.0.113.10:443","sni":"s1.example.com","node_id":"nod_de1"}`)
	if err := os.WriteFile(filepath.Join(dir, "identity.pem"), []byte("enrolled"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	link := "wss://panel.example.com/" + strings.Repeat("n", 24) + "/"
	if code := dispatch([]string{"set-panel", "--link-url", link, "--state-dir", dir}); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["next_link"] != link || got["panel"] != "203.0.113.10:443" || got["sni"] != "s1.example.com" || got["link"] != nil {
		t.Fatalf("agent.json = %s", raw)
	}
}
