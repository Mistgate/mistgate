package fleet

import (
	"strings"
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A host's role reaches its agent only, and only one with sub-host/1; nobody else's settings or signature change, and the
// signature holds the role's hash, never the bridge path.
func TestSubHostRoleInNodeSettings(t *testing.T) {
	host := store.NodeRow{ID: "nod_host", DialTimeoutS: 30, CountryCode: "DE", AwgBackend: "userspace"}
	other := store.NodeRow{ID: "nod_other", DialTimeoutS: 30, CountryCode: "DE", AwgBackend: "userspace"}
	capable := []string{plugin.CapSubHost}
	f := &Fleet{}
	base := settingsSig(f.nodeSettings(host, capable))

	const path = "/sub-host-bridge-path-0123"
	role := &agentv1.SubHostRole{Names: []string{"sub.example.com"}, Path: path, SiteName: "de1.example.com", LinkSources: []string{"203.0.113.5/32"}}
	f.SetSubHostRoles(map[string]*agentv1.SubHostRole{host.ID: role})
	s := f.nodeSettings(host, capable)
	if s.GetSubHost().GetPath() != path {
		t.Fatalf("the host's agent got %+v", s.GetSubHost())
	}
	sig := settingsSig(s)
	if sig == base || strings.Contains(sig, path) || !strings.Contains(sig, "|sub-host=") {
		t.Fatalf("signature %q", sig)
	}
	if got := f.nodeSettings(host, nil); got.SubHost != nil || settingsSig(got) != base {
		t.Fatal("an agent without sub-host/1 must get no role")
	}
	if got := f.nodeSettings(other, capable); got.SubHost != nil || settingsSig(got) != base {
		t.Fatal("another node must get no role")
	}
	role.Path = "/changed" // the fleet keeps its own copy for what it sends
	if f.nodeSettings(host, capable).GetSubHost() == role {
		t.Fatal("the settings must not share the caller's message")
	}
	f.SetSubHostRoles(nil)
	if got := f.nodeSettings(host, capable); got.SubHost != nil || settingsSig(got) != base {
		t.Fatal("no role once cleared")
	}
}
