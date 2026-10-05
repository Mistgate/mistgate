package access

import (
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// DNS per node for the person. Keys and the AmneziaWG proxies of a Mihomo profile carry a resolver per server and get the
// DNS of their node; Hysteria2 has one resolver for the whole subscription and keeps the rule that applied before.

const (
	adblockPair  = "94.140.14.14, 94.140.15.15"
	standardPair = "1.1.1.1, 8.8.8.8"
	yandexPair   = "77.88.8.8, 77.88.8.1"
)

// offer is what the owner offers on a node.
func (e *env) offer(node, def string, presets ...string) {
	e.t.Helper()
	if err := e.st.DNS().SetNodeOptions(e.ctx, node, presets, def); err != nil {
		e.t.Fatal(err)
	}
}

func confDNS(conf string) string {
	for _, l := range strings.Split(conf, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "DNS = "); ok {
			return v
		}
	}
	return ""
}

// dnsByNode is the DNS line of each config of a device, by node id.
func dnsByNode(cfgs []DeviceConfig) map[string]string {
	out := map[string]string{}
	for _, c := range cfgs {
		out[c.NodeID] = confDNS(c.Conf)
	}
	return out
}

func awgOnBothNodes(t *testing.T) (*mixed, string, string) {
	m := newMixed(t)
	e := m.e
	e.inbound(m.profile, "nod_nl1")
	u := e.user("both", m.all, nil).User
	return m, u.Id, e.tokenOf(u.Id)
}

func TestKeysCarryTheDNSOfTheirNode(t *testing.T) {
	m, uid, _ := awgOnBothNodes(t)
	e := m.e
	fetch := func(dev string) map[string]string {
		t.Helper()
		_, cfgs, err := e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, dev)
		if err != nil {
			t.Fatal(err)
		}
		return dnsByNode(cfgs)
	}
	added := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Label: "phone"}))).Msg
	dev := added.Device.Id
	got := map[string]string{}
	for _, c := range added.Configs {
		got[c.NodeId] = confDNS(c.Conf)
	}
	if len(got) != 2 || got["nod_de1"] != standardPair || got["nod_nl1"] != standardPair {
		t.Fatalf("no node offers anything: the old rule, got %v", got)
	}

	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	e.offer("nod_nl1", "dns_builtin_yandex", "dns_builtin_yandex")
	if got := fetch(dev); got["nod_de1"] != adblockPair || got["nod_nl1"] != yandexPair {
		t.Fatalf("node defaults: %v", got)
	}
	e.clock = e.clock.Add(time.Minute)
	if err := e.s.SetPageDNS(e.ctx, uid, PageDNSChoice{NodeID: "nod_de1", PresetID: "dns_builtin_standard"}); err != nil {
		t.Fatal(err)
	}
	if got := fetch(dev); got["nod_de1"] != standardPair || got["nod_nl1"] != yandexPair {
		t.Fatalf("after the pick on de1: %v", got)
	}
	// Back to the node's default.
	e.clock = e.clock.Add(time.Minute)
	if err := e.s.SetPageDNS(e.ctx, uid, PageDNSChoice{NodeID: "nod_de1"}); err != nil {
		t.Fatal(err)
	}
	if got := fetch(dev); got["nod_de1"] != adblockPair {
		t.Fatalf("after the pick was cleared: %v", got)
	}
	// A preset with no plain IPv4 server: the key falls back and says so, per node.
	_, cfgs, _ := e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, dev)
	for _, c := range cfgs {
		if slices.Contains(c.Warnings, "dns_fallback") {
			t.Errorf("%s warns about a fallback: %v", c.NodeID, c.Warnings)
		}
	}
	doh := must(e.s.dns.CreateDnsPreset(e.ctx, req(&adminv1.CreateDnsPresetRequest{Name: "Only DoH", Servers: []*adminv1.DnsServer{{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_DOH, Address: "https://dns.example/dns-query"}}}))).Msg.Preset
	e.offer("nod_nl1", doh.Id, doh.Id)
	_, cfgs, _ = e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, dev)
	for _, c := range cfgs {
		if want := c.NodeID == "nod_nl1"; slices.Contains(c.Warnings, "dns_fallback") != want {
			t.Errorf("%s: warnings %v", c.NodeID, c.Warnings)
		}
	}
}

