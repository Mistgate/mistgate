package dns

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func (e *env) node(id, name string) {
	e.sql(`INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)`, id, name, name+".example.com")
}

func (e *env) offer(node string, def string, presets ...string) {
	e.t.Helper()
	if _, err := e.s.SetNodeDnsOptions(e.ctx, connect.NewRequest(&adminv1.SetNodeDnsOptionsRequest{NodeId: node, PresetIds: presets, DefaultPresetId: def})); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) pick(user, node, preset string) {
	e.t.Helper()
	if err := e.st.DNS().SetUserNodeChoice(e.ctx, user, node, preset, time.UnixMilli(1_700_000_000_000)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) onNode(user, node string) string {
	e.t.Helper()
	p, err := e.s.EffectiveOnNode(e.ctx, user, node)
	if err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

// The effective DNS of a person on a node: their pick while the node still offers it, else the node's default, else what
// applied before (their own preset, the group's, the instance default). A node that offers nothing changes nothing.
func TestEffectiveDNSOnANode(t *testing.T) {
	e := newEnv(t)
	e.node("nod_ru", "x1")
	e.node("nod_de", "y1")
	e.node("nod_nl", "z1")
	e.group("grp", "dns_builtin_family") // the group's preset: the rule of rung 3
	e.user("alice", "grp", "")
	e.user("bob", "grp", "dns_builtin_quad9") // bob's own preset wins over the group's, on rung 3
	e.offer("nod_ru", "dns_builtin_yandex", "dns_builtin_yandex", "dns_builtin_standard")
	e.offer("nod_de", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_family")
	// nod_nl offers nothing; nod_no_default has options but none is the default
	e.node("nod_nd", "w1")
	e.offer("nod_nd", "", "dns_builtin_adblock", "dns_builtin_standard")

	for _, c := range []struct{ user, node, want, why string }{
		{"alice", "nod_ru", "dns_builtin_yandex", "the node's default"},
		{"alice", "nod_de", "dns_builtin_adblock", "the node's default"},
		{"alice", "nod_nl", "dns_builtin_family", "no options: the group's preset, as before"},
		{"bob", "nod_nl", "dns_builtin_quad9", "no options: the user's own preset, as before"},
		{"bob", "nod_de", "dns_builtin_adblock", "options win over the user's own preset"},
		{"alice", "nod_nd", "dns_builtin_family", "options without a default: the old rule"},
		{"alice", "nod_x", "dns_builtin_family", "an unknown node: the old rule"},
	} {
		if got := e.onNode(c.user, c.node); got != c.want {
			t.Errorf("%s on %s: %s, want %s (%s)", c.user, c.node, got, c.want, c.why)
		}
	}

	e.pick("alice", "nod_de", "dns_builtin_standard")
	e.pick("alice", "nod_nd", "dns_builtin_standard")
	if got := e.onNode("alice", "nod_de"); got != "dns_builtin_standard" {
		t.Errorf("the pick: %s", got)
	}
	if got := e.onNode("alice", "nod_nd"); got != "dns_builtin_standard" {
		t.Errorf("the pick on a node without a default: %s", got)
	}
	if got := e.onNode("bob", "nod_de"); got != "dns_builtin_adblock" {
		t.Errorf("alice's pick reached bob: %s", got)
	}
	if got := e.onNode("alice", "nod_ru"); got != "dns_builtin_yandex" {
		t.Errorf("a pick on another node reached this one: %s", got)
	}
	// The node stops offering the preset: the pick stays but does nothing, the default applies.
	e.offer("nod_de", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_family")
	if got := e.onNode("alice", "nod_de"); got != "dns_builtin_adblock" {
		t.Errorf("a pick the node no longer offers: %s", got)
	}
	// Offered again, the pick works again.
	e.offer("nod_de", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	if got := e.onNode("alice", "nod_de"); got != "dns_builtin_standard" {
		t.Errorf("offered again: %s", got)
	}
	// The node offers nothing any more: the old rule, whatever was picked.
	e.offer("nod_de", "")
	if got := e.onNode("alice", "nod_de"); got != "dns_builtin_family" {
		t.Errorf("no options: %s", got)
	}
}

func TestSetNodeDnsOptionsRules(t *testing.T) {
	e := newEnv(t)
	e.node("nod_a", "a1")
	set := func(node string, def string, ids ...string) error {
		_, err := e.s.SetNodeDnsOptions(e.ctx, connect.NewRequest(&adminv1.SetNodeDnsOptionsRequest{NodeId: node, PresetIds: ids, DefaultPresetId: def}))
		return err
	}
	wantCode(t, set("nod_x", "", "dns_builtin_standard"), connect.CodeNotFound)
	wantCode(t, set("nod_a", "", "dns_nope"), connect.CodeInvalidArgument)
	wantCode(t, set("nod_a", "", "dns_builtin_standard", "dns_builtin_standard"), connect.CodeInvalidArgument)
	wantCode(t, set("nod_a", "dns_builtin_family", "dns_builtin_standard"), connect.CodeInvalidArgument) // the default must be offered
	var many []string
	for i := 0; i < MaxNodeOptions+1; i++ {
		many = append(many, "dns_builtin_standard")
	}
	wantCode(t, set("nod_a", "", many...), connect.CodeInvalidArgument)
	if l := e.listOffers(); len(l) != 0 {
		t.Fatalf("a refused write changed something: %v", l)
	}

	e.offer("nod_a", "dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_adblock")
	e.offer("nod_a", "dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_adblock") // unchanged: not audited again
	l := e.listOffers()
	if len(l) != 1 || strings.Join(l[0].PresetIds, ",") != "dns_builtin_standard,dns_builtin_adblock" || l[0].DefaultPresetId != "dns_builtin_adblock" {
		t.Fatalf("offers = %v", l)
	}
	e.offer("nod_a", "")
	if l := e.listOffers(); len(l) != 0 {
		t.Errorf("cleared: %v", l)
	}
	rows, err := e.st.ListAudit(e.ctx, "", 0, 20)
	if err != nil || len(rows) != 2 {
		t.Fatalf("audit rows: %v %v", rows, err)
	}
	for _, r := range rows {
		if r.Action != "node_dns_options" || !strings.Contains(r.Params, `"node":"nod_a"`) || !strings.Contains(r.Params, `"node_name":"a1"`) {
			t.Errorf("row %s %s", r.Action, r.Params)
		}
	}
	if !strings.Contains(rows[1].Params, `"presets":2`) || !strings.Contains(rows[1].Params, `"default":"dns_builtin_adblock"`) {
		t.Errorf("first row: %s", rows[1].Params)
	}
	// Deleting a preset that a node offers takes it out of the offers.
	e.offer("nod_a", "", "dns_builtin_standard")
	c, err := e.s.CreateDnsPreset(e.ctx, connect.NewRequest(&adminv1.CreateDnsPresetRequest{Name: "Mine", Servers: []*adminv1.DnsServer{plain("10.0.0.53")}}))
	if err != nil {
		t.Fatal(err)
	}
	e.offer("nod_a", c.Msg.Preset.Id, "dns_builtin_standard", c.Msg.Preset.Id)
	if _, err := e.s.DeleteDnsPreset(e.ctx, connect.NewRequest(&adminv1.DeleteDnsPresetRequest{Id: c.Msg.Preset.Id})); err != nil {
		t.Fatal(err)
	}
	if l := e.listOffers(); len(l) != 1 || strings.Join(l[0].PresetIds, ",") != "dns_builtin_standard" || l[0].DefaultPresetId != "" {
		t.Errorf("after the preset was deleted: %v", l)
	}
}

func (e *env) listOffers() []*adminv1.NodeDnsOptions {
	e.t.Helper()
	r, err := e.s.ListNodeDnsOptions(e.ctx, connect.NewRequest(&adminv1.ListNodeDnsOptionsRequest{}))
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Msg.Nodes
}

// What the owner sees of a person's picks, and the reset.
func TestUserDnsChoicesAndReset(t *testing.T) {
	e := newEnv(t)
	e.node("nod_b", "b-node")
	e.node("nod_a", "a-node")
	e.group("grp", "")
	e.user("alice", "grp", "")
	e.user("bob", "grp", "")
	e.offer("nod_a", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	e.offer("nod_b", "dns_builtin_yandex", "dns_builtin_yandex", "dns_builtin_standard")
	e.pick("alice", "nod_b", "dns_builtin_standard")
	e.pick("alice", "nod_a", "dns_builtin_standard")
	e.pick("bob", "nod_a", "dns_builtin_standard")
	e.offer("nod_a", "dns_builtin_adblock", "dns_builtin_adblock") // alice's pick on a-node is no longer offered

	r, err := e.s.GetUserDnsChoices(e.ctx, connect.NewRequest(&adminv1.GetUserDnsChoicesRequest{UserId: "alice"}))
	if err != nil || len(r.Msg.Choices) != 2 {
		t.Fatalf("choices = %v, %v", r.Msg, err)
	}
	a, b := r.Msg.Choices[0], r.Msg.Choices[1] // by node name
	if a.NodeName != "a-node" || a.Offered || a.PresetId != "dns_builtin_standard" || a.EffectivePresetId != "dns_builtin_adblock" ||
		b.NodeName != "b-node" || !b.Offered || b.EffectivePresetId != "dns_builtin_standard" || b.UpdatedUnix != 1_700_000_000 || b.PresetName == "" {
		t.Errorf("choices = %v", r.Msg.Choices)
	}
	_, err = e.s.GetUserDnsChoices(e.ctx, connect.NewRequest(&adminv1.GetUserDnsChoicesRequest{UserId: "usr_x"}))
	wantCode(t, err, connect.CodeNotFound)

	rr, err := e.s.ResetUserDnsChoices(e.ctx, connect.NewRequest(&adminv1.ResetUserDnsChoicesRequest{UserId: "alice"}))
	if err != nil || rr.Msg.Removed != 2 {
		t.Fatalf("reset: %v %v", rr, err)
	}
	if got := e.onNode("alice", "nod_b"); got != "dns_builtin_yandex" {
		t.Errorf("after the reset: %s", got)
	}
	if got := e.onNode("bob", "nod_a"); got != "dns_builtin_adblock" { // bob's pick is gone with the offer; alice's reset did not touch it
		t.Errorf("bob: %s", got)
	}
	if picks, _ := e.st.DNS().UserNodeChoices(e.ctx, "bob"); len(picks) != 1 {
		t.Errorf("a reset of alice removed bob's pick: %v", picks)
	}
	_, err = e.s.ResetUserDnsChoices(e.ctx, connect.NewRequest(&adminv1.ResetUserDnsChoicesRequest{UserId: "usr_x"}))
	wantCode(t, err, connect.CodeNotFound)
	rows, _ := e.st.ListAudit(e.ctx, "", 0, 20)
	resets := 0
	for _, row := range rows {
		if row.Action == "user_dns_choices_reset" {
			resets++
		}
	}
	if resets != 1 {
		t.Errorf("%d reset audit rows", resets)
	}
}

// The page speaks the language of the visitor: built-in presets are stored in Russian, with the description as
// "Russian\nEnglish"; an owner's own preset, or a built-in one they renamed, keeps its text in any language.
func TestPresetWordsForThePage(t *testing.T) {
	stock := Preset{ID: "dns_builtin_adblock", Name: "AdGuard: без рекламы", Description: "AdGuard DNS: без рекламы и трекеров\nAdGuard DNS: no ads, no trackers", Builtin: true}
	for _, c := range []struct {
		p                 Preset
		lang, name, descr string
	}{
		{stock, "ru", "AdGuard: без рекламы", "AdGuard DNS: без рекламы и трекеров"},
		{stock, "en", "AdGuard: no ads", "AdGuard DNS: no ads, no trackers"},
		{Preset{ID: "dns_builtin_adblock", Name: "Мой блок", Description: "Моё\nMine", Builtin: true}, "en", "Мой блок", "Mine"},
		{Preset{ID: "dns_abc", Name: "Домашний", Description: "Только у меня"}, "en", "Домашний", "Только у меня"},
		{Preset{ID: "dns_abc", Name: "Home", Description: "line one\nline two"}, "ru", "Home", "line one\nline two"},
		{Preset{ID: "dns_builtin_yandex", Name: "Яндекс DNS", Builtin: true}, "en", "Yandex DNS", ""},
	} {
		if got := c.p.NameIn(c.lang); got != c.name {
			t.Errorf("%s/%s name %q, want %q", c.p.ID, c.lang, got, c.name)
		}
		if got := c.p.DescriptionIn(c.lang); got != c.descr {
			t.Errorf("%s/%s description %q, want %q", c.p.ID, c.lang, got, c.descr)
		}
	}
}
