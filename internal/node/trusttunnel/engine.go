// Package trusttunnel serves TrustTunnel over HTTP/2 and optionally HTTP/3 in-process.
package trusttunnel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/ratelimit"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
	"golang.org/x/crypto/acme"
)

const Protocol = "trusttunnel"

var Factory engine.Factory = New

// BindIP is nil in production. Tests bind the listener to loopback so a test
// binary never opens a wildcard listener.
var BindIP net.IP

const (
	codeStartFailed   = "trusttunnel_start_failed"
	codeServeFailed   = "trusttunnel_serve_failed"
	codeH3ServeFailed = "trusttunnel_h3_serve_failed"
)

type timings struct {
	connect        time.Duration
	streamIdle     time.Duration
	connectionIdle time.Duration
	udpFlowIdle    time.Duration
	udpDrain       time.Duration
	eofGrace       time.Duration
	readHeader     time.Duration
	read           time.Duration
	write          time.Duration
	serverIdle     time.Duration
}

var defaultTimings = timings{
	connect:        30 * time.Second,
	streamIdle:     300 * time.Second,
	connectionIdle: 60 * time.Second,
	udpFlowIdle:    300 * time.Second,
	udpDrain:       5 * time.Second,
	eofGrace:       time.Second,
	readHeader:     10 * time.Second,
	read:           30 * time.Second,
	write:          30 * time.Second,
	serverIdle:     60 * time.Second,
}

type eng struct {
	env    engine.Env
	log    *slog.Logger
	now    func() time.Time
	out    func(string) (engine.Egress, error)
	users  *ratelimit.Registry
	timing timings

	mu              sync.Mutex
	inbounds        map[string]*inbound
	retired         []*credState
	retiredInbounds []*inbound
	closed          bool
}

var _ engine.Engine = (*eng)(nil)

func New(env engine.Env) (engine.Engine, error) {
	return newWithTimings(env, defaultTimings)
}

func newWithTimings(env engine.Env, ts timings) (*eng, error) {
	if env.Certs == nil {
		return nil, errors.New("trusttunnel: Env.Certs is required")
	}
	e := &eng{env: env, log: env.Log, now: env.Now, out: env.Egress, users: env.UserLimits, timing: ts, inbounds: map[string]*inbound{}}
	if e.log == nil {
		e.log = slog.Default()
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.users == nil {
		e.users = ratelimit.NewRegistry()
	}
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
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == "golang.org/x/net" && dep.Version != "" {
				return "TrustTunnel h2/h3, " + dep.Version
			}
		}
	}
	return "TrustTunnel h2/h3"
}

func (e *eng) Capabilities() engine.Capabilities {
	return engine.Capabilities{RateLimitPerCred: true, RateLimitPerUser: true, HardExpiry: true}
}

type inbound struct {
	e        *eng
	spec     plugin.InboundSpec
	specHash string
	creds    []plugin.UserCred // guarded by e.mu
	idx      atomic.Pointer[index]

	out         engine.Egress
	cert        engine.Cert
	certHeld    bool
	ln          net.Listener
	srv         *http.Server
	serveDone   chan struct{}
	everServe   bool
	h3Ln        net.PacketConn
	h3Transport *quic.Transport
	h3Listener  *quic.EarlyListener
	h3Server    *http3.Server
	h3Retries   atomic.Uint64 // handshakes that had to prove their address first
	h3ServeDone chan struct{}
	everH3Serve bool
	h3Enabled   bool
	h3Anonymous *sitehttp.AnonymousLimiter

	connMu sync.Mutex
	conns  map[any]*connectionState

	unknownKeys atomic.Uint32

	mu       sync.Mutex
	state    plugin.RunState
	detail   string
	since    time.Time
	restarts uint32
}

func parseSpec(spec plugin.InboundSpec) error {
	if spec.Protocol != Protocol {
		return fmt.Errorf("protocol must be %q", Protocol)
	}
	if spec.ID == "" {
		return errors.New("inbound id is required")
	}
	if spec.Listen.Network != "tcp" || spec.Listen.Port == 0 {
		return errors.New("a TCP listen port is required")
	}
	var settings struct {
		H3 bool
	}
	if len(spec.Settings) != 0 {
		if err := json.Unmarshal(spec.Settings, &settings); err != nil {
			return errors.New("settings are invalid")
		}
	}
	return nil
}

