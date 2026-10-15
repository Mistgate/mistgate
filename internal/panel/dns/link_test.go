package dns

import (
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func (e *env) offerLink(def string, presets ...string) {
	e.t.Helper()
	if err := e.s.SetLinkOptions(e.ctx, &adminv1.LinkDnsOptions{PresetIds: presets, DefaultPresetId: def}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) pickLink(user, preset string) {
	e.t.Helper()
	if err := e.st.DNS().SetUserLinkChoice(e.ctx, user, preset, time.UnixMilli(1_700_000_000_000)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) link(user string) string {
	e.t.Helper()
	p, err := e.s.LinkEffective(e.ctx, user)
	if err != nil {
		e.t.Fatal(err)
	}
	st, err := e.s.SubscriptionState(e.ctx, user)
	if err != nil || st.Link.Effective != p.ID {
		e.t.Fatalf("the two reads of the link apps differ: %q and %+v (%v)", p.ID, st.Link, err)
	}
	return p.ID
}

// The DNS of the link apps: the person's pick while the owner offers it, else the user's own preset, else the group's, else the
// default of the offer, else the instance default. The keys never see the pick, and the link apps never see a pick per server.
func TestLinkDNSPrecedence(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de", "de1")
	e.group("grp", "")
	e.group("fam", "dns_builtin_family")
	e.user("alice", "grp", "")
	e.user("bob", "grp", "dns_builtin_quad9")
	e.user("carol", "fam", "")
	if err := e.s.SetDefaultPresetID(e.ctx, "dns_builtin_yandex"); err != nil {
		t.Fatal(err)
	}

	// no offer: the old rule
	for user, want := range map[string]string{"alice": "dns_builtin_yandex", "bob": "dns_builtin_quad9", "carol": "dns_builtin_family"} {
		if got := e.link(user); got != want {
			t.Errorf("no offer, %s: %s, want %s", user, got, want)
		}
	}
	e.offerLink("dns_builtin_standard", "dns_builtin_standard", "dns_builtin_adblock", "dns_builtin_ru_split")
	for user, want := range map[string]string{
		"alice": "dns_builtin_standard", // the offer's default before the instance default
		"bob":   "dns_builtin_quad9",    // the admin's preset of the user before the offer's default
		"carol": "dns_builtin_family",   // and the group's
	} {
		if got := e.link(user); got != want {
			t.Errorf("offer, no pick, %s: %s, want %s", user, got, want)
		}
	}

	e.pickLink("alice", "dns_builtin_adblock")
	e.pickLink("bob", "dns_builtin_adblock")
	e.pickLink("carol", "dns_builtin_ru_split")
	for user, want := range map[string]string{"alice": "dns_builtin_adblock", "bob": "dns_builtin_adblock", "carol": "dns_builtin_ru_split"} {
		if got := e.link(user); got != want {
			t.Errorf("the pick, %s: %s, want %s", user, got, want)
		}
	}
	// the pick is the link apps' alone: the keys of a node without a choice keep the admin's rule
	if p, _, err := e.s.Effective(e.ctx, "bob"); err != nil || p.ID != "dns_builtin_quad9" {
		t.Errorf("the keys of bob: %s %v", p.ID, err)
	}
	if got := e.onNode("alice", "nod_de"); got != "dns_builtin_yandex" {
		t.Errorf("the keys of alice on a node: %s", got)
	}
	// and a pick per server is the keys' alone
	e.offer("nod_de", "", "dns_builtin_family", "dns_builtin_standard")
	e.pick("alice", "nod_de", "dns_builtin_family")
	if got := e.link("alice"); got != "dns_builtin_adblock" {
		t.Errorf("a pick per server reached the link apps: %s", got)
	}

	// the owner stops offering adblock: alice and bob fall back, carol keeps her pick
	e.offerLink("", "dns_builtin_standard", "dns_builtin_ru_split")
	for user, want := range map[string]string{"alice": "dns_builtin_yandex", "bob": "dns_builtin_quad9", "carol": "dns_builtin_ru_split"} {
		if got := e.link(user); got != want {
			t.Errorf("no longer offered, %s: %s, want %s", user, got, want)
		}
	}
	st, err := e.s.SubscriptionState(e.ctx, "alice")
	if err != nil || st.Link.Choice != "" || st.Link.Default != "dns_builtin_yandex" || strings.Join(st.Link.Options, ",") != "dns_builtin_standard,dns_builtin_ru_split" {
		t.Errorf("alice's link state: %+v %v", st.Link, err)
	}
	// offered again: the pick works again (it was kept)
	e.offerLink("", "dns_builtin_adblock")
	if got := e.link("alice"); got != "dns_builtin_adblock" {
		t.Errorf("offered again: %s", got)
	}
	// a deleted custom preset takes the pick and the offer with it
	custom, err := e.create("Home", plain("10.0.0.53"))
	if err != nil {
		t.Fatal(err)
	}
	e.offerLink(custom.Id, custom.Id, "dns_builtin_adblock")
	e.pickLink("carol", custom.Id)
	if got := e.link("carol"); got != custom.Id {
		t.Fatalf("carol's custom pick: %s", got)
	}
	if _, err := e.s.DeleteDnsPreset(e.ctx, connect.NewRequest(&adminv1.DeleteDnsPresetRequest{Id: custom.Id})); err != nil {
		t.Fatal(err)
	}
	if got := e.link("carol"); got != "dns_builtin_family" {
		t.Errorf("after the delete, carol: %s", got)
	}
	if o, err := e.s.LinkOptions(e.ctx); err != nil || strings.Join(o.PresetIds, ",") != "dns_builtin_adblock" || o.DefaultPresetId != "" {
		t.Errorf("the offer after the delete: %v %v", o, err)
	}
	if _, err := e.s.LinkEffective(e.ctx, "usr_x"); err == nil {
		t.Error("an unknown user has link DNS")
	}
}

// The offer follows the rules of a node's offer; a save that changes nothing writes no audit row.
func TestSetLinkOptionsRules(t *testing.T) {
	e := newEnv(t)
	many := make([]string, MaxNodeOptions+1)
	for i := range many {
		many[i] = "dns_builtin_standard"
	}
	for _, c := range []struct {
		o    *adminv1.LinkDnsOptions
		want string
	}{
		{&adminv1.LinkDnsOptions{PresetIds: []string{"dns_nope"}}, "link_dns.preset_ids: unknown preset"},
		{&adminv1.LinkDnsOptions{PresetIds: []string{"dns_builtin_standard", "dns_builtin_standard"}}, "link_dns.preset_ids: a preset is listed twice"},
		{&adminv1.LinkDnsOptions{PresetIds: []string{"dns_builtin_standard"}, DefaultPresetId: "dns_builtin_adblock"}, "link_dns.default_preset_id: the default must be one of the offered presets"},
		{&adminv1.LinkDnsOptions{PresetIds: many}, "link_dns.preset_ids: too many presets"},
	} {
		if err := e.s.SetLinkOptions(e.ctx, c.o); !errors.Is(err, ErrLinkOptions) || err.Error() != c.want {
			t.Errorf("%v: %v, want %q", c.o, err, c.want)
		}
	}
	e.offerLink("dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_adblock")
	e.offerLink("dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_adblock") // saved again with the settings
	if o, err := e.s.LinkOptions(e.ctx); err != nil || strings.Join(o.PresetIds, ",") != "dns_builtin_standard,dns_builtin_adblock" || o.DefaultPresetId != "dns_builtin_adblock" {
		t.Errorf("the offer: %v %v", o, err)
	}
	e.offerLink("")
	if o, _ := e.s.LinkOptions(e.ctx); len(o.PresetIds) != 0 || o.DefaultPresetId != "" {
		t.Errorf("an empty offer: %v", o)
	}
	rows, _ := e.st.ListAudit(e.ctx, "", 0, 20)
	var got []string
	for _, r := range rows {
		if r.Action == "link_dns_options" {
			got = append(got, r.Params)
		}
	}
	if len(got) != 2 || !strings.Contains(got[0], `"presets":0`) || !strings.Contains(got[1], `"presets":2`) || !strings.Contains(got[1], `"default":"dns_builtin_adblock"`) {
		t.Errorf("audit: %v", got)
	}
}

// The user card shows the pick for the link apps next to the picks per server; the reset removes it too.
func TestUserDnsChoicesShowAndResetTheLinkPick(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de", "de1")
	e.group("grp", "dns_builtin_family")
	e.user("alice", "grp", "")
	e.user("bob", "grp", "")
	e.offerLink("", "dns_builtin_adblock", "dns_builtin_standard")
	e.offer("nod_de", "dns_builtin_yandex", "dns_builtin_yandex", "dns_builtin_standard")

	get := func(user string) *adminv1.GetUserDnsChoicesResponse {
		t.Helper()
		r, err := e.s.GetUserDnsChoices(e.ctx, connect.NewRequest(&adminv1.GetUserDnsChoicesRequest{UserId: user}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	if r := get("alice"); r.Link != nil || len(r.Choices) != 0 {
		t.Errorf("nothing picked: %v", r)
	}
	e.pickLink("alice", "dns_builtin_adblock")
	e.pickLink("bob", "dns_builtin_standard")
	e.pick("alice", "nod_de", "dns_builtin_standard")
	r := get("alice")
	if l := r.Link; l == nil || l.NodeId != "" || l.PresetId != "dns_builtin_adblock" || l.PresetName != "AdGuard: без рекламы" || !l.Offered ||
		l.EffectivePresetId != "dns_builtin_adblock" || l.UpdatedUnix != 1_700_000_000 || len(r.Choices) != 1 {
		t.Errorf("alice: %v", r)
	}
	e.offerLink("", "dns_builtin_standard") // adblock is no longer offered
	if l := get("alice").Link; l == nil || l.Offered || l.EffectivePresetId != "dns_builtin_family" || l.EffectivePresetName == "" {
		t.Errorf("no longer offered: %v", l)
	}

	rr, err := e.s.ResetUserDnsChoices(e.ctx, connect.NewRequest(&adminv1.ResetUserDnsChoicesRequest{UserId: "alice"}))
	if err != nil || rr.Msg.Removed != 2 {
		t.Fatalf("reset: %v %v", rr, err)
	}
	if r := get("alice"); r.Link != nil || len(r.Choices) != 0 {
		t.Errorf("after the reset: %v", r)
	}
	if l := get("bob").Link; l == nil || l.PresetId != "dns_builtin_standard" {
		t.Errorf("alice's reset touched bob: %v", l)
	}
	// a person with only a pick for the link apps is reset too, and the row says so
	rr, err = e.s.ResetUserDnsChoices(e.ctx, connect.NewRequest(&adminv1.ResetUserDnsChoicesRequest{UserId: "bob"}))
	if err != nil || rr.Msg.Removed != 1 {
		t.Fatalf("reset bob: %v %v", rr, err)
	}
	rows, _ := e.st.ListAudit(e.ctx, "", 0, 20)
	resets := 0
	for _, row := range rows {
		if row.Action == "user_dns_choices_reset" {
			resets++
		}
	}
	if resets != 2 {
		t.Errorf("%d reset audit rows", resets)
	}
}
