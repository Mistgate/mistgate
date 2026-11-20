//go:build !js

package subhost

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"io"
	"os"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	subhostv1 "github.com/mistgate/mistgate/gen/mistgate/subhost/v1"
	"github.com/mistgate/mistgate/internal/panel/pubaddr"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/store/storetest"
	"github.com/mistgate/mistgate/internal/plugin"
)

type fakeFleet struct {
	mu        sync.Mutex
	online    map[string]bool
	hosts     map[string]string // node id -> SessionHost
	specs     map[string][]plugin.InboundSpec
	roles     map[string]*agentv1.SubHostRole
	isHost    func(string) bool
	link      string
	enrolLink bool
	target    func(id, site, bootstrap string) (string, string, string, bool, bool)
	status    map[string]*agentv1.SubHostStatus
	pushes    int // StateChanged calls
}

func (f *fakeFleet) HasSession(id string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.online[id] }
func (f *fakeFleet) SessionHost(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hosts[id]
}
func (f *fakeFleet) InboundSpecs(_ context.Context, id string) ([]plugin.InboundSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[id], nil
}
func (f *fakeFleet) SetSubHostRoles(r map[string]*agentv1.SubHostRole) {
	f.mu.Lock()
	f.roles = r
	f.mu.Unlock()
}
func (f *fakeFleet) SetSubHostNodes(fn func(string) bool) { f.mu.Lock(); f.isHost = fn; f.mu.Unlock() }
func (f *fakeFleet) SetPanelLink(l string)                { f.mu.Lock(); f.link = l; f.mu.Unlock() }
func (f *fakeFleet) SetLinkEnrollment(on bool)            { f.mu.Lock(); f.enrolLink = on; f.mu.Unlock() }
func (f *fakeFleet) StateChanged()                        { f.mu.Lock(); f.pushes++; f.mu.Unlock() }
func (f *fakeFleet) PanelIdentity(time.Time) ([]byte, *ecdsa.PrivateKey, error) {
	return nil, nil, errors.New("no CA in this test")
}
func (f *fakeFleet) SubHostTarget(_ context.Context, id, site, bootstrap string) (string, string, string, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.target == nil {
		return "", "", "", false, false
	}
	return f.target(id, site, bootstrap)
}
func (f *fakeFleet) SubHostStatus(id string) *agentv1.SubHostStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[id]
}

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
	logs *bytes.Buffer
	// clock is the service's clock (unix seconds): the dialer reads it from its own goroutines.
	clock atomic.Int64
}

var allCaps = []string{plugin.CapSubHost, plugin.CapPanelLink, capWSLink}

// a node's own site on 443 (TrustTunnel): the host needs no site name
var ownSite = []plugin.InboundSpec{{ID: "inb_t", Protocol: "trusttunnel", Listen: plugin.Listen{Network: "tcp", Port: 443},
	TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "tt.example.com"}, Settings: []byte(`{}`)}}

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
	e := &env{t: t, ctx: ctx, st: st, fl: &fakeFleet{online: map[string]bool{}, hosts: map[string]string{}, specs: map[string][]plugin.InboundSpec{}},
		res: fakeResolver{}, addr: pubaddr.New("https://old.example"), now: time.Unix(1_800_000_000, 0), logs: &bytes.Buffer{}}
	e.clock.Store(e.now.Unix())
	e.svc = New(Config{Store: st, Fleet: e.fl, Addr: e.addr, StepUp: func(context.Context) error { return nil }, LinkPrefix: "/lnk0123456789abcdef/",
		Resolver: e.res, Now: func() time.Time { return time.Unix(e.clock.Load(), 0) }, Log: slog.New(slog.NewTextHandler(e.logs, nil)),
		CAA: func(context.Context, string) ([]string, bool, error) { return nil, false, nil }})
	e.rpc = rpc{e.svc}
	return e
}

