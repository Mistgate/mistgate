//go:build !js

// Package subhost is the panel's side of the subscription host (design/adr/0018-panel-behind-nat.md): one node serves a
// subdomain on its 443 and hands its TLS to the panel, which dials the node over the bridge. People's subscriptions and the
// agents' link then reach a panel that has no public address of its own.
package subhost

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/acme"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	subhostv1 "github.com/mistgate/mistgate/gen/mistgate/subhost/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/pubaddr"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"google.golang.org/protobuf/proto"
)

const capWSLink = "ws-link/1"

// Fleet is what the service needs of the fleet (*fleet.Fleet).
type Fleet interface {
	HasSession(nodeID string) bool
	SessionHost(nodeID string) string
	InboundSpecs(ctx context.Context, nodeID string) ([]plugin.InboundSpec, error)
	SetSubHostRoles(roles map[string]*agentv1.SubHostRole)
	SetSubHostNodes(isHost func(nodeID string) bool)
	SetPanelLink(linkURL string)
	SetLinkEnrollment(on bool)
	StateChanged()
	PanelIdentity(now time.Time) ([]byte, *ecdsa.PrivateKey, error)
	SubHostTarget(ctx context.Context, nodeID, siteName, bootstrapPin string) (host, sni, pin string, public, ok bool)
	SubHostStatus(nodeID string) *agentv1.SubHostStatus
}

// Resolver looks up the addresses of a host (net.DefaultResolver).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Config struct {
	Store  *store.Store
	Fleet  Fleet
	Addr   *pubaddr.Holder
	StepUp func(context.Context) error // auth.Service.RequireStepUp; nil denies every change
	// LinkPrefix is the secret path prefix of the agent link ("/<secret>/").
	LinkPrefix string
	// ACMEDir is the autocert cache directory (the account key for the CAA record); "" = no account yet.
	ACMEDir string
	// Test seams.
	HostPort    uint16                                               // the hosts' port; 0 = 443
	ACMEClient  *acme.Client                                         // nil = Let's Encrypt
	Certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error) // the subdomain's certificate instead of ACME's
	Resolver    Resolver
	CAA         func(ctx context.Context, name string) (issue []string, found bool, err error)
	Now         func() time.Time
	Log         *slog.Logger
}

// Service is the subscription host: the owner's RPCs and the routing the panel follows.
type Service struct {
	cfg Config
	mu  sync.Mutex // serialises transitions

	cmu sync.Mutex
	cur state // the state of the last applyRouting

	tun *tunnel // tunnel.go

	// readiness.go; mu
	downSince time.Time
	downTold  bool
	upSeam    func(nodeID string) bool // tests: whether the panel holds a connection to the host

	caaMu    sync.Mutex
	caaCache map[string]caaEntry
	account  string // the ACME account URI, once known

	// What the fleet was last told (applyRouting; mu): a push only when it changes, so a panel without a host, and a host
	// whose roles did not change, cause no settings round to the agents.
	sentRoles map[string]*agentv1.SubHostRole
	sentLink  string
	sentEnrol bool

	wake      chan struct{} // a host was set: Run starts ticking
	ready     chan struct{} // closed once a host was set: the tunnel's server starts (PublicTunnelReady)
	readyOnce sync.Once
}

// New builds the service; Load applies the stored state.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	if cfg.CAA == nil {
		cfg.CAA = lookupCAA
	}
	s := &Service{cfg: cfg, caaCache: map[string]caaEntry{}, wake: make(chan struct{}, 1), ready: make(chan struct{})}
	s.newTunnel()
	return s
}

// Load reads the stored state and applies it (the public address, the fleet's settings). The app calls it at start, so the
// first request already sees the right address. While the host is off the panel keeps the address it started with.
func (s *Service) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := loadState(ctx, s.cfg.Store)
	if err != nil {
		return err
	}
	if st.State == stateOff && s.cfg.Addr != nil {
		st.PublicURL = s.cfg.Addr.PublicURL()
	}
	s.applyRouting(ctx, st)
	return nil
}

