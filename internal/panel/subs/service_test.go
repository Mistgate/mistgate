package subs_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

type networkUsageFunc func(nodeID string) (rxBps, txBps uint64, sampledAt time.Time, ok bool)

func (f networkUsageFunc) NetworkUsage(nodeID string) (uint64, uint64, time.Time, bool) {
	return f(nodeID)
}

// The admin's preview of server names is drawn from what a person of the busiest group gets in Happ now: its running
// servers in subscription order, not made-up ones. AmneziaWG, a switched-off inbound and a node out of service are not
// in Happ's list, so not in the preview either.
func TestSettingsCarryServerSamples(t *testing.T) {
	m := newM3Rig(t) // de1 holds the hysteria2 profile "p" and the AmneziaWG one; group g has p, group g2 both
	m.st.W.Exec(`UPDATE node SET country_code = 'DE' WHERE id = 'nod_1'`)
	m.st.W.Exec(`UPDATE node SET bandwidth_mbps = 100 WHERE id = 'nod_1'`)
	m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES ('nod_2', 'nl1', 'nl1.example.com', 'NL', 'active', 1)`)
	m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES ('nod_3', 'aa-down', 'a.example.com', 'FI', 'pending', 1)`)
	off := must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: m.profile, NodeId: "nod_2"}))).Msg.Inbound
	must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: m.profile, NodeId: "nod_3"})))
	svc := subs.NewService(m.st, subsettings.NewCache(m.st, nil), builtin.Registry(),
		func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, m.st) }, nil, nil,
		networkUsageFunc(func(nodeID string) (uint64, uint64, time.Time, bool) {
			if nodeID == "nod_1" {
				return 64_000_000, 10_000_000, time.Now(), true
			}
			return 0, 0, time.Time{}, false
		}))
	get := func() *adminv1.GetSubscriptionSettingsResponse {
		return must(svc.GetSubscriptionSettings(m.ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg
	}

	// No user anywhere: the first group.
	r := get()
	if r.SampleGroup != "g" || len(r.ServerSamples) != 2 || r.NamesLanguage != "en" {
		t.Fatalf("samples = %q %v %q", r.SampleGroup, r.ServerSamples, r.NamesLanguage)
	}
	if s := r.ServerSamples; s[0].Node != "de1" || s[0].CountryCode != "DE" || s[0].Profile != "p" || s[0].GetLoadPercent() != 64 || s[1].Node != "nl1" || s[1].CountryCode != "NL" || s[1].LoadPercent != nil {
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
