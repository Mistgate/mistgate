package access

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A target chosen on the node (UpdateInbound / CreateInbound reality_target_override): the node shows that site instead of the profile's,
// and the people of the node get its name in every format of their subscription.

func (e *env) specOf(inboundID string) plugin.InboundSpec {
	e.t.Helper()
	for _, f := range must(e.st.Access().InboundsFull(e.ctx, "")) {
		if f.Inbound.ID != inboundID {
			continue
		}
		merged, err := e.s.mergedSettings(f.Profile)
		if err != nil {
			e.t.Fatal(err)
		}
		return must(e.s.buildSpec(f, merged))
	}
	e.t.Fatalf("no inbound %s", inboundID)
	return plugin.InboundSpec{}
}

func TestUpdateInboundSetsAndResetsTheTargetOfTheNode(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	in := e.inbound(e.vlessProfile("vless site").Id, "nod_de1")
	e.sql(`UPDATE inbound SET state = 'active' WHERE id = ?`, in.Id)
	if in.RealityTarget != "www.example.org:443" || in.RealityTargetOverride != "" || in.TlsServerName != "" {
		t.Fatalf("before: target %q override %q name %q", in.RealityTarget, in.RealityTargetOverride, in.TlsServerName)
	}
	e.resetNotify()

	// the check shows what it would be and writes nothing
	checked := must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{
		InboundId: in.Id, RealityTargetOverride: new("shop.example.com:443"), ValidateOnly: true,
	}))).Msg.Inbound
	if checked.RealityTarget != "shop.example.com:443" || checked.TlsServerName != "shop.example.com" {
		t.Fatalf("checked: target %q name %q", checked.RealityTarget, checked.TlsServerName)
	}
	stored := must(e.st.Access().Inbound(e.ctx, in.Id))
	if stored.RealityTargetOverride != "" || stored.SpecVersion != 1 || stored.State != "active" || e.notify.n.Load() != 0 {
		t.Fatalf("the check wrote %+v (notified %d)", stored, e.notify.n.Load())
	}

	// the change: a new spec version, back to pending, the node is told, the audit row says which site
	set := must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new(" shop.example.com:443 ")}))).Msg.Inbound
	stored = must(e.st.Access().Inbound(e.ctx, in.Id))
	if stored.RealityTargetOverride != "shop.example.com:443" || stored.SpecVersion != 2 || stored.State != "pending" || e.notify.n.Load() != 1 {
		t.Fatalf("stored %+v (notified %d)", stored, e.notify.n.Load())
	}
	if set.RealityTargetOverride != "shop.example.com:443" || set.RealityTarget != "shop.example.com:443" || set.TlsServerName != "shop.example.com" {
		t.Errorf("answer %+v", set)
	}
	spec := e.specOf(in.Id)
	if spec.TLS != (plugin.TLS{ServerName: "shop.example.com"}) || plugin.RealityTargetShown(spec) != "shop.example.com:443" || spec.Version != 2 {
		t.Errorf("spec %+v", spec)
	}
	if rows := e.audited("inbound_update"); len(rows) != 1 || !strings.Contains(rows[0].Params, `"reality_target":"shop.example.com:443"`) {
		t.Errorf("audit %+v", rows)
	}

	// the same value again is no change; an absent field leaves it
	e.resetNotify()
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new("shop.example.com:443")})))
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, Enabled: new(true)})))
	if stored = must(e.st.Access().Inbound(e.ctx, in.Id)); stored.SpecVersion != 2 || stored.RealityTargetOverride != "shop.example.com:443" || e.notify.n.Load() != 0 {
		t.Fatalf("no change: %+v (notified %d)", stored, e.notify.n.Load())
	}

	// "" puts the profile's target back
	back := must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new("")}))).Msg.Inbound
	if stored = must(e.st.Access().Inbound(e.ctx, in.Id)); stored.RealityTargetOverride != "" || stored.SpecVersion != 3 ||
		back.RealityTarget != "www.example.org:443" || back.TlsServerName != "" || e.specOf(in.Id).TLS != (plugin.TLS{}) {
		t.Fatalf("reset: %+v, answer %+v", stored, back)
	}
}

