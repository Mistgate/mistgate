package fleet

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// "IPv6 for clients": owner only, turning it off needs "client-ipv6/1", an old agent is never sent the field, and a
// change reaches a new agent as a settings-only delta.
func TestClientIPv6SettingIsOwnerOnlyNeedsCapabilityAndPropagates(t *testing.T) {
	x, a := newL3Env(t)
	service := nodeService{x.f}
	as := func(role string, v bool) error {
		ctx := auth.WithAdmin(x.ctx, store.Admin{ID: "adm_1", Role: role})
		_, err := service.UpdateNode(ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, ClientIpv6: &v}))
		return err
	}
	getNode := func() *adminv1.Node {
		t.Helper()
		resp, err := service.GetNode(x.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Node
	}

	if got := getNode(); !got.ClientIpv6 || got.ClientIpv6Supported {
		t.Fatalf("a new node: client_ipv6=%v supported=%v", got.ClientIpv6, got.ClientIpv6Supported)
	}
	// A helper (UpdateNode is open to helpers) and a call without an admin are refused, whatever the agent lists.
	for _, role := range []string{store.RoleHelper, store.RoleReadonly, ""} {
		if err := as(role, false); code(err) != connect.CodePermissionDenied {
			t.Fatalf("role %q turned the setting off: %v", role, err)
		}
	}
	if _, err := service.UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, ClientIpv6: ptr(false)})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("no admin in the context: %v", err)
	}
	// Other fields stay a helper's: the check is on the field, not on the call.
	hctx := auth.WithAdmin(x.ctx, store.Admin{ID: "adm_2", Role: store.RoleHelper})
	if _, err := service.UpdateNode(hctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, Notes: ptr("note")})); err != nil {
		t.Fatalf("a helper edits notes: %v", err)
	}

	// Off needs the capability.
	if err := as(store.RoleOwner, false); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Fatalf("off without client-ipv6/1: %v", err)
	}
	if n, _ := x.st.Node(x.ctx, a.nodeID); !n.ClientIPv6 {
		t.Fatal("a refused call changed the setting")
	}
	// On is always allowed, also for an agent that lost the capability.
	if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{ClientIPv6: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if err := as(store.RoleOwner, true); err != nil {
		t.Fatalf("on without client-ipv6/1: %v", err)
	}
	if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{ClientIPv6: ptr(false)}); err != nil {
		t.Fatal(err)
	}

	// An old agent is never sent the field and its settings signature does not move; a new one is.
	n, err := x.st.Node(x.ctx, a.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if n.ClientIPv6 {
		t.Fatal("the store kept the setting on")
	}
	oldSettings, newSettings := nodeSettings(n, nil), nodeSettings(n, []string{capClientIPv6})
	if oldSettings.ClientIpv6Disabled || !newSettings.ClientIpv6Disabled {
		t.Fatalf("settings: old agent %v, new agent %v", oldSettings.ClientIpv6Disabled, newSettings.ClientIpv6Disabled)
	}
	on := n
	on.ClientIPv6 = true
	if settingsSig(oldSettings) != settingsSig(nodeSettings(on, nil)) || settingsSig(oldSettings) != settingsSig(nodeSettings(on, []string{capClientIPv6})) {
		t.Fatal("the signature of an agent without the setting (or with it on) changed")
	}
	if settingsSig(newSettings) == settingsSig(nodeSettings(on, []string{capClientIPv6})) {
		t.Fatal("turning the setting off did not change the settings signature")
	}

	// A connected agent that lists the capability: initial settings, then a settings-only delta on the change.
	c, _, initial := connectCaps(a, "ipv6", capClientIPv6)
	if initial.Settings == nil || !initial.Settings.ClientIpv6Disabled {
		t.Fatalf("initial settings = %v", initial.Settings)
	}
	if got := getNode(); !got.ClientIpv6Supported || got.ClientIpv6 {
		t.Fatalf("node: supported=%v client_ipv6=%v", got.ClientIpv6Supported, got.ClientIpv6)
	}
	if err := as(store.RoleOwner, true); err != nil {
		t.Fatal(err)
	}
	delta := c.desired()
	if delta.Settings == nil || delta.Settings.ClientIpv6Disabled || len(delta.Inbounds) != 0 {
		t.Fatalf("settings delta = %v", delta)
	}
	if err := as(store.RoleOwner, false); err != nil { // the agent lists the capability now
		t.Fatal(err)
	}
	if delta := c.desired(); delta.Settings == nil || !delta.Settings.ClientIpv6Disabled || len(delta.Inbounds) != 0 {
		t.Fatalf("second settings delta = %v", delta)
	}
}
