package warp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/curve25519"
	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/plugin"
)

// CapWarp is the Hello capability of an agent that can run WARP. The panel registers or imports an account only for
// a node that listed it (agent.proto, AWG AND WARP).
const CapWarp = "warp/1"

// Fleet is what the module needs from the fleet module (*fleet.Fleet implements it).
type Fleet interface {
	// Live reads the durable liveness projection for the fleet in one statement.
	Live(ctx context.Context) ([]store.NodeLiveRow, error)
	// NodeLive reads one node's durable liveness projection.
	NodeLive(ctx context.Context, nodeID string) (store.NodeLiveRow, error)
	// StateChanged asks for a recompute of the desired state of the nodes. Deleting an account must reach the node as
	// a FULL state, because a delta cannot say "remove" (agent.proto, DesiredState.warp).
	StateChanged()
	// OnlineByInbound counts the open sessions of every inbound of the connected nodes.
	OnlineByInbound(ctx context.Context) map[string]int
	// WarpPauseApplied: the node confirmed a desired state that pauses its WARP (RestartWarp waits for it).
	WarpPauseApplied(ctx context.Context, nodeID string) bool
}

type noFleet struct{}

func (noFleet) Live(context.Context) ([]store.NodeLiveRow, error) { return nil, nil }
func (noFleet) NodeLive(context.Context, string) (store.NodeLiveRow, error) {
	return store.NodeLiveRow{}, nil
}
func (noFleet) StateChanged()                                  {}
func (noFleet) OnlineByInbound(context.Context) map[string]int { return nil }
func (noFleet) WarpPauseApplied(context.Context, string) bool  { return false }

// Config configures Service.
type Config struct {
	// StepUp is auth.Service.RequireStepUp: register, import and delete call it first. Required.
	StepUp func(context.Context) error
	// Actor returns the admin id for audit rows. Default: the signed-in admin.
	Actor func(context.Context) string
	// NewClient builds the Cloudflare client for the current Params. Default NewClient (uTLS); tests inject a fake API.
	NewClient func(Params) (*Client, error)
	// Resolver resolves the endpoint name of an imported profile, once. Default: the system resolver, IPv4 only.
	Resolver func(ctx context.Context, host string) ([]netip.Addr, error)
	// Backoff is the wait before each retry after HTTP 429; when the last retry is refused too, the owner is told to
	// import a wgcf profile. Default 3 s, 9 s.
	Backoff []time.Duration
	// Pause is the gap kept between two registrations (they are sequential, and Cloudflare rate-limits bursts).
	// Default: a random 2-5 s.
	Pause func() time.Duration
	// Sleep waits d or until ctx ends (tests replace it). Now is the clock.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
	Log   *slog.Logger
	// RestartPolls is how many times (restartPoll apart) RestartWarp looks whether the node applied the pause before
	// it resumes anyway. Default 80 (20 s).
	RestartPolls int
}

// restartPoll is the gap between two looks of RestartWarp at the node's applied state.
const restartPoll = 250 * time.Millisecond

// Service is the WARP module.
type Service struct {
	st  *store.Store
	v   *vault.Vault
	fl  Fleet
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex           // serialises every change of an account, and therefore every call to Cloudflare
	lastReg  time.Time            // when the last registration call ended (mu)
	lastAuto map[string]time.Time // node id -> last AutoReregister attempt (mu)
}

// New builds the module. fl may be nil.
func New(st *store.Store, v *vault.Vault, fl Fleet, cfg Config) (*Service, error) {
	if cfg.StepUp == nil {
		return nil, errors.New("warp: StepUp is required")
	}
	if v == nil {
		return nil, errors.New("warp: a vault is required")
	}
	if fl == nil {
		fl = noFleet{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Actor == nil {
		cfg.Actor = func(ctx context.Context) string {
			if a, ok := auth.AdminFrom(ctx); ok {
				return a.ID
			}
			return "unknown"
		}
	}
	if cfg.NewClient == nil {
		cfg.NewClient = NewClient
	}
	if cfg.Resolver == nil {
		cfg.Resolver = systemResolver
	}
	if cfg.Backoff == nil {
		cfg.Backoff = []time.Duration{3 * time.Second, 9 * time.Second}
	}
	if cfg.Pause == nil {
		cfg.Pause = func() time.Duration { return 2*time.Second + randDuration(3*time.Second) }
	}
	if cfg.RestartPolls <= 0 {
		cfg.RestartPolls = 80
	}
	if cfg.Sleep == nil {
		cfg.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &Service{st: st, v: v, fl: fl, cfg: cfg, log: cfg.Log, now: cfg.Now, lastAuto: map[string]time.Time{}}, nil
}

func randDuration(max time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}

// Handler is the admin WarpService (see fleet.NodeHandler for how it is mounted behind the session).
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewWarpServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)...)
}