func TestMihomoAWGProxiesCarryTheDNSOfTheirNode(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock")
	e.offer("nod_nl1", "dns_builtin_yandex", "dns_builtin_yandex")
	if err := e.s.SetPageDNS(e.ctx, uid, PageDNSChoice{NodeID: "nod_nl1", PresetID: "dns_builtin_yandex"}); err != nil {
		t.Fatal(err)
	}
	v := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Format: plugin.FormatMihomo}))
	seen := map[string]bool{}
	for i, s := range v.Servers {
		line := v.Lines[i]
		if s.Protocol != "awg" {
			// Hysteria2 has no resolver of its own: its line carries none, whatever the node offers.
			if strings.Contains(line, "dns") && !strings.Contains(line, "hysteria2") {
				t.Errorf("hysteria2 proxy: %s", line)
			}
			continue
		}
		want := map[string]string{"nod_de1": "94.140.14.14", "nod_nl1": "77.88.8.8"}[s.NodeID]
		if !strings.Contains(line, `"`+want+`"`) && !strings.Contains(line, want) {
			t.Errorf("%s: no %s in %s", s.NodeID, want, line)
		}
		seen[s.NodeID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("AWG proxies on %v", seen)
	}
	// The user's own preset is what Hysteria2 and Happ get, on every node: the person's picks never reach them.
	pre, _, err := e.s.dns.Effective(e.ctx, uid)
	if err != nil || pre.ID != "dns_builtin_ru_split" {
		t.Errorf("the rule for the link: %v %v", pre.ID, err)
	}
	if v.DNSLink != "dns_builtin_ru_split" {
		t.Errorf("DNSLink = %q", v.DNSLink)
	}
}

