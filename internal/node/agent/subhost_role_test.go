package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The role is saved to agent.json before anything runs on it, so the agent comes back with it after a restart (before its
// first session, which runs through the role); a panel that sends none clears it; the path never reaches the log.
func TestSubHostRoleIsKeptInAgentJSON(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	a := &Agent{cfg: Config{StateDir: dir}, log: slog.New(slog.NewTextHandler(&logs, nil)), meta: Meta{NodeID: "nod_host", Link: "wss://sub.example.com/" + "p0123456789abcdefghijk" + "/"}}
	role := &pb.SubHostRole{Names: []string{"sub.example.com"}, Path: "/secret-bridge-path-0123", SiteName: "de1.example.com",
		LinkSources: []string{"203.0.113.5/32", "2001:db8:1::1/64", "not a prefix"}}
	readBack := func() Meta {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, fileMeta))
		if err != nil {
			t.Fatal(err)
		}
		var m Meta
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	if !a.applySubHost(&pb.NodeSettings{SubHost: role}) {
		t.Fatal("a new role is a change")
	}
	saved := readBack()
	if saved.SubHost == nil || saved.SubHost.Path != role.Path || saved.SubHost.SiteName != "de1.example.com" || saved.Link != a.meta.Link {
		t.Fatalf("agent.json: %+v", saved.SubHost)
	}
	got, ok := a.hostRole()
	if !ok || len(got.LinkSources) != 2 || got.LinkSources[1] != netip.MustParsePrefix("2001:db8:1::/64") {
		t.Fatalf("role %+v", got)
	}
	restarted := &Agent{meta: saved}
	if r, ok := restarted.hostRole(); !ok || r.Path != role.Path {
		t.Fatal("a restarted agent must route with the saved role")
	}

	if a.applySubHost(&pb.NodeSettings{SubHost: role}) || a.applySubHost(nil) {
		t.Fatal("the same role, or no settings at all, is no change")
	}
	if !a.applySubHost(&pb.NodeSettings{}) || readBack().SubHost != nil {
		t.Fatal("settings without a role clear it, in agent.json too")
	}
	if _, ok := a.hostRole(); ok {
		t.Fatal("no role after it was cleared")
	}
	if bytes.Contains(logs.Bytes(), []byte(role.Path)) {
		t.Fatal("the bridge path reached the log")
	}
}