func (e *eng) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	if err := parseSpec(spec); err != nil {
		return engine.ApplyReport{}, fmt.Errorf("trusttunnel inbound %q: %w", spec.ID, err)
	}
	spec = cloneSpec(spec)
	creds = cloneCredentials(creds)
	hash := statehash.Spec(spec)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return engine.ApplyReport{}, errors.New("trusttunnel: engine is closed")
	}
	old := e.inbounds[spec.ID]
	var oldIndex *index
	if old != nil {
		oldIndex = old.idx.Load()
	}
	next, accepted, warnings := buildIndex(spec.ID, oldIndex, creds, e.users)
	if old != nil && old.specHash == hash && old.settled() {
		e.swapIndex(old, oldIndex, next, accepted)
		report := e.report(old, len(accepted), false)
		report.Warnings = warnings
		return report, nil
	}

	in := &inbound{
		e: e, spec: spec, specHash: hash, creds: accepted, conns: make(map[any]*connectionState),
		h3Enabled: settingsH3(spec),
		state:     plugin.RunStarting, since: e.now(), serveDone: make(chan struct{}),
	}
	in.idx.Store(next)
	restarted := false
	if old != nil {
		old.mu.Lock()
		restarted = old.state == plugin.RunRunning
		in.restarts = old.restarts
		if restarted {
			in.restarts++
		}
		old.mu.Unlock()
		// The old listener goes first (same port), but the certificate claim is kept: a restart is no reason to ask the
		// CA again.
		old.stop(!spec.Enabled)
		e.retiredInbounds = append(e.retiredInbounds, old)
		e.retireMissing(oldIndex, next)
	}
	e.inbounds[spec.ID] = in

	if !spec.Enabled {
		in.setState(plugin.RunStopped, "disabled")
		report := e.report(in, len(accepted), restarted)
		report.Warnings = warnings
		return report, nil
	}
	if err := in.start(ctx); err != nil {
		e.log.Error("TrustTunnel inbound failed to start", "code", codeStartFailed, "inbound", spec.ID, "err", err)
		in.stop(true)
		in.setState(plugin.RunFailed, err.Error())
		report := engine.ApplyReport{SpecHash: hash, CredCount: len(accepted), Restarted: restarted}
		report.Warnings = warnings
		return report, fmt.Errorf("trusttunnel inbound %q: %w", spec.ID, err)
	}
	report := e.report(in, len(accepted), restarted)
	report.Warnings = warnings
	return report, nil
}

func (e *eng) report(in *inbound, count int, restarted bool) engine.ApplyReport {
	report := engine.ApplyReport{SpecHash: in.specHash, CredCount: count, Restarted: restarted}
	if in.cert.Info != nil {
		report.Cert = in.cert.Info()
	}
	return report
}

func (e *eng) swapIndex(in *inbound, old, next *index, creds []plugin.UserCred) {
	in.creds = creds
	in.idx.Store(next)
	e.retireMissing(old, next)
}

func (e *eng) retireMissing(old, next *index) {
	if old == nil {
		return
	}
	for id, cs := range old.byID {
		if next.byID[id] != cs {
			cs.retire()
			e.retired = append(e.retired, cs)
		}
	}
}

func (in *inbound) settled() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state == plugin.RunRunning || (in.state == plugin.RunStopped && !in.spec.Enabled)
}

func (in *inbound) setState(state plugin.RunState, detail string) {
	in.mu.Lock()
	in.state, in.detail, in.since = state, detail, in.e.now()
	in.mu.Unlock()
}

