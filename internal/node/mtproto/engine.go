// Package mtproto is the node's Telegram MTProto proxy engine: one TCP listener per inbound and a direct relay to a
// Telegram DC (the protocol is internal/mtproto). Fake TLS inbounds authenticate on the raw listener and send strangers
// to the node's decoy site or a forwarded site. Web inbounds serve the node's HTTPS site on that listener and carry
// authenticated streams through the in-process Web Proxy relay. See fallback.go.
package mtproto

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mp "github.com/mistgate/mistgate/internal/mtproto"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/nets"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
	"github.com/mistgate/mistgate/internal/webproxy"
)

const Protocol = "mtproto"

const Version = "go-mtproto 0.2"

var Factory engine.Factory = New

// BindIP is nil in production (all interfaces). Tests bind to loopback.
var BindIP net.IP

// listen is a seam for tests.
var listen = net.Listen

func New(env engine.Env) (engine.Engine, error) {
	log := env.Log
	if log == nil {
		log = slog.Default()
	}
	now := env.Now
	if now == nil {
		now = time.Now
	}
	return &Engine{env: env, log: log, now: now, inbounds: map[string]*inbound{}}, nil
}

type Engine struct {
	env engine.Env
	log *slog.Logger
	now func() time.Time

	// opMu serializes control-plane transitions. Data-plane handlers and observer callbacks never take it.
	opMu     sync.Mutex
	mu       sync.Mutex
	inbounds map[string]*inbound
	closed   bool
	// pending is the traffic of stopped inbounds (a restart, a disable, a removal) not collected yet; the next Collect
	// hands it out.
	pending []plugin.UserTraffic
}

var _ engine.Engine = (*Engine)(nil)

func (*Engine) Protocol() string { return Protocol }
func (*Engine) Version() string  { return Version }

func (*Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{}
}

const (
	fallbackDecoy = "decoy"
	fallbackSite  = "site"
)

type settings struct {
	handshakeTimeout time.Duration
	idleTimeout      time.Duration // 0: the core's default
	replayWindow     time.Duration
	ignoreTimeSkew   bool
	preferIPv6       bool
	domains          []string // lower case, no trailing dot; the first one is the certificate's and the default target
	fallback         string
	web              bool
}

// settingsJSON is InboundSpec.Settings. domains are the name of the Fake TLS links or Web inbound site; fallback says
// whose site Fake TLS strangers see: "decoy" (the default) the node's own, or "site" someone else's, spliced to port 443.
type settingsJSON struct {
	HandshakeTimeoutSecs int      `json:"handshake_timeout_secs"`
	IdleTimeoutSecs      int      `json:"idle_timeout_secs"`
	ReplayWindowSecs     int      `json:"replay_window_secs"`
	IgnoreTimeSkew       bool     `json:"ignore_time_skew"`
	PreferIPv6           bool     `json:"prefer_ipv6"`
	Domains              []string `json:"domains"`
	Fallback             string   `json:"fallback"`
}