// --- secrets -----------------------------------------------------------------------------------------------

// secrets is the sealed part of an account (warp_account.secret_enc, AAD = node id). token, reg_id and license are
// empty for an imported profile that came without wgcf-account.toml. Every formatting path prints [REDACTED].
type secrets struct {
	PrivateKey string `json:"private_key"`
	Token      string `json:"token"`
	RegID      string `json:"reg_id"`
	License    string `json:"license"`
}

func (secrets) String() string       { return "[REDACTED]" }
func (secrets) GoString() string     { return "[REDACTED]" }
func (secrets) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }
func (secrets) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte("[REDACTED]"))
}

func (s secrets) hasToken() bool { return s.Token != "" && s.RegID != "" }

func (s *Service) seal(nodeID string, x secrets) []byte {
	b, _ := json.Marshal(x)
	return s.v.Seal(b, nodeID)
}

func (s *Service) open(a store.WarpAccountRow) (secrets, error) {
	raw, err := s.v.Open(a.SecretEnc, a.NodeID)
	if err != nil {
		return secrets{}, err
	}
	var x secrets
	if err := json.Unmarshal(raw, &x); err != nil {
		return secrets{}, errors.New("warp secrets are not valid")
	}
	return x, nil
}

// --- errors ------------------------------------------------------------------------------------------------

// fail is an error for the client: a Connect code and a short machine key the UI localises.
func fail(code connect.Code, key string) error { return connect.NewError(code, errors.New(key)) }

