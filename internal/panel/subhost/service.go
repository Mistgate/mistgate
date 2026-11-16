//go:build !js

// Package subhost puts the panel's public address on a subdomain served by one node (the subscription host): people's
// subscriptions and the agents' link reach the panel through that node, which forwards them over a tunnel the panel dials.
package subhost

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/protocols/vless"
	"github.com/mistgate/mistgate/internal/panel/pubaddr"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	capWSLink = "ws-link/1"
	// tunnelDownAfter is how long a tunnel may stay down before the owner hears of it.
	tunnelDownAfter = 30 * time.Second
)

// Fleet is what the service needs of the fleet (*fleet.Fleet).
type Fleet interface {
	HasSession(nodeID string) bool
	SessionVia(nodeID string) string
	TunnelCertificate(sni string) (tls.Certificate, error)
	VerifyNodeCertificate(nodeID string, raw [][]byte) error
	SetPanelRouting(r fleet.PanelRouting)
	StateChanged()
	LinkHandler() http.Handler
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
	Resolver Resolver
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	CAA      func(ctx context.Context, name string) (issue []string, found bool, err error)
	Now      func() time.Time
	Log      *slog.Logger
}

// Service is the subscription host: the owner's RPCs, the tunnels to the host nodes and the listener of the public
// connections they carry.
type Service struct {
	cfg  Config
	mu   sync.Mutex // serialises transitions
	link http.Handler

	pub   *chanListener // KindTLS streams: served as the public listener's connections
	links *chanListener // KindLink streams: the agent link of the host node

	tmu     sync.Mutex
	cur     state // the state of the last applyRouting
	runCtx  context.Context
	tunnels map[string]*tunnel

	caaMu    sync.Mutex
	caaCache map[string]caaEntry
	account  string // the ACME account URI, once known
}

// New builds the service. Call Run to dial the tunnels.
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
	if cfg.Dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		cfg.Dial = d.DialContext
	}
	if cfg.CAA == nil {
		cfg.CAA = lookupCAA
	}
	return &Service{cfg: cfg, link: cfg.Fleet.LinkHandler(), pub: newChanListener(), links: newChanListener(),
		tunnels: map[string]*tunnel{}, caaCache: map[string]caaEntry{}}
}

// Handler is the SubscriptionHostService mount.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewSubscriptionHostServiceHandler(rpc{s}, opts...)
}

// PublicListener yields the public connections (TLS to the subdomain) the tunnels carry.
func (s *Service) PublicListener() net.Listener { return s.pub }

// ACMEHost is the subdomain while the host is in use: the public listener may get a certificate for it.
func (s *Service) ACMEHost() string {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.cur.State == stateOff {
		return ""
	}
	return s.cur.Domain
}

// Load reads the stored state and applies it (the public address, the fleet's routing). Run calls it; the app calls it at
// start so the address is right before the first request. While the host is off the public address stays the one the panel
// started with: the stored public_url is the same in a configured installation, and absent from in-process fixtures.
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
	s.applyRouting(st)
	return nil
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

// normDomain checks a host name: lower case, DNS-name syntax, at least two labels, not an address.
func normDomain(d string) (string, error) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	bad := coded(connect.CodeInvalidArgument, "sub_host_domain_invalid")
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

// fitNode refuses a node that cannot be the host.
func (s *Service) fitNode(ctx context.Context, id string) (store.NodeRow, error) {
	n, err := s.cfg.Store.Node(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return n, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return n, s.internal("read node", err)
	}
	unfit := func(reason string) error {
		return coded(connect.CodeFailedPrecondition, "sub_host_node_unfit", "reason", reason)
	}
	switch {
	case n.State == "retired":
		return n, unfit("retired")
	case !store.CapabilityPresent(n.AgentCaps, plugin.CapHostTunnel):
		return n, unfit("agent_too_old")
	case !s.cfg.Fleet.HasSession(n.ID):
		return n, unfit("offline")
	}
	foreign, err := s.realityForeign443(ctx, n.ID)
	if err != nil {
		return n, s.internal("read inbounds", err)
	}
	if foreign {
		return n, unfit("reality_foreign_target")
	}
	return n, nil
}

