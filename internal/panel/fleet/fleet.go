// Package fleet is the panel side of the node fleet: the panel CA and the agent endpoint (mTLS), node
// enrollment, the agent stream (liveness, reliable stats/events ingestion), desired-state computation and
// push, and the admin NodeService / FleetService handlers backed by that data.
package fleet

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/statehash"
)

// APIVersion is the agent wire API version this panel serves (Hello.api_version, EnrollRequest.api_version).
const APIVersion = 1

// Defaults of NodeSettings that are not stored per node (agent.proto).
const (
	defaultStatsIntervalS    = 10
	defaultKeepaliveInterval = 20
	defaultKeepaliveTimeout  = 45
	blipWindow               = 10 * time.Minute // stream lost for less than this is a "blip", not an outage
)

// Config configures Fleet.
type Config struct {
	// AgentSNI is the secret server name of the agent endpoint (e.g. "q3m8x2kd7w.invalid"). Required.
	AgentSNI string
	// PanelAddr is the host:port agents dial; it goes into the install command. Required for CreateEnrollment.
	PanelAddr string
	// ExpectedAgentVersion is the newest agent version this panel ships (ListNodes marks older agents).
	ExpectedAgentVersion string
	// Debounce is the StateChanged coalescing window. Default 200 ms.
	Debounce time.Duration
	// Desired is the desired-state source, required: it returns the enabled inbounds of a node with their
	// complete credential sets (the access module's effective-access rule, so it exists once). Spec.ID must
	// be set. This module owns revisions, hashes, deltas and push.
	Desired func(ctx context.Context, nodeID string) ([]statehash.Inbound, error)
	// MaxNodeBytesPerSec is the most traffic one node may report in total per second of a stats interval; a batch
	// above it is dropped (see statsguard.go). Default 1.25e9 bytes/s (10 Gbit/s).
	MaxNodeBytesPerSec uint64
	// OnUsage is called after a stats batch added usage to these users (new used_bytes are committed), e.g.
	// access.Service.Recompute, so a quota overrun takes effect within one stats interval.
	OnUsage func(ctx context.Context, userIDs []string)
	// OnAwgPrepareFailed is called when a node reports that the build of its AmneziaWG kernel module failed (the Telegram
	// alerts hang on it). at is the time the node gave; a replay of the same report carries the same value. Must not block.
	OnAwgPrepareFailed func(nodeID, code string, at int64)
	// Actor returns the admin id for audit rows. Default: the signed-in admin from the auth interceptor.
	Actor func(context.Context) string
	Log   *slog.Logger
	// Now is the clock (tests). Default time.Now.
	Now func() time.Time
}

// Fleet is the fleet module.
type Fleet struct {
	st  *store.Store
	v   *vault.Vault
	reg *protocols.Registry
	cfg Config
	log *slog.Logger
	now func() time.Time
	ca  *ca

	tlsMu  sync.Mutex
	tlsCfg *tls.Config
	tlsExp time.Time

	mu       sync.Mutex
	health   Health              // set by SetHealth (health.go); guarded by mu
	upd      Updates             // set by SetUpdates (update.go); guarded by mu
	warp     Warp                // set by SetWarp (l3.go); guarded by mu
	sessions map[string]*session // node id -> the one live stream
	stuck    map[string]stuckSeq // node id -> the stats batch the database refused last (statsguard.go)

	kick      chan struct{}
	enrollLim *failLimiter
	unit      time.Duration // one "second" of per-node timeouts; a test seam, time.Second otherwise
	certCheck time.Duration // how often a running stream rechecks its client certificate
	// The bandwidth test (bandwidth.go): how long a request waits for the node's answer, and how long after a node's first
	// start the automatic measurement waits. Test seams.
	measureWait, measureDelay time.Duration
}