func parseSpec(spec plugin.InboundSpec) (settings, error) {
	s := settings{handshakeTimeout: 10 * time.Second, replayWindow: 10 * time.Minute, fallback: fallbackDecoy}
	if spec.ID == "" {
		return s, errors.New("inbound id is empty")
	}
	if spec.Protocol != Protocol {
		return s, fmt.Errorf("protocol %q is not %s", spec.Protocol, Protocol)
	}
	if spec.Egress != "" && spec.Egress != "direct" {
		return s, fmt.Errorf("egress %q is not supported: mtproto always dials Telegram directly", spec.Egress)
	}
	if spec.Listen.HopFrom != 0 || spec.Listen.HopTo != 0 {
		return s, errors.New("port hopping is not supported")
	}
	if spec.Enabled {
		switch spec.Listen.Network {
		case "", "tcp":
		default:
			return s, fmt.Errorf("listen network %q: mtproto is tcp", spec.Listen.Network)
		}
		if spec.Listen.Port == 0 {
			return s, errors.New("listen port is 0")
		}
	}
	var j settingsJSON
	if b := bytes.TrimSpace(spec.Settings); len(b) > 0 && !bytes.Equal(b, []byte("null")) {
		if err := json.Unmarshal(b, &j); err != nil {
			return s, fmt.Errorf("settings: %w", err)
		}
	}
	if j.HandshakeTimeoutSecs != 0 {
		if j.HandshakeTimeoutSecs < 1 || j.HandshakeTimeoutSecs > 600 {
			return s, errors.New("settings: handshake_timeout_secs must be 1-600")
		}
		s.handshakeTimeout = time.Duration(j.HandshakeTimeoutSecs) * time.Second
	}
	if j.IdleTimeoutSecs != 0 {
		if j.IdleTimeoutSecs < 60 || j.IdleTimeoutSecs > 86_400 {
			return s, errors.New("settings: idle_timeout_secs must be 60-86400")
		}
		s.idleTimeout = time.Duration(j.IdleTimeoutSecs) * time.Second
	}
	if j.ReplayWindowSecs != 0 {
		if j.ReplayWindowSecs < 1 || j.ReplayWindowSecs > 86_400 {
			return s, errors.New("settings: replay_window_secs must be 1-86400")
		}
		s.replayWindow = time.Duration(j.ReplayWindowSecs) * time.Second
	}
	s.ignoreTimeSkew = j.IgnoreTimeSkew
	s.preferIPv6 = j.PreferIPv6
	s.web = plugin.MTProtoWeb(spec)
	for _, d := range j.Domains {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" {
			return s, errors.New("settings: domains has an empty entry")
		}
		if !slices.Contains(s.domains, d) {
			s.domains = append(s.domains, d)
		}
	}
	if spec.Enabled && len(s.domains) == 0 {
		if s.web {
			return s, errors.New("settings: domains is empty: Web Proxy needs the inbound domain")
		}
		return s, errors.New("settings: domains is empty: Fake TLS needs the domain of the links")
	}
	switch j.Fallback {
	case "", fallbackDecoy:
	case fallbackSite:
		s.fallback = fallbackSite
	default:
		return s, fmt.Errorf("settings: fallback %q is not %q or %q", j.Fallback, fallbackDecoy, fallbackSite)
	}
	return s, nil
}

type parsedCred struct {
	cred plugin.UserCred
	key  [mp.KeyLen]byte
}

func parseCreds(creds []plugin.UserCred) ([]parsedCred, []error) {
	creds = append([]plugin.UserCred(nil), creds...)
	sort.SliceStable(creds, func(i, j int) bool { return creds[i].CredID < creds[j].CredID })
	out := make([]parsedCred, 0, len(creds))
	seen := make(map[string]struct{}, len(creds))
	seenKey := make(map[[mp.KeyLen]byte]string, len(creds))
	var skipped []error
	for _, c := range creds {
		if c.CredID == "" {
			skipped = append(skipped, errors.New("credential without id"))
			continue
		}
		if _, dup := seen[c.CredID]; dup {
			skipped = append(skipped, fmt.Errorf("duplicate credential id %s", c.CredID))
			continue
		}
		seen[c.CredID] = struct{}{}
		var d struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(c.Data, &d); err != nil {
			skipped = append(skipped, fmt.Errorf("credential %s: data: %w", c.CredID, err))
			continue
		}
		raw, err := hex.DecodeString(d.Key)
		if err != nil || len(raw) != mp.KeyLen {
			skipped = append(skipped, fmt.Errorf("credential %s: key must be %d hex characters", c.CredID, 2*mp.KeyLen))
			continue
		}
		var key [mp.KeyLen]byte
		copy(key[:], raw)
		if other, dup := seenKey[key]; dup {
			skipped = append(skipped, fmt.Errorf("credentials %s and %s share one key", other, c.CredID))
			continue
		}
		seenKey[key] = c.CredID
		out = append(out, parsedCred{cred: c, key: key})
	}
	return out, skipped
}