// node adds an active node with caps, online or not, with its own site on 443.
func (e *env) node(name, address string, caps []string, online bool) string {
	e.t.Helper()
	id := store.NewID("nod_")
	if _, err := e.st.CreateEnrollment(e.ctx, &store.NodeRow{ID: id, Name: name, Address: address}, "", []byte(name), "test", e.now, e.now.Add(time.Hour)); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET agent_caps = ?, state = 'active' WHERE id = ?`, strings.Join(caps, " "), id); err != nil {
		e.t.Fatal(err)
	}
	e.fl.online[id] = online
	e.fl.specs[id] = ownSite
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
	_, err := e.rpc.SetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.SetSubscriptionHostRequest{NodeId: node, Domain: domain, SkipDnsCheck: skip}))
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

// ready marks the host ready, as readiness does once its conditions hold.
func (e *env) ready() {
	e.t.Helper()
	if err := e.svc.transition(e.ctx, func(st *state) error {
		st.ReadyAt, st.PublicURL = "1800000000", "https://"+st.Domain
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

func TestTransitions(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	b := e.node("b", "203.0.113.2", allCaps, true)
	finish := func(force bool) error {
		_, err := e.rpc.FinishSubscriptionHostChange(e.ctx, connect.NewRequest(&subhostv1.FinishSubscriptionHostChangeRequest{Force: force}))
		return err
	}
	cancel := func() error {
		_, err := e.rpc.CancelSubscriptionHostChange(e.ctx, connect.NewRequest(&subhostv1.CancelSubscriptionHostChangeRequest{}))
		return err
	}
	move := func(id string) error {
		_, err := e.rpc.StartSubscriptionHostMove(e.ctx, connect.NewRequest(&subhostv1.StartSubscriptionHostMoveRequest{NodeId: id}))
		return err
	}
	disable := func(u string) error {
		_, err := e.rpc.DisableSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.DisableSubscriptionHostRequest{PublicUrl: u}))
		return err
	}

	wantCode(t, finish(false), "sub_host_wrong_state")
	wantCode(t, cancel(), "sub_host_wrong_state")
	wantCode(t, e.set(a, "203.0.113.4", true), "sub_host_domain_invalid")
	if err := e.set(a, "Sub.Example.com.", true); err != nil {
		t.Fatal(err)
	}
	st := e.state()
	if st.State != stateOn || st.Domain != "sub.example.com" || st.Node != a || st.PrevURL != "https://old.example" || st.ReadyAt != "" ||
		st.PublicURL != "https://old.example" || len(st.Path) != 25 || !strings.HasPrefix(st.Path, "/") {
		t.Fatalf("on: %+v", st)
	}
	// Not ready: the panel keeps its address, the agents are not moved, the host has its role.
	if e.addr.PublicURL() != "https://old.example" || e.fl.link != "" || e.fl.enrolLink {
		t.Errorf("before ready: address %q, link %q, link enrolment %v", e.addr.PublicURL(), e.fl.link, e.fl.enrolLink)
	}
	if r := e.fl.roles[a]; r == nil || r.Path != st.Path || r.Names[0] != "sub.example.com" || len(r.LinkSources) != 2 || !e.fl.isHost(a) || e.fl.isHost(b) {
		t.Errorf("roles %+v", e.fl.roles)
	}
	e.ready()
	if e.addr.PublicURL() != "https://sub.example.com" || e.fl.link != "wss://sub.example.com/lnk0123456789abcdef/" || !e.fl.enrolLink {
		t.Errorf("ready: address %q, link %q", e.addr.PublicURL(), e.fl.link)
	}
	wantCode(t, e.set(a, "sub.example.com", true), "sub_host_wrong_state")

	wantCode(t, move(a), "sub_host_same_node")
	if err := move(b); err != nil {
		t.Fatal(err)
	}
	// the next node gets a bridge path of its own: the old host never learns its successor's
	if st := e.state(); st.State != stateMoving || st.Next != b || len(e.fl.roles) != 2 || e.fl.roles[b].Path == e.fl.roles[a].Path ||
		e.fl.roles[b].Path != st.NextPath || e.fl.roles[a].Path != st.Path || !e.fl.isHost(b) {
		t.Fatalf("moving: %+v %+v", st, e.fl.roles)
	}
	if err := cancel(); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateOn || st.Next != "" || st.NextPath != "" || len(e.fl.roles) != 1 {
		t.Fatalf("cancelled move: %+v", st)
	}
	if err := move(b); err != nil {
		t.Fatal(err)
	}
	oldPath, nextPath := e.state().Path, e.state().NextPath
	wantCode(t, finish(false), "sub_host_dns_mismatch") // the record still names a
	e.res["sub.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.2")}
	// the record is right, but b cannot carry the subdomain yet: no role report, no public site certificate, no bridge
	var ce *connect.Error
	if err := finish(false); !errors.As(err, &ce) || ce.Message() != "sub_host_next_not_ready: not_ready=host_error%2Csite_cert%2Cbridge" {
		t.Fatalf("finish before b is ready: %v", err)
	}
	e.fl.mu.Lock()
	if e.fl.status == nil {
		e.fl.status = map[string]*agentv1.SubHostStatus{}
	}
	e.fl.status[b] = &agentv1.SubHostStatus{}
	e.fl.target = func(id, _, _ string) (string, string, string, bool, bool) {
		return "203.0.113.2", "b.example.com", "", id == b, true
	}
	e.fl.mu.Unlock()
	e.svc.upSeam = func(id string) bool { return id == b }
	if err := finish(false); err != nil {
		t.Fatal(err)
	}
	e.svc.upSeam = nil
	if st := e.state(); st.State != stateOn || st.Node != b || st.Next != "" || e.fl.roles[a] != nil || e.fl.roles[b] == nil ||
		st.Path != nextPath || st.Path == oldPath || st.NextPath != "" || e.fl.roles[b].Path != nextPath {
		t.Fatalf("moved: %+v", st)
	}
	if e.fl.link != "wss://sub.example.com/lnk0123456789abcdef/" {
		t.Fatal("a move keeps the agents' address")
	}

	wantCode(t, disable(""), "sub_host_return_url_required") // node a remains
	wantCode(t, disable("ftp://x"), "sub_host_return_url_required")
	if err := disable("https://back.example/"); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateLeaving || st.PublicURL != "https://back.example" || e.addr.PublicURL() != "https://back.example" ||
		e.fl.link != "wss://back.example/lnk0123456789abcdef/" || e.fl.enrolLink {
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
	e.fl.hosts[a] = "sub.example.com"
	e.fl.hosts[b] = "sub.example.com" // the host's own session, inside the bridge: not pending
	wantCode(t, finish(false), "sub_host_nodes_pending")
	g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.GetSubscriptionHostRequest{}))
	if err != nil || len(g.Msg.Host.Pending) != 1 || g.Msg.Host.Pending[0].NodeId != a || g.Msg.Host.State != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING {
		t.Fatalf("get while leaving: %v %v", g, err)
	}
	if err := finish(true); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st != (state{PublicURL: "https://back.example"}) {
		t.Fatalf("off: %+v", st)
	}
	// off: the path is gone (a host set later gets a new one), and the old host is sent no role, which clears its agent.json
	if st := e.state(); st.Path != "" || st.NextPath != "" {
		t.Errorf("off keeps a bridge path")
	}
	if len(e.fl.roles) != 0 || e.fl.isHost(b) || e.fl.link != "" {
		t.Errorf("off: roles %v, link %q", e.fl.roles, e.fl.link)
	}
	wantCode(t, disable(""), "sub_host_wrong_state")
	wantCode(t, move(a), "sub_host_wrong_state")
}

func TestNodeFitness(t *testing.T) {
	e := newEnv(t)
	offline := e.node("off", "203.0.113.3", allCaps, false)
	old := e.node("old", "203.0.113.4", []string{plugin.CapPanelLink, capWSLink}, true)
	bare := e.node("bare", "203.0.113.5", allCaps, true)
	e.fl.specs[bare] = nil
	foreign := e.node("foreign", "203.0.113.6", allCaps, true)
	e.fl.specs[foreign] = append(ownSite, plugin.InboundSpec{ID: "inb_r", Protocol: "vless", Listen: plugin.Listen{Network: "tcp", Port: 443},
		Settings: []byte(`{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`)})
	hy2 := e.node("hy2", "203.0.113.7", allCaps, true)
	e.fl.specs[hy2] = []plugin.InboundSpec{{ID: "inb_h", Protocol: "hysteria2", Listen: plugin.Listen{Network: "udp", Port: 443},
		TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "hy.example.com"}, Settings: []byte(`{}`)}}
	wantCode(t, e.set(offline, "sub.example.com", true), "sub_host_node_unfit: reason=offline")
	wantCode(t, e.set(old, "sub.example.com", true), "sub_host_node_unfit: reason=agent_too_old")
	wantCode(t, e.set(bare, "sub.example.com", true), "sub_host_node_unfit: reason=site_name_required")
	wantCode(t, e.set(foreign, "sub.example.com", true), "sub_host_node_unfit: reason=reality_foreign_target")
	wantCode(t, e.set(hy2, "sub.example.com", true), "sub_host_node_unfit: reason=hysteria2_site_443")
	if _, err := e.rpc.SetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.SetSubscriptionHostRequest{NodeId: bare, Domain: "sub.example.com",
		SiteName: "bare.example.com", SkipDnsCheck: true})); err != nil {
		t.Fatalf("a bare node with a site name: %v", err)
	}
	if e.state().SiteName != "bare.example.com" || e.fl.roles[bare].SiteName != "bare.example.com" {
		t.Fatal("the site name must reach the role")
	}
}

func TestFleetMustFollowTheLink(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	lacking := e.node("legacy", "203.0.113.9", []string{plugin.CapSubHost}, true)
	wantCode(t, e.set(a, "sub.example.com", true), "sub_host_nodes_lack_link: nodes="+lacking)
	g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.GetSubscriptionHostRequest{}))
	if err != nil || g.Msg.Host.State != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_OFF ||
		len(g.Msg.Host.LackingLink) != 1 || g.Msg.Host.LackingLink[0].NodeId != lacking || g.Msg.Host.LackingLink[0].Name != "legacy" {
		t.Fatalf("get: %v %v", g, err)
	}
}

func TestDNSRules(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	ip := netip.MustParseAddr
	check := func(site string) *subhostv1.CheckSubscriptionHostDomainResponse {
		t.Helper()
		r, err := e.rpc.CheckSubscriptionHostDomain(e.ctx, connect.NewRequest(&subhostv1.CheckSubscriptionHostDomainRequest{NodeId: a, Domain: "sub.example.com", SiteName: site}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	if c := check(""); c.Matches || len(c.Found) != 0 || len(c.Expected) != 1 || c.Expected[0] != "203.0.113.1" {
		t.Errorf("unresolved: %+v", c)
	}
	e.res["sub.example.com"] = []netip.Addr{ip("198.51.100.1")}
	if c := check(""); c.Matches || c.Found[0] != "198.51.100.1" {
		t.Errorf("foreign A: %+v", c)
	}
	wantCode(t, e.set(a, "sub.example.com", false), "sub_host_dns_mismatch")

	e.res["sub.example.com"] = []netip.Addr{ip("203.0.113.1"), ip("2001:db8::5")}
	e.facts(a, false)
	if c := check(""); c.Matches {
		t.Errorf("AAAA without IPv6 on the node: %+v", c)
	}
	e.facts(a, true)
	if c := check(""); c.Matches {
		t.Errorf("AAAA not the node's: %+v", c)
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	if c := check(""); !c.Matches || len(c.Expected) != 2 {
		t.Errorf("AAAA of the node: %+v", c)
	}
	if c := check("site.example.com"); c.Matches {
		t.Errorf("the site name must point at the node too: %+v", c)
	}
	e.res["site.example.com"] = []netip.Addr{ip("203.0.113.1")}
	if c := check("site.example.com"); !c.Matches {
		t.Errorf("both names point at the node: %+v", c)
	}
	e.res["sub.example.com"] = []netip.Addr{ip("203.0.113.1")}
	if err := e.set(a, "sub.example.com", false); err != nil {
		t.Fatalf("matching DNS: %v", err)
	}
}

func TestGetReportsCAAAndNoSecret(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.GetSubscriptionHostRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	h := g.Msg.Host
	if h.State != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON || h.NodeId != a || h.PublicUrl != "https://old.example" ||
		h.CaaStatus != "account_pending" || h.CaaRecord != `sub.example.com. CAA 0 issue "letsencrypt.org; validationmethods=tls-alpn-01"` {
		t.Fatalf("get: %+v", h)
	}
	path := e.state().Path
	rows, err := e.st.W.QueryContext(e.ctx, `SELECT params FROM audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var audit strings.Builder
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		audit.WriteString(p)
	}
	for where, text := range map[string]string{"view": g.Msg.String(), "audit": audit.String(), "log": e.logs.String()} {
		if strings.Contains(text, path) || strings.Contains(text, "lnk0123456789abcdef") {
			t.Errorf("the %s holds the bridge path or the link prefix", where)
		}
	}
}

