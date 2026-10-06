package access

import (
	"slices"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// A key is stale for the reason "dns" when the DNS that applies to the person on its node is not the one the key was issued
// with, whatever changed it: the person's pick or its removal, the owner's offer or default, a preset that was edited or deleted.
// Only what the person's device fetched counts as received: an admin looking at the keys does not.

func staleKeysOf(t *testing.T, e *env, token string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, d := range must(e.s.Subscription(e.ctx, token)).Devices {
		if d.AWG != nil && len(d.AWG.DNSStale) > 0 {
			out[d.ID] = d.AWG.DNSStale
		}
	}
	return out
}

func wantStale(t *testing.T, step string, got map[string][]string, dev string, nodes ...string) {
	t.Helper()
	if len(nodes) == 0 {
		if len(got) != 0 {
			t.Errorf("%s: stale %v, want none", step, got)
		}
		return
	}
	if len(got) != 1 || !slices.Equal(got[dev], nodes) {
		t.Errorf("%s: stale %v, want %v on %s", step, got, nodes, dev)
	}
}

// The person picks, fetches, and goes back to the node's default: the key holds the pick, and the pick is gone.
func TestKeyIsStaleAfterThePickIsReset(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	dev := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Label: "one"}))).Msg.Device.Id
	pick := func(preset string) {
		t.Helper()
		e.clock = e.clock.Add(time.Minute)
		if err := e.s.SetPageDNS(e.ctx, uid, PageDNSChoice{NodeID: "nod_de1", PresetID: preset}); err != nil {
			t.Fatal(err)
		}
	}
	fetch := func() {
		t.Helper()
		if _, _, err := e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, dev); err != nil {
			t.Fatal(err)
		}
	}
	pick("dns_builtin_standard")
	wantStale(t, "after the pick", staleKeysOf(t, e, token), dev, "nod_de1")
	fetch()
	wantStale(t, "after the fetch", staleKeysOf(t, e, token), dev)
	pick("") // back to the default: the row is deleted, there is nothing to compare with a time
	wantStale(t, "after the reset", staleKeysOf(t, e, token), dev, "nod_de1")
	fetch()
	wantStale(t, "after the second fetch", staleKeysOf(t, e, token), dev)
}

// The owner changes what a node offers or applies: the keys issued before hold another DNS now.
func TestKeyIsStaleWhenTheOwnerChangesTheNodeDNS(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	mine := must(e.s.dns.CreateDnsPreset(e.ctx, req(&adminv1.CreateDnsPresetRequest{Name: "Mine", Servers: []*adminv1.DnsServer{
		{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "9.9.9.9"}, {Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "149.112.112.112"}}}))).Msg.Preset
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard", mine.Id)
	dev := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Label: "one"}))).Msg.Device.Id
	fetch := func() {
		t.Helper()
		if _, _, err := e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, dev); err != nil {
			t.Fatal(err)
		}
	}
	wantStale(t, "nothing changed", staleKeysOf(t, e, token), dev)

	e.offer("nod_de1", "dns_builtin_standard", "dns_builtin_adblock", "dns_builtin_standard", mine.Id) // the default moved
	wantStale(t, "a new default", staleKeysOf(t, e, token), dev, "nod_de1")
	fetch()
	wantStale(t, "after the fetch", staleKeysOf(t, e, token), dev)

	e.offer("nod_de1", "dns_builtin_standard", "dns_builtin_standard", mine.Id) // the order and the list changed, not what applies
	wantStale(t, "the same default", staleKeysOf(t, e, token), dev)

	e.offer("nod_de1", "", "dns_builtin_standard") // no default: the person's usual rule applies, which has the same resolvers here
	wantStale(t, "no default, same resolvers", staleKeysOf(t, e, token), dev)

	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock")
	wantStale(t, "another default", staleKeysOf(t, e, token), dev, "nod_de1")
	fetch()

	e.offer("nod_de1", mine.Id, mine.Id)
	fetch()
	wantStale(t, "after the fetch", staleKeysOf(t, e, token), dev)
	must(e.s.dns.UpdateDnsPreset(e.ctx, req(&adminv1.UpdateDnsPresetRequest{Id: mine.Id, Name: "Mine", Servers: []*adminv1.DnsServer{
		{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "9.9.9.10"}, {Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "149.112.112.10"}}})))
	wantStale(t, "the preset was edited", staleKeysOf(t, e, token), dev, "nod_de1")
	fetch()
	wantStale(t, "after the fetch", staleKeysOf(t, e, token), dev)

	must(e.s.dns.DeleteDnsPreset(e.ctx, req(&adminv1.DeleteDnsPresetRequest{Id: mine.Id}))) // takes its offer along
	wantStale(t, "the preset was deleted", staleKeysOf(t, e, token), dev, "nod_de1")
}

// An admin who opens the keys of a person has not delivered them: the phone still holds the older DNS.
func TestAdminViewOfTheKeysDoesNotHideStaleDNS(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	dev := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Label: "one"}))).Msg.Device.Id
	e.clock = e.clock.Add(time.Minute)
	if err := e.s.SetPageDNS(e.ctx, uid, PageDNSChoice{NodeID: "nod_de1", PresetID: "dns_builtin_standard"}); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(time.Minute)
	must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: dev})))
	wantStale(t, "after the admin's look", staleKeysOf(t, e, token), dev, "nod_de1")
	if _, _, err := e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, dev); err != nil { // the person's own fetch does
		t.Fatal(err)
	}
	wantStale(t, "after the person's fetch", staleKeysOf(t, e, token), dev)
}

// Keys issued before the signature was recorded are not called stale: an upgrade raises no false alarm.
func TestKeyWithoutRecordedDNSIsNotStale(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	dev := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Label: "one"}))).Msg.Device.Id
	e.sql(`UPDATE device_credential SET dns_sig = ''`)
	e.offer("nod_de1", "dns_builtin_standard", "dns_builtin_adblock", "dns_builtin_standard")
	wantStale(t, "an old key", staleKeysOf(t, e, token), dev)
}
