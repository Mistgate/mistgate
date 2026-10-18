package subs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The admin's preview of server names is drawn from what a person of the busiest group gets in Happ now: its running
// servers in subscription order, not made-up ones. AmneziaWG, a switched-off inbound and a node out of service are not
// in Happ's list, so not in the preview either.
func TestSettingsCarryServerSamples(t *testing.T) {
	m := newM3Rig(t) // de1 holds the hysteria2 profile "p" and the AmneziaWG one; group g has p, group g2 both
	m.st.W.Exec(`UPDATE node SET country_code = 'DE' WHERE id = 'nod_1'`)
	if _, err := m.st.W.Exec(`UPDATE node SET bandwidth_mbps = 100 WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES ('nod_2', 'nl1', 'nl1.example.com', 'NL', 'active', 1)`)
	if _, err := m.st.W.Exec(`UPDATE node SET bandwidth_mbps = 50 WHERE id = 'nod_2'`); err != nil {
		t.Fatal(err)
	}
	m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES ('nod_3', 'aa-down', 'a.example.com', 'FI', 'pending', 1)`)
	off := must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: m.profile, NodeId: "nod_2"}))).Msg.Inbound
	must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: m.profile, NodeId: "nod_3"})))
	now := time.Now().UTC()
	for _, sample := range []struct {
		node   string
		rx, tx int64
	}{
		{node: "nod_1", rx: 64_000_000, tx: 10_000_000},
		{node: "nod_2", rx: 20_000_000, tx: 6_000_000},
	} {
		m.st.W.Exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, now.Unix(), sample.node)
		m.st.W.Exec(`INSERT INTO node_live (node_id, session, sample_at, rx_bps, tx_bps, users, live_json)
			VALUES (?, 1, ?, ?, ?, '{}', '{}')`, sample.node, now.Add(-time.Second).Unix(), sample.rx, sample.tx)
	}
	svc := subs.NewService(m.st, subsettings.NewCache(m.st, nil), builtin.Registry(),
		func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, m.st) }, nil, nil)
	get := func() *adminv1.GetSubscriptionSettingsResponse {
		return must(svc.GetSubscriptionSettings(m.ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg
	}

	// No user anywhere: the first group.
	r := get()
	if r.SampleGroup != "g" || len(r.ServerSamples) != 2 || r.NamesLanguage != "en" {
		t.Fatalf("samples = %q %v %q", r.SampleGroup, r.ServerSamples, r.NamesLanguage)
	}
	if s := r.ServerSamples; s[0].Node != "de1" || s[0].CountryCode != "DE" || s[0].Profile != "p" || s[0].GetLoadPercent() != 64 || s[1].Node != "nl1" || s[1].CountryCode != "NL" || s[1].GetLoadPercent() != 40 {
		t.Errorf("samples = %v", s)
	}
	// The group most people are in; the switched-off inbound drops out.
	m.newUser("alice", nil)
	m.newUser("bob", nil)
	must(m.svc.UpdateInbound(m.ctx, connect.NewRequest(&adminv1.UpdateInboundRequest{InboundId: off.Id, Enabled: new(false)})))
	if r := get(); r.SampleGroup != "g2" || len(r.ServerSamples) != 1 || r.ServerSamples[0].Node != "de1" {
		t.Errorf("busiest group = %q %v", r.SampleGroup, r.ServerSamples)
	}
}

// The offer of DNS presets for the link apps is a subscription setting the DNS module keeps: saved with the settings, kept when a
// request leaves it out, refused when it names a preset that does not exist; never part of the stored document.
func TestSettingsCarryTheLinkDNSOffer(t *testing.T) {
	m := newM3Rig(t)
	svc := subs.NewService(m.st, subsettings.NewCache(m.st, nil), builtin.Registry(),
		func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, m.st) }, dns.New(m.st), nil)
	ctx := auth.WithAdmin(m.ctx, store.Admin{ID: "adm_1"})
	get := func() *adminv1.SubscriptionSettings {
		return must(svc.GetSubscriptionSettings(ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg.Settings
	}
	update := func(s *adminv1.SubscriptionSettings) (*adminv1.SubscriptionSettings, error) {
		r, err := svc.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: s}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Settings, nil
	}
	if o := get().GetLinkDns(); o == nil || len(o.PresetIds) != 0 {
		t.Fatalf("a fresh install offers nothing: %v", o)
	}
	s := get()
	s.LinkDns = &adminv1.LinkDnsOptions{PresetIds: []string{"dns_builtin_standard", "dns_builtin_adblock"}, DefaultPresetId: "dns_builtin_adblock"}
	out, err := update(s)
	if err != nil || strings.Join(out.GetLinkDns().GetPresetIds(), ",") != "dns_builtin_standard,dns_builtin_adblock" || out.GetLinkDns().GetDefaultPresetId() != "dns_builtin_adblock" {
		t.Fatalf("saved: %v %v", out.GetLinkDns(), err)
	}
	// An older admin that knows nothing of the offer saves the settings without it: the offer stays.
	s = get()
	s.LinkDns, s.Title = nil, "VPN"
	if out, err := update(s); err != nil || len(out.GetLinkDns().GetPresetIds()) != 2 || out.Title != "VPN" {
		t.Errorf("left out: %v %v", out.GetLinkDns(), err)
	}
	s = get()
	beforeTitle, beforeDefaultDNS := s.Title, s.DefaultDnsPresetId
	storedBefore, err := m.st.Setting(m.ctx, subsettings.Key)
	if err != nil {
		t.Fatal(err)
	}
	s.Title = "must not save"
	s.DefaultDnsPresetId = "dns_builtin_family"
	s.LinkDns = &adminv1.LinkDnsOptions{PresetIds: []string{"dns_nope"}}
	if _, err := update(s); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "link_dns.preset_ids: unknown preset") {
		t.Errorf("an unknown preset: %v", err)
	}
	if after := get(); after.Title != beforeTitle || after.DefaultDnsPresetId != beforeDefaultDNS {
		t.Errorf("an invalid offer changed the settings: title %q default DNS %q", after.Title, after.DefaultDnsPresetId)
	}
	if v, err := m.st.Setting(m.ctx, subsettings.Key); err != nil || v != storedBefore || strings.Contains(v, "link_dns") || strings.Contains(v, "dns_builtin") {
		t.Errorf("the stored document changed: %s %v", v, err)
	}
	if o := get().GetLinkDns(); len(o.GetPresetIds()) != 2 {
		t.Errorf("a refused save changed the offer: %v", o)
	}
	s = get()
	s.UpdateIntervalHours = 1 << 20 // an invalid document with a valid new offer: nothing is written
	s.LinkDns = &adminv1.LinkDnsOptions{PresetIds: []string{"dns_builtin_family"}}
	if _, err := update(s); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("an invalid document: %v", err)
	}
	if o := get().GetLinkDns(); len(o.GetPresetIds()) != 2 {
		t.Errorf("an invalid document changed the offer: %v", o)
	}
}