// Run keeps the dialer on the hosts (their pins and certificates change) and refreshes their roles (the fleet's addresses
// change) until ctx ends. Without a host it only waits for one to be set.
func (s *Service) Run(ctx context.Context) {
	defer s.Close()
	var tick *time.Ticker
	defer func() {
		if tick != nil {
			tick.Stop()
		}
	}()
	for n := 1; ; n++ {
		if s.current().State == stateOff {
			if tick != nil {
				tick.Stop()
				tick = nil
			}
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		if tick == nil {
			tick = time.NewTicker(10 * time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if n%6 == 0 {
			s.refresh(ctx)
		} else {
			s.mu.Lock()
			s.syncDials(s.current())
			s.mu.Unlock()
		}
		s.checkReady(ctx)
	}
}

// refresh applies the stored state again (the roles' link sources follow the fleet's nodes).
func (s *Service) refresh(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := loadState(ctx, s.cfg.Store)
	if err != nil {
		s.cfg.Log.Warn("subscription host: read state", "code", "sub_host_state_read_failed", "err", err)
		return
	}
	if st.State == stateOff {
		return
	}
	s.applyRouting(ctx, st)
}

// current is the state of the last applyRouting.
func (s *Service) current() state {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	return s.cur
}

// coded is a refusal the admin UI words itself ("err.<code>"): the code, then its values as a query string.
func coded(c connect.Code, code string, kv ...string) error {
	q := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	if len(q) > 0 {
		code += ": " + q.Encode()
	}
	return connect.NewError(c, errors.New(code))
}

func wrongState() error { return coded(connect.CodeFailedPrecondition, "sub_host_wrong_state") }

func (s *Service) internal(op string, err error) error {
	s.cfg.Log.Error("subscription host: "+op, "code", "sub_host_internal", "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

func (s *Service) stepUp(ctx context.Context) error {
	if s.cfg.StepUp == nil {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up unavailable"))
	}
	return s.cfg.StepUp(ctx)
}

// audit records a change: domains, names and node ids only, never the bridge path or the link prefix.
func (s *Service) audit(ctx context.Context, action string, params map[string]string) {
	actor := "unknown"
	if a, ok := auth.AdminFrom(ctx); ok {
		actor = a.ID
	}
	raw, _ := json.Marshal(params)
	if err := s.cfg.Store.Audit(ctx, s.cfg.Now(), store.AuditEntry{Actor: actor, Action: action, Params: string(raw), Result: "ok"}); err != nil {
		s.cfg.Log.Warn("subscription host audit", "code", "sub_host_audit_failed", "action", action, "err", err)
	}
}

// normName checks a host name: lower case, DNS-name syntax, at least two labels, not an address. code is the refusal.
func normName(d, code string) (string, error) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	bad := coded(connect.CodeInvalidArgument, code)
	if d == "" || len(d) > 253 || net.ParseIP(d) != nil {
		return "", bad
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", bad
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", bad
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", bad
			}
		}
	}
	return d, nil
}

func normDomain(d string) (string, error) { return normName(d, "sub_host_domain_invalid") }

// normSiteName is an optional site name: "" stays "".
func normSiteName(d string) (string, error) {
	if strings.TrimSpace(d) == "" {
		return "", nil
	}
	return normName(d, "sub_host_site_name_invalid")
}

func unfit(reason string) error {
	return coded(connect.CodeFailedPrecondition, "sub_host_node_unfit", "reason", reason)
}

// fitNode refuses a node that cannot be the host for domain with the site name siteName.
func (s *Service) fitNode(ctx context.Context, id, domain, siteName string) (store.NodeRow, error) {
	n, err := s.cfg.Store.Node(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return n, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return n, s.internal("read node", err)
	}
	switch {
	case n.State == "retired":
		return n, unfit("retired")
	case !store.CapabilityPresent(n.AgentCaps, plugin.CapSubHost):
		return n, unfit("agent_too_old")
	case !s.cfg.Fleet.HasSession(n.ID):
		return n, unfit("offline")
	}
	if reason, err := s.unfitReason(ctx, n.ID, domain, siteName); err != nil {
		return n, s.internal("read inbounds", err)
	} else if reason != "" {
		return n, unfit(reason)
	}
	return n, nil
}

// unfitReason is plugin.SubHostRoutes' verdict on the node's inbounds, as the agent will judge them.
func (s *Service) unfitReason(ctx context.Context, nodeID, domain, siteName string) (string, error) {
	specs, err := s.cfg.Fleet.InboundSpecs(ctx, nodeID)
	if err != nil {
		return "", err
	}
	_, reason := plugin.SubHostRoutes(plugin.SubHostRole{Names: []string{domain}, Path: "/x", SiteName: siteName}, specs)
	return reason, nil
}

// lackingLink is every non-retired node whose agent cannot follow the panel to a new link address.
func (s *Service) lackingLink(ctx context.Context) ([]*subhostv1.SubscriptionHostNodeRef, error) {
	nodes, err := s.cfg.Store.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []*subhostv1.SubscriptionHostNodeRef
	for _, n := range nodes {
		if n.State == "retired" || n.State == "pending" {
			continue
		}
		if !store.CapabilityPresent(n.AgentCaps, capWSLink) || !store.CapabilityPresent(n.AgentCaps, plugin.CapPanelLink) {
			out = append(out, &subhostv1.SubscriptionHostNodeRef{NodeId: n.ID, Name: n.Name})
		}
	}
	return out, nil
}

func refIDs(refs []*subhostv1.SubscriptionHostNodeRef) string {
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.NodeId
	}
	return strings.Join(ids, ",")
}

// pending is every online node still connected through the subdomain. The host nodes' own sessions are not: they run inside
// the bridge and end with it.
func (s *Service) pending(ctx context.Context, st state) ([]*subhostv1.SubscriptionHostPendingNode, error) {
	if st.Domain == "" {
		return nil, nil
	}
	nodes, err := s.cfg.Store.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []*subhostv1.SubscriptionHostPendingNode
	for _, n := range nodes {
		if n.State == "retired" || n.ID == st.Node || n.ID == st.Next || !s.cfg.Fleet.HasSession(n.ID) {
			continue
		}
		if s.cfg.Fleet.SessionHost(n.ID) == st.Domain {
			out = append(out, &subhostv1.SubscriptionHostPendingNode{NodeId: n.ID, Name: n.Name})
		}
	}
	slices.SortFunc(out, func(a, b *subhostv1.SubscriptionHostPendingNode) int { return strings.Compare(a.NodeId, b.NodeId) })
	return out, nil
}

// returnURL checks the address the panel goes back to: "" or http(s)://host[:port], like `mistgate setup --public-url`.
func returnURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return "", coded(connect.CodeInvalidArgument, "sub_host_return_url_required")
	}
	return u.Scheme + "://" + u.Host, nil
}