// realityForeign443 says whether the node has an enabled VLESS REALITY inbound on TCP 443 whose target is another site: the
// node's port 443 then belongs to that site's camouflage and cannot also serve the subdomain.
func (s *Service) realityForeign443(ctx context.Context, nodeID string) (bool, error) {
	ins, err := s.cfg.Store.FleetInbounds(ctx, nodeID, true)
	if err != nil {
		return false, err
	}
	for _, in := range ins {
		if in.Protocol != vless.ID {
			continue
		}
		var cfg vless.Settings
		if json.Unmarshal([]byte(in.Settings), &cfg) != nil || cfg.Security != "reality" {
			continue
		}
		port := int(in.PortOverride)
		if port == 0 {
			port = cfg.Port
		}
		kind := cfg.Reality.TargetKind
		if kind == "" && strings.TrimSpace(cfg.Reality.Target) != "" {
			kind = "site"
		}
		if port == 443 && kind != "" && kind != "self" {
			return true, nil
		}
	}
	return false, nil
}

// lackingLink is every non-retired node whose agent cannot follow the panel to a new link address.
func (s *Service) lackingLink(ctx context.Context) ([]*adminv1.SubscriptionHostNodeRef, error) {
	nodes, err := s.cfg.Store.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []*adminv1.SubscriptionHostNodeRef
	for _, n := range nodes {
		if n.State == "retired" {
			continue
		}
		if !store.CapabilityPresent(n.AgentCaps, capWSLink) || !store.CapabilityPresent(n.AgentCaps, plugin.CapPanelLink) {
			out = append(out, &adminv1.SubscriptionHostNodeRef{NodeId: n.ID, Name: n.Name})
		}
	}
	return out, nil
}

// pending is every online node still connected through the subdomain or a tunnel name.
func (s *Service) pending(ctx context.Context, st state) ([]*adminv1.SubscriptionHostPendingNode, error) {
	if st.Domain == "" {
		return nil, nil
	}
	nodes, err := s.cfg.Store.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []*adminv1.SubscriptionHostPendingNode
	for _, n := range nodes {
		if n.State == "retired" || !s.cfg.Fleet.HasSession(n.ID) {
			continue
		}
		via := s.cfg.Fleet.SessionVia(n.ID)
		h := hostOnly(via)
		if h != "" && (h == st.Domain || h == st.SNI || h == st.NextSNI) {
			if h != st.Domain {
				via = "tunnel" // the host node's own session: the tunnel SNI is a secret and never leaves the panel
			}
			out = append(out, &adminv1.SubscriptionHostPendingNode{NodeId: n.ID, Name: n.Name, Via: via})
		}
	}
	return out, nil
}