// credState holds one credential's counters. It is shared by the indexes that contain it and by the sessions opened
// under it (sessionEntry), so a session's traffic and its open count land on the state it was opened with even after
// its credential was swapped out.
type credState struct {
	id        string
	inboundID string
	key       [mp.KeyLen]byte
	userID    atomic.Pointer[string]

	up, down atomic.Uint64
	sessions atomic.Int32
}

// credIndex is immutable once stored in inbound.idx: the observer callbacks read it without a lock, so Apply and
// Collect replace it with a new one instead of editing it (copy on write, under Engine.mu).
type credIndex struct {
	current map[string]*credState
	retired []*credState // removed credentials whose sessions are still open or whose traffic is not collected yet
}

func (ix *credIndex) lookup(id string) *credState {
	if cs := ix.current[id]; cs != nil {
		return cs
	}
	for _, cs := range ix.retired {
		if cs.id == id {
			return cs
		}
	}
	return nil
}

type sessionEntry struct {
	info mp.SessionInfo
	cs   *credState // nil when the credential was already gone at Opened
}

type inbound struct {
	e        *Engine
	spec     plugin.InboundSpec
	specHash string
	creds    []plugin.UserCred
	cfg      settings

	idx atomic.Pointer[credIndex]

	ctx       context.Context
	cancel    context.CancelFunc
	server    *mp.Server
	ln        net.Listener
	siteSrv   *http.Server
	relay     *webproxy.Relay
	decoy     *decoySite
	forwarder *forwarder
	certHeld  bool
	wg        sync.WaitGroup
	stopOnce  sync.Once

	smu      sync.Mutex
	sessions map[uint64]sessionEntry

	mu       sync.Mutex
	state    plugin.RunState
	detail   string
	since    time.Time
	restarts uint32
	ever     bool
}

func (in *inbound) setState(state plugin.RunState, detail string) {
	in.mu.Lock()
	in.state, in.detail, in.since = state, detail, in.e.now()
	if state == plugin.RunRunning {
		in.ever = true
	}
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
	return in.ever
}

