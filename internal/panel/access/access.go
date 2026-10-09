// Package access is the panel's access module: profiles and their inbounds, groups, users, devices and
// credentials, the user status rules (disabled > expired > limited > active), the effective-access rule
// that turns all of it into desired state for a node, and the data of the public subscription.
//
// The admin Profile/User/Group/Device/Awg services are methods of Service; ProfileHandler, UserHandler,
// GroupHandler, DeviceHandler and AwgHandler wrap it for Connect. Every change that can alter what a node should run ends in
// StateNotifier.StateChanged().
package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/plugin"
)

// StateNotifier is implemented by the fleet module: desired state of the nodes must be recomputed and
// pushed. It must be cheap and non-blocking (the fleet debounces).
type StateNotifier interface{ StateChanged() }

// OnlineSource tells which users have an open session right now. The fleet module implements it.
type OnlineSource interface {
	// OnlineUsers maps user id to the id of the node of the user's newest open session.
	OnlineUsers() map[string]string
}

// AfterResponseRunner starts work that must not delay a request response. A nil runner uses a goroutine.
type AfterResponseRunner func(func())

// Config of the access module.
type Config struct {
	// SubscriptionBaseURL is the public subscription address up to and including the secret prefix, e.g.
	// "https://sub.example.com/k3xq8"; the user's token is appended after a slash.
	SubscriptionBaseURL string
	Log                 *slog.Logger
	AfterResponse       AfterResponseRunner
	// CheckPorts runs the fleet UDP delivery check; nil disables save-time checks.
	CheckPorts func(context.Context, string, []uint16) ([]store.PortCheck, string, string)
}

// Service implements ProfileService, UserService, GroupService, DeviceService and AwgService (adminv1connect
// handler interfaces).
type Service struct {
	st            *store.Store
	vault         *vault.Vault
	reg           *protocols.Registry
	notify        StateNotifier
	online        OnlineSource
	dns           *dns.Service
	cfg           Config
	log           *slog.Logger
	now           func() time.Time
	afterResponse AfterResponseRunner

	touching                  sync.Map // device id -> struct{}: a last_seen_at write is in flight (sub.go)
	touchHookForTest          func(started bool)
	portChecksReadHookForTest func()

	// awgNets serialises "check the client networks of an AWG profile against the others" with the write that stores
	// them (CreateProfile, UpdateProfile), so two requests cannot both pick or accept the same network.
	// Process-wide mutex, correct for one panel process; several panels on one database need the check
	// and the write in one BEGIN IMMEDIATE transaction (or a unique index on the networks).
	awgNets sync.Mutex

	secretPtrs   map[string][]string // protocol id -> x-secret pointers
	criticalPtrs map[string][]string // protocol id -> x-critical pointers
}

type noNotify struct{}

func (noNotify) StateChanged() {}

type noOnline struct{}

func (noOnline) OnlineUsers() map[string]string { return nil }