// The facts the page lists per server: label inputs, ways to use it, whether the agent answers, the load level, and the DNS.
func TestSubViewNodes(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	e.sql(`UPDATE node SET country_code = 'DE', location = 'Frankfurt', bandwidth_mbps = 100 WHERE id = 'nod_de1'`)
	e.sql(`UPDATE node SET country_code = 'NL', bandwidth_mbps = 100 WHERE id = 'nod_nl1'`)
	e.node("nod_old", "old1", "old1.example.com", "active")
	e.inbound(m.hy2, "nod_old")
	e.s.online = &liveSource{
		agents:  map[string]bool{"nod_de1": true},
		samples: map[string]sample{"nod_nl1": {rx: 85_000_000, at: e.clock.Add(-30 * time.Second)}, "nod_old": {rx: 1, at: e.clock.Add(-2 * time.Minute)}},
	}
	// A warp exit: a profile with egress warp on nl1.
	warp := e.profile("hy2 warp", `{"egress":"warp","port":8444}`)
	e.inbound(warp.Id, "nod_nl1")
	g := e.group("all2", m.hy2, m.profile, warp.Id)
	u2 := e.user("two", g, nil).User
	v := must(e.s.Subscription(e.ctx, e.tokenOf(u2.Id)))
	_, _ = uid, token

	byID := map[string]SubNode{}
	var order []string
	for _, n := range v.Nodes {
		byID[n.ID] = n
		order = append(order, n.ID)
	}
	if !slices.Equal(order, []string{"nod_de1", "nod_nl1", "nod_old"}) {
		t.Fatalf("nodes in subscription order: %v", order)
	}
	de, nl, old := byID["nod_de1"], byID["nod_nl1"], byID["nod_old"]
	if de.Name != "de1" || de.CountryCode != "DE" || de.Location != "Frankfurt" || !de.Online || de.LoadPercent != nil {
		t.Errorf("de1: %+v", de)
	}
	if !nl.Online || nl.LoadPercent == nil || *nl.LoadPercent != 85 {
		t.Errorf("nl1: %+v", nl)
	}
	if old.Online || old.LoadPercent != nil {
		t.Errorf("a stale sample and no session: %+v", old)
	}
	type conn struct{ way, exit string }
	ways := func(n SubNode) (out []conn) {
		for _, c := range n.Conns {
			out = append(out, conn{c.Way, c.Exit})
		}
		return out
	}
	if got := ways(de); !slices.Equal(got, []conn{{"link", "direct"}, {"key", "direct"}}) && !slices.Equal(got, []conn{{"key", "direct"}, {"link", "direct"}}) {
		t.Errorf("de1 ways: %v", got)
	}
	if got := ways(nl); len(got) != 3 || !slices.Contains(got, conn{"link", "warp"}) || !slices.Contains(got, conn{"key", "direct"}) {
		t.Errorf("nl1 ways: %v", got)
	}
	for _, c := range nl.Conns {
		if c.Way == "link" && (c.Server < 0 || c.Server >= len(v.Servers) || v.Servers[c.Server].NodeID != "nod_nl1") {
			t.Errorf("a link connection points at %d", c.Server)
		}
		if c.Way == "key" && c.ProfileID != m.profile {
			t.Errorf("a key connection on profile %q", c.ProfileID)
		}
	}
	// Nobody offers anything: no DNS rows, no presets to name.
	if de.DNS != nil || nl.DNS != nil || len(v.DNSPresets) != 0 || v.DNSLink != "dns_builtin_ru_split" {
		t.Errorf("DNS with no offers: %+v %+v %v %q", de.DNS, nl.DNS, v.DNSPresets, v.DNSLink)
	}

	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_family")
	e.offer("nod_nl1", "", "dns_builtin_yandex", "dns_builtin_standard") // options and no default: the old rule applies
	e.clock = e.clock.Add(time.Second)
	if err := e.s.SetPageDNS(e.ctx, u2.Id, PageDNSChoice{NodeID: "nod_de1", PresetID: "dns_builtin_family"}); err != nil {
		t.Fatal(err)
	}
	v = must(e.s.Subscription(e.ctx, e.tokenOf(u2.Id)))
	de, nl = v.Nodes[0], v.Nodes[1]
	if de.DNS == nil || de.DNS.Choice != "dns_builtin_family" || de.DNS.Effective != "dns_builtin_family" ||
		!slices.Equal(de.DNS.Options, []string{"dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_family"}) || len(de.DNS.KeysToRefresh) != 0 {
		t.Errorf("de1 dns: %+v", de.DNS)
	}
	if nl.DNS == nil || nl.DNS.Choice != "" || nl.DNS.Effective != "dns_builtin_ru_split" || len(nl.DNS.Options) != 2 {
		t.Errorf("nl1 dns: %+v", nl.DNS)
	}
	if v.Nodes[2].DNS != nil {
		t.Errorf("a node that offers nothing has a DNS row: %+v", v.Nodes[2].DNS)
	}
	// The presets the page names: everything offered, everything that applies and the link's.
	var ids []string
	for _, p := range v.DNSPresets {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	if want := []string{"dns_builtin_adblock", "dns_builtin_family", "dns_builtin_ru_split", "dns_builtin_standard", "dns_builtin_yandex"}; !slices.Equal(ids, want) {
		t.Errorf("presets = %v, want %v", ids, want)
	}
}

type sample struct {
	rx uint64
	at time.Time
}

// liveSource is the fleet as the page sees it: which agents hold a session, the latest network samples.
type liveSource struct {
	agents  map[string]bool
	samples map[string]sample
}

func (liveSource) OnlineUsers() map[string]string { return nil }
func (l *liveSource) AgentConnected(nodeID string) bool {
	return l.agents[nodeID]
}
func (l *liveSource) NetworkUsage(nodeID string) (uint64, uint64, time.Time, bool) {
	s, ok := l.samples[nodeID]
	return s.rx, 0, s.at, ok
}

func TestPageDNSPickRules(t *testing.T) {
	m, uid, _ := awgOnBothNodes(t)
	e := m.e
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	pick := func(user, node, preset string) error {
		return e.s.SetPageDNS(e.ctx, user, PageDNSChoice{NodeID: node, PresetID: preset})
	}
	wantCode(t, pick(uid, "nod_nope", "dns_builtin_standard"), connect.CodeNotFound)       // not a server
	wantCode(t, pick(uid, "nod_wait", "dns_builtin_standard"), connect.CodeNotFound)       // a node that is not in service
	wantCode(t, pick("usr_nope", "nod_de1", "dns_builtin_standard"), connect.CodeNotFound) // not a user
	err := pick(uid, "nod_de1", "dns_builtin_family")                                      // not offered
	wantCode(t, err, connect.CodeFailedPrecondition)
	wantMsg(t, err, "failed_precondition: not_allowed")
	err = pick(uid, "nod_nl1", "dns_builtin_standard") // the node offers nothing
	wantCode(t, err, connect.CodeFailedPrecondition)
	// A node outside the user's selection is not theirs.
	only := e.user("only", m.all, func(m *adminv1.CreateUserRequest) { m.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_nl1"}} }).User
	wantCode(t, pick(only.Id, "nod_de1", "dns_builtin_standard"), connect.CodeNotFound)
	if n := e.count(`SELECT count(*) FROM user_node_dns`); n != 0 {
		t.Fatalf("refused picks were stored: %d", n)
	}

	e.notify.n.Store(0)
	if err := pick(uid, "nod_de1", "dns_builtin_standard"); err != nil {
		t.Fatal(err)
	}
	if e.notify.n.Load() != 0 {
		t.Error("a pick told the nodes: DNS is carried by the client")
	}
	// The audit row: the person's name, the node's name and the preset; no link, no address.
	rows := must(e.st.ListAudit(e.ctx, "", 0, 5))
	if len(rows) == 0 || rows[0].Action != "page_dns_choice" || rows[0].Actor != "user:"+uid || rows[0].IP != "" ||
		!strings.Contains(rows[0].Params, `"user":"both"`) || !strings.Contains(rows[0].Params, `"node":"de1"`) || !strings.Contains(rows[0].Params, `"preset":"dns_builtin_standard"`) ||
		strings.Contains(rows[0].Params, e.tokenOf(uid)) {
		t.Errorf("audit = %+v", rows)
	}
	// The pick is replaced, and "" removes it.
	e.clock = e.clock.Add(time.Second)
	if err := pick(uid, "nod_de1", "dns_builtin_adblock"); err != nil {
		t.Fatal(err)
	}
	if picks, _ := e.st.DNS().UserNodeChoices(e.ctx, uid); len(picks) != 1 || picks["nod_de1"].PresetID != "dns_builtin_adblock" || picks["nod_de1"].UpdatedMs != e.clock.UnixMilli() {
		t.Errorf("picks = %+v", picks)
	}
	if err := pick(uid, "nod_de1", ""); err != nil {
		t.Fatal(err)
	}
	if picks, _ := e.st.DNS().UserNodeChoices(e.ctx, uid); len(picks) != 0 {
		t.Errorf("picks after the default was chosen: %+v", picks)
	}
	// A user whose subscription is not active has no servers to pick on.
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{uid}, Enabled: false})))
	wantMsg(t, pick(uid, "nod_de1", "dns_builtin_standard"), "failed_precondition: user_inactive")
}