func (s *Service) internal(what string, err error) error {
	s.log.Error("warp: "+what, "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// cloudflareErr maps what a call to Cloudflare returned to the client error, and logs the class (never a body).
func (s *Service) cloudflareErr(nodeID, what string, err error) error {
	st := apiStatus(err)
	s.log.Warn("warp: cloudflare call failed", "node", nodeID, "call", what, "status", st, "err", err)
	switch {
	case st == http.StatusTooManyRequests:
		return fail(connect.CodeResourceExhausted, "cloudflare_rate_limited") // import a wgcf profile instead
	case what == "refresh" && revoked(st):
		return fail(connect.CodeFailedPrecondition, "account_revoked")
	case st != 0:
		return fail(connect.CodeUnavailable, "cloudflare_rejected")
	}
	return fail(connect.CodeUnavailable, "cloudflare_unreachable")
}

// revoked: Cloudflare no longer knows the device (404) or its token (401). A 403 can be a block of the panel's
// address, not of the account, so it does not count.
func revoked(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusNotFound
}

func (s *Service) audit(ctx context.Context, actor, action string, params map[string]string) {
	b, _ := json.Marshal(params)
	if err := s.st.Audit(ctx, s.now(), store.AuditEntry{Actor: actor, Action: action, Params: string(b), Result: "ok"}); err != nil {
		s.log.Warn("warp: audit", "action", action, "err", err)
	}
}

// --- nodes -------------------------------------------------------------------------------------------------

// node loads a node. mutating = a change that needs a live node row: retired nodes refuse it.
func (s *Service) node(ctx context.Context, id string, mutating bool) (store.NodeRow, error) {
	if id == "" {
		return store.NodeRow{}, fail(connect.CodeInvalidArgument, "node_id_required")
	}
	n, err := s.st.Node(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.NodeRow{}, fail(connect.CodeNotFound, "node_not_found")
	case err != nil:
		return store.NodeRow{}, s.internal("load node", err)
	case mutating && n.State == "retired":
		return store.NodeRow{}, fail(connect.CodeFailedPrecondition, "node_retired")
	}
	return n, nil
}

// supports is whether the node's agent (last Hello, or the live stream) lists warp/1.
func (s *Service) supports(n store.NodeRow) bool {
	return slices.Contains(n.AgentCaps, CapWarp)
}

func (s *Service) liveNode(ctx context.Context, nodeID string) (store.NodeLiveRow, error) {
	return s.fl.NodeLive(ctx, nodeID)
}

func (s *Service) params(ctx context.Context) Params {
	raw, err := s.st.Setting(ctx, SettingKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("warp: read params", "err", err)
	}
	return parseParams(raw)
}

// --- register ----------------------------------------------------------------------------------------------

// newKey makes a WireGuard key pair: private and public, base64.
func newKey() (priv, pub string, err error) {
	var k [32]byte
	if _, err = rand.Read(k[:]); err != nil {
		return "", "", err
	}
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	p, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k[:]), base64.StdEncoding.EncodeToString(p), nil
}

// registerWith creates a device at Cloudflare (sequential, with the gap and the 429 back-off) and returns the account row
// for node n, not yet stored. The caller holds s.mu.
func (s *Service) registerWith(ctx context.Context, n store.NodeRow, p Params, tosURL, acceptedBy string) (store.WarpAccountRow, secrets, error) {
	client, err := s.cfg.NewClient(p)
	if err != nil {
		return store.WarpAccountRow{}, secrets{}, s.internal("cloudflare client", err)
	}
	priv, pub, err := newKey()
	if err != nil {
		return store.WarpAccountRow{}, secrets{}, s.internal("make key", err)
	}
	if !s.lastReg.IsZero() { // keep the gap to the previous registration
		if wait := s.cfg.Pause() - s.now().Sub(s.lastReg); wait > 0 {
			if err := s.cfg.Sleep(ctx, wait); err != nil {
				return store.WarpAccountRow{}, secrets{}, fail(connect.CodeUnavailable, "cloudflare_unreachable")
			}
		}
	}
	var reg Registration
	for try := 0; ; try++ {
		reg, err = client.Register(ctx, p, pub, s.now())
		s.lastReg = s.now()
		if apiStatus(err) != http.StatusTooManyRequests || try >= len(s.cfg.Backoff) {
			break
		}
		if err := s.cfg.Sleep(ctx, s.cfg.Backoff[try]); err != nil {
			return store.WarpAccountRow{}, secrets{}, fail(connect.CodeUnavailable, "cloudflare_unreachable")
		}
	}
	if err != nil {
		return store.WarpAccountRow{}, secrets{}, s.cloudflareErr(n.ID, "register", err)
	}
	now := s.now()
	row := store.WarpAccountRow{
		NodeID: n.ID, Source: store.WarpRegistered, PeerPublicKey: reg.PeerPublicKey,
		EndpointV4: reg.EndpointV4, EndpointV6: reg.EndpointV6, Ports: reg.Ports,
		AddressV4: reg.AddressV4, AddressV6: reg.AddressV6, MTU: 1280, ClientID: reg.ClientID,
		// Reserved bytes are recorded (client_id) but not stamped until the live check against Cloudflare
		// shows they matter; flip UseReserved here when it does.
		UseReserved: false, AccountType: reg.AccountType, Enabled: true,
		TOSURL: tosURL, TOSAcceptedBy: acceptedBy, TOSAcceptedAt: now,
		RegisteredWith: p.APIVersion + " " + p.UserAgent, CreatedAt: now, UpdatedAt: now,
	}
	x := secrets{PrivateKey: priv, Token: reg.Token, RegID: reg.ID, License: reg.License}
	return row, x, nil
}

// forget deletes a device at Cloudflare without failing the caller: used to not leave a device behind when the
// local insert failed, and for the old device after a re-registration. Bounded by its own short timeout.
func (s *Service) forget(nodeID string, p Params, x secrets) bool {
	if !x.hasToken() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := s.cfg.NewClient(p)
	if err != nil {
		return false
	}
	if err := c.Delete(ctx, p, x.RegID, x.Token); err != nil {
		s.log.Warn("warp: delete device at cloudflare failed", "node", nodeID, "status", apiStatus(err), "err", err)
		return false
	}
	return true
}

// register makes a new device at Cloudflare for the node. replace: the node has an account already (Cloudflare revoked
// it) and the new one takes its place in one step, as AutoReregister does: the old account stays until the new one is
// stored, then its device is deleted at Cloudflare.
func (s *Service) register(ctx context.Context, nodeID string, acceptTOS bool, tosShown string, replace bool) (store.WarpAccountRow, error) {
	if !acceptTOS {
		return store.WarpAccountRow{}, fail(connect.CodeInvalidArgument, "tos_not_accepted")
	}
	tosURL, err := cleanTOSURL(tosShown)
	if err != nil {
		return store.WarpAccountRow{}, err
	}
	n, err := s.node(ctx, nodeID, true)
	if err != nil {
		return store.WarpAccountRow{}, err
	}
	if !s.supports(n) {
		return store.WarpAccountRow{}, fail(connect.CodeFailedPrecondition, "agent too old")
	}
	// A browser that goes away must not leave a device at Cloudflare that the panel never stored.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	actor := s.cfg.Actor(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.st.WarpAccount(ctx, nodeID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		replace = false // nothing to replace: a plain registration
	case err != nil:
		return store.WarpAccountRow{}, s.internal("load account", err)
	case !replace:
		return store.WarpAccountRow{}, fail(connect.CodeFailedPrecondition, "account_exists")
	}
	p := s.params(ctx)
	row, x, err := s.registerWith(ctx, n, p, tosURL, actor)
	if err != nil {
		return store.WarpAccountRow{}, err
	}
	row.SecretEnc = s.seal(nodeID, x)
	if replace {
		oldSecrets, _ := s.open(old) // unreadable (wrong master key): only the local account goes
		if err := s.st.ReplaceWarpAccount(ctx, row); err != nil {
			s.forget(nodeID, p, x)
			return store.WarpAccountRow{}, s.accountErr("replace account", err)
		}
		remote := s.forget(nodeID, p, oldSecrets)
		s.log.Info("warp: account registered again", "node", nodeID, "admin", actor, "old_deleted", remote)
		s.audit(ctx, actor, "warp_reregister", map[string]string{"node_id": nodeID, "node": n.Name, "tos_url": tosURL, "api": p.APIVersion,
			"old_deleted": fmt.Sprint(remote)})
		s.fl.StateChanged()
		return row, nil
	}
	if err := s.st.CreateWarpAccount(ctx, row); err != nil {
		s.forget(nodeID, p, x)
		if errors.Is(err, store.ErrConflict) {
			return store.WarpAccountRow{}, fail(connect.CodeFailedPrecondition, "account_exists")
		}
		return store.WarpAccountRow{}, s.internal("store account", err)
	}
	s.log.Info("warp: account registered", "node", nodeID, "admin", actor)
	s.audit(ctx, actor, "warp_register", map[string]string{"node_id": nodeID, "node": n.Name, "tos_url": tosURL, "api": p.APIVersion})
	s.fl.StateChanged()
	return row, nil
}