// New builds the service. notify and online may be nil (nothing to notify, nobody online).
func New(st *store.Store, v *vault.Vault, reg *protocols.Registry, notify StateNotifier, online OnlineSource, cfg Config) (*Service, error) {
	s := &Service{
		st: st, vault: v, reg: reg, notify: notify, online: online, cfg: cfg, log: cfg.Log, now: time.Now,
		afterResponse: cfg.AfterResponse,
		dns:           dns.New(st),
		secretPtrs:    map[string][]string{}, criticalPtrs: map[string][]string{},
	}
	if s.afterResponse == nil {
		s.afterResponse = func(work func()) { go work() }
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.notify == nil {
		s.notify = noNotify{}
	}
	if s.online == nil {
		s.online = noOnline{}
	}
	for _, p := range reg.List() {
		var err error
		if s.secretPtrs[p.ID()], err = protocols.FlaggedPointers(p.SettingsSchema(), "x-secret"); err != nil {
			return nil, fmt.Errorf("protocol %s: %w", p.ID(), err)
		}
		if s.criticalPtrs[p.ID()], err = protocols.FlaggedPointers(p.SettingsSchema(), "x-critical"); err != nil {
			return nil, fmt.Errorf("protocol %s: %w", p.ID(), err)
		}
	}
	return s, nil
}

var handlerOpts = []connect.HandlerOption{connect.WithReadMaxBytes(1 << 20)}

// ProfileHandler returns the Connect path and handler of ProfileService (session checks are added by the
// HTTP server).
func (s *Service) ProfileHandler() (string, http.Handler) {
	return adminv1connect.NewProfileServiceHandler(s, handlerOpts...)
}

// UserHandler returns the Connect path and handler of UserService.
func (s *Service) UserHandler() (string, http.Handler) {
	return adminv1connect.NewUserServiceHandler(s, handlerOpts...)
}

// GroupHandler returns the Connect path and handler of GroupService.
func (s *Service) GroupHandler() (string, http.Handler) {
	return adminv1connect.NewGroupServiceHandler(s, handlerOpts...)
}

// DeviceHandler returns the Connect path and handler of DeviceService.
func (s *Service) DeviceHandler() (string, http.Handler) {
	return adminv1connect.NewDeviceServiceHandler(s, handlerOpts...)
}

// AwgHandler returns the Connect path and handler of AwgService.
func (s *Service) AwgHandler() (string, http.Handler) {
	return adminv1connect.NewAwgServiceHandler(s, handlerOpts...)
}

// Run maintains user statuses until ctx ends: a sweep at start and then every minute (expiry and quota
// period resets happen by the clock, not by an event).
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("access: status sweep failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep recomputes the status of every user and returns how many changed. A full scan of the
// user table once a minute (a few thousand rows); an expiry-ordered index walk if it ever shows in a profile.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	states, err := s.st.Access().UserStates(ctx, nil)
	if err != nil {
		return 0, err
	}
	return s.refresh(ctx, states)
}

// Recompute refreshes the status of the given users; the fleet calls it after it added their usage, so a
// quota overrun takes effect within one stats interval. Changed statuses notify the nodes.
func (s *Service) Recompute(ctx context.Context, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	states, err := s.st.Access().UserStates(ctx, userIDs)
	if err != nil {
		return err
	}
	_, err = s.refresh(ctx, states)
	return err
}

func (s *Service) refresh(ctx context.Context, states []store.AccessUserState) (int, error) {
	now := s.now()
	a := s.st.Access()
	changed := 0
	for _, u := range states {
		ps, reset := AdvancePeriod(u.QuotaReset, u.PeriodStart, now)
		used := u.UsedBytes
		if reset {
			used = 0
		}
		status := ComputeStatus(u.Disabled, u.ExpiresAt, u.QuotaBytes, used, now)
		var err error
		switch {
		case reset:
			err = a.ResetUserPeriod(ctx, u.ID, u.PeriodStart, ps, status)
		case status != u.Status:
			err = a.SetUserStatus(ctx, u.ID, status)
		default:
			continue
		}
		if err != nil {
			s.notify.StateChanged()
			return changed, err
		}
		if status != u.Status {
			changed++
		}
	}
	if changed > 0 {
		s.notify.StateChanged()
	}
	return changed, nil
}

// ---- errors ----

func invalid(format string, a ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, a...))
}

func notFound(what string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s not found", what))
}

func failed(format string, a ...any) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(format, a...))
}

// coded is a refusal the admin UI words itself ("err.<code>", web/src/lib/errors.ts): the message is the code, then its
// values (key, value pairs) as a query string, "group_not_empty: users=3". API and MCP clients read the same message.
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

func (s *Service) internal(op string, err error) error {
	s.log.Error("access: "+op, "err", err)
	return connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
}

