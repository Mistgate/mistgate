package globalping

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The IPv6 round of the Globalping source. The tests read the JSON of the requests the fake received; the real API is never
// called.

func round6(targets ...health.CountryTarget) health.CountryRound {
	r := round(targets...)
	r.Family = 6
	return r
}

func TestIPv6RoundHasItsOwnControlAndNodeRequests(t *testing.T) {
	e := newEnv(t)
	e.enable(secretToken)
	f := e.fake
	f.icmp["2001:db8::10"] = true
	f.tcp["2001:db8::10:443"] = true
	r := round6(health.CountryTarget{Key: "de1", IP: "2001:db8::10", Ports: []uint16{443}})
	r.Want = []health.CountryWant{{Name: "Rostelecom", ASNs: []uint32{12389}}} // an IPv4 round would read the probe list for this

	obs, err := e.source().Measure(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range f.requests() {
		if req.Path == "/v1/probes" {
			t.Fatal("an IPv6 round read the probe list: it does not say which probes have IPv6")
		}
	}
	posts := f.posts()
	if len(posts) != 3 { // the control, ICMP and one TCP port
		t.Fatalf("%d measurements: %+v", len(posts), posts)
	}
	// the control is a hostname measured over IPv6, on generic eyeball probes
	want := map[string]any{
		"type": "ping", "target": "ya.ru",
		"locations":          []any{map[string]any{"country": "RU", "limit": 3.0, "tags": []any{"eyeball-network"}}},
		"measurementOptions": map[string]any{"protocol": "TCP", "port": 443.0, "packets": 3.0, "ipVersion": 6.0},
	}
	if got := jsonOf(t, posts[0].Body); !reflect.DeepEqual(got, want) {
		t.Fatalf("IPv6 control = %s", posts[0].Body)
	}
	// the nodes are IPv6 literals: no ipVersion (the API refuses it with a literal), and the probes of the IPv6 control
	icmp := map[string]any{"type": "ping", "target": "2001:db8::10", "locations": "m1", "measurementOptions": map[string]any{"packets": 3.0}}
	tcp := map[string]any{"type": "ping", "target": "2001:db8::10", "locations": "m1",
		"measurementOptions": map[string]any{"protocol": "TCP", "port": 443.0, "packets": 3.0}}
	if got := jsonOf(t, posts[1].Body); !reflect.DeepEqual(got, icmp) {
		t.Errorf("IPv6 ICMP request = %s", posts[1].Body)
	}
	if got := jsonOf(t, posts[2].Body); !reflect.DeepEqual(got, tcp) {
		t.Errorf("IPv6 TCP request = %s", posts[2].Body)
	}
	if !obs.Eyeball || len(obs.Probes) != 3 || !obs.Probes[0].Nodes["de1"].Complete || !obs.Probes[0].Nodes["de1"].ICMPOK || !obs.Probes[0].Nodes["de1"].TCP[0].OK {
		t.Fatalf("observed = %+v", obs)
	}

	// an IPv4 round on the same source keeps its requests: no ipVersion anywhere
	f4 := newEnv(t)
	f4.enable("")
	f4.fake.icmp["203.0.113.10"] = true
	if _, err := f4.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"})); err != nil {
		t.Fatal(err)
	}
	for _, p := range f4.fake.posts() {
		if strings.Contains(p.Body, "ipVersion") {
			t.Fatalf("an IPv4 request carries ipVersion: %s", p.Body)
		}
	}
}

