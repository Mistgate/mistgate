//go:build !js

package subhost

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/pubaddr"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/store/storetest"
	"github.com/mistgate/mistgate/internal/plugin"
)

type fakeFleet struct {
	mu      sync.Mutex
	online  map[string]bool
	via     map[string]string
	routing fleet.PanelRouting
}

func (f *fakeFleet) HasSession(id string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.online[id] }
func (f *fakeFleet) SessionVia(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.via[id]
}
func (f *fakeFleet) TunnelCertificate(string) (tls.Certificate, error) {
	return tls.Certificate{}, errors.New("no")
}
func (f *fakeFleet) VerifyNodeCertificate(string, [][]byte) error { return errors.New("no") }
func (f *fakeFleet) SetPanelRouting(r fleet.PanelRouting) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routing = r
}
func (f *fakeFleet) StateChanged()             {}
func (f *fakeFleet) LinkHandler() http.Handler { return http.NotFoundHandler() }

type fakeResolver map[string][]netip.Addr

func (r fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := r[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

type env struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	fl   *fakeFleet
	res  fakeResolver
	addr *pubaddr.Holder
	now  time.Time
	svc  *Service
	rpc  rpc
}

var allCaps = []string{plugin.CapHostTunnel, plugin.CapPanelLink, capWSLink}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetSettings(ctx, map[string]string{"public_url": "https://old.example"}); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, st: st, fl: &fakeFleet{online: map[string]bool{}, via: map[string]string{}}, res: fakeResolver{},
		addr: pubaddr.New("https://old.example"), now: time.Unix(1_800_000_000, 0)}
	e.svc = New(Config{Store: st, Fleet: e.fl, Addr: e.addr, StepUp: func(context.Context) error { return nil }, LinkPrefix: "/lnk/",
		Resolver: e.res, Now: func() time.Time { return e.now },
		CAA: func(context.Context, string) ([]string, bool, error) { return nil, false, nil }})
	e.rpc = rpc{e.svc}
	return e
}