func (e *Engine) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	cfg, err := parseSpec(spec)
	if err != nil {
		return engine.ApplyReport{}, fmt.Errorf("mtproto inbound %q: %w", spec.ID, err)
	}
	parsed, skipped := parseCreds(creds)
	validCreds := make([]plugin.UserCred, 0, len(parsed))
	for _, p := range parsed {
		validCreds = append(validCreds, p.cred)
	}
	creds = validCreds
	warnings := make([]string, len(skipped))
	for i, err := range skipped {
		warnings[i] = err.Error()
	}
	mpCreds := coreCredentials(parsed)
	webCreds := relayCredentials(parsed)
	sh := statehash.Spec(spec)

	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return engine.ApplyReport{}, errors.New("mtproto: engine is closed")
	}
	old := e.inbounds[spec.ID]

	if !spec.Enabled {
		restarts := uint32(0)
		if old != nil {
			restarts = old.restarts
		}
		in := &inbound{
			e: e, spec: spec, specHash: sh, creds: cloneCreds(creds), cfg: cfg,
			state: plugin.RunStopped, detail: "disabled", since: e.now(), restarts: restarts,
		}
		in.idx.Store(&credIndex{current: map[string]*credState{}})
		e.inbounds[spec.ID] = in
		e.mu.Unlock()
		if old != nil {
			e.appendPending(e.stop(old))
		}
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds), Warnings: warnings}, nil
	}

	if old != nil && old.specHash == sh && old.settled() {
		// The index first, so a session the server opens under a new credential finds its state; the server then drops
		// the removed credentials and closes their sessions in one step.
		prev := old.idx.Load()
		prevCreds := cloneCreds(old.creds)
		old.idx.Store(buildIndex(old.spec.ID, prev, parsed))
		if err := old.server.SetCredentials(mpCreds); err != nil {
			old.idx.Store(prev)
			e.mu.Unlock()
			return engine.ApplyReport{SpecHash: sh}, fmt.Errorf("mtproto inbound %q credentials: %w", spec.ID, err)
		}
		e.mu.Unlock()
		if old.relay != nil {
			if err := old.relay.SetCredentials(webCreds); err != nil {
				previousParsed, _ := parseCreds(prevCreds)
				_ = old.relay.SetCredentials(relayCredentials(previousParsed))
				e.mu.Lock()
				old.idx.Store(prev)
				_ = old.server.SetCredentials(coreCredentials(previousParsed))
				e.mu.Unlock()
				return engine.ApplyReport{SpecHash: sh}, fmt.Errorf("mtproto inbound %q relay credentials: %w", spec.ID, err)
			}
		}
		e.mu.Lock()
		old.creds = cloneCreds(creds)
		e.mu.Unlock()
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds), Warnings: warnings}, nil
	}

	restarted, restarts := false, uint32(0)
	if old != nil {
		restarted, restarts = old.serving(), old.restarts
		if restarted {
			restarts++
		}
	}

	in := &inbound{
		e: e, spec: spec, specHash: sh, creds: cloneCreds(creds), cfg: cfg,
		state: plugin.RunStarting, since: e.now(), restarts: restarts,
	}
	in.idx.Store(buildFreshIndex(spec.ID, parsed))
	e.inbounds[spec.ID] = in
	e.mu.Unlock()
	if old != nil {
		e.appendPending(e.stop(old))
	}
	if err := e.start(ctx, in, mpCreds, webCreds); err != nil {
		e.appendPending(e.stop(in))
		in.setState(plugin.RunFailed, err.Error())
		e.log.Error("mtproto inbound failed to start", "inbound", spec.ID, "err", err)
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, fmt.Errorf("mtproto inbound %q: %w", spec.ID, err)
	}
	in.setState(plugin.RunRunning, "")
	return engine.ApplyReport{SpecHash: sh, CredCount: len(creds), Restarted: restarted, Warnings: warnings}, nil
}

func (e *Engine) Remove(ctx context.Context, inboundID string) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	in := e.inbounds[inboundID]
	delete(e.inbounds, inboundID)
	e.mu.Unlock()
	if in != nil {
		e.appendPending(e.stop(in))
	}
	return nil
}

func (e *Engine) appendPending(traffic []plugin.UserTraffic) {
	if len(traffic) == 0 {
		return
	}
	e.mu.Lock()
	e.pending = append(e.pending, traffic...)
	e.mu.Unlock()
}

func coreCredentials(parsed []parsedCred) []mp.Credential {
	credentials := make([]mp.Credential, len(parsed))
	for i, p := range parsed {
		credentials[i] = mp.Credential{ID: p.cred.CredID, Key: p.key}
	}
	return credentials
}

func relayCredentials(parsed []parsedCred) []webproxy.Credential {
	credentials := make([]webproxy.Credential, len(parsed))
	for i, p := range parsed {
		credentials[i] = webproxy.Credential{ID: p.cred.CredID, Secret: append([]byte(nil), p.key[:]...)}
	}
	return credentials
}

func buildFreshIndex(inboundID string, parsed []parsedCred) *credIndex {
	ix := &credIndex{current: make(map[string]*credState, len(parsed))}
	for _, p := range parsed {
		cs := &credState{id: p.cred.CredID, inboundID: inboundID, key: p.key}
		storeOwner(cs, p.cred.UserID)
		ix.current[p.cred.CredID] = cs
	}
	return ix
}