// cleanTOSURL is the terms URL stored with the acceptance: what the UI showed, which must be an https URL; empty = TOSURL.
func cleanTOSURL(shown string) (string, error) {
	shown = strings.TrimSpace(shown)
	if shown == "" {
		return TOSURL, nil
	}
	u, err := url.Parse(shown)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(shown) > 300 || strings.ContainsFunc(shown, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		return "", fail(connect.CodeInvalidArgument, "bad_tos_url")
	}
	return shown, nil
}

// --- import ------------------------------------------------------------------------------------------------

func (s *Service) importAccount(ctx context.Context, nodeID, profileText, accountText string) (store.WarpAccountRow, error) {
	n, err := s.node(ctx, nodeID, true)
	if err != nil {
		return store.WarpAccountRow{}, err
	}
	if !s.supports(n) {
		return store.WarpAccountRow{}, fail(connect.CodeFailedPrecondition, "agent too old")
	}
	bad := func(err error) error { return connect.NewError(connect.CodeInvalidArgument, err) }
	pr, err := parseProfile(profileText)
	if err != nil {
		return store.WarpAccountRow{}, bad(err)
	}
	var ac account
	if strings.TrimSpace(accountText) != "" {
		if ac, err = parseAccount(accountText); err != nil {
			return store.WarpAccountRow{}, bad(err)
		}
		if ac.privateKey != "" && ac.privateKey != pr.privateKey {
			return store.WarpAccountRow{}, bad(errors.New("the account file belongs to another profile"))
		}
	}
	ep, err := endpointV4(ctx, s.cfg.Resolver, pr.endpointHost)
	if err != nil {
		return store.WarpAccountRow{}, bad(err)
	}
	now := s.now()
	ports := []uint16{pr.endpointPort}
	for _, d := range defaultPorts {
		if d != pr.endpointPort {
			ports = append(ports, d)
		}
	}
	row := store.WarpAccountRow{
		NodeID: nodeID, Source: store.WarpImported, PeerPublicKey: pr.peerPublicKey, EndpointV4: ep, Ports: ports,
		AddressV4: pr.addressV4, AddressV6: pr.addressV6, MTU: pr.mtu, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	row.SecretEnc = s.seal(nodeID, secrets{PrivateKey: pr.privateKey, Token: ac.accessToken, RegID: ac.deviceID, License: ac.licenseKey})
	actor := s.cfg.Actor(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.st.CreateWarpAccount(ctx, row); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.WarpAccountRow{}, fail(connect.CodeFailedPrecondition, "account_exists")
		}
		return store.WarpAccountRow{}, s.internal("store account", err)
	}
	s.log.Info("warp: account imported", "node", nodeID, "admin", actor, "with_token", ac.accessToken != "")
	s.audit(ctx, actor, "warp_import", map[string]string{"node_id": nodeID, "node": n.Name, "with_token": fmt.Sprint(ac.accessToken != "")})
	s.fl.StateChanged()
	return row, nil
}