func (in *inbound) start(ctx context.Context) error {
	out, err := in.e.out(in.spec.Egress)
	if err != nil {
		return err
	}
	if out == nil {
		return errors.New("egress is unavailable")
	}
	cert, err := in.e.env.Certs.Acquire(ctx, in.spec.ID, in.spec.TLS)
	if err != nil {
		return fmt.Errorf("certificate: %w", err)
	}
	in.cert, in.certHeld, in.out = cert, true, out

	host := ""
	if BindIP != nil {
		host = BindIP.String()
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(in.spec.Listen.Port)))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen tcp %d: %w", in.spec.Listen.Port, err)
	}
	// A subscription host on this port takes its subdomain off this listener (internal/node/subhost), as from every site.
	if wrap := in.e.env.SiteWrap(in.spec.Listen.Port, false); wrap != nil {
		ln = wrap(ln)
	}
	in.ln = ln
	in.srv = &http.Server{
		Handler:           http.HandlerFunc(in.serveHTTP),
		ReadHeaderTimeout: in.e.timing.readHeader,
		ReadTimeout:       in.e.timing.read,
		WriteTimeout:      in.e.timing.write,
		IdleTimeout:       in.e.timing.serverIdle,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: cert.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
		},
		ConnState: in.connState,
	}
	if err := sitehttp.ConfigureSiteHTTP2(in.srv); err != nil {
		_ = ln.Close()
		return fmt.Errorf("configure HTTP/2: %w", err)
	}
	if in.h3Enabled {
		if err := in.startH3(addr); err != nil {
			_ = ln.Close()
			return err
		}
	}
	tlsListener := tls.NewListener(ln, in.srv.TLSConfig)
	in.mu.Lock()
	in.state, in.detail, in.since, in.everServe = plugin.RunRunning, "", in.e.now(), true
	in.mu.Unlock()
	go func() {
		err := in.srv.Serve(tlsListener)
		close(in.serveDone)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			in.e.log.Warn("TrustTunnel listener stopped", "code", codeServeFailed, "inbound", in.spec.ID, "err", err)
			in.mu.Lock()
			if in.state == plugin.RunRunning {
				in.state, in.detail, in.since = plugin.RunFailed, err.Error(), in.e.now()
			}
			in.mu.Unlock()
		}
	}()
	if in.h3Enabled {
		in.serveH3()
	}
	return nil
}

func (in *inbound) connState(conn net.Conn, state http.ConnState) {
	if state == http.StateClosed || state == http.StateHijacked {
		in.closeConnection(conn)
	}
}

func (in *inbound) stop(releaseCert bool) {
	in.stopH3()
	if in.srv != nil {
		_ = in.srv.Close()
	}
	if in.ln != nil {
		_ = in.ln.Close()
	}
	in.connMu.Lock()
	conns := make([]any, 0, len(in.conns))
	for conn := range in.conns {
		conns = append(conns, conn)
	}
	in.connMu.Unlock()
	for _, conn := range conns {
		in.closeConnection(conn)
	}
	if releaseCert && in.certHeld {
		in.e.env.Certs.Release(in.spec.ID)
		in.certHeld = false
	}
	in.setState(plugin.RunStopped, "")
}

func (e *eng) Remove(_ context.Context, inboundID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if in := e.inbounds[inboundID]; in != nil {
		e.drop(in)
	}
	return nil
}

func (e *eng) drop(in *inbound) {
	in.stop(true)
	e.users.Sync(in.spec.ID, nil)
	e.retiredInbounds = append(e.retiredInbounds, in)
	for _, cs := range in.idx.Load().byID {
		cs.retire()
		e.retired = append(e.retired, cs)
	}
	delete(e.inbounds, in.spec.ID)
}