// buildIndex is the index after a credential change: a credential kept with the same key keeps its state (and
// counters), one that comes back from retired takes its old state back, one that left is retired.
func buildIndex(inboundID string, old *credIndex, parsed []parsedCred) *credIndex {
	ix := &credIndex{current: make(map[string]*credState, len(parsed))}
	usedRetired := make([]bool, len(old.retired))
	for _, p := range parsed {
		var cs *credState
		if prev := old.current[p.cred.CredID]; prev != nil && prev.key == p.key {
			cs = prev
		} else {
			for i, prev := range old.retired {
				if !usedRetired[i] && prev.id == p.cred.CredID && prev.key == p.key {
					cs = prev
					usedRetired[i] = true
					break
				}
			}
		}
		if cs == nil {
			cs = &credState{id: p.cred.CredID, inboundID: inboundID, key: p.key}
		}
		storeOwner(cs, p.cred.UserID)
		ix.current[p.cred.CredID] = cs
	}
	for id, cs := range old.current {
		if ix.current[id] != cs {
			ix.retired = append(ix.retired, cs)
		}
	}
	for i, cs := range old.retired {
		if !usedRetired[i] {
			ix.retired = append(ix.retired, cs)
		}
	}
	return ix
}

func (e *Engine) start(ctx context.Context, in *inbound, creds []mp.Credential, webCreds []webproxy.Credential) error {
	in.smu.Lock()
	in.sessions = make(map[uint64]sessionEntry)
	in.smu.Unlock()
	log := e.log.With("inbound", in.spec.ID)
	var fallback func(context.Context, net.Conn, []byte) error
	if !in.cfg.web {
		if in.cfg.fallback == fallbackSite {
			in.forwarder = newForwarder(in.cfg.domains, log)
			fallback = in.forwarder.serve
		} else {
			d, err := in.startDecoy(ctx)
			if err != nil {
				return fmt.Errorf("decoy site: %w", err)
			}
			in.decoy = d
			fallback = d.serve
		}
	}
	port := in.spec.Listen.Port
	addr := ":" + strconv.Itoa(int(port))
	if BindIP != nil {
		addr = net.JoinHostPort(BindIP.String(), strconv.Itoa(int(port)))
	}
	srv, err := mp.New(mp.Config{
		Credentials:      creds,
		Domains:          in.cfg.domains,
		Now:              e.now,
		Observer:         inboundObserver{in: in},
		Log:              log,
		HandshakeTimeout: in.cfg.handshakeTimeout,
		IdleTimeout:      in.cfg.idleTimeout,
		ReplayWindow:     in.cfg.replayWindow,
		IgnoreTimeSkew:   in.cfg.ignoreTimeSkew,
		PreferIPv6:       in.cfg.preferIPv6,
		DCAddress:        dcAddress,
		Fallback:         fallback,
	})
	if err != nil {
		return err
	}
	var ln net.Listener
	if e.env.SharedListener != nil {
		ln = e.env.SharedListener(in.spec.ID, port)
	}
	if ln == nil {
		if ln, err = listen("tcp", addr); err != nil {
			return err
		}
	}
	in.ctx, in.cancel = context.WithCancel(context.Background())
	in.server, in.ln = srv, ln
	if in.cfg.web {
		getCertificate, err := in.siteCertificate(ctx)
		if err != nil {
			return fmt.Errorf("web site: %w", err)
		}
		relay, err := webproxy.NewRelay(webproxy.RelayOptions{
			Host: in.cfg.domains[0],
			Backend: func(ctx context.Context, conn net.Conn, credID string) error {
				return srv.ServeStream(ctx, conn, credID)
			},
			BridgePage: bootstrapPage,
			OnAuthenticated: func(r *http.Request) {
				sitehttp.MarkAuthenticated(r.Context())
			},
		})
		if err != nil {
			return fmt.Errorf("web relay: %w", err)
		}
		in.relay = relay
		if err := relay.SetCredentials(webCreds); err != nil {
			return fmt.Errorf("web relay credentials: %w", err)
		}
		in.siteSrv, err = newSiteHTTPServer(relay.Handler(in.siteHandler()), getCertificate)
		if err != nil {
			return err
		}
		siteServer := in.siteSrv
		in.wg.Add(1)
		go func() {
			defer in.wg.Done()
			_ = siteServer.ServeTLS(ln, "", "")
		}()
		return nil
	}
	in.wg.Add(1)
	go func() {
		defer in.wg.Done()
		_ = srv.ServeListener(in.ctx, ln) // admits each connection before it costs a goroutine
	}()
	return nil
}

