package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// set-panel on the state directory of an mTLS node (agent.json without link) exits 1 and leaves agent.json as it was.
func TestSetPanelRefusesAnMTLSNode(t *testing.T) {
	dir := t.TempDir()
	meta := []byte(`{"panel":"203.0.113.10:443","sni":"s1.example.com","node_id":"nod_de1"}`)
	if err := os.WriteFile(filepath.Join(dir, "identity.pem"), []byte("enrolled"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	link := "wss://panel.example.com/" + strings.Repeat("n", 24) + "/"
	if code := dispatch([]string{"set-panel", "--link-url", link, "--state-dir", dir}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	got, err := os.ReadFile(filepath.Join(dir, "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, meta) {
		t.Fatalf("agent.json changed: %s", got)
	}
}