// New builds the module: it loads (or creates) the panel CA. Call Run to start the background work.
func New(st *store.Store, v *vault.Vault, reg *protocols.Registry, cfg Config) (*Fleet, error) {
	if cfg.AgentSNI == "" {
		return nil, errors.New("fleet: AgentSNI is required")
	}
	if cfg.Desired == nil {
		return nil, errors.New("fleet: Desired is required")
	}
	cfg.AgentSNI = strings.ToLower(strings.TrimSuffix(cfg.AgentSNI, "."))
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 200 * time.Millisecond
	}
	if cfg.Actor == nil {
		cfg.Actor = func(ctx context.Context) string {
			if a, ok := auth.AdminFrom(ctx); ok {
				return a.ID
			}
			return "unknown"
		}
	}
	f := &Fleet{
		st: st, v: v, reg: reg, cfg: cfg, log: cfg.Log, now: cfg.Now,
		sessions:  map[string]*session{},
		stuck:     map[string]stuckSeq{},
		kick:      make(chan struct{}, 1),
		enrollLim: newFailLimiter(10, time.Minute),
		unit:      time.Second,
		certCheck: 30 * time.Second,
		// A measurement is about ten seconds and forty at most (the agent's own limit), and the admin's request must be
		// answered within the panel's 60 s write timeout. The first measurement waits until the node has applied its first
		// state and settled: it saturates the link for a moment.
		measureWait: 50 * time.Second, measureDelay: 45 * time.Second,
	}
	c, err := loadCA(context.Background(), st, v, cfg.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("fleet: panel CA: %w", err)
	}
	f.ca = c
	return f, nil
}

// AgentHandler serves EnrollmentService and AgentService. Mount it on the agent endpoint only (TLS with
// AgentTLSConfig); it refuses requests that did not arrive with the agent SNI.
func (f *Fleet) AgentHandler() http.Handler {
	mux := http.NewServeMux()
	p, h := agentv1connect.NewEnrollmentServiceHandler(enrollmentService{f}, connect.WithReadMaxBytes(16<<10))
	mux.Handle(p, h)
	p, h = agentv1connect.NewAgentServiceHandler(agentService{f}, connect.WithReadMaxBytes(4<<20))
	mux.Handle(p, h)
	return f.agentMiddleware(mux)
}

// NodeHandler is the admin NodeService. Pass connect.WithInterceptors(auth.Interceptor()) so that it is
// behind the admin session; the path is the Connect prefix to mount it on.
func (f *Fleet) NodeHandler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewNodeServiceHandler(nodeService{f}, adminOpts(opts)...)
}

// FleetHandler is the admin FleetService (Overview, ListEvents); see NodeHandler for opts.
func (f *Fleet) FleetHandler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewFleetServiceHandler(fleetService{f}, adminOpts(opts)...)
}

// adminOpts puts a small request size cap first, so that the caller's options can still override it.
func adminOpts(opts []connect.HandlerOption) []connect.HandlerOption {
	return append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)
}

// StateChanged tells the module that something that feeds desired state changed (users, profiles,
// inbounds, nodes). It returns at once; a debounced recompute pushes the result to connected nodes.
func (f *Fleet) StateChanged() {
	select {
	case f.kick <- struct{}{}:
	default: // one is already pending
	}
}

// Run drives the debounced recompute and the node-down sweeper until ctx ends, then closes all streams.
func (f *Fleet) Run(ctx context.Context) error {
	sweep := time.NewTicker(30 * time.Second)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			f.closeAll()
			return nil
		case <-f.kick:
			t := time.NewTimer(f.cfg.Debounce)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				f.closeAll()
				return nil
			}
			select { // a kick that arrived during the window is covered by this recompute
			case <-f.kick:
			default:
			}
			f.recomputeAll(ctx)
		case <-sweep.C:
			f.sweep(ctx)
		}
	}
}

// OnlineSession is one open client session as last reported by a node.
type OnlineSession struct {
	NodeID, UserID, DeviceID, Protocol, InboundID, RemoteIP string
	ConnectedAt                                             time.Time
}

// Online returns the open sessions of every connected node (in memory, refreshed by each stats batch).
func (f *Fleet) Online() []OnlineSession {
	var out []OnlineSession
	for _, s := range f.snapshotSessions() {
		s.liveMu.Lock()
		for _, o := range s.online {
			out = append(out, OnlineSession{NodeID: s.nodeID, UserID: o.userID, DeviceID: o.deviceID, Protocol: o.protocol,
				InboundID: o.inboundID, RemoteIP: o.remoteIP, ConnectedAt: o.since})
		}
		s.liveMu.Unlock()
	}
	return out
}