// A key holds the DNS it was issued with: a pick made after the device fetched its configs makes it stale for the
// reason "dns" (the same key, fetched again, carries the new one); fetching the configs clears it.
func TestKeyIsStaleAfterADNSPick(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	e.offer("nod_nl1", "dns_builtin_yandex", "dns_builtin_yandex", "dns_builtin_standard")
	d1 := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Label: "one"}))).Msg.Device.Id
	d2 := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "android", Label: "two"}))).Msg.Device.Id
	stale := func() (map[string][]string, map[string][]string) {
		t.Helper()
		v := must(e.s.Subscription(e.ctx, token))
		devs, nodes := map[string][]string{}, map[string][]string{}
		for _, d := range v.Devices {
			if d.AWG != nil && len(d.AWG.DNSStale) > 0 {
				devs[d.ID] = d.AWG.DNSStale
			}
		}
		for _, n := range v.Nodes {
			if n.DNS != nil && len(n.DNS.KeysToRefresh) > 0 {
				nodes[n.ID] = n.DNS.KeysToRefresh
			}
		}
		return devs, nodes
	}
	if devs, nodes := stale(); len(devs) != 0 || len(nodes) != 0 {
		t.Fatalf("fresh keys are stale: %v %v", devs, nodes)
	}

	e.clock = e.clock.Add(time.Minute)
	if err := e.s.SetPageDNS(e.ctx, uid, PageDNSChoice{NodeID: "nod_de1", PresetID: "dns_builtin_standard"}); err != nil {
		t.Fatal(err)
	}
	same := func(got []string, want ...string) bool {
		g, w := slices.Clone(got), slices.Clone(want)
		slices.Sort(g)
		slices.Sort(w)
		return slices.Equal(g, w)
	}
	devs, nodes := stale()
	if len(devs) != 2 || !same(devs[d1], "nod_de1") || !same(devs[d2], "nod_de1") {
		t.Errorf("stale devices = %v", devs)
	}
	if len(nodes) != 1 || !same(nodes["nod_de1"], d1, d2) {
		t.Errorf("keys to refresh by node = %v", nodes)
	}
	// Device one fetches its configs: only device two is stale now.
	if _, _, err := e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, d1); err != nil {
		t.Fatal(err)
	}
	devs, nodes = stale()
	if len(devs) != 1 || !same(devs[d2], "nod_de1") || !same(nodes["nod_de1"], d2) {
		t.Errorf("after device one fetched: %v %v", devs, nodes)
	}
	// A pick on the other node: device one is stale on nl1 only, device two on both.
	e.clock = e.clock.Add(time.Minute)
	if err := e.s.SetPageDNS(e.ctx, uid, PageDNSChoice{NodeID: "nod_nl1", PresetID: "dns_builtin_standard"}); err != nil {
		t.Fatal(err)
	}
	devs, nodes = stale()
	if !same(devs[d1], "nod_nl1") || !same(devs[d2], "nod_de1", "nod_nl1") || !same(nodes["nod_nl1"], d1, d2) || !same(nodes["nod_de1"], d2) {
		t.Errorf("after a pick on nl1: %v %v", devs, nodes)
	}
	// The rotation hands the key out again with the current DNS: device two is not stale. A pick the node stops offering
	// stales nothing.
	if _, _, err := e.s.RotateDevice(e.ctx, "user:"+uid, uid, d2); err != nil {
		t.Fatal(err)
	}
	devs, _ = stale()
	if len(devs) != 1 || !same(devs[d1], "nod_nl1") {
		t.Errorf("after the rotation: %v", devs)
	}
	e.offer("nod_nl1", "dns_builtin_yandex", "dns_builtin_yandex")
	if devs, nodes = stale(); len(devs) != 0 || len(nodes) != 0 {
		t.Errorf("after the offer was withdrawn: %v %v", devs, nodes)
	}
	e.offer("nod_nl1", "dns_builtin_yandex", "dns_builtin_yandex", "dns_builtin_standard") // offered again: the pick works again
	if devs, _ = stale(); !same(devs[d1], "nod_nl1") {
		t.Errorf("offered again: %v", devs)
	}
	// Fetching clears it.
	if _, _, err := e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, d1); err != nil {
		t.Fatal(err)
	}
	if devs, nodes := stale(); len(devs) != 0 || len(nodes) != 0 {
		t.Errorf("after every fetch: %v %v", devs, nodes)
	}
}

// A person whose subscription ended still sees the keys they hold (the page lists them and can remove them); the servers and
// the DNS are for an active user only.
func TestInactiveUserKeepsTheirKeysInTheView(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock")
	dev := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Label: "phone"}))).Msg.Device.Id
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{uid}, Enabled: false})))
	v := must(e.s.Subscription(e.ctx, token))
	if v.Status == StatusActive || len(v.Nodes) != 0 || len(v.DNSPresets) != 0 || v.DNSLink != "" || v.AccessAmnezia || len(v.AWGProfiles) != 0 || !v.AppAmnezia {
		t.Errorf("inactive view: %+v", v)
	}
	if len(v.Devices) != 2 || v.Devices[len(v.Devices)-1].ID != dev && v.Devices[0].ID != dev {
		t.Errorf("the keys of an inactive user: %+v", v.Devices)
	}
	// Removing a key and renaming it still work for them.
	if _, err := e.s.RelabelDevice(e.ctx, uid, dev, "old phone"); err != nil {
		t.Errorf("rename: %v", err)
	}
	if err := e.s.RevokeOwnDevice(e.ctx, "user:"+uid, uid, dev); err != nil {
		t.Errorf("revoke: %v", err)
	}
}