// A panel restarted in the middle of a move comes back with both hosts' roles and the address it had.
func TestRestartInTheMiddleOfAMove(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	b := e.node("b", "203.0.113.2", allCaps, true)
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	e.ready()
	if _, err := e.rpc.StartSubscriptionHostMove(e.ctx, connect.NewRequest(&subhostv1.StartSubscriptionHostMoveRequest{NodeId: b})); err != nil {
		t.Fatal(err)
	}
	fl := &fakeFleet{online: e.fl.online, hosts: map[string]string{}, specs: e.fl.specs}
	addr := pubaddr.New("https://old.example")
	again := New(Config{Store: e.st, Fleet: fl, Addr: addr, LinkPrefix: "/lnk0123456789abcdef/", Resolver: e.res})
	if err := again.Load(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(fl.roles) != 2 || fl.roles[a] == nil || fl.roles[b] == nil || addr.PublicURL() != "https://sub.example.com" || fl.link == "" {
		t.Fatalf("after a restart: roles %v, address %q", fl.roles, addr.PublicURL())
	}
}

// The panel dials exactly the hosts of the state (both while moving), with the path of the state and the pin the fleet gives,
// and stops once the role is gone; a host with nothing to verify it by is not dialled.
func TestPanelDialsItsHosts(t *testing.T) {
	e := newEnv(t)
	t.Cleanup(e.svc.Close)
	a := e.node("a", "203.0.113.1", allCaps, true)
	b := e.node("b", "203.0.113.2", allCaps, true)
	pin := strings.Repeat("ab", 32)
	e.fl.target = func(id, _, _ string) (string, string, string, bool, bool) {
		if id == b {
			return "", "", "", false, false
		}
		return "127.0.0.1", "de1.example.com", pin, false, true
	}
	e.svc.cfg.HostPort = 1 // nothing listens: the dial fails, which is all this test needs
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	peers := func() map[string]bool {
		out := map[string]bool{}
		for _, d := range e.svc.tun.dialer.States() {
			if d.Error != "no_verification" {
				out[d.PeerNodeID] = true
			}
		}
		return out
	}
	if p := peers(); !p[a] || p[b] {
		t.Fatalf("dialled %v, want a only", p)
	}
	if _, err := e.rpc.StartSubscriptionHostMove(e.ctx, connect.NewRequest(&subhostv1.StartSubscriptionHostMoveRequest{NodeId: b})); err != nil {
		t.Fatal(err)
	}
	g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.GetSubscriptionHostRequest{}))
	if err != nil || len(g.Msg.Host.Bridges) != 2 || g.Msg.Host.Bridges[0].Up {
		t.Fatalf("bridges %v, %v", g.Msg.GetHost().GetBridges(), err)
	}
	if _, err := e.rpc.DisableSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.DisableSubscriptionHostRequest{PublicUrl: "https://back.example"})); err == nil {
		t.Fatal("a move must be finished or cancelled before turning off")
	}
	if _, err := e.rpc.CancelSubscriptionHostChange(e.ctx, connect.NewRequest(&subhostv1.CancelSubscriptionHostChangeRequest{})); err != nil {
		t.Fatal(err)
	}
	// never ready: turning off ends at once
	if _, err := e.rpc.DisableSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.DisableSubscriptionHostRequest{PublicUrl: "https://back.example"})); err != nil {
		t.Fatal(err)
	}
	for _, d := range e.svc.tun.dialer.States() {
		if d.Conns != 0 {
			t.Fatalf("still connected to %s", d.PeerNodeID)
		}
	}
}

