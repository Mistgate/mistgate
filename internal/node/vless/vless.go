// Package vless is the node's VLESS engine on top of xray-core.
package vless

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/core"
	xinbound "github.com/xtls/xray-core/features/inbound"
	xoutbound "github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/proxy"

	agentpb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

const Protocol = "vless"

var Factory engine.Factory = New

// BindIP is nil in production (all interfaces). Tests bind engines to loopback.
var BindIP net.IP

type eng struct {
	env   engine.Env
	log   *slog.Logger
	now   func() time.Time
	after afterFunc
	out   func(string) (engine.Egress, error)

	torrentEnabled atomic.Bool
	idleTimeout    time.Duration

	mu       sync.Mutex
	inbounds map[string]*inbound
	retired  []*credState
	closed   bool
}

var _ engine.Engine = (*eng)(nil)

// generations numbers inbound instances process-wide: the connection trackers are keyed by inbound id and generation in
// a global map, so two engines must never hand out the same pair.
var generations atomic.Uint64

type inbound struct {
	e          *eng
	spec       plugin.InboundSpec
	specHash   string
	generation uint64
	cfg        settings
	creds      []plugin.UserCred
	idx        atomic.Pointer[credentialIndex]

	ctx    context.Context
	cancel context.CancelFunc

	instance    *coreInstance
	out         engine.Egress
	userManager proxy.UserManager
	connTracker *tcpConnTracker

	mu        sync.Mutex
	state     plugin.RunState
	detail    string
	since     time.Time
	restarts  uint32
	everServe bool
}

// coreInstance keeps the concrete dependency out of the state declaration's comments and mirrors Xray's lifetime.
type coreInstance = core.Instance

func New(env engine.Env) (engine.Engine, error) {
	logger := env.Log
	if logger == nil {
		logger = slog.Default()
	}
	installXrayLogging(logger)
	now := env.Now
	if now == nil {
		now = time.Now
	}
	after := afterFunc(func(d time.Duration, f func()) stoppable { return time.AfterFunc(d, f) })
	e := &eng{env: env, log: logger, now: now, after: after, idleTimeout: 300 * time.Second, inbounds: make(map[string]*inbound)}
	e.out = env.Egress
	if e.out == nil {
		direct := egress.New(env.DNS)
		e.out = func(name string) (engine.Egress, error) {
			if name != "" && name != "direct" {
				return nil, fmt.Errorf("egress %q is not available", name)
			}
			return direct, nil
		}
	}
	return e, nil
}

func (e *eng) Protocol() string { return Protocol }

func (e *eng) Version() string {
	version := "v26.3.27"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == "github.com/xtls/xray-core" && dep.Version != "" {
				version = displayVersion(dep.Version)
				break
			}
		}
	}
	return "xray-core " + version
}

func displayVersion(v string) string {
	if v == "v1.260327.0" {
		return "v26.3.27"
	}
	return v
}

func (e *eng) Capabilities() engine.Capabilities {
	return engine.Capabilities{RateLimitPerCred: true, HardExpiry: true}
}

func (e *eng) NodeSettings(_ context.Context, st *agentpb.NodeSettings) bool {
	enabled := st != nil && st.GetTorrentBlockerEnabled()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.torrentEnabled.Load() == enabled {
		return false
	}
	e.torrentEnabled.Store(enabled)
	return false
}

func (e *eng) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	cfg, err := parseSpec(spec)
	if err != nil {
		return engine.ApplyReport{}, fmt.Errorf("vless inbound %q: %w", spec.ID, err)
	}
	creds = cloneCredentials(creds)
	sh := statehash.Spec(spec)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return engine.ApplyReport{}, errors.New("vless: engine is closed")
	}
	old := e.inbounds[spec.ID]
	var oldIdx *credentialIndex
	if old != nil {
		oldIdx = old.idx.Load()
	}
	ix, err := buildIndex(spec.ID, oldIdx, creds, e.now, e.after)
	if err != nil {
		return engine.ApplyReport{SpecHash: sh}, fmt.Errorf("vless inbound %q: %w", spec.ID, err)
	}

	if old != nil && old.specHash == sh && old.settled() {
		if old.userManager != nil {
			if err := syncUsers(ctx, old.userManager, oldIdx, ix, old.cfg.flow); err != nil {
				return engine.ApplyReport{SpecHash: sh}, fmt.Errorf("vless inbound %q users: %w", spec.ID, err)
			}
		}
		old.creds = creds
		old.idx.Store(ix)
		e.retireMissing(oldIdx, ix)
		return e.report(old, len(creds), false), nil
	}

	in := &inbound{
		e: e, spec: spec, specHash: sh, generation: generations.Add(1), cfg: cfg, creds: creds,
		state: plugin.RunStarting, since: e.now(),
	}
	in.ctx, in.cancel = context.WithCancel(context.Background())
	in.idx.Store(ix)
	restarted := false
	if old != nil {
		restarted = old.serving()
		in.restarts = old.restarts
		if restarted {
			in.restarts++
		}
		old.stop()
		e.retireMissing(oldIdx, ix)
	}
	e.inbounds[spec.ID] = in
	if !spec.Enabled {
		in.setState(plugin.RunStopped, "disabled")
		return e.report(in, len(creds), restarted), nil
	}
	if err := in.start(ctx); err != nil {
		in.setState(plugin.RunFailed, err.Error())
		e.log.Error("vless inbound failed to start", "inbound", spec.ID, "err", err)
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, fmt.Errorf("vless inbound %q: %w", spec.ID, err)
	}
	return e.report(in, len(creds), restarted), nil
}