// A probe without IPv6 must not be counted as a probe that cannot reach the node: the control of the IPv6 round leaves it out.
func TestIPv6ControlLeavesOutProbesWithoutIPv6(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	f := e.fake
	f.probes[0].NoIPv6 = true // Rostelecom
	f.icmp["2001:db8::10"] = true
	f.icmp["203.0.113.10"] = true

	obs, err := e.source().Measure(context.Background(), round6(health.CountryTarget{Key: "de1", IP: "2001:db8::10"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Probes) != 2 {
		t.Fatalf("probes = %+v, want the two with IPv6", obs.Probes)
	}
	for _, p := range obs.Probes {
		if p.Network == "Rostelecom" || !p.ControlOK || !p.Nodes["de1"].ICMPOK {
			t.Errorf("probe = %+v", p)
		}
	}
	// the IPv4 round on the same probes still has all three: nothing of it changed
	obs, err = e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
	if err != nil || len(obs.Probes) != 3 {
		t.Fatalf("IPv4 round = %+v, %v", obs.Probes, err)
	}
}

func TestIPv6RoundWithNoIPv6ProbesAtAll(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	for i := range e.fake.probes {
		e.fake.probes[i].NoIPv6 = true
	}
	_, err := e.source().Measure(context.Background(), round6(health.CountryTarget{Key: "de1", IP: "2001:db8::10"}))
	if !errors.Is(err, health.ErrCountryNoProbes) {
		t.Fatalf("err = %v, want ErrCountryNoProbes", err)
	}
	if n := len(e.fake.posts()); n != 2 { // the eyeball control and the any-network one; no node measurement was made
		t.Fatalf("%d measurements after a country without IPv6 probes, want the two controls", n)
	}
	// IPv4 is not an IPv6 matter
	if _, err := e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"})); err != nil {
		t.Fatalf("IPv4 round: %v", err)
	}
}

func TestIPv6SwitchIsOnByDefaultAndStored(t *testing.T) {
	e := newEnv(t)
	cur, err := e.update(&adminv1.UpdateGlobalpingRequest{IntervalMin: ptr(uint32(20))})
	if err != nil || !cur.Ipv6 {
		t.Fatalf("default = %+v, %v; want IPv6 on", cur, err)
	}
	e.enable("")
	if cc, _ := e.svc.Country(context.Background()); !cc.IPv6 {
		t.Fatalf("Country() = %+v, want IPv6 on", cc)
	}

	cur, err = e.update(&adminv1.UpdateGlobalpingRequest{Ipv6: ptr(false)})
	if err != nil || cur.Ipv6 || !cur.Enabled || cur.IntervalMin != 20 {
		t.Fatalf("switched off = %+v, %v", cur, err)
	}
	if cc, src := e.svc.Country(context.Background()); cc.IPv6 || !cc.Enabled || src == nil {
		t.Fatalf("Country() = %+v, %v; want IPv6 off, the rest on", cc, src)
	}
	// the other fields leave the switch alone
	if cur, err = e.update(&adminv1.UpdateGlobalpingRequest{IntervalMin: ptr(uint32(30)), Token: ptr(secretToken)}); err != nil || cur.Ipv6 {
		t.Fatalf("an update of other fields = %+v, %v", cur, err)
	}
	if cur, err = e.update(&adminv1.UpdateGlobalpingRequest{Ipv6: ptr(true)}); err != nil || !cur.Ipv6 || cur.IntervalMin != 30 {
		t.Fatalf("switched on = %+v, %v", cur, err)
	}
	// a disconnect switches the integration off, IPv6 included
	if _, err := (rpc{e.svc}).DisconnectGlobalping(ownerCtx(), connect.NewRequest(&adminv1.DisconnectGlobalpingRequest{})); err != nil {
		t.Fatal(err)
	}
	if cc, src := e.svc.Country(context.Background()); cc.Enabled || src != nil {
		t.Fatalf("Country() after a disconnect = %+v, %v", cc, src)
	}
}

// The health scheduler with this integration behind it: the IPv4 round, then the IPv6 one on the next poll, with the IPv6
// rows stored under family 6; with the switch off, no IPv6 measurement is made.
func TestSchedulerRunsTheIPv6RoundNextAndTheSwitchStopsIt(t *testing.T) {
	e := newEnv(t)
	now := e.now
	for _, n := range []struct{ id, address string }{{"de1", "203.0.113.10"}, {"nl1", "203.0.113.20"}} {
		if _, err := e.st.W.ExecContext(context.Background(), `INSERT INTO node (id, name, address, provider, state, created_at, last_seen_at, last_connected_at)
			VALUES (?, ?, ?, 'hoster', 'active', 1, ?, ?)`, n.id, n.id, n.address, now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	// de1 has working IPv6; nl1 has an address that does not work at the hoster
	for id, params := range map[string]map[string]string{
		"de1": {"ipv6": "yes", "connect": "ok", "iface": "eth0", "addrs": "2001:db8::10/64", "src": "2001:db8::10"},
		"nl1": {"ipv6": "yes", "connect": "failed", "src": "2001:db8::20"},
	} {
		row := store.DoctorRow{NodeID: id, CheckID: "ipv6", Status: 1, TitleKey: "doctor.ipv6.title", Params: params}
		if err := e.st.PutDoctor(context.Background(), id, []store.DoctorRow{row}, true, now); err != nil {
			t.Fatal(err)
		}
	}
	e.fake.icmp["203.0.113.10"], e.fake.icmp["203.0.113.20"], e.fake.icmp["2001:db8::10"] = true, true, true
	clock := now
	hs := health.New(e.st, e.v, builtin.Registry(), twoNodeFleet{}, health.Config{
		Country: e.svc.Country, Now: func() time.Time { return clock }, Log: slog.New(slog.NewTextHandler(e.logs, nil)),
		SnapshotTTL: time.Nanosecond, StartGrace: time.Nanosecond,
	})
	ctx := context.Background()
	e.enable("")

	if !hs.CountryTick(ctx) {
		t.Fatal("no IPv4 round")
	}
	v4Posts := len(e.fake.posts())
	for _, p := range e.fake.posts() {
		if strings.Contains(p.Body, "ipVersion") || strings.Contains(p.Body, "2001:db8") {
			t.Fatalf("the IPv4 round sent %s", p.Body)
		}
	}
	if !hs.CountryTick(ctx) {
		t.Fatal("no IPv6 round on the next poll")
	}
	if hs.CountryTick(ctx) {
		t.Fatal("a third round ran inside the interval")
	}
	posts := e.fake.posts()[v4Posts:]
	if len(posts) != 2 { // the IPv6 control and the one IPv6 node (ICMP only): nl1 is not measured
		t.Fatalf("IPv6 round made %d measurements: %+v", len(posts), posts)
	}
	if got := jsonOf(t, posts[0].Body); got["target"] != "ya.ru" || got["measurementOptions"].(map[string]any)["ipVersion"] != 6.0 {
		t.Errorf("IPv6 control = %s", posts[0].Body)
	}
	if got := jsonOf(t, posts[1].Body); got["target"] != "2001:db8::10" || got["locations"] != "m"+strconv.Itoa(v4Posts+1) || got["measurementOptions"].(map[string]any)["ipVersion"] != nil {
		t.Errorf("IPv6 node request = %s", posts[1].Body)
	}

	for family, want := range map[int]map[string]string{4: {"de1": "ok", "nl1": "ok"}, 6: {"de1": "ok", "nl1": "unknown"}} {
		rows, err := e.st.RecentCountryChecks(ctx, family, 5)
		if err != nil || len(rows) != 2 {
			t.Fatalf("family %d rows = %+v, %v", family, rows, err)
		}
		for _, r := range rows {
			if r.Family != family || r.Verdict != want[r.NodeID] {
				t.Errorf("family %d: %+v, want verdict %s", family, r, want[r.NodeID])
			}
			if family == 6 && r.NodeID == "nl1" && !strings.Contains(r.Detail, `"reason":"ipv6_broken"`) {
				t.Errorf("nl1 IPv6 row = %s, want the reason ipv6_broken", r.Detail)
			}
		}
	}

	// the switch off: IPv4 goes on, nothing is measured over IPv6
	if _, err := e.update(&adminv1.UpdateGlobalpingRequest{Ipv6: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	before := len(e.fake.posts())
	clock = clock.Add(time.Hour)
	if !hs.CountryTick(ctx) || hs.CountryTick(ctx) {
		t.Fatal("with the IPv6 checks off exactly one round (IPv4) is due")
	}
	for _, p := range e.fake.posts()[before:] {
		if strings.Contains(p.Body, "ipVersion") || strings.Contains(p.Body, "2001:db8") {
			t.Fatalf("an IPv6 measurement with the switch off: %s", p.Body)
		}
	}
	if rows, _ := e.st.RecentCountryChecks(ctx, 6, 5, "de1"); len(rows) != 1 {
		t.Fatalf("IPv6 rows = %+v, want the earlier one only", rows)
	}
}

// twoNodeFleet reports de1 and nl1 as connected.
type twoNodeFleet struct{ liveFleet }

func (twoNodeFleet) Live(context.Context) ([]store.NodeLiveRow, error) {
	return []store.NodeLiveRow{{NodeID: "de1", State: "active", Exists: true, Connected: true}, {NodeID: "nl1", State: "active", Exists: true, Connected: true}}, nil
}

// ProbeTCP6 (fleet.Config.GlobalpingTCP6): the last resort of the IPv6 check of a node.
func TestProbeTCP6IsOneTCPPingOfTheLiteralFromTwoProbes(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	f := e.fake
	f.tcp["2001:db8::10:443"] = true
	answered, connected, err := e.svc.ProbeTCP6(context.Background(), "2001:db8::10", 443)
	if err != nil || answered != 2 || connected != 2 {
		t.Fatalf("answered %d, connected %d, %v", answered, connected, err)
	}
	posts := f.posts()
	if len(posts) != 1 {
		t.Fatalf("%d measurements: %+v", len(posts), posts)
	}
	// a literal takes no ipVersion; two probes anywhere (no country: the audience's own probes are not wanted here)
	want := map[string]any{"type": "ping", "target": "2001:db8::10", "locations": []any{map[string]any{"limit": 2.0}},
		"measurementOptions": map[string]any{"protocol": "TCP", "port": 443.0, "packets": 3.0}}
	if got := jsonOf(t, posts[0].Body); !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %s", posts[0].Body)
	}
	for _, r := range f.requests() {
		if r.Path == "/v1/probes" {
			t.Error("the probe list was read")
		}
	}

	// the address does not answer: both probes tried and none connected
	delete(f.tcp, "2001:db8::10:443")
	if answered, connected, err = e.svc.ProbeTCP6(context.Background(), "2001:db8::10", 443); err != nil || answered != 2 || connected != 0 {
		t.Fatalf("closed port: answered %d, connected %d, %v", answered, connected, err)
	}
	// Globalping refuses: an error, not a verdict
	f.rateLimited = true
	if _, _, err = e.svc.ProbeTCP6(context.Background(), "2001:db8::10", 443); err == nil {
		t.Fatal("a rate limit was not an error")
	}
}

func TestProbeTCP6DoesNothingWhileGlobalpingIsOff(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.svc.ProbeTCP6(context.Background(), "2001:db8::10", 443); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
	if n := e.fake.count(); n != 0 {
		t.Errorf("%d requests while switched off", n)
	}
}