// The panel moves to the subdomain only once every condition holds, says what is missing until then, and does not move back
// when the host's connection drops later; the owner hears of a connection down for 30 seconds, and of its return.
func TestReadiness(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	public, up := false, false
	e.fl.target = func(string, string, string) (string, string, string, bool, bool) {
		return "127.0.0.1", "de1.example.com", "", public, true
	}
	e.svc.upSeam = func(string) bool { return up }
	e.fl.status = map[string]*agentv1.SubHostStatus{}
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	notReady := func() string {
		g, err := e.rpc.GetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.GetSubscriptionHostRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(g.Msg.Host.NotReady, ",")
	}
	if got := notReady(); got != "host_error,site_cert,bridge,panel_cert" {
		t.Fatalf("not ready: %q", got)
	}
	e.fl.status[a] = &agentv1.SubHostStatus{}
	public, up = true, true
	// the panel asks for the subdomain's certificate itself once the bridge is up (Let's Encrypt validates through the host)
	var asked atomic.Int32
	e.svc.tun.get = func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if h.ServerName != "sub.example.com" {
			return nil, errors.New("wrong name")
		}
		asked.Add(1)
		return &tls.Certificate{}, nil
	}
	e.fl.online[a] = false
	e.svc.checkReady(e.ctx)
	waitUntil(t, "the panel obtains the subdomain's certificate", func() bool { return e.svc.panelCert(e.ctx, "sub.example.com") })
	if e.state().ReadyAt != "" || notReady() != "host_offline" || asked.Load() != 1 {
		t.Fatalf("offline host made ready: %+v (asked %d)", e.state(), asked.Load())
	}
	e.fl.online[a] = true
	e.svc.checkReady(e.ctx)
	if st := e.state(); st.ReadyAt == "" || st.PublicURL != "https://sub.example.com" || e.addr.PublicURL() != "https://sub.example.com" ||
		e.fl.link != "wss://sub.example.com/lnk0123456789abcdef/" || !e.fl.enrolLink {
		t.Fatalf("ready: %+v, link %q", st, e.fl.link)
	}
	up = false
	e.svc.checkReady(e.ctx)
	e.clock.Add(31) // seconds
	e.svc.checkReady(e.ctx)
	e.svc.checkReady(e.ctx)
	up = true
	e.svc.checkReady(e.ctx)
	rows, _, err := e.st.Events(e.ctx, store.EventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range rows {
		codes = append(codes, r.Code)
	}
	if strings.Join(codes, ",") != "sub_host_tunnel_up,sub_host_tunnel_down,sub_host_ready" {
		t.Fatalf("events %v", codes)
	}
	if e.addr.PublicURL() != "https://sub.example.com" {
		t.Fatal("a connection that dropped must not move the panel back")
	}
}

