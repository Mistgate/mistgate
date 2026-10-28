// Package globalping is the Globalping integration: the owner's settings (off by default, an interval, an optional
// write-only token; the country is the shared one of countryreach), a client of the Globalping API and the health.CountrySource that the country check measures through.
// It knows nothing about Telegram or any other integration; alerts it leads to travel through the usual alert pipeline.
//
// Switching it off stops everything: Country reports it as off, so the health module starts no round and makes no request,
// and a round in flight is cancelled.
package globalping

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	settingEnabled  = "globalping.enabled" // "1" or "0"; absent = off
	settingInterval = "globalping.interval_min"
	settingIPv6     = "globalping.ipv6"  // "0" or "1"; absent = on
	settingToken    = "globalping.token" // base64 of the vault-sealed token
	tokenAAD        = "setting/globalping.token"

	defaultInterval = 15
	minInterval     = 10
	maxInterval     = 1440
	maxToken        = 256

	limitAnonymous = 250 // tests an hour without a token
	limitToken     = 500 // with a free token

	testEvery = 10 * time.Second
)

// Config configures Service. Store, Vault and StepUp are required.
type Config struct {
	Store *store.Store
	Vault *vault.Vault
	// StepUp is auth.Service.RequireStepUp: changing the settings asks for it.
	StepUp func(context.Context) error
	// HTTPClient reaches Globalping (tests use a fake server's). Default: a client with a 20 s timeout.
	HTTPClient *http.Client
	// BaseURL of the API; default DefaultBaseURL (tests use a fake server).
	BaseURL string
	// PollDelay between two reads of a running measurement; default 500 ms, which the API's guidelines ask for.
	PollDelay time.Duration
	// Country is the country to look from, the shared setting of the country check (countryreach.Service.Country). Default RU.
	Country func(context.Context) string
	// OnChange is called after the settings were changed or the integration was disconnected, so that the alerts that depended
	// on it are brought up to date at once (health.Service.ReconcileCountry). May be nil.
	OnChange func(context.Context)
	Log      *slog.Logger
	Now      func() time.Time
}

// Service is the integration.
type Service struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	gen      uint64 // bumped when the integration is switched off or disconnected; older sources refuse to start
	inflight map[uint64]context.CancelFunc
	next     uint64
	lastTest time.Time
}

// New builds the service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Vault == nil || cfg.StepUp == nil {
		return nil, errors.New("globalping: Store, Vault and StepUp are required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.PollDelay <= 0 {
		cfg.PollDelay = defaultPoll
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, log: cfg.Log, now: cfg.Now, inflight: map[uint64]context.CancelFunc{}}, nil
}

// Handler is the admin GlobalpingService.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewGlobalpingServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(16 << 10)}, opts...)...)
}

// ---------------------------------------------------------------------------------------------------
// Settings

type settings struct {
	Enabled     bool
	IntervalMin int
	IPv6        bool
	sealed      []byte // the sealed token; nil = none
}

func (s *Service) load(ctx context.Context) (settings, error) {
	kv, err := s.cfg.Store.SettingValues(ctx, []string{settingEnabled, settingInterval, settingIPv6, settingToken})
	if err != nil {
		return settings{}, err
	}
	st := settings{Enabled: kv[settingEnabled] == "1", IntervalMin: defaultInterval, IPv6: kv[settingIPv6] != "0"}
	if n, err := strconv.Atoi(kv[settingInterval]); err == nil && n >= minInterval && n <= maxInterval {
		st.IntervalMin = n
	}
	if v := kv[settingToken]; v != "" {
		if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) > 0 {
			st.sealed = b
		}
	}
	return st, nil
}

func (st settings) proto() *adminv1.GlobalpingSettings {
	return &adminv1.GlobalpingSettings{Enabled: st.Enabled, IntervalMin: uint32(st.IntervalMin), HasToken: len(st.sealed) > 0, Ipv6: st.IPv6}
}

// token opens the stored token. A token that cannot be opened (another master key) counts as none.
func (s *Service) token(st settings) vault.Redacted {
	if len(st.sealed) == 0 {
		return ""
	}
	b, err := s.cfg.Vault.Open(st.sealed, tokenAAD)
	if err != nil {
		s.log.Warn("globalping: the stored token cannot be opened; it is ignored")
		return ""
	}
	return vault.Redacted(b)
}

func (s *Service) client(tok vault.Redacted) *client {
	return &client{hc: s.cfg.HTTPClient, base: s.cfg.BaseURL, token: tok}
}

// Country is health.Config.Country: the owner's choice and the source to measure with. While the integration is off (the
// default, or any trouble reading the settings) it says so and returns no source.
func (s *Service) Country(ctx context.Context) (health.CountryConfig, health.CountrySource) {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("globalping: read settings", "err", err)
		return health.CountryConfig{}, nil
	}
	cc := health.CountryConfig{Enabled: st.Enabled, Country: "RU", Interval: time.Duration(st.IntervalMin) * time.Minute, IPv6: st.IPv6}
	if !st.Enabled {
		return cc, nil // off costs one read: the shared country is not looked up
	}
	if s.cfg.Country != nil {
		cc.Country = s.cfg.Country(ctx)
	}
	tok := s.token(st)
	limit := limitAnonymous
	if tok != "" {
		limit = limitToken
	}
	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()
	return cc, &source{svc: s, c: s.client(tok), limit: limit, poll: s.cfg.PollDelay, gen: gen}
}

