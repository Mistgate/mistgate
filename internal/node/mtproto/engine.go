// Package mtproto is the node's Telegram MTProto proxy engine: one TCP listener
// per inbound, Fake TLS authentication and a direct relay to a Telegram DC.
package mtproto

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	mp "github.com/mistgate/mistgate/internal/mtproto"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

const Protocol = "mtproto"

const Version = "go-mtproto 0.1"

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

	mu       sync.Mutex
	inbounds map[string]*inbound
	closed   bool
}

var _ engine.Engine = (*Engine)(nil)

func (*Engine) Protocol() string { return Protocol }
func (*Engine) Version() string  { return Version }

func (*Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{}
}

type settings struct {
	handshakeTimeout time.Duration
	replayWindow     time.Duration
	ignoreTimeSkew   bool
	preferIPv6       bool
}

type settingsJSON struct {
	HandshakeTimeoutSecs int  `json:"handshake_timeout_secs"`
	ReplayWindowSecs     int  `json:"replay_window_secs"`
	IgnoreTimeSkew       bool `json:"ignore_time_skew"`
	PreferIPv6           bool `json:"prefer_ipv6"`
}

func parseSpec(spec plugin.InboundSpec) (settings, error) {
	s := settings{handshakeTimeout: 30 * time.Second, replayWindow: 10 * time.Minute}
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
	if j.ReplayWindowSecs != 0 {
		if j.ReplayWindowSecs < 1 || j.ReplayWindowSecs > 86_400 {
			return s, errors.New("settings: replay_window_secs must be 1-86400")
		}
		s.replayWindow = time.Duration(j.ReplayWindowSecs) * time.Second
	}
	s.ignoreTimeSkew = j.IgnoreTimeSkew
	s.preferIPv6 = j.PreferIPv6
	return s, nil
}

type parsedCred struct {
	cred plugin.UserCred
	key  [mp.KeyLen]byte
}

func parseCreds(creds []plugin.UserCred) ([]parsedCred, error) {
	out := make([]parsedCred, 0, len(creds))
	seen := make(map[string]struct{}, len(creds))
	seenKey := make(map[[mp.KeyLen]byte]string, len(creds))
	for _, c := range creds {
		if c.CredID == "" {
			return nil, errors.New("credential without id")
		}
		if _, dup := seen[c.CredID]; dup {
			return nil, fmt.Errorf("duplicate credential id %s", c.CredID)
		}
		seen[c.CredID] = struct{}{}
		var d struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(c.Data, &d); err != nil {
			return nil, fmt.Errorf("credential %s: data: %w", c.CredID, err)
		}
		raw, err := hex.DecodeString(d.Key)
		if err != nil || len(raw) != mp.KeyLen {
			return nil, fmt.Errorf("credential %s: key must be %d hex characters", c.CredID, 2*mp.KeyLen)
		}
		var key [mp.KeyLen]byte
		copy(key[:], raw)
		if other, dup := seenKey[key]; dup {
			return nil, fmt.Errorf("credentials %s and %s share one key", other, c.CredID)
		}
		seenKey[key] = c.CredID
		out = append(out, parsedCred{cred: c, key: key})
	}
	return out, nil
}

type credState struct {
	id        string
	inboundID string
	key       [mp.KeyLen]byte

	up, down atomic.Uint64
	sessions atomic.Int32
	removed  atomic.Bool
}

type credIndex struct {
	current map[string]*credState
	retired []*credState
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

type inbound struct {
	e        *Engine
	spec     plugin.InboundSpec
	specHash string
	creds    []plugin.UserCred
	cfg      settings

	idx atomic.Pointer[credIndex]

	ctx    context.Context
	cancel context.CancelFunc
	server *mp.Server
	ln     net.Listener
	wg     sync.WaitGroup

	smu      sync.Mutex
	sessions map[uint64]mp.SessionInfo

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
	parsed, err := parseCreds(creds)
	if err != nil {
		return engine.ApplyReport{}, fmt.Errorf("mtproto inbound %q: %w", spec.ID, err)
	}
	mpCreds := make([]mp.Credential, len(parsed))
	for i, p := range parsed {
		mpCreds[i] = mp.Credential{ID: p.cred.CredID, Key: p.key}
	}
	sh := statehash.Spec(spec)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return engine.ApplyReport{}, errors.New("mtproto: engine is closed")
	}
	old := e.inbounds[spec.ID]

	if !spec.Enabled {
		restarts := uint32(0)
		if old != nil {
			restarts = old.restarts
			e.stop(old)
		}
		in := &inbound{
			e: e, spec: spec, specHash: sh, creds: cloneCreds(creds), cfg: cfg,
			state: plugin.RunStopped, detail: "disabled", since: e.now(), restarts: restarts,
		}
		in.idx.Store(&credIndex{current: map[string]*credState{}})
		e.inbounds[spec.ID] = in
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, nil
	}

	if old != nil && old.specHash == sh && old.settled() {
		if err := old.server.SetCredentials(mpCreds); err != nil {
			return engine.ApplyReport{SpecHash: sh}, fmt.Errorf("mtproto inbound %q credentials: %w", spec.ID, err)
		}
		next, removed := buildIndex(old.spec.ID, old.idx.Load(), parsed)
		old.idx.Store(next)
		old.creds = cloneCreds(creds)
		for _, id := range removed {
			old.server.CloseCredential(id)
		}
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, nil
	}

	restarted, restarts := false, uint32(0)
	if old != nil {
		restarted, restarts = old.serving(), old.restarts
		if restarted {
			restarts++
		}
		e.stop(old)
	}

	in := &inbound{
		e: e, spec: spec, specHash: sh, creds: cloneCreds(creds), cfg: cfg,
		state: plugin.RunStarting, since: e.now(), restarts: restarts,
	}
	in.idx.Store(buildFreshIndex(spec.ID, parsed))
	e.inbounds[spec.ID] = in
	if err := e.start(in, mpCreds); err != nil {
		e.stop(in)
		in.setState(plugin.RunFailed, err.Error())
		e.log.Error("mtproto inbound failed to start", "inbound", spec.ID, "err", err)
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, fmt.Errorf("mtproto inbound %q: %w", spec.ID, err)
	}
	in.setState(plugin.RunRunning, "")
	return engine.ApplyReport{SpecHash: sh, CredCount: len(creds), Restarted: restarted}, nil
}