// OnlineUsers maps each user with an open session to the node of the newest one (access.OnlineSource).
func (f *Fleet) OnlineUsers() map[string]string {
	out := map[string]string{}
	newest := map[string]time.Time{}
	for _, o := range f.Online() {
		if t, ok := newest[o.UserID]; !ok || o.ConnectedAt.After(t) {
			out[o.UserID], newest[o.UserID] = o.NodeID, o.ConnectedAt
		}
	}
	return out
}

// NetworkUsage returns the latest host-network sample received from a connected node. The timestamp is when the
// panel received that sample, so callers can hide percentages when the agent stops reporting.
func (f *Fleet) NetworkUsage(nodeID string) (rxBps, txBps uint64, sampledAt time.Time, ok bool) {
	s := f.session(nodeID)
	if s == nil {
		return 0, 0, time.Time{}, false
	}
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.metrics == nil || s.metricsAt.IsZero() {
		return 0, 0, time.Time{}, false
	}
	return s.metrics.NetRxBps, s.metrics.NetTxBps, s.metricsAt, true
}

// CPUUsage returns the CPU use of the latest host sample of a connected node, with the time the panel received it.
func (f *Fleet) CPUUsage(nodeID string) (pct float64, sampledAt time.Time, ok bool) {
	s := f.session(nodeID)
	if s == nil {
		return 0, time.Time{}, false
	}
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.metrics == nil || s.metricsAt.IsZero() {
		return 0, time.Time{}, false
	}
	return float64(s.metrics.CpuPct), s.metricsAt, true
}

func (f *Fleet) snapshotSessions() []*session {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*session, 0, len(f.sessions))
	for _, s := range f.sessions {
		out = append(out, s)
	}
	return out
}

// AgentConnected reports whether the node's agent holds a live session with the panel (access.AgentSessionSource: the
// user page calls a server that answers "online").
func (f *Fleet) AgentConnected(nodeID string) bool { return f.session(nodeID) != nil }

func (f *Fleet) session(nodeID string) *session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[nodeID]
}

func (f *Fleet) closeAll() {
	for _, s := range f.snapshotSessions() {
		s.cancel(connect.NewError(connect.CodeUnavailable, errors.New("panel shutting down")))
	}
}

// recomputeAll rebuilds desired state for every connected node and pushes deltas (4 nodes at a time: the
// read pool has 4 connections).
// A full rebuild per connected node on every change; per-user dirty tracking if 50 nodes x 5000
// users make this slow.
func (f *Fleet) recomputeAll(ctx context.Context) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, s := range f.snapshotSessions() {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := f.reconcile(ctx, s, reconcileChange, nil); err != nil && ctx.Err() == nil {
				f.log.Warn("recompute desired state", "node", s.nodeID, "err", err)
			}
		}()
	}
	wg.Wait()
}

// sweep emits node_down once per outage for nodes that have been silent for longer than the blip window.
func (f *Fleet) sweep(ctx context.Context) {
	nodes, err := f.st.Nodes(ctx, false)
	if err != nil {
		f.log.Warn("sweep", "err", err)
		return
	}
	now := f.now()
	for _, n := range nodes {
		if n.State != "active" || f.session(n.ID) != nil {
			continue
		}
		last := latest(n.LastSeenAt, n.LastDisconnectedAt, n.LastConnectedAt)
		if last.IsZero() || now.Sub(last) < blipWindow {
			continue
		}
		code, err := f.st.LastNodeEventCode(ctx, n.ID, "node_down", "node_recovered")
		if err != nil || code == "node_down" {
			continue
		}
		f.event(ctx, 3, "node_down", n.ID, map[string]string{"minutes": fmt.Sprint(int(now.Sub(last).Minutes()))})
	}
}

func latest(ts ...time.Time) time.Time {
	var m time.Time
	for _, t := range ts {
		if t.After(m) {
			m = t
		}
	}
	return m
}

// event writes a panel event about a node.
func (f *Fleet) event(ctx context.Context, sev int, code, nodeID string, params map[string]string) {
	err := f.st.InsertEvent(ctx, store.EventRow{Time: f.now(), Severity: sev, Code: code, Source: "panel", NodeID: nodeID, Params: params})
	if err != nil {
		f.log.Warn("write event", "code", code, "err", err)
	}
}