func hostOnly(hostport string) string {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
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

// applyRouting makes the panel follow st: the public address, the fleet's routing and the tunnels. Call with mu held.
func (s *Service) applyRouting(st state) {
	if s.cfg.Addr != nil {
		s.cfg.Addr.Set(st.PublicURL)
	}
	r := fleet.PanelRouting{LinkURL: linkURL(st.PublicURL, s.cfg.LinkPrefix), LinkOnly: st.State != stateOff}
	want := map[string]string{}
	if st.State != stateOff {
		want[st.Node] = st.SNI
		if st.State == stateMoving && st.Next != "" {
			want[st.Next] = st.NextSNI
		}
		r.Tunnels = map[string]fleet.HostTunnel{}
		for id, sni := range want {
			r.Tunnels[id] = fleet.HostTunnel{SNI: sni, Names: []string{st.Domain}}
		}
		node, next := st.Node, st.Next
		r.IsHost = func(id string) bool { return id == node || (next != "" && id == next) }
	}
	s.cfg.Fleet.SetPanelRouting(r)
	s.cfg.Fleet.StateChanged()
	s.tmu.Lock()
	s.cur = st
	s.tmu.Unlock()
	s.reconcile(want)
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
	s.applyRouting(st)
	return nil
}

// view is the SubscriptionHost message of the current state.
func (s *Service) view(ctx context.Context) (*adminv1.SubscriptionHost, error) {
	st, err := loadState(ctx, s.cfg.Store)
	if err != nil {
		return nil, s.internal("read state", err)
	}
	h := &adminv1.SubscriptionHost{Domain: st.Domain, NodeId: st.Node, NextNodeId: st.Next, PublicUrl: st.PublicURL}
	switch st.State {
	case stateOn:
		h.State = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON
	case stateMoving:
		h.State = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING
	case stateLeaving:
		h.State = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING
	default:
		h.State = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_OFF
	}
	if h.LackingLink, err = s.lackingLink(ctx); err != nil {
		return nil, s.internal("read nodes", err)
	}
	if st.State == stateOff {
		return h, nil
	}
	h.Tunnels = s.tunnelViews()
	slices.SortFunc(h.Tunnels, func(a, b *adminv1.SubscriptionHostTunnel) int { return strings.Compare(a.GetNodeId(), b.GetNodeId()) })
	if st.State == stateLeaving {
		if h.Pending, err = s.pending(ctx, st); err != nil {
			return nil, s.internal("read nodes", err)
		}
		slices.SortFunc(h.Pending, func(a, b *adminv1.SubscriptionHostPendingNode) int {
			return strings.Compare(a.GetNodeId(), b.GetNodeId())
		})
	}
	h.CaaRecord, h.CaaStatus = s.caa(ctx, st.Domain)
	return h, nil
}

type rpc struct{ s *Service }

func (r rpc) GetSubscriptionHost(ctx context.Context, _ *connect.Request[adminv1.GetSubscriptionHostRequest]) (*connect.Response[adminv1.GetSubscriptionHostResponse], error) {
	h, err := r.s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetSubscriptionHostResponse{Host: h}), nil
}

func (r rpc) CheckSubscriptionHostDomain(ctx context.Context, req *connect.Request[adminv1.CheckSubscriptionHostDomainRequest]) (*connect.Response[adminv1.CheckSubscriptionHostDomainResponse], error) {
	s := r.s
	domain, err := normDomain(req.Msg.Domain)
	if err != nil {
		return nil, err
	}
	n, err := s.cfg.Store.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, s.internal("read node", err)
	}
	c, err := s.checkDNS(ctx, n, domain)
	if err != nil {
		return nil, s.internal("read node facts", err)
	}
	return connect.NewResponse(&adminv1.CheckSubscriptionHostDomainResponse{Matches: c.matches, Found: c.found, Expected: c.expected}), nil
}