// track registers a round in flight so that switching the integration off can cancel it. It refuses to start when the
// integration was switched off after the source was made.
func (s *Service) track(ctx context.Context, gen uint64) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return ctx, func() {}, errSwitchedOff
	}
	ctx, cancel := context.WithCancel(ctx)
	id := s.next
	s.next++
	s.inflight[id] = cancel
	return ctx, func() {
		s.mu.Lock()
		delete(s.inflight, id)
		s.mu.Unlock()
		cancel()
	}, nil
}

// stop cancels every round in flight and keeps later ones from starting on an older source.
func (s *Service) stop() {
	s.mu.Lock()
	s.gen++
	for _, cancel := range s.inflight {
		cancel()
	}
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------------------------------
// The RPCs

type rpc struct{ s *Service }

func fail(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

var errInternal = fail(connect.CodeInternal, "internal error")

// owner is the signed-in owner; a token (API or MCP) is refused again here, whatever the policy table says.
func owner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

// current is the settings as an answer.
func (s *Service) current(ctx context.Context) (*adminv1.GlobalpingSettings, error) {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Error("globalping: read settings", "err", err)
		return nil, errInternal
	}
	return st.proto(), nil
}

func (r rpc) GetGlobalping(ctx context.Context, _ *connect.Request[adminv1.GetGlobalpingRequest]) (*connect.Response[adminv1.GetGlobalpingResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	cur, err := r.s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetGlobalpingResponse{Settings: cur}), nil
}

func (r rpc) UpdateGlobalping(ctx context.Context, req *connect.Request[adminv1.UpdateGlobalpingRequest]) (*connect.Response[adminv1.UpdateGlobalpingResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s, m := r.s, req.Msg
	// validate everything before the step-up and before anything is written
	kv := map[string]string{}
	if m.IntervalMin != nil {
		if *m.IntervalMin < minInterval || *m.IntervalMin > maxInterval {
			return nil, fail(connect.CodeInvalidArgument, "interval_min: 10 to 1440")
		}
		kv[settingInterval] = strconv.Itoa(int(*m.IntervalMin))
	}
	if m.Token != nil {
		kv[settingToken] = ""
		if t := vault.Redacted(strings.TrimSpace(*m.Token)); t != "" {
			if len(t) > maxToken || strings.IndexFunc(t.Reveal(), func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
				return nil, fail(connect.CodeInvalidArgument, "token_invalid")
			}
			kv[settingToken] = base64.StdEncoding.EncodeToString(s.cfg.Vault.Seal([]byte(t.Reveal()), tokenAAD))
		}
	}
	if m.Ipv6 != nil {
		kv[settingIPv6] = "0"
		if *m.Ipv6 {
			kv[settingIPv6] = "1"
		}
	}
	if m.Enabled != nil {
		kv[settingEnabled] = "0"
		if *m.Enabled {
			kv[settingEnabled] = "1"
		}
	}
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if len(kv) > 0 {
		if err := s.cfg.Store.SetSettings(ctx, kv); err != nil {
			s.log.Error("globalping: save settings", "err", err)
			return nil, errInternal
		}
	}
	if m.Enabled != nil && !*m.Enabled {
		s.stop()
	}
	s.audit(ctx, admin, "globalping_update", `{"token_changed":`+strconv.FormatBool(m.Token != nil)+`}`)
	s.changed(ctx)
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateGlobalpingResponse{Settings: cur}), nil
}

func (r rpc) DisconnectGlobalping(ctx context.Context, _ *connect.Request[adminv1.DisconnectGlobalpingRequest]) (*connect.Response[adminv1.DisconnectGlobalpingResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	s.stop() // first: nothing new may start while the settings change
	if err := s.cfg.Store.SetSettings(ctx, map[string]string{settingEnabled: "0", settingToken: ""}); err != nil {
		s.log.Error("globalping: disconnect", "err", err)
		return nil, errInternal
	}
	s.audit(ctx, admin, "globalping_disconnect", "")
	s.changed(ctx)
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.DisconnectGlobalpingResponse{Settings: cur}), nil
}

// TestGlobalping asks GET /v1/limits. It never carries an address and does not use a test.
func (r rpc) TestGlobalping(ctx context.Context, _ *connect.Request[adminv1.TestGlobalpingRequest]) (*connect.Response[adminv1.TestGlobalpingResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	s := r.s
	s.mu.Lock()
	wait := s.lastTest.Add(testEvery).Sub(s.now())
	if wait <= 0 {
		s.lastTest = s.now()
	}
	s.mu.Unlock()
	if wait > 0 {
		return nil, fail(connect.CodeResourceExhausted, "tested a moment ago: try again in "+strconv.Itoa(int(wait.Seconds())+1)+" s")
	}
	st, err := s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	tok := s.token(st)
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lim, err := s.client(tok).limits(cctx)
	var ae *apiError
	switch {
	case errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden):
		return nil, fail(connect.CodeFailedPrecondition, "globalping_unauthorized")
	case err != nil:
		return nil, fail(connect.CodeUnavailable, "globalping_unreachable")
	}
	c := lim.RateLimit.Measurements.Create
	return connect.NewResponse(&adminv1.TestGlobalpingResponse{Reachable: true, TokenUsed: tok != "", Limit: c.Limit, Remaining: c.Remaining,
		ResetS: c.Reset, Credits: lim.Credits.Remaining}), nil
}

func (s *Service) changed(ctx context.Context) {
	if s.cfg.OnChange != nil {
		s.cfg.OnChange(ctx)
	}
}

// audit records who did what; params is a JSON object and never holds the token.
func (s *Service) audit(ctx context.Context, admin store.Admin, action, params string) {
	if err := s.cfg.Store.Audit(ctx, s.now(), store.AuditEntry{Actor: admin.ID, Action: action, Params: params, Result: "ok"}); err != nil {
		s.log.Warn("globalping: audit", "action", action, "err", err)
	}
}