// dcAddress is a seam for the tests (a fake DC); nil in production.
var dcAddress func(dc int16) (string, bool)

func (e *Engine) stop(in *inbound) (pending []plugin.UserTraffic) {
	if in == nil {
		return nil
	}
	in.stopOnce.Do(func() {
		if in.cancel != nil {
			in.cancel()
		}
		if in.server != nil {
			in.server.CloseAll()
		}
		if in.ln != nil {
			_ = in.ln.Close()
			in.ln = nil
		}
		if in.relay != nil {
			_ = in.relay.Close()
			in.relay = nil
		}
		if in.siteSrv != nil {
			_ = in.siteSrv.Shutdown(context.Background())
			_ = in.siteSrv.Close()
			in.siteSrv = nil
		}
		if in.decoy != nil {
			in.decoy.shutdown()
			in.decoy = nil
		}
		if in.forwarder != nil {
			in.forwarder.shutdown()
			in.forwarder = nil
		}
		// Every connection is closed now; once their goroutines are done, the sessions have made their last AddTraffic and
		// what the counters hold is final. It is owed to the panel like any delta, so it waits in pending for the next
		// Collect instead of going away with the inbound.
		in.wg.Wait()
		ix := in.idx.Load()
		for _, cs := range append(slices.Collect(maps.Values(ix.current)), ix.retired...) {
			if up, down := cs.up.Swap(0), cs.down.Swap(0); up != 0 || down != 0 {
				pending = append(pending, plugin.UserTraffic{CredID: cs.id, InboundID: in.spec.ID, Up: up, Down: down})
			}
		}
		if in.certHeld {
			e.env.Certs.Release(in.spec.ID)
			in.certHeld = false
		}
		in.smu.Lock()
		in.sessions = nil
		in.smu.Unlock()
	})
	in.setState(plugin.RunStopped, "")
	return pending
}

type inboundObserver struct{ in *inbound }

func (o inboundObserver) Opened(info mp.SessionInfo) {
	o.in.smu.Lock()
	if o.in.sessions == nil {
		o.in.smu.Unlock()
		return // the inbound stopped
	}
	cs := o.in.idx.Load().lookup(info.CredentialID)
	if cs != nil {
		cs.sessions.Add(1)
	}
	o.in.sessions[info.ID] = sessionEntry{info: info, cs: cs}
	o.in.smu.Unlock()

	// Opened follows MTProto authentication through Fake TLS or ServeStream. The core exposes no separate failed-handshake event.
	if agg := o.in.e.env.Nets; agg != nil && agg.Enabled() {
		asn := agg.Lookup(mtprotoClientAddr(info.Remote))
		userID := ""
		if cs != nil {
			if owner := cs.userID.Load(); owner != nil {
				userID = *owner
			}
		}
		agg.Add(o.in.spec.ID, asn, nets.Attempt, "", userID)
		agg.Add(o.in.spec.ID, asn, nets.Connect, info.CredentialID, userID)
	}
}

func storeOwner(cs *credState, userID string) {
	if cs == nil {
		return
	}
	if userID == "" {
		cs.userID.Store(nil)
		return
	}
	owner := userID
	cs.userID.Store(&owner)
}

// mtprotoClientAddr parses the peer address while it is still inside the engine. The address is never retained.
func mtprotoClientAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func (o inboundObserver) Closed(info mp.SessionInfo) {
	o.in.smu.Lock()
	entry, ok := o.in.sessions[info.ID]
	delete(o.in.sessions, info.ID)
	o.in.smu.Unlock()
	if ok && entry.cs != nil {
		entry.cs.sessions.Add(-1)
	}
}