func (r rpc) SetSubscriptionHost(ctx context.Context, req *connect.Request[adminv1.SetSubscriptionHostRequest]) (*connect.Response[adminv1.SetSubscriptionHostResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	domain, err := normDomain(req.Msg.Domain)
	if err != nil {
		return nil, err
	}
	err = s.transition(ctx, func(st *state) error {
		if st.State != stateOff {
			return wrongState()
		}
		n, err := s.fitNode(ctx, req.Msg.NodeId)
		if err != nil {
			return err
		}
		lacking, err := s.lackingLink(ctx)
		if err != nil {
			return s.internal("read nodes", err)
		}
		if len(lacking) > 0 {
			ids := make([]string, len(lacking))
			for i, l := range lacking {
				ids[i] = l.NodeId
			}
			return coded(connect.CodeFailedPrecondition, "sub_host_nodes_lack_link", "nodes", strings.Join(ids, ","))
		}
		if !req.Msg.SkipDnsCheck {
			c, err := s.checkDNS(ctx, n, domain)
			if err != nil {
				return s.internal("read node facts", err)
			}
			if !c.matches {
				return coded(connect.CodeFailedPrecondition, "sub_host_dns_mismatch", "found", strings.Join(c.found, ","), "expected", strings.Join(c.expected, ","))
			}
		}
		*st = state{State: stateOn, Domain: domain, Node: n.ID, SNI: tunnelSNI(domain), PrevURL: st.PublicURL, PublicURL: "https://" + domain}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "sub_host_set", map[string]string{"domain": domain, "node_id": req.Msg.NodeId})
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.SetSubscriptionHostResponse{Host: h}), nil
}

func (r rpc) StartSubscriptionHostMove(ctx context.Context, req *connect.Request[adminv1.StartSubscriptionHostMoveRequest]) (*connect.Response[adminv1.StartSubscriptionHostMoveResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	var from string
	err := s.transition(ctx, func(st *state) error {
		if st.State != stateOn {
			return wrongState()
		}
		if req.Msg.NodeId == st.Node {
			return coded(connect.CodeInvalidArgument, "sub_host_same_node")
		}
		n, err := s.fitNode(ctx, req.Msg.NodeId)
		if err != nil {
			return err
		}
		from = st.Node
		st.State, st.Next, st.NextSNI = stateMoving, n.ID, tunnelSNI(st.Domain)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "sub_host_move_start", map[string]string{"node_id": from, "next_node_id": req.Msg.NodeId})
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.StartSubscriptionHostMoveResponse{Host: h}), nil
}

func (r rpc) FinishSubscriptionHostChange(ctx context.Context, req *connect.Request[adminv1.FinishSubscriptionHostChangeRequest]) (*connect.Response[adminv1.FinishSubscriptionHostChangeResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	params := map[string]string{}
	err := s.transition(ctx, func(st *state) error {
		params["domain"], params["node_id"] = st.Domain, st.Node
		switch st.State {
		case stateMoving:
			params["from"] = "moving"
			st.State, st.Node, st.SNI, st.Next, st.NextSNI = stateOn, st.Next, st.NextSNI, "", ""
			params["node_id"] = st.Node
		case stateLeaving:
			params["from"] = "leaving"
			if !req.Msg.Force {
				p, err := s.pending(ctx, *st)
				if err != nil {
					return s.internal("read nodes", err)
				}
				if len(p) > 0 {
					ids := make([]string, len(p))
					for i, n := range p {
						ids[i] = n.NodeId
					}
					return coded(connect.CodeFailedPrecondition, "sub_host_nodes_pending", "nodes", strings.Join(ids, ","))
				}
			}
			*st = state{PublicURL: st.PublicURL}
		default:
			return wrongState()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if req.Msg.Force {
		params["force"] = "true"
	}
	s.audit(ctx, "sub_host_finish", params)
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.FinishSubscriptionHostChangeResponse{Host: h}), nil
}

func (r rpc) CancelSubscriptionHostChange(ctx context.Context, _ *connect.Request[adminv1.CancelSubscriptionHostChangeRequest]) (*connect.Response[adminv1.CancelSubscriptionHostChangeResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	params := map[string]string{}
	err := s.transition(ctx, func(st *state) error {
		params["domain"], params["node_id"] = st.Domain, st.Node
		switch st.State {
		case stateMoving:
			params["from"], params["next_node_id"] = "moving", st.Next
			st.State, st.Next, st.NextSNI = stateOn, "", ""
		case stateLeaving:
			params["from"] = "leaving"
			st.State, st.PublicURL = stateOn, "https://"+st.Domain
		default:
			return wrongState()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "sub_host_cancel", params)
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CancelSubscriptionHostChangeResponse{Host: h}), nil
}

func (r rpc) DisableSubscriptionHost(ctx context.Context, req *connect.Request[adminv1.DisableSubscriptionHostRequest]) (*connect.Response[adminv1.DisableSubscriptionHostResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	back, err := returnURL(req.Msg.PublicUrl)
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	err = s.transition(ctx, func(st *state) error {
		if st.State != stateOn {
			return wrongState()
		}
		if back == "" {
			nodes, err := s.cfg.Store.Nodes(ctx, false)
			if err != nil {
				return s.internal("read nodes", err)
			}
			if slices.ContainsFunc(nodes, func(n store.NodeRow) bool { return n.State != "retired" && n.ID != st.Node }) {
				return coded(connect.CodeInvalidArgument, "sub_host_return_url_required")
			}
		}
		params["domain"], params["node_id"] = st.Domain, st.Node
		st.State, st.PublicURL = stateLeaving, back
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "sub_host_disable", params)
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.DisableSubscriptionHostResponse{Host: h}), nil
}