// node adds a node with caps, online or not.
func (e *env) node(name, address string, caps []string, online bool) string {
	e.t.Helper()
	id := store.NewID("nod_")
	if _, err := e.st.CreateEnrollment(e.ctx, &store.NodeRow{ID: id, Name: name, Address: address}, "", []byte(name), "test", e.now, e.now.Add(time.Hour)); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET agent_caps = ? WHERE id = ?`, strings.Join(caps, " "), id); err != nil {
		e.t.Fatal(err)
	}
	e.fl.online[id] = online
	return id
}

func (e *env) facts(id string, hasIPv6 bool) {
	e.t.Helper()
	v := 0
	if hasIPv6 {
		v = 1
	}
	if _, err := e.st.W.ExecContext(e.ctx, `INSERT INTO node_facts (node_id, has_ipv6, updated_at) VALUES (?, ?, 1)
		ON CONFLICT(node_id) DO UPDATE SET has_ipv6 = excluded.has_ipv6`, id, v); err != nil {
		e.t.Fatal(err)
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) || !strings.HasPrefix(ce.Message(), code) {
		t.Fatalf("error %v, want %s", err, code)
	}
}

func (e *env) set(node, domain string, skip bool) error {
	_, err := e.rpc.SetSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.SetSubscriptionHostRequest{NodeId: node, Domain: domain, SkipDnsCheck: skip}))
	return err
}

func (e *env) state() state {
	e.t.Helper()
	st, err := loadState(e.ctx, e.st)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func TestTransitions(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	b := e.node("b", "203.0.113.2", allCaps, true)
	finish := func(force bool) error {
		_, err := e.rpc.FinishSubscriptionHostChange(e.ctx, connect.NewRequest(&adminv1.FinishSubscriptionHostChangeRequest{Force: force}))
		return err
	}
	cancel := func() error {
		_, err := e.rpc.CancelSubscriptionHostChange(e.ctx, connect.NewRequest(&adminv1.CancelSubscriptionHostChangeRequest{}))
		return err
	}
	move := func(id string) error {
		_, err := e.rpc.StartSubscriptionHostMove(e.ctx, connect.NewRequest(&adminv1.StartSubscriptionHostMoveRequest{NodeId: id}))
		return err
	}
	disable := func(u string) error {
		_, err := e.rpc.DisableSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.DisableSubscriptionHostRequest{PublicUrl: u}))
		return err
	}

	wantCode(t, finish(false), "sub_host_wrong_state")
	wantCode(t, cancel(), "sub_host_wrong_state")
	wantCode(t, e.set(a, "1.2.3.4", true), "sub_host_domain_invalid")
	if err := e.set(a, "Sub.Example.com.", true); err != nil {
		t.Fatal(err)
	}
	st := e.state()
	if st.State != stateOn || st.Domain != "sub.example.com" || st.Node != a || st.PrevURL != "https://old.example" ||
		st.PublicURL != "https://sub.example.com" || !strings.HasSuffix(st.SNI, ".sub.example.com") || len(st.SNI) != 16+len(".sub.example.com") {
		t.Fatalf("on: %+v", st)
	}
	if e.addr.PublicURL() != "https://sub.example.com" || e.svc.ACMEHost() != "sub.example.com" {
		t.Errorf("address %q, ACME host %q", e.addr.PublicURL(), e.svc.ACMEHost())
	}
	r := e.fl.routing
	if !r.LinkOnly || r.LinkURL != "wss://sub.example.com/lnk/" || r.Tunnels[a].SNI != st.SNI || !r.IsHost(a) || r.IsHost(b) {
		t.Errorf("routing on: %+v", r)
	}
	wantCode(t, e.set(a, "sub.example.com", true), "sub_host_wrong_state")

	if err := move(b); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateMoving || st.Next != b || st.NextSNI == "" || len(e.fl.routing.Tunnels) != 2 || !e.fl.routing.IsHost(b) {
		t.Fatalf("moving: %+v %+v", st, e.fl.routing)
	}
	if err := cancel(); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateOn || st.Next != "" || st.NextSNI != "" {
		t.Fatalf("cancelled move: %+v", st)
	}
	if err := move(b); err != nil {
		t.Fatal(err)
	}
	next := e.state().NextSNI
	if err := finish(false); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateOn || st.Node != b || st.SNI != next || st.Next != "" {
		t.Fatalf("moved: %+v", st)
	}

	wantCode(t, disable(""), "sub_host_return_url_required") // node a remains
	wantCode(t, disable("ftp://x"), "sub_host_return_url_required")
	if err := disable("https://back.example/"); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateLeaving || st.PublicURL != "https://back.example" || e.addr.PublicURL() != "https://back.example" ||
		e.fl.routing.LinkURL != "wss://back.example/lnk/" {
		t.Fatalf("leaving: %+v", st)
	}
	if err := cancel(); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateOn || st.PublicURL != "https://sub.example.com" {
		t.Fatalf("cancelled leave: %+v", st)
	}
	if err := disable("https://back.example"); err != nil {
		t.Fatal(err)
	}
	e.fl.via[a] = "sub.example.com:443"
	wantCode(t, finish(false), "sub_host_nodes_pending")
	g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionHostRequest{}))
	if err != nil || len(g.Msg.Host.Pending) != 1 || g.Msg.Host.Pending[0].NodeId != a || g.Msg.Host.State != adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING {
		t.Fatalf("get while leaving: %v %v", g, err)
	}
	if err := finish(true); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st != (state{PublicURL: "https://back.example"}) {
		t.Fatalf("off: %+v", st)
	}
	if e.fl.routing.LinkOnly || e.fl.routing.Tunnels != nil || e.svc.ACMEHost() != "" {
		t.Errorf("routing off: %+v", e.fl.routing)
	}
	wantCode(t, disable(""), "sub_host_wrong_state")
	wantCode(t, move(a), "sub_host_wrong_state")
}

func TestNodeFitness(t *testing.T) {
	e := newEnv(t)
	offline := e.node("off", "203.0.113.3", allCaps, false)
	old := e.node("old", "203.0.113.4", []string{plugin.CapPanelLink, capWSLink}, true)
	wantCode(t, e.set(offline, "sub.example.com", true), "sub_host_node_unfit: reason=offline")
	wantCode(t, e.set(old, "sub.example.com", true), "sub_host_node_unfit: reason=agent_too_old")
}

func TestFleetMustFollowTheLink(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	lacking := e.node("legacy", "203.0.113.9", []string{plugin.CapHostTunnel}, true)
	wantCode(t, e.set(a, "sub.example.com", true), "sub_host_nodes_lack_link: nodes="+lacking)
	g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionHostRequest{}))
	if err != nil || g.Msg.Host.State != adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_OFF ||
		len(g.Msg.Host.LackingLink) != 1 || g.Msg.Host.LackingLink[0].NodeId != lacking || g.Msg.Host.LackingLink[0].Name != "legacy" {
		t.Fatalf("get: %v %v", g, err)
	}
}

func TestDNSRules(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	ip := netip.MustParseAddr
	check := func() *adminv1.CheckSubscriptionHostDomainResponse {
		t.Helper()
		r, err := e.rpc.CheckSubscriptionHostDomain(e.ctx, connect.NewRequest(&adminv1.CheckSubscriptionHostDomainRequest{NodeId: a, Domain: "sub.example.com"}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	if c := check(); c.Matches || len(c.Found) != 0 || len(c.Expected) != 1 || c.Expected[0] != "203.0.113.1" {
		t.Errorf("unresolved: %+v", c)
	}
	e.res["sub.example.com"] = []netip.Addr{ip("198.51.100.1")}
	if c := check(); c.Matches || c.Found[0] != "198.51.100.1" {
		t.Errorf("foreign A: %+v", c)
	}
	wantCode(t, e.set(a, "sub.example.com", false), "sub_host_dns_mismatch")

	e.res["sub.example.com"] = []netip.Addr{ip("203.0.113.1"), ip("2001:db8::5")}
	e.facts(a, false)
	if c := check(); c.Matches {
		t.Errorf("AAAA without IPv6 on the node: %+v", c)
	}
	e.facts(a, true)
	if c := check(); c.Matches {
		t.Errorf("AAAA not the node's: %+v", c)
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	if c := check(); !c.Matches || len(c.Expected) != 2 {
		t.Errorf("AAAA of the node: %+v", c)
	}
	e.res["sub.example.com"] = []netip.Addr{ip("203.0.113.1")}
	if err := e.set(a, "sub.example.com", false); err != nil {
		t.Fatalf("matching DNS: %v", err)
	}
}

func TestTunnelEvents(t *testing.T) {
	e := newEnv(t)
	tn := &tunnel{nodeID: "nod_x", since: e.now, cancel: func() {}}
	e.svc.tunnels["nod_x"] = tn
	events := func() []string {
		rows, _, err := e.st.Events(e.ctx, store.EventFilter{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.Code)
		}
		return out
	}
	e.now = e.now.Add(29 * time.Second)
	e.svc.checkTunnels(e.ctx)
	if got := events(); len(got) != 0 {
		t.Fatalf("too early: %v", got)
	}
	e.now = e.now.Add(2 * time.Second)
	e.svc.checkTunnels(e.ctx)
	e.svc.checkTunnels(e.ctx)
	if got := events(); len(got) != 1 || got[0] != "sub_host_tunnel_down" {
		t.Fatalf("down: %v", got)
	}
	e.svc.setUp(tn, true)
	e.svc.checkTunnels(e.ctx)
	e.svc.checkTunnels(e.ctx)
	if got := events(); len(got) != 2 || got[0] != "sub_host_tunnel_up" {
		t.Fatalf("up: %v", got)
	}
}

func TestGetReportsCAA(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionHostRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	h := g.Msg.Host
	if h.State != adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON || h.NodeId != a || h.PublicUrl != "https://sub.example.com" ||
		h.CaaStatus != "account_pending" || h.CaaRecord != `sub.example.com. CAA 0 issue "letsencrypt.org; validationmethods=tls-alpn-01"` {
		t.Fatalf("get: %+v", h)
	}
}

// The host node's own session comes in through its tunnel: while leaving, Get lists it as pending, but never with the
// tunnel SNI it dialled.
func TestPendingNeverShowsTheTunnelSNI(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	b := e.node("b", "203.0.113.2", allCaps, true)
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	sni := e.state().SNI
	e.fl.via[a] = sni
	e.fl.via[b] = "sub.example.com:443"
	if _, err := e.rpc.DisableSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.DisableSubscriptionHostRequest{PublicUrl: "https://back.example"})); err != nil {
		t.Fatal(err)
	}
	g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionHostRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	vias := map[string]string{}
	for _, p := range g.Msg.Host.Pending {
		vias[p.NodeId] = p.Via
	}
	if vias[a] != "tunnel" || vias[b] != "sub.example.com:443" || strings.Contains(g.Msg.String(), sni) {
		t.Fatalf("pending %v; the response must not carry the tunnel SNI", vias)
	}
}
