package access

import (
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A profile whose REALITY target is the node's own site: no target in the settings, the default kind.
func (e *env) vlessSelfProfile(name string) *adminv1.ProfileSummary {
	e.t.Helper()
	return must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: name, SettingsJson: `{}`}))).Msg.Profile
}

func TestVLESSSelfNeedsADomainBeforeTheClick(t *testing.T) {
	e := newEnv(t)
	e.node("nod_ip", "ip1", "203.0.113.10", "active")
	e.node("nod_de1", "de1", "de1.example.com", "active")
	self := e.vlessSelfProfile("vless self")

	// An IP node: the node's own site has no name for Let's Encrypt. The refusal is its own code, not the Hysteria2 one.
	_, err := e.check(&adminv1.CreateInboundRequest{ProfileId: self.Id, NodeId: "nod_ip"})
	wantCoded(t, err, connect.CodeInvalidArgument, "reality_needs_domain", map[string]string{"address": "203.0.113.10", "node": "ip1"})
	_, err = e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: self.Id, NodeId: "nod_ip"}))
	wantCoded(t, err, connect.CodeInvalidArgument, "reality_needs_domain", map[string]string{"address": "203.0.113.10", "node": "ip1"})
	if n := e.count(`SELECT count(*) FROM inbound`); n != 0 {
		t.Fatalf("a refused inbound was written: %d", n)
	}

	// A domain of its own for this inbound makes it pass, and a domain node passes as it is.
	ok, err := e.check(&adminv1.CreateInboundRequest{ProfileId: self.Id, NodeId: "nod_ip", TlsServerNameOverride: "vpn.example.com"})
	if err != nil || ok.Inbound.TlsServerName != "vpn.example.com" {
		t.Fatalf("with an SNI: %+v, %v", ok, err)
	}
	if ok, err = e.check(&adminv1.CreateInboundRequest{ProfileId: self.Id, NodeId: "nod_de1"}); err != nil || ok.Inbound.TlsServerName != "de1.example.com" {
		t.Fatalf("domain node: %+v, %v", ok, err)
	}
	// An IP typed as the name is the field's problem.
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: self.Id, NodeId: "nod_ip", TlsServerNameOverride: "203.0.113.10"})
	wantCoded(t, err, connect.CodeInvalidArgument, "sni_needs_domain", map[string]string{"name": "203.0.113.10"})

	// Another site as the target needs no domain: the same IP node takes it.
	site := e.vlessProfile("vless site")
	if _, err := e.check(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_ip"}); err != nil {
		t.Fatalf("another site on an IP node: %v", err)
	}
}

func TestVLESSSelfDecoyWarningBorrowsNoSite(t *testing.T) {
	assertSelf := func(t *testing.T, got []*adminv1.StatusReason) {
		t.Helper()
		if len(got) != 1 || got[0].Code != "hy2_decoy_paused" {
			t.Fatalf("warnings = %+v", got)
		}
		want := map[string]string{"node": "de1", "port": "443", "hy2": "hy2", "vless": "vless", "via": "self", "target": "", "port80": "unchecked"}
		for key, value := range want {
			if got[0].Params[key] != value {
				t.Errorf("param %s = %q, want %q", key, got[0].Params[key], value)
			}
		}
	}
	t.Run("adding VLESS beside Hysteria2", func(t *testing.T) {
		e := newEnv(t)
		e.node("nod_de1", "de1", "de1.example.com", "active")
		e.inbound(e.profile("hy2", "").Id, "nod_de1")
		checked, err := e.check(&adminv1.CreateInboundRequest{ProfileId: e.vlessSelfProfile("vless").Id, NodeId: "nod_de1"})
		if err != nil {
			t.Fatal(err)
		}
		assertSelf(t, checked.Warnings)
	})
	t.Run("adding Hysteria2 beside VLESS", func(t *testing.T) {
		e := newEnv(t)
		e.node("nod_de1", "de1", "de1.example.com", "active")
		e.inbound(e.vlessSelfProfile("vless").Id, "nod_de1")
		checked, err := e.check(&adminv1.CreateInboundRequest{ProfileId: e.profile("hy2", "").Id, NodeId: "nod_de1"})
		if err != nil {
			t.Fatal(err)
		}
		assertSelf(t, checked.Warnings)
	})
	t.Run("a self-signed Hysteria2 certificate does not renew over port 80", func(t *testing.T) {
		e := newEnv(t)
		e.node("nod_de1", "de1", "de1.example.com", "active")
		e.inbound(e.profile("hy2", `{"tls_mode":"self_signed"}`).Id, "nod_de1")
		checked, err := e.check(&adminv1.CreateInboundRequest{ProfileId: e.vlessSelfProfile("vless").Id, NodeId: "nod_de1"})
		if err != nil || len(checked.Warnings) != 1 {
			t.Fatalf("%+v, %v", checked, err)
		}
		if p := checked.Warnings[0].Params; p["via"] != "self" || p["port80"] != "" {
			t.Errorf("params = %v", p)
		}
	})
}