func TestTargetOfTheNodeRefusalsAreCodes(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	site := e.vlessProfile("vless site")
	in := e.inbound(site.Id, "nod_de1")
	hy2 := e.profile("hy2", "")
	hy2in := e.inbound(hy2.Id, "nod_de1")
	tlsProfile := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "vless tls",
		SettingsJson: `{"security":"tls","transport":"ws","tls_mode":"acme_domain","ws":{"path":"/ws","host":""}}`}))).Msg.Profile
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")

	update := func(id, target string) error {
		_, err := e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: id, RealityTargetOverride: &target, ValidateOnly: true}))
		return err
	}
	wantCoded(t, update(in.Id, "198.51.100.7:443"), connect.CodeInvalidArgument, "reality_target_invalid", map[string]string{"target": "198.51.100.7:443"})
	wantCoded(t, update(in.Id, "shop.example.com"), connect.CodeInvalidArgument, "reality_target_invalid", map[string]string{"target": "shop.example.com"})
	wantCoded(t, update(in.Id, "de1.example.com:443"), connect.CodeInvalidArgument, "reality_target_same_node", map[string]string{"target": "de1.example.com:443", "node": "de1"})
	wantCoded(t, update(hy2in.Id, "shop.example.com:443"), connect.CodeInvalidArgument, "reality_target_unsupported", nil)
	// the same refusals before the click, for a profile not yet on the node
	_, err := e.check(&adminv1.CreateInboundRequest{ProfileId: tlsProfile.Id, NodeId: "nod_nl1", RealityTargetOverride: "shop.example.com:443"})
	wantCoded(t, err, connect.CodeInvalidArgument, "reality_target_unsupported", nil)
	_, err = e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: hy2.Id, NodeId: "nod_nl1", RealityTargetOverride: "shop.example.com:443"}))
	wantCoded(t, err, connect.CodeInvalidArgument, "reality_target_unsupported", nil)
	if n := e.count(`SELECT count(*) FROM inbound WHERE reality_target_override != ''`); n != 0 {
		t.Fatalf("a refused target was written: %d", n)
	}
	// a node can show the target on a new inbound
	made := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_nl1", RealityTargetOverride: "shop.example.com:443"}))).Msg.Inbound
	if made.RealityTargetOverride != "shop.example.com:443" || made.TlsServerName != "shop.example.com" {
		t.Errorf("created %+v", made)
	}
}

// A node whose agent is known not to take the target (no vless-target/1) is refused with agent_too_old; one that never reported its
// capabilities is not, its inbound waits for it.
func TestTargetOfTheNodeNeedsAnAgentThatTakesIt(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	in := e.inbound(e.vlessProfile("vless site").Id, "nod_de1")
	set := func() error {
		_, err := e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new("shop.example.com:443")}))
		return err
	}
	e.sql(`UPDATE node SET agent_caps = 'vless/1 vless-self/1 vless-tls/1' WHERE id = 'nod_de1'`)
	wantCoded(t, set(), connect.CodeFailedPrecondition, "agent_too_old", nil)
	e.sql(`UPDATE node SET agent_caps = 'vless/1 vless-target/1' WHERE id = 'nod_de1'`)
	if err := set(); err != nil {
		t.Fatalf("an agent with vless-target/1: %v", err)
	}
}