func fieldErrors(errs []protocols.FieldError) error {
	e := connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: %s", errs[0].Pointer, errs[0].Message))
	for _, fe := range errs {
		if d, err := connect.NewErrorDetail(&adminv1.FieldError{Pointer: fe.Pointer, Code: fe.Code, Message: fe.Message}); err == nil {
			e.AddDetail(d)
		}
	}
	return e
}

// ---- tokens and secrets ----

func newToken() (token string, hash []byte) {
	b := make([]byte, 32)
	rand.Read(b) // never fails on supported platforms
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func cleanName(kind, name string) (string, error) {
	name = strings.TrimSpace(name)
	// characters, not bytes: "Алексей" is 7 of the 64, as the admin's form counts it
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", invalid("%s name must be 1-64 characters without control characters", kind)
	}
	return name, nil
}

// cleanSubscriptionName validates the optional human-readable name shown on a user's public subscription page.
// Empty means the page should use the user's internal account name.
func cleanSubscriptionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, unicode.IsControl) {
		return "", invalid("subscription name must be at most 64 characters without control characters")
	}
	return name, nil
}

// mergedSettings returns a profile's settings with its decrypted secrets in place.
func (s *Service) mergedSettings(p store.AccessProfile) (json.RawMessage, error) {
	secrets := map[string]string{}
	if len(p.SecretsEnc) > 0 {
		pt, err := s.vault.Open(p.SecretsEnc, p.ID)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(pt, &secrets); err != nil {
			return nil, err
		}
	}
	return protocols.MergeSecrets(json.RawMessage(p.SettingsJSON), secrets)
}

// appsOf lists the client apps a user has switched on.
func appsOf(happ, amnezia bool) []plugin.ClientID {
	var apps []plugin.ClientID
	if happ {
		apps = append(apps, plugin.ClientHapp)
	}
	if amnezia {
		apps = append(apps, plugin.ClientAmnezia)
	}
	return apps
}

// allowed reports whether one of the user's enabled apps consumes the protocol.
func (s *Service) allowed(protocol string, happ, amnezia bool) bool {
	p, ok := s.reg.Get(protocol)
	return ok && protocols.AllowedForApps(p, appsOf(happ, amnezia)...)
}

func nodeView(n store.AccessNode) protocols.NodeView {
	return protocols.NodeView{ID: n.ID, Name: n.Name, Address: n.Address, CountryCode: n.CountryCode}
}

// buildSpec turns a stored inbound (with its profile and node) into the node-side spec.
func (s *Service) buildSpec(f store.AccessInboundFull, settings json.RawMessage) (plugin.InboundSpec, error) {
	proto, ok := s.reg.Get(f.Profile.Protocol)
	if !ok {
		return plugin.InboundSpec{}, fmt.Errorf("unknown protocol %q", f.Profile.Protocol)
	}
	var state []byte
	if len(f.Inbound.PluginStateEnc) > 0 {
		var err error
		if state, err = s.vault.Open(f.Inbound.PluginStateEnc, f.Inbound.ID); err != nil {
			return plugin.InboundSpec{}, fmt.Errorf("cannot open the inbound's key material: %w", err)
		}
	}
	spec, err := proto.BuildInbound(protocols.InboundInput{
		Profile:               protocols.ProfileView{ID: f.Profile.ID, Version: f.Profile.Version, Settings: settings},
		Node:                  nodeView(f.Node),
		PortOverride:          f.Inbound.PortOverride,
		TLSServerNameOverride: f.Inbound.TLSServerNameOverride,
		SpecVersion:           f.Inbound.SpecVersion,
		Enabled:               f.Inbound.Enabled,
		InboundID:             f.Inbound.ID,
		PluginState:           state,
		PluginPublic:          json.RawMessage(f.Inbound.PluginPublicJSON),
	})
	// The framework owns the identity fields; the plugin does not know them.
	spec.ID, spec.Protocol, spec.ProfileID, spec.Version, spec.Enabled =
		f.Inbound.ID, f.Profile.Protocol, f.Profile.ID, f.Inbound.SpecVersion, f.Inbound.Enabled
	return spec, err
}