// Profiles made in v0.1.34 have a target and no target_kind: they must build the same spec and render the same links
// whether or not the migration has written the kind yet (it does, see the store test; this is the panel reading the
// document as it was stored).
func TestVLESSProfileWithoutTargetKindBuildsAndRendersAsBefore(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	profile := e.vlessProfile("vless") // a target and no kind in the request: another site
	e.inbound(profile.Id, "nod_de1")
	group := e.group("vless", profile.Id)
	token := e.tokenOf(e.user("alice", group, nil).User.Id)

	state := func() (spec plugin.InboundSpec, line string) {
		t.Helper()
		for _, f := range must(e.st.Access().InboundsFull(e.ctx, "")) {
			if f.Profile.ID != profile.Id {
				continue
			}
			merged, err := e.s.mergedSettings(f.Profile)
			if err != nil {
				t.Fatal(err)
			}
			if spec, err = e.s.buildSpec(f, merged); err != nil {
				t.Fatal(err)
			}
		}
		view := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true, NoPageData: true}))
		if len(view.Lines) != 1 {
			t.Fatalf("lines = %v", view.Lines)
		}
		return spec, view.Lines[0]
	}
	var stored string
	if err := e.st.R.QueryRow(`SELECT settings_json FROM profile WHERE id = ?`, profile.Id).Scan(&stored); err != nil || !strings.Contains(stored, `"target_kind":"site"`) {
		t.Fatalf("the profile was stored as %s (%v), want the kind written down", stored, err)
	}
	specNow, lineNow := state()

	// the same profile as v0.1.34 stored it
	e.sql(`UPDATE profile SET settings_json = json_remove(settings_json, '$.reality.target_kind') WHERE id = ?`, profile.Id)
	specOld, lineOld := state()

	if string(specNow.Settings) != string(specOld.Settings) || specNow.TLS != specOld.TLS || specOld.TLS != (plugin.TLS{}) {
		t.Errorf("the spec changed:\n%s %+v\n%s %+v", specNow.Settings, specNow.TLS, specOld.Settings, specOld.TLS)
	}
	if lineNow != lineOld {
		t.Errorf("the link changed:\n%s\n%s", lineNow, lineOld)
	}
	var node struct {
		Reality struct {
			Target      string   `json:"target"`
			ServerNames []string `json:"server_names"`
		} `json:"reality"`
	}
	if err := json.Unmarshal(specOld.Settings, &node); err != nil || node.Reality.Target != "www.example.org:443" || len(node.Reality.ServerNames) != 1 || node.Reality.ServerNames[0] != "www.example.org" {
		t.Errorf("node settings = %s (%v)", specOld.Settings, err)
	}
}

func TestVLESSSelfSpecAndLink(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	profile := e.vlessSelfProfile("vless self")
	in := e.inbound(profile.Id, "nod_de1")
	group := e.group("vless", profile.Id)
	token := e.tokenOf(e.user("alice", group, nil).User.Id)

	var spec plugin.InboundSpec
	for _, f := range must(e.st.Access().InboundsFull(e.ctx, "")) {
		if f.Inbound.ID != in.Id {
			continue
		}
		merged, err := e.s.mergedSettings(f.Profile)
		if err != nil {
			t.Fatal(err)
		}
		if spec, err = e.s.buildSpec(f, merged); err != nil {
			t.Fatal(err)
		}
	}
	if spec.TLS != (plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "de1.example.com"}) || !strings.Contains(string(spec.Settings), `"target":"self"`) {
		t.Fatalf("spec = %+v %s", spec.TLS, spec.Settings)
	}
	view := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true, NoPageData: true}))
	if len(view.Lines) != 1 || !strings.Contains(view.Lines[0], "&sni=de1.example.com&") {
		t.Fatalf("link = %v", view.Lines)
	}
}

// Switching a profile that is already on an IP node to the node's own site is refused with the code the editor words.
func TestVLESSProfileSwitchToSelfIsRefusedOnAnIPNode(t *testing.T) {
	e := newEnv(t)
	e.node("nod_ip", "ip1", "203.0.113.10", "active")
	profile := e.vlessProfile("vless")
	e.inbound(profile.Id, "nod_ip")
	_, err := e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{
		ProfileId: profile.Id, ExpectedVersion: profile.Version, SettingsJson: new(`{"reality":{"target_kind":"self"}}`), DryRun: true,
	}))
	wantCoded(t, err, connect.CodeInvalidArgument, "reality_needs_domain", map[string]string{"address": "203.0.113.10", "node": "ip1"})
}
