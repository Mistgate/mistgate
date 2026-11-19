//go:build !js

package subhost

import (
	"net/netip"
	"strings"
	"testing"
)

// The SSH install of the first host: prepare checks and stores nothing, activate turns the role on (again: no change), and
// deactivate undoes it unless the host is ready.
func TestInstallLifecycle(t *testing.T) {
	e := newEnv(t)
	ip := netip.MustParseAddr
	wantCode(t, func() error {
		_, err := e.svc.PrepareInstall(e.ctx, "203.0.113.9", "sub.example.com", "", true)
		return err
	}(), "sub_host_node_unfit")
	wantCode(t, func() error {
		_, err := e.svc.PrepareInstall(e.ctx, "203.0.113.9", "sub.example.com", "sub.example.com", true)
		return err
	}(), "sub_host_node_unfit")
	// DNS: both names must point at the server
	e.res["sub.example.com"] = []netip.Addr{ip("203.0.113.9")}
	e.res["www.example.com"] = []netip.Addr{ip("198.51.100.1")}
	wantCode(t, func() error {
		_, err := e.svc.PrepareInstall(e.ctx, "203.0.113.9", "sub.example.com", "www.example.com", false)
		return err
	}(), "sub_host_dns_mismatch")
	e.res["www.example.com"] = []netip.Addr{ip("203.0.113.9")}
	path, err := e.svc.PrepareInstall(e.ctx, "203.0.113.9", "Sub.Example.com.", "www.example.com", false)
	if err != nil || !strings.HasPrefix(path, "/") || len(path) != 25 {
		t.Fatalf("prepare = %q %v", path, err)
	}
	if st := e.state(); st.State != stateOff {
		t.Fatalf("prepare stored state %+v", st)
	}
	// a node that cannot follow the panel to the new link stops it
	lacking := e.node("old", "203.0.113.1", nil, true)
	wantCode(t, func() error {
		_, err := e.svc.PrepareInstall(e.ctx, "203.0.113.9", "sub.example.com", "www.example.com", true)
		return err
	}(), "sub_host_nodes_lack_link")
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET state = 'retired' WHERE id = ?`, lacking); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.ActivateInstalled(e.ctx, "nod_h", "sub.example.com", "www.example.com", path, "aa"); err != nil {
		t.Fatal(err)
	}
	st := e.state()
	if st.State != stateOn || st.Node != "nod_h" || st.Path != path || st.SiteName != "www.example.com" || st.BootstrapPin != "aa" ||
		st.PublicURL != "https://old.example" || st.PrevURL != "https://old.example" || st.ReadyAt != "" {
		t.Fatalf("state %+v", st)
	}
	if e.fl.roles["nod_h"] == nil {
		t.Fatal("no role sent to the installed node")
	}
	// the same install again (a retry after the panel died in it): no change but a new pin
	if err := e.svc.ActivateInstalled(e.ctx, "nod_h", "sub.example.com", "www.example.com", path, "bb"); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateOn || st.BootstrapPin != "bb" {
		t.Fatalf("again: state %+v", st)
	}
	// another install while the role is on
	wantCode(t, e.svc.ActivateInstalled(e.ctx, "nod_x", "sub.example.com", "www.example.com", path, "cc"), "sub_host_wrong_state")
	wantCode(t, func() error {
		_, err := e.svc.PrepareInstall(e.ctx, "203.0.113.9", "sub.example.com", "www.example.com", true)
		return err
	}(), "sub_host_wrong_state")

	// deactivate: another node's undo changes nothing; a ready host is in use
	if err := e.svc.DeactivateInstalled(e.ctx, "nod_x"); err != nil || e.state().State != stateOn {
		t.Fatalf("foreign undo: %v %+v", err, e.state())
	}
	e.ready()
	wantCode(t, e.svc.DeactivateInstalled(e.ctx, "nod_h"), "sub_host_in_use")
	if err := e.svc.transition(e.ctx, func(st *state) error { st.ReadyAt, st.PublicURL = "", st.PrevURL; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeactivateInstalled(e.ctx, "nod_h"); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateOff || st.Path != "" || st.PublicURL != "https://old.example" {
		t.Fatalf("after undo: %+v", st)
	}
	if strings.Contains(e.logs.String(), path) {
		t.Fatal("the bridge path is in the log")
	}
}