// AddTraffic counts on the state the session was opened with, not on whatever its credential id names now: after a key
// change under the same id the old session's bytes stay with the old key's state.
func (o inboundObserver) AddTraffic(info mp.SessionInfo, up, down uint64) {
	o.in.smu.Lock()
	entry, ok := o.in.sessions[info.ID]
	o.in.smu.Unlock()
	if ok && entry.cs != nil {
		entry.cs.up.Add(up)
		entry.cs.down.Add(down)
	}
}

func (e *Engine) Collect(ctx context.Context) (engine.Collected, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out engine.Collected
	// One entry per (inbound, credential): a stopped inbound's pending delta and its successor's under the same ids add up.
	sums := make(map[[2]string]int) // -> index in out.Traffic
	add := func(t plugin.UserTraffic) {
		if t.Up == 0 && t.Down == 0 {
			return
		}
		k := [2]string{t.InboundID, t.CredID}
		if i, ok := sums[k]; ok {
			out.Traffic[i].Up += t.Up
			out.Traffic[i].Down += t.Down
			return
		}
		sums[k] = len(out.Traffic)
		out.Traffic = append(out.Traffic, t)
	}
	for _, t := range e.pending {
		add(t)
	}
	e.pending = nil
	for _, in := range e.sorted() {
		ix := in.idx.Load()
		flush := func(cs *credState) {
			up, down := cs.up.Swap(0), cs.down.Swap(0)
			add(plugin.UserTraffic{CredID: cs.id, InboundID: in.spec.ID, Up: up, Down: down})
		}
		for _, cs := range ix.current {
			flush(cs)
		}
		var kept []*credState
		for _, cs := range ix.retired {
			flush(cs)
			if cs.sessions.Load() > 0 {
				kept = append(kept, cs)
			}
		}
		if len(kept) != len(ix.retired) {
			in.idx.Store(&credIndex{current: ix.current, retired: kept})
		}

		in.smu.Lock()
		for _, s := range in.sessions {
			out.Sessions = append(out.Sessions, plugin.Session{
				CredID: s.info.CredentialID, InboundID: in.spec.ID, Since: s.info.Since,
			})
		}
		in.smu.Unlock()
	}
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

func (e *Engine) Kick(ctx context.Context, credIDs []string) (int, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	inbounds := e.sorted()
	e.mu.Unlock()
	hit := map[string]struct{}{}
	for _, in := range inbounds {
		if in.server == nil {
			continue
		}
		for _, id := range credIDs {
			if in.idx.Load().lookup(id) == nil {
				continue
			}
			if in.server.CloseCredential(id) > 0 {
				hit[id] = struct{}{}
			}
			if in.relay != nil {
				in.relay.CloseCredential(id)
			}
		}
	}
	return len(hit), nil
}

func (e *Engine) Observed() []statehash.Inbound {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]statehash.Inbound, 0, len(e.inbounds))
	for _, in := range e.sorted() {
		out = append(out, statehash.Inbound{Spec: in.spec, Creds: in.creds})
	}
	return out
}

func (e *Engine) Health() []plugin.EngineHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]plugin.EngineHealth, 0, len(e.inbounds))
	for _, in := range e.sorted() {
		in.mu.Lock()
		h := plugin.EngineHealth{
			InboundID: in.spec.ID, State: in.state, Detail: in.detail,
			Restarts: in.restarts, Since: in.since,
		}
		in.mu.Unlock()
		out = append(out, h)
	}
	return out
}

func (e *Engine) Close(ctx context.Context) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.closed = true
	inbounds := e.sorted()
	e.mu.Unlock()
	for _, in := range inbounds {
		e.appendPending(e.stop(in))
	}
	return nil
}

func (e *Engine) sorted() []*inbound {
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

func cloneCreds(in []plugin.UserCred) []plugin.UserCred {
	out := make([]plugin.UserCred, len(in))
	copy(out, in)
	return out
}