func (in *inbound) start(ctx context.Context) error {
	out, err := in.e.out(in.spec.Egress)
	if err != nil {
		return err
	}
	tracker := newTCPConnTracker(in.spec.ID, in.generation)
	tracker.register()
	in.connTracker = tracker
	started := false
	defer func() {
		if !started {
			tracker.stop()
			if in.connTracker == tracker {
				in.connTracker = nil
			}
		}
	}()
	inst, err := core.New(buildCoreConfig(in.spec, in.cfg, in.idx.Load(), tracker))
	if err != nil {
		return err
	}
	outManager, ok := inst.GetFeature(xoutbound.ManagerType()).(xoutbound.Manager)
	if !ok || outManager == nil {
		_ = inst.Close()
		return errors.New("xray outbound manager unavailable")
	}
	if err := outManager.AddHandler(context.Background(), &egressHandler{in: in}); err != nil {
		_ = inst.Close()
		return fmt.Errorf("add egress handler: %w", err)
	}
	inManager, ok := inst.GetFeature(xinbound.ManagerType()).(xinbound.Manager)
	if !ok || inManager == nil {
		_ = inst.Close()
		return errors.New("xray inbound manager unavailable")
	}
	handler, err := inManager.GetHandler(context.Background(), in.spec.ID)
	if err != nil {
		_ = inst.Close()
		return fmt.Errorf("get inbound handler: %w", err)
	}
	getter, ok := handler.(proxy.GetInbound)
	if !ok {
		_ = inst.Close()
		return errors.New("xray inbound handler does not expose its proxy")
	}
	userManager, ok := getter.GetInbound().(proxy.UserManager)
	if !ok {
		_ = inst.Close()
		return errors.New("xray VLESS inbound has no user manager")
	}
	in.instance, in.out, in.userManager = inst, out, userManager
	if err := inst.Start(); err != nil {
		in.instance, in.out, in.userManager = nil, nil, nil
		_ = inst.Close()
		return err
	}
	started = true
	in.mu.Lock()
	in.state, in.detail, in.since, in.everServe = plugin.RunRunning, "", in.e.now(), true
	in.mu.Unlock()
	go in.probeTarget()
	return nil
}

func syncUsers(ctx context.Context, um proxy.UserManager, old, next *credentialIndex, flow string) error {
	var remove []*credState
	var add []*credState
	for _, id := range sortedCredentialIDs(old) {
		before := old.byID[id]
		after := next.byID[id]
		if after == nil || after.uuid != before.uuid {
			remove = append(remove, before)
		}
	}
	for _, id := range sortedCredentialIDs(next) {
		after := next.byID[id]
		before := old.byID[id]
		if before == nil || before.uuid != after.uuid {
			add = append(add, after)
		}
	}
	var removed []*credState
	var added []*credState
	rollback := func() {
		for _, cs := range added {
			_ = um.RemoveUser(ctx, cs.id)
		}
		for _, cs := range removed {
			if user, err := userConfig(cs, flow).ToMemoryUser(); err == nil {
				_ = um.AddUser(ctx, user)
			}
		}
	}
	for _, cs := range remove {
		if err := um.RemoveUser(ctx, cs.id); err != nil {
			rollback()
			return err
		}
		removed = append(removed, cs)
	}
	for _, cs := range add {
		user, err := userConfig(cs, flow).ToMemoryUser()
		if err == nil {
			err = um.AddUser(ctx, user)
		}
		if err != nil {
			rollback()
			return err
		}
		added = append(added, cs)
	}
	return nil
}

func (in *inbound) stop() {
	if in.cancel != nil {
		in.cancel()
	}
	if in.connTracker != nil {
		in.connTracker.stop()
		in.connTracker = nil
	}
	if ix := in.idx.Load(); ix != nil {
		for _, cs := range ix.byID {
			cs.cancelLinks()
		}
	}
	if in.instance != nil {
		_ = in.instance.Close()
		in.instance = nil
		in.userManager = nil
	}
	in.mu.Lock()
	in.state, in.since = plugin.RunStopped, in.e.now()
	in.mu.Unlock()
}

func (in *inbound) settled() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state == plugin.RunRunning || (in.state == plugin.RunStopped && !in.spec.Enabled)
}

func (in *inbound) serving() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.everServe
}

func (in *inbound) setState(state plugin.RunState, detail string) {
	in.mu.Lock()
	in.state, in.detail, in.since = state, detail, in.e.now()
	in.mu.Unlock()
}

func (e *eng) report(in *inbound, n int, restarted bool) engine.ApplyReport {
	return engine.ApplyReport{SpecHash: in.specHash, CredCount: n, Restarted: restarted}
}