func (e *Engine) Remove(ctx context.Context, inboundID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if in := e.inbounds[inboundID]; in != nil {
		e.stop(in)
		delete(e.inbounds, inboundID)
	}
	return nil
}

func buildFreshIndex(inboundID string, parsed []parsedCred) *credIndex {
	ix := &credIndex{current: make(map[string]*credState, len(parsed))}
	for _, p := range parsed {
		ix.current[p.cred.CredID] = &credState{id: p.cred.CredID, inboundID: inboundID, key: p.key}
	}
	return ix
}

func buildIndex(inboundID string, old *credIndex, parsed []parsedCred) (*credIndex, []string) {
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
					cs.removed.Store(false)
					break
				}
			}
		}
		if cs == nil {
			cs = &credState{id: p.cred.CredID, inboundID: inboundID, key: p.key}
		}
		ix.current[p.cred.CredID] = cs
	}
	var removed []string
	for id, cs := range old.current {
		if ix.current[id] == nil {
			cs.removed.Store(true)
			removed = append(removed, id)
			ix.retired = append(ix.retired, cs)
		}
	}
	for i, cs := range old.retired {
		if !usedRetired[i] {
			ix.retired = append(ix.retired, cs)
		}
	}
	sort.Strings(removed)
	return ix, removed
}

func (e *Engine) start(in *inbound, creds []mp.Credential) error {
	in.smu.Lock()
	in.sessions = make(map[uint64]mp.SessionInfo)
	in.smu.Unlock()
	port := in.spec.Listen.Port
	addr := ":" + strconv.Itoa(int(port))
	if BindIP != nil {
		addr = net.JoinHostPort(BindIP.String(), strconv.Itoa(int(port)))
	}
	ln, err := listen("tcp", addr)
	if err != nil {
		return err
	}
	srv, err := mp.New(mp.Config{
		Credentials:      creds,
		Now:              e.now,
		Observer:         inboundObserver{in: in},
		HandshakeTimeout: in.cfg.handshakeTimeout,
		ReplayWindow:     in.cfg.replayWindow,
		IgnoreTimeSkew:   in.cfg.ignoreTimeSkew,
		PreferIPv6:       in.cfg.preferIPv6,
	})
	if err != nil {
		_ = ln.Close()
		return err
	}
	in.ctx, in.cancel = context.WithCancel(context.Background())
	in.server, in.ln = srv, ln
	in.wg.Add(1)
	go func() {
		defer in.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			in.wg.Add(1)
			go func() {
				defer in.wg.Done()
				_ = srv.Serve(in.ctx, conn)
			}()
		}
	}()
	return nil
}

func (e *Engine) stop(in *inbound) {
	if in.cancel != nil {
		in.cancel()
	}
	if in.server != nil {
		in.server.CloseAll()
	}
	if in.ln != nil {
		_ = in.ln.Close()
	}
	in.smu.Lock()
	in.sessions = nil
	in.smu.Unlock()
	in.setState(plugin.RunStopped, "")
}

type inboundObserver struct{ in *inbound }

func (o inboundObserver) Opened(info mp.SessionInfo) {
	if cs := o.in.idx.Load().lookup(info.CredentialID); cs != nil {
		cs.sessions.Add(1)
	}
	o.in.smu.Lock()
	if o.in.sessions != nil {
		o.in.sessions[info.ID] = info
	}
	o.in.smu.Unlock()
}

func (o inboundObserver) Closed(info mp.SessionInfo) {
	if cs := o.in.idx.Load().lookup(info.CredentialID); cs != nil {
		if n := cs.sessions.Add(-1); n < 0 {
			cs.sessions.Store(0)
		}
	}
	o.in.smu.Lock()
	delete(o.in.sessions, info.ID)
	o.in.smu.Unlock()
}

func (o inboundObserver) AddTraffic(credID string, up, down uint64) {
	if cs := o.in.idx.Load().lookup(credID); cs != nil {
		cs.up.Add(up)
		cs.down.Add(down)
	}
}

func (e *Engine) Collect(ctx context.Context) (engine.Collected, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out engine.Collected
	for _, in := range e.sorted() {
		ix := in.idx.Load()
		flush := func(cs *credState) {
			up, down := cs.up.Swap(0), cs.down.Swap(0)
			if up != 0 || down != 0 {
				out.Traffic = append(out.Traffic, plugin.UserTraffic{
					CredID: cs.id, InboundID: in.spec.ID, Up: up, Down: down,
				})
			}
		}
		for _, cs := range ix.current {
			flush(cs)
		}
		kept := ix.retired[:0]
		for _, cs := range ix.retired {
			flush(cs)
			if cs.sessions.Load() > 0 {
				kept = append(kept, cs)
			}
		}
		ix.retired = kept

		in.smu.Lock()
		for _, s := range in.sessions {
			out.Sessions = append(out.Sessions, plugin.Session{
				CredID: s.CredentialID, InboundID: in.spec.ID, Since: s.Since,
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
	e.mu.Lock()
	defer e.mu.Unlock()
	hit := map[string]struct{}{}
	for _, in := range e.sorted() {
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
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for _, in := range e.sorted() {
		e.stop(in)
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