// --- enable, refresh, delete -------------------------------------------------------------------------------

func (s *Service) setEnabled(ctx context.Context, nodeID string, enabled bool) (store.WarpAccountRow, error) {
	if _, err := s.node(ctx, nodeID, false); err != nil {
		return store.WarpAccountRow{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.st.SetWarpEnabled(ctx, nodeID, enabled, s.now()); err != nil {
		return store.WarpAccountRow{}, s.accountErr("set enabled", err)
	}
	row, err := s.st.WarpAccount(ctx, nodeID)
	if err != nil {
		return store.WarpAccountRow{}, s.accountErr("load account", err)
	}
	action := "warp_disable"
	if enabled {
		action = "warp_enable"
	}
	s.audit(ctx, s.cfg.Actor(ctx), action, map[string]string{"node_id": nodeID})
	s.fl.StateChanged()
	return row, nil
}

// restart is "Restart WARP": pause, wait until the node confirms the paused state (or restartPolls run out), resume.
// The resume always goes out, on a context the caller cannot cancel: the call must never leave WARP paused. It
// reports whether the node confirmed the pause.
func (s *Service) restart(ctx context.Context, nodeID string) (store.WarpAccountRow, bool, error) {
	n, err := s.node(ctx, nodeID, true)
	if err != nil {
		return store.WarpAccountRow{}, false, err
	}
	live, err := s.liveNode(ctx, nodeID)
	if err != nil {
		return store.WarpAccountRow{}, false, s.internal("load live node", err)
	}
	if !live.Connected {
		return store.WarpAccountRow{}, false, fail(connect.CodeFailedPrecondition, "node_offline")
	}
	ctx = context.WithoutCancel(ctx)
	actor := s.cfg.Actor(ctx)
	// Held from the pause to the resume: an owner's pause (setEnabled) in between waits and then wins, instead of being
	// undone by the resume. Nothing the node's apply needs takes it: Spec only reads, and the fleet calls RefreshByNode and
	// AutoReregister off the stream's goroutine.
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.st.WarpAccount(ctx, nodeID)
	switch {
	case err != nil:
		return store.WarpAccountRow{}, false, s.accountErr("load account", err)
	case !a.Enabled:
		return store.WarpAccountRow{}, false, fail(connect.CodeFailedPrecondition, "account_paused")
	}
	if err := s.st.SetWarpEnabled(ctx, nodeID, false, s.now()); err != nil {
		return store.WarpAccountRow{}, false, s.accountErr("pause", err)
	}
	s.fl.StateChanged()
	confirmed := s.waitApplied(ctx, nodeID)
	if err := s.st.SetWarpEnabled(ctx, nodeID, true, s.now()); err != nil {
		return store.WarpAccountRow{}, false, s.accountErr("resume", err)
	}
	s.fl.StateChanged()
	s.audit(ctx, actor, "warp_restart", map[string]string{"node_id": nodeID, "node": n.Name, "confirmed": fmt.Sprint(confirmed)})
	row, err := s.st.WarpAccount(ctx, nodeID)
	if err != nil {
		return store.WarpAccountRow{}, false, s.accountErr("load account", err)
	}
	return row, confirmed, nil
}

// waitApplied reports whether the node confirmed a state with WARP paused within restartPolls looks. Any other applied
// change (a recompute that still had WARP on) does not count: the tunnel would not have been torn down.
func (s *Service) waitApplied(ctx context.Context, nodeID string) bool {
	for range s.cfg.RestartPolls {
		if s.fl.WarpPauseApplied(ctx, nodeID) {
			return true
		}
		if s.cfg.Sleep(ctx, restartPoll) != nil {
			return false
		}
	}
	return false
}

// warpInbounds lists the node's enabled profiles whose exit is WARP, with their open sessions now.
func (s *Service) warpInbounds(ctx context.Context, nodeID string) ([]*adminv1.WarpInbound, error) {
	rows, err := s.st.FleetInbounds(ctx, nodeID, true)
	if err != nil {
		return nil, err
	}
	online := s.fl.OnlineByInbound(ctx)
	var out []*adminv1.WarpInbound
	for _, r := range rows {
		var v struct {
			Egress string `json:"egress"`
		}
		if json.Unmarshal([]byte(r.Settings), &v) == nil && v.Egress == "warp" {
			out = append(out, &adminv1.WarpInbound{InboundId: r.ID, ProfileName: r.ProfileName, Online: uint32(online[r.ID])})
		}
	}
	return out, nil
}

func (s *Service) accountErr(what string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fail(connect.CodeFailedPrecondition, "no_account")
	}
	return s.internal(what, err)
}

// refresh reads the account back from Cloudflare. A token that Cloudflare no longer accepts marks the account
// "needs attention" (the owner decides) instead of touching it. The caller holds s.mu.
func (s *Service) refreshLocked(ctx context.Context, nodeID, actor string) (store.WarpAccountRow, error) {
	a, err := s.st.WarpAccount(ctx, nodeID)
	if err != nil {
		return store.WarpAccountRow{}, s.accountErr("load account", err)
	}
	x, err := s.open(a)
	if err != nil {
		return store.WarpAccountRow{}, s.internal("open secrets (wrong master key?)", err)
	}
	if !x.hasToken() {
		return store.WarpAccountRow{}, fail(connect.CodeFailedPrecondition, "no_token")
	}
	p := s.params(ctx)
	c, err := s.cfg.NewClient(p)
	if err != nil {
		return store.WarpAccountRow{}, s.internal("cloudflare client", err)
	}
	reg, err := c.Get(ctx, p, x.RegID, x.Token)
	if err != nil {
		if revoked(apiStatus(err)) {
			if aerr := s.st.SetWarpAttention(ctx, nodeID, "revoked", s.now()); aerr != nil {
				s.log.Warn("warp: record attention", "node", nodeID, "err", aerr)
			}
		}
		return store.WarpAccountRow{}, s.cloudflareErr(nodeID, "refresh", err)
	}
	err = s.st.UpdateWarpRefresh(ctx, nodeID, store.WarpRefresh{PeerPublicKey: reg.PeerPublicKey, EndpointV4: reg.EndpointV4,
		EndpointV6: reg.EndpointV6, Ports: reg.Ports, AddressV4: reg.AddressV4, AddressV6: reg.AddressV6, ClientID: reg.ClientID,
		UseReserved: a.UseReserved && reg.ClientID != "", AccountType: reg.AccountType}, s.now())
	if err != nil {
		return store.WarpAccountRow{}, s.accountErr("store refresh", err)
	}
	s.audit(ctx, actor, "warp_refresh", map[string]string{"node_id": nodeID})
	s.fl.StateChanged()
	return s.st.WarpAccount(ctx, nodeID)
}

// RefreshByNode is for the fleet module: a node whose WARP cannot heal itself asked the panel to read the account
// again (agent.proto warp ladder, read-only at Cloudflare). It creates nothing.
func (s *Service) RefreshByNode(ctx context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.refreshLocked(ctx, nodeID, "system")
	return err
}

func (s *Service) delete(ctx context.Context, nodeID, confirmName string) (remoteDeleted bool, err error) {
	n, err := s.node(ctx, nodeID, false)
	if err != nil {
		return false, err
	}
	if confirmName != n.Name {
		return false, fail(connect.CodeInvalidArgument, "confirm_name_mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.st.WarpAccount(ctx, nodeID)
	if err != nil {
		return false, s.accountErr("load account", err)
	}
	x, err := s.open(a)
	if err != nil {
		s.log.Error("warp: open secrets (wrong master key?); deleting the local account only", "node", nodeID, "err", err)
	}
	remote := s.forget(nodeID, s.params(ctx), x)
	if err := s.st.DeleteWarpAccount(ctx, nodeID); err != nil {
		return false, s.accountErr("delete account", err)
	}
	actor := s.cfg.Actor(ctx)
	s.log.Info("warp: account deleted", "node", nodeID, "admin", actor, "remote_deleted", remote)
	s.audit(ctx, actor, "warp_delete", map[string]string{"node_id": nodeID, "node": n.Name, "tos_url": a.TOSURL, "remote_deleted": fmt.Sprint(remote)})
	s.fl.StateChanged()
	return remote, nil
}

// AutoReregister is for the fleet module, when a node reports that its WARP needs attention: with the owner's
// auto_reregister switch on (off by default; turning it on accepts the terms for future accounts too), an account
// that came from a registration and is marked revoked is replaced by a new registration, at most once an hour per
// node. The old account stays until the new one is stored. It reports whether a new account was made.
func (s *Service) AutoReregister(ctx context.Context, nodeID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.params(ctx)
	if !p.AutoReregister {
		return false, nil
	}
	if last := s.lastAuto[nodeID]; !last.IsZero() && s.now().Sub(last) < time.Hour {
		return false, nil
	}
	a, err := s.st.WarpAccount(ctx, nodeID)
	if err != nil || a.Source != store.WarpRegistered || a.Attention == "" {
		return false, nil
	}
	n, err := s.node(ctx, nodeID, true)
	if err != nil {
		return false, err
	}
	old, _ := s.open(a)
	s.lastAuto[nodeID] = s.now()
	row, x, err := s.registerWith(ctx, n, p, TOSURL, "auto")
	if err != nil {
		return false, err
	}
	row.SecretEnc = s.seal(nodeID, x)
	row.Enabled, row.CreatedAt = a.Enabled, a.CreatedAt
	if err := s.st.ReplaceWarpAccount(ctx, row); err != nil {
		s.forget(nodeID, p, x)
		return false, s.internal("replace account", err)
	}
	s.forget(nodeID, p, old)
	s.log.Info("warp: account re-registered automatically", "node", nodeID)
	s.audit(ctx, "system", "warp_reregister", map[string]string{"node_id": nodeID, "node": n.Name, "tos_url": TOSURL})
	s.fl.StateChanged()
	return true, nil
}

// --- for the fleet module ----------------------------------------------------------------------------------

// Spec is the node's WarpSpec for the desired state: nil when the node has no account. An account that is paused
// (enabled = false) is returned with Enabled false, which tears the tunnel down on the node and makes every inbound
// with egress "warp" fail closed. The caller must send it only to a stream that listed CapWarp.
func (s *Service) Spec(ctx context.Context, nodeID string) (*plugin.WarpSpec, error) {
	a, err := s.st.WarpAccount(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	x, err := s.open(a)
	if err != nil {
		return nil, fmt.Errorf("warp account of %s: %w", nodeID, err)
	}
	return specOf(a, x.PrivateKey), nil
}

func specOf(a store.WarpAccountRow, privateKey string) *plugin.WarpSpec {
	sp := &plugin.WarpSpec{
		Enabled: a.Enabled, PrivateKey: privateKey, PeerPublicKey: a.PeerPublicKey, EndpointV4: a.EndpointV4,
		// Never the IPv6 endpoint. Dead IPv6 that looks configured black-holes the tunnel;
		// send it once the panel has a preflight that proves IPv6 to Cloudflare works from the node.
		EndpointV6: "", Ports: a.Ports, AddressV4: a.AddressV4, AddressV6: a.AddressV6, MTU: uint16(a.MTU), Backend: "auto",
	}
	if a.UseReserved {
		if id, err := base64.StdEncoding.DecodeString(a.ClientID); err == nil && len(id) >= 3 {
			sp.Reserved = id[:3]
		}
	}
	return sp
}

// StoreHealth keeps the last WarpHealth a node reported (StatsBatch.warp), for GetWarp and the node badge. A node
// without an account is ignored.
func (s *Service) StoreHealth(ctx context.Context, nodeID string, h *agentv1.WarpHealth) error {
	b, err := protojson.Marshal(h)
	if err != nil {
		return err
	}
	if err := s.st.SetWarpHealth(ctx, nodeID, string(b), s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// NeedsAttention records (or clears, with "") why the owner has to decide; the node event warp_needs_attention
// {reason} ends here. A node without an account is ignored.
func (s *Service) NeedsAttention(ctx context.Context, nodeID, reason string) error {
	if err := s.st.SetWarpAttention(ctx, nodeID, reason, s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}