// linkURL is the agents' link address for a public URL: wss://host + the link prefix; "" without either.
func linkURL(publicURL, prefix string) string {
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" || prefix == "" {
		return ""
	}
	return "wss://" + u.Host + prefix
}

// linkSources are the fleet nodes' addresses as prefixes (an IPv4 address, the /64 of an IPv6 one): the host counts
// connections from them as agents' links. A node known by a name only is left out.
func (s *Service) linkSources(ctx context.Context) []string {
	nodes, err := s.cfg.Store.Nodes(ctx, false)
	if err != nil {
		s.cfg.Log.Warn("subscription host: read nodes", "code", "sub_host_internal", "err", err)
		return nil
	}
	var out []string
	add := func(raw string) {
		a, err := netip.ParseAddr(raw)
		if err != nil {
			return
		}
		a = a.Unmap()
		bits := 32
		if a.Is6() {
			bits = 64
		}
		p, _ := a.Prefix(bits)
		if v := p.String(); !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	for _, n := range nodes {
		if n.State == "retired" {
			continue
		}
		add(n.Address)
		add(n.IPv6Entry)
	}
	slices.Sort(out)
	return out
}

// applyRouting makes the panel follow st: the hosts' roles, and, once the host is ready, the public address, the agents'
// link address and link-only enrolments. Call with mu held.
func (s *Service) applyRouting(ctx context.Context, st state) {
	roles := map[string]*agentv1.SubHostRole{}
	if st.State != stateOff && st.Domain != "" && st.Path != "" {
		sources := s.linkSources(ctx)
		roles[st.Node] = &agentv1.SubHostRole{Names: []string{st.Domain}, Path: st.Path, SiteName: st.SiteName, LinkSources: sources}
		if st.State == stateMoving && st.Next != "" && st.NextPath != "" {
			roles[st.Next] = &agentv1.SubHostRole{Names: []string{st.Domain}, Path: st.NextPath, SiteName: st.NextSiteName, LinkSources: sources}
		}
	}
	if s.cfg.Addr != nil {
		s.cfg.Addr.Set(st.PublicURL)
	}
	hosted := (st.State == stateOn || st.State == stateMoving) && st.ReadyAt != ""
	link := ""
	if hosted || st.State == stateLeaving {
		link = linkURL(st.PublicURL, s.cfg.LinkPrefix)
	}
	if !sameRoles(roles, s.sentRoles) || link != s.sentLink || hosted != s.sentEnrol {
		s.cfg.Fleet.SetSubHostRoles(roles)
		s.cfg.Fleet.SetSubHostNodes(func(id string) bool { return roles[id] != nil })
		s.cfg.Fleet.SetPanelLink(link)
		s.cfg.Fleet.SetLinkEnrollment(hosted)
		s.cfg.Fleet.StateChanged()
		s.sentRoles, s.sentLink, s.sentEnrol = roles, link, hosted
	}
	if st.State != stateOff {
		s.readyOnce.Do(func() { close(s.ready) })
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	s.cmu.Lock()
	s.cur = st
	s.cmu.Unlock()
	s.syncDials(st)
}

// transition loads the state, lets change edit it, saves and applies it. change returns the refusal, if any.
func (s *Service) transition(ctx context.Context, change func(st *state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := loadState(ctx, s.cfg.Store)
	if err != nil {
		return s.internal("read state", err)
	}
	if err := change(&st); err != nil {
		return err
	}
	if err := saveState(ctx, s.cfg.Store, st); err != nil {
		return s.internal("save state", err)
	}
	s.applyRouting(ctx, st)
	return nil
}

func stateOf(v string) subhostv1.SubscriptionHostState {
	switch v {
	case stateOn:
		return subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON
	case stateMoving:
		return subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING
	case stateLeaving:
		return subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING
	}
	return subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_OFF
}

// view is the SubscriptionHost message of the current state. It holds no secret: no bridge path, no link prefix.
func (s *Service) view(ctx context.Context) (*subhostv1.SubscriptionHost, error) {
	st, err := loadState(ctx, s.cfg.Store)
	if err != nil {
		return nil, s.internal("read state", err)
	}
	h := &subhostv1.SubscriptionHost{State: stateOf(st.State), Domain: st.Domain, NodeId: st.Node, NextNodeId: st.Next,
		PublicUrl: st.PublicURL, SiteName: st.SiteName, Ready: st.ReadyAt != ""}
	if h.LackingLink, err = s.lackingLink(ctx); err != nil {
		return nil, s.internal("read nodes", err)
	}
	if st.State == stateOff {
		return h, nil
	}
	if st.State == stateLeaving {
		if h.Pending, err = s.pending(ctx, st); err != nil {
			return nil, s.internal("read nodes", err)
		}
	}
	s.addLive(ctx, st, h)
	if !h.Ready {
		h.NotReady = s.notReady(ctx, st)
	}
	h.CaaRecord, h.CaaStatus = s.caa(ctx, st.Domain)
	return h, nil
}

// RoleOf is the subscription host role of a node now (the host, and the next one while moving): what its inbounds must fit.
func (s *Service) RoleOf(nodeID string) (plugin.SubHostRole, bool) {
	st := s.current()
	if st.State == stateOff || nodeID == "" {
		return plugin.SubHostRole{}, false
	}
	switch nodeID {
	case st.Node:
		return plugin.SubHostRole{Names: []string{st.Domain}, Path: st.Path, SiteName: st.SiteName}, true
	case st.Next:
		if st.State == stateMoving {
			return plugin.SubHostRole{Names: []string{st.Domain}, Path: st.NextPath, SiteName: st.NextSiteName}, true
		}
	}
	return plugin.SubHostRole{}, false
}

// PublicTunnelReady closes once a host was set: the public server starts serving the tunnel's connections then.
func (s *Service) PublicTunnelReady() <-chan struct{} { return s.ready }

// sameRoles says two role sets are the same (the fleet then needs no push).
func sameRoles(a, b map[string]*agentv1.SubHostRole) bool {
	if len(a) != len(b) {
		return false
	}
	for id, r := range a {
		if !proto.Equal(r, b[id]) {
			return false
		}
	}
	return true
}