// Every format of the subscription gives a person the site of the node that has a target of its own, and the profile's for the others;
// a node without a target of its own gives what it gave before.
func TestSubscriptionsOfTwoNodesCarryEachNodesTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile func(e *env) *adminv1.ProfileSummary
		before  string
	}{
		{"another site", func(e *env) *adminv1.ProfileSummary {
			return must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "vless site", SettingsJson: `{"transport":"tcp","reality":{"target":"www.example.org:443"}}`}))).Msg.Profile
		}, "www.example.org"},
		{"the node's own site", func(e *env) *adminv1.ProfileSummary {
			return must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "vless self", SettingsJson: `{"transport":"tcp"}`}))).Msg.Profile
		}, "nl1.example.com"},
	} {
		e := newEnv(t)
		e.node("nod_de1", "de1", "de1.example.com", "active")
		e.node("nod_nl1", "nl1", "nl1.example.com", "active")
		p := tc.profile(e)
		de1, nl1 := e.inbound(p.Id, "nod_de1"), e.inbound(p.Id, "nod_nl1")
		token := e.tokenOf(e.user("alice", e.group("g", p.Id), nil).User.Id)
		lines := func(format plugin.ClientFormat) []string {
			t.Helper()
			view := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Format: format, NoTouch: true, NoPageData: true}))
			return view.Lines
		}
		plain := map[plugin.ClientFormat][]string{}
		for _, format := range linkFormats {
			plain[format] = lines(format)
			if len(plain[format]) != 2 {
				t.Fatalf("%s %s: %d lines", tc.name, format, len(plain[format]))
			}
		}
		must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: de1.Id, RealityTargetOverride: new("shop.example.com:443")})))
		for _, format := range linkFormats {
			now := lines(format)
			var own, other string // the line of de1 (own target), the line of nl1
			for _, l := range now {
				if strings.Contains(l, "de1") && !strings.Contains(l, "nl1") {
					own = l
				} else {
					other = l
				}
			}
			if !strings.Contains(own, "shop.example.com") || strings.Contains(own, tc.before) && tc.name == "another site" {
				t.Errorf("%s %s: the node with a target of its own gives\n%s", tc.name, format, own)
			}
			// nl1 gives what it gave before, byte for byte
			if !slicesContain(plain[format], other) {
				t.Errorf("%s %s: the node without one changed:\n%s", tc.name, format, other)
			}
		}
		_ = nl1
	}
}

func slicesContain(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

// The WARP twin of a profile keeps the target of each node as it keeps its name.
func TestTwinKeepsTheTargetOfTheNode(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	p := e.vlessProfile("vless site")
	de1 := e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_nl1")
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: de1.Id, RealityTargetOverride: new("shop.example.com:443")})))
	e.group("default", p.Id)
	res, err := e.twin(p.Id, "warp", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range must(e.st.Access().InboundsOfProfile(e.ctx, res.Profile.Id)) {
		got[row.NodeID] = row.RealityTargetOverride
	}
	if len(got) != 2 || got["nod_de1"] != "shop.example.com:443" || got["nod_nl1"] != "" {
		t.Fatalf("the twin's targets of the nodes = %v", got)
	}
}

// Moving a node's REALITY from its own site to a neighbour on 443 is judged by the spec the node gets: the Hysteria2 site there
// yields now to another site, and the warning says which.
func TestTargetOfTheNodeChangesTheDecoyWarningFromSelfToSite(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.inbound(e.profile("hy2", "").Id, "nod_de1")
	in := e.inbound(e.vlessSelfProfile("vless self").Id, "nod_de1")
	via := func(target *string) string {
		t.Helper()
		r := must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: target, ValidateOnly: true}))).Msg
		w := warningByCode(r.Warnings, "hy2_decoy_paused")
		if w == nil {
			t.Fatalf("no hy2_decoy_paused in %+v", r.Warnings)
		}
		return w.Params["via"] + " " + w.Params["target"]
	}
	if got := via(nil); got != "self " {
		t.Errorf("own site: %q", got)
	}
	if got := via(new("shop.example.com:443")); got != "site shop.example.com" {
		t.Errorf("a neighbour: %q", got)
	}
}

func TestInboundDescribesWhatStrangersSee(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"self", "site", "hy2"} { // one node each: two VLESS servers cannot share TCP 443 without names
		e.node("nod_"+n, n, n+".example.com", "active")
	}
	for node, c := range map[string]struct {
		profile func() *adminv1.ProfileSummary
		want    string
	}{
		"nod_self": {func() *adminv1.ProfileSummary { return e.vlessSelfProfile("vless self") }, "self"},
		"nod_site": {func() *adminv1.ProfileSummary { return e.vlessProfile("vless site") }, "www.example.org:443"},
		"nod_hy2":  {func() *adminv1.ProfileSummary { return e.profile("hy2", "") }, ""},
	} {
		id := e.inbound(c.profile().Id, node).Id
		r := must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: id, ValidateOnly: true}))).Msg.Inbound
		if r.RealityTarget != c.want {
			t.Errorf("%s: reality_target %q, want %q", node, r.RealityTarget, c.want)
		}
	}
}