func (e *eng) Collect(ctx context.Context) (engine.Collected, error) {
	if err := ctx.Err(); err != nil {
		return engine.Collected{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out engine.Collected
	for _, in := range e.sortedInbounds() {
		ix := in.idx.Load()
		for _, cs := range ix.byID {
			flushCredential(cs, &out)
			cs.mu.Lock()
			for _, since := range cs.connections {
				out.Sessions = append(out.Sessions, plugin.Session{CredID: cs.id, InboundID: in.spec.ID, Since: since})
			}
			cs.mu.Unlock()
		}
		if unknown := in.unknownKeys.Swap(0); unknown != 0 {
			out.Links = append(out.Links, plugin.LinkStat{InboundID: in.spec.ID, UnknownKeys: unknown})
		}
	}
	kept := e.retired[:0]
	for _, cs := range e.retired {
		flushCredential(cs, &out)
		cs.mu.Lock()
		live := cs.active != 0 || len(cs.connections) != 0
		cs.mu.Unlock()
		if live {
			kept = append(kept, cs)
		}
	}
	e.retired = kept
	for _, in := range e.retiredInbounds {
		if unknown := in.unknownKeys.Swap(0); unknown != 0 {
			out.Links = append(out.Links, plugin.LinkStat{InboundID: in.spec.ID, UnknownKeys: unknown})
		}
	}
	e.retiredInbounds = nil
	sort.Slice(out.Traffic, func(i, j int) bool {
		a, b := out.Traffic[i], out.Traffic[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && a.CredID < b.CredID)
	})
	sort.Slice(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && (a.CredID < b.CredID || (a.CredID == b.CredID && a.Since.Before(b.Since))))
	})
	sort.Slice(out.Links, func(i, j int) bool {
		a, b := out.Links[i], out.Links[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && a.CredID < b.CredID)
	})
	return out, nil
}

func flushCredential(cs *credState, out *engine.Collected) {
	up, down := cs.up.Swap(0), cs.down.Swap(0)
	if up != 0 || down != 0 {
		out.Traffic = append(out.Traffic, plugin.UserTraffic{CredID: cs.id, InboundID: cs.inboundID, Up: up, Down: down})
	}
	stat := plugin.LinkStat{
		CredID: cs.id, InboundID: cs.inboundID, Connects: cs.connects.Swap(0),
		AuthFailed: cs.authFailed.Swap(0),
	}
	if !stat.Zero() {
		out.Links = append(out.Links, stat)
	}
}

func (e *eng) Kick(_ context.Context, credIDs []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	wanted := make(map[string]struct{}, len(credIDs))
	for _, id := range credIDs {
		wanted[id] = struct{}{}
	}
	hit := make(map[string]struct{})
	for _, in := range e.inbounds {
		for id := range wanted {
			if cs := in.idx.Load().byID[id]; cs != nil && cs.closeSessions() {
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
		out = append(out, statehash.Inbound{Spec: cloneSpec(in.spec), Creds: cloneCredentials(in.creds)})
	}
	return out
}

func (e *eng) Health() []plugin.EngineHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []plugin.EngineHealth
	for _, in := range e.sortedInbounds() {
		in.mu.Lock()
		health := plugin.EngineHealth{InboundID: in.spec.ID, State: in.state, Detail: in.detail, Restarts: in.restarts, Since: in.since}
		in.mu.Unlock()
		if in.cert.Info != nil {
			cert := in.cert.Info()
			health.CertPinSHA256, health.CertNotAfter = cert.PinSHA256, cert.NotAfter
		}
		out = append(out, health)
	}
	return out
}

func (e *eng) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	closing := e.sortedInbounds()
	for _, in := range closing {
		e.drop(in)
	}
	e.mu.Unlock()
	for _, in := range closing {
		if !in.everServe && !in.everH3Serve {
			continue
		}
		for _, done := range []<-chan struct{}{in.serveDone, in.h3ServeDone} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func settingsH3(spec plugin.InboundSpec) bool {
	var settings struct {
		H3 bool `json:"h3"`
	}
	return json.Unmarshal(spec.Settings, &settings) == nil && settings.H3
}

func (e *eng) sortedInbounds() []*inbound {
	ids := make([]string, 0, len(e.inbounds))
	for id := range e.inbounds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*inbound, 0, len(ids))
	for _, id := range ids {
		out = append(out, e.inbounds[id])
	}
	return out
}

func cloneSpec(spec plugin.InboundSpec) plugin.InboundSpec {
	spec.Settings = append(spec.Settings[:0:0], spec.Settings...)
	return spec
}

func cloneCredentials(creds []plugin.UserCred) []plugin.UserCred {
	out := make([]plugin.UserCred, len(creds))
	for i, cred := range creds {
		cred.Data = append(cred.Data[:0:0], cred.Data...)
		out[i] = cred
	}
	return out
}