// The certificate the panel holds is per domain (M3): a host turned off and set again for another domain is not ready until the
// panel has that domain's certificate, and a failed request is not retried within a minute.
func TestPanelCertIsPerDomain(t *testing.T) {
	e := newEnv(t)
	var asked atomic.Int32
	fail := true
	e.svc.tun.get = func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		asked.Add(1)
		if fail {
			return nil, errors.New("no order yet")
		}
		return &tls.Certificate{}, nil
	}
	e.svc.tun.seen("old.example.com")
	if !e.svc.panelCert(e.ctx, "old.example.com") || e.svc.panelCert(e.ctx, "new.example.com") {
		t.Fatal("the certificate of one domain counts for another")
	}
	e.svc.obtainCert("new.example.com")
	waitUntil(t, "the first try", func() bool {
		e.svc.tun.mu.Lock()
		defer e.svc.tun.mu.Unlock()
		return asked.Load() == 1 && !e.svc.tun.obtaining
	})
	fail = false
	e.svc.obtainCert("new.example.com") // within the minute: not again
	if asked.Load() != 1 || e.svc.panelCert(e.ctx, "new.example.com") {
		t.Fatal("asked again within a minute")
	}
	e.clock.Add(61)
	e.svc.obtainCert("new.example.com")
	waitUntil(t, "the certificate", func() bool { return e.svc.panelCert(e.ctx, "new.example.com") })
}