func (e *eng) retireMissing(old, next *credentialIndex) {
	if old == nil {
		return
	}
	for id, cs := range old.byID {
		if next.byID[id] != cs && cs.retire() {
			e.retired = append(e.retired, cs)
		}
	}
}

func (e *eng) Remove(_ context.Context, inboundID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if in := e.inbounds[inboundID]; in != nil {
		in.stop()
		if ix := in.idx.Load(); ix != nil {
			for _, cs := range ix.byID {
				if cs.retire() {
					e.retired = append(e.retired, cs)
				}
			}
		}
		delete(e.inbounds, inboundID)
	}
	return nil
}

func (e *eng) Collect(context.Context) (engine.Collected, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out engine.Collected
	flush := func(cs *credState) {
		up, down := cs.up.Swap(0), cs.down.Swap(0)
		if up != 0 || down != 0 {
			out.Traffic = append(out.Traffic, plugin.UserTraffic{CredID: cs.id, InboundID: cs.inboundID, Up: up, Down: down})
		}
	}
	for _, in := range e.sortedInbounds() {
		if ix := in.idx.Load(); ix != nil {
			for _, cs := range ix.byID {
				flush(cs)
				if !cs.removed.Load() {
					if since, open := cs.session(); open {
						out.Sessions = append(out.Sessions, plugin.Session{CredID: cs.id, InboundID: in.spec.ID, Since: since})
					}
				}
			}
		}
	}
	keep := e.retired[:0]
	for _, cs := range e.retired {
		flush(cs)
		_, open := cs.session()
		if open || cs.up.Load() != 0 || cs.down.Load() != 0 {
			keep = append(keep, cs)
		}
	}
	e.retired = keep
	sort.Slice(out.Traffic, func(i, j int) bool {
		a, b := out.Traffic[i], out.Traffic[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && a.CredID < b.CredID)
	})
	sort.Slice(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && (a.CredID < b.CredID || (a.CredID == b.CredID && a.Since.Before(b.Since))))
	})
	return out, nil
}

func (e *eng) Kick(_ context.Context, credIDs []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	hit := make(map[string]struct{})
	for _, in := range e.inbounds {
		ix := in.idx.Load()
		if ix == nil {
			continue
		}
		for _, id := range credIDs {
			if cs := ix.byID[id]; cs != nil && cs.cancelLinks() != 0 {
				hit[id] = struct{}{}
			}
		}
	}
	return len(hit), nil
}

func (e *eng) Observed() []statehash.Inbound {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]statehash.Inbound, 0, len(e.inbounds))
	for _, in := range e.sortedInbounds() {
		out = append(out, statehash.Inbound{Spec: in.spec, Creds: cloneCredentials(in.creds)})
	}
	return out
}

func (e *eng) Health() []plugin.EngineHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]plugin.EngineHealth, 0, len(e.inbounds))
	for _, in := range e.sortedInbounds() {
		in.mu.Lock()
		out = append(out, plugin.EngineHealth{InboundID: in.spec.ID, State: in.state, Detail: in.detail, Restarts: in.restarts, Since: in.since})
		in.mu.Unlock()
	}
	return out
}

func (e *eng) Close(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for _, in := range e.sortedInbounds() {
		in.stop()
		if ix := in.idx.Load(); ix != nil {
			for _, cs := range ix.byID {
				if cs.retire() {
					e.retired = append(e.retired, cs)
				}
			}
		}
		delete(e.inbounds, in.spec.ID)
	}
	return nil
}

func (e *eng) sortedInbounds() []*inbound {
	ids := make([]string, 0, len(e.inbounds))
	for id := range e.inbounds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*inbound, len(ids))
	for i, id := range ids {
		out[i] = e.inbounds[id]
	}
	return out
}

func cloneCredentials(in []plugin.UserCred) []plugin.UserCred {
	out := make([]plugin.UserCred, len(in))
	for i, c := range in {
		out[i] = c
		out[i].Data = append([]byte(nil), c.Data...)
	}
	return out
}

func (in *inbound) probeTarget() {
	ctx, cancel := context.WithTimeout(in.ctx, 5*time.Second)
	defer cancel()
	result := probeRealityTarget(ctx, in.cfg.reality.target, in.cfg.reality.serverNames[0])
	if result == "" || in.ctx.Err() != nil {
		return
	}
	detail := "reality_target: " + result
	in.mu.Lock()
	if in.state == plugin.RunRunning {
		in.detail = detail
	}
	in.mu.Unlock()
	in.e.log.Warn("vless REALITY target probe warning", "inbound", in.spec.ID, "detail", detail)
}

func probeRealityTarget(ctx context.Context, target, serverName string) string {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
	if err != nil {
		return "unreachable"
	}
	defer conn.Close()
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: serverName, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		CurvePreferences: []tls.CurveID{tls.X25519MLKEM768, tls.X25519}, InsecureSkipVerify: true,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		if ctx.Err() != nil {
			return "unreachable"
		}
		return "no_tls13"
	}
	if tlsConn.ConnectionState().CurveID != tls.X25519MLKEM768 {
		return "no_mlkem"
	}
	return ""
}