// Turning off a host that never became ready keeps the address the panel uses (never an empty public_url, never the return
// address pushed to agents that never moved) and ends at once.
func TestDisableANeverReadyHost(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	e.node("b", "203.0.113.2", allCaps, true)
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rpc.DisableSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.DisableSubscriptionHostRequest{})); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.State != stateOff || st.PublicURL != "https://old.example" || st.Path != "" {
		t.Fatalf("after turning off: %+v", st)
	}
	if e.addr.PublicURL() != "https://old.example" || e.fl.link != "" || len(e.fl.roles) != 0 {
		t.Fatalf("address %q, link %q, roles %v", e.addr.PublicURL(), e.fl.link, e.fl.roles)
	}
}

// A panel without a host changes nothing at run time: loading pushes no settings, the tunnel's server does not start; a host
// set starts it, and a refresh that changes nothing pushes nothing (L1, L8).
func TestNoHostNoChange(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	if err := e.svc.Load(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.svc.refresh(e.ctx)
	pushes := func() int { e.fl.mu.Lock(); defer e.fl.mu.Unlock(); return e.fl.pushes }
	if pushes() != 0 {
		t.Fatalf("a panel without a host pushed settings %d times", pushes())
	}
	select {
	case <-e.svc.PublicTunnelReady():
		t.Fatal("the tunnel is ready without a host")
	default:
	}
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.svc.PublicTunnelReady():
	default:
		t.Fatal("the tunnel is not ready once a host is set")
	}
	after := pushes()
	if after != 1 {
		t.Fatalf("setting a host pushed %d times", after)
	}
	e.svc.refresh(e.ctx)
	e.svc.refresh(e.ctx)
	if pushes() != after {
		t.Fatal("a refresh that changed nothing pushed the fleet again")
	}
}

// While a host is set, public_url stays the panel's own address (what the CLI reads at the next start: the agents' mTLS
// address, the admin URL a restore prints), and the effective one is kept apart; a host turned off leaves public_url on the
// address the owner returns to (L2).
func TestPublicURLStaysThePanelsOwn(t *testing.T) {
	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	e.node("b", "203.0.113.2", allCaps, true)
	setting := func(k string) string {
		v, _ := e.st.Setting(e.ctx, k)
		return v
	}
	if err := e.set(a, "sub.example.com", true); err != nil {
		t.Fatal(err)
	}
	e.ready()
	if setting("public_url") != "https://old.example" || setting("sub_host_public_url") != "https://sub.example.com" ||
		e.addr.PublicURL() != "https://sub.example.com" || e.state().PublicURL != "https://sub.example.com" {
		t.Fatalf("ready: public_url %q, effective %q, holder %q", setting("public_url"), setting("sub_host_public_url"), e.addr.PublicURL())
	}
	if _, err := e.rpc.DisableSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.DisableSubscriptionHostRequest{PublicUrl: "https://back.example"})); err != nil {
		t.Fatal(err)
	}
	if setting("public_url") != "https://old.example" || e.addr.PublicURL() != "https://back.example" {
		t.Fatalf("leaving: public_url %q, holder %q", setting("public_url"), e.addr.PublicURL())
	}
	if _, err := e.rpc.FinishSubscriptionHostChange(e.ctx, connect.NewRequest(&subhostv1.FinishSubscriptionHostChangeRequest{Force: true})); err != nil {
		t.Fatal(err)
	}
	if setting("public_url") != "https://back.example" || setting("sub_host_public_url") != "" {
		t.Fatalf("off: public_url %q, effective %q", setting("public_url"), setting("sub_host_public_url"))
	}
}

// The in-memory target keeps a half-close: a person who ends what they send still gets the reply (L6).
func TestDuplexPipeHalfClose(t *testing.T) {
	exit, server := duplexPipe()
	defer exit.Close()
	defer server.Close()
	go func() {
		_, _ = exit.Write([]byte("request"))
		_ = exit.(interface{ CloseWrite() error }).CloseWrite()
	}()
	got, err := io.ReadAll(server)
	if err != nil || string(got) != "request" {
		t.Fatalf("server read %q, %v", got, err)
	}
	go func() { _, _ = server.Write([]byte("reply")); _ = server.Close() }()
	reply, err := io.ReadAll(exit)
	if err != nil || string(reply) != "reply" {
		t.Fatalf("the reply after a half-close: %q, %v", reply, err)
	}
	idle, peer := duplexPipe()
	defer idle.Close()
	defer peer.Close()
	_ = idle.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := idle.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a read past its deadline: %v", err)
	}
}
