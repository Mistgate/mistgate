// Package bsbord adds the paid mobile-network reachability source to the country check.
package bsbord

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	Source = countryreach.SourceBsbord

	settingCountry  = "country_reachability.country" // the country the owner checks from, owned by countryreach
	settingEnabled  = "bsbord.enabled"
	settingKey      = "bsbord.key"
	settingUnits    = "bsbord.units"
	settingInterval = "bsbord.interval_min"
	settingCap      = "bsbord.cap_credits"
	settingSpend    = "bsbord.spend"
	settingPending  = "bsbord.pending"
	settingHold     = "bsbord.hold_until"
	settingLastRun  = "bsbord.last_run"
	settingStatus   = "bsbord.last_status"
	settingLastPost = "bsbord.last_post"

	keyAAD             = "setting/bsbord.key"
	DefaultIntervalMin = 1440
	MinIntervalMin     = 60
	MaxIntervalMin     = 1440
	DefaultCapCredits  = 10000 // 100 RUB; one credit is one kopeck.
	MaxCapCredits      = 1000000000
	maxKey             = 256
	maxUnits           = 128
	maxTargets         = 10
	maxPendingAge      = 23 * time.Hour
	requestGap         = time.Second
	pollEvery          = 30 * time.Second
	testEvery          = 10 * time.Second
)

var errTickSuperseded = errors.New("bsbord: tick was superseded")

// Config configures Service. Store, Vault, StepUp and Targets are required.
type Config struct {
	Store      *store.Store
	Vault      *vault.Vault
	StepUp     func(context.Context) error
	Country    func(context.Context) string
	Targets    func(context.Context) ([]health.CountryTarget, error)
	HTTPClient *http.Client
	BaseURL    string // tests supply an httptest URL; empty uses the production API host.
	Edge       bool
	RoundLease func(ctx context.Context, name string, ttl time.Duration) bool
	OnChange   func(context.Context)
	Log        *slog.Logger
	Now        func() time.Time
}

type Service struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu     sync.Mutex
	gen    uint64
	cancel context.CancelFunc
	tickMu sync.Mutex

	lastTest time.Time
	version  string
}

type spend struct {
	Month   string `json:"month"`
	Credits int64  `json:"credits"`
}

type targetRef struct {
	Target  string   `json:"target"`
	NodeIDs []string `json:"node_ids"`
}

type plannedCall struct {
	Body    string      `json:"body"`
	Targets []targetRef `json:"targets"`
}

type skippedNode struct {
	NodeID string `json:"node_id"`
	Reason string `json:"reason"`
}

type pending struct {
	Key          string        `json:"key"`
	Body         string        `json:"body"`
	Targets      []targetRef   `json:"targets"`
	At           int64         `json:"at"`
	RoundAt      int64         `json:"round_at"`
	Reserve      int64         `json:"reserve"`
	Sent         bool          `json:"sent"`
	Running      bool          `json:"running,omitempty"`
	Remaining    []plannedCall `json:"remaining,omitempty"`
	SkippedNodes []skippedNode `json:"skipped_nodes,omitempty"`
}

type settings struct {
	Enabled     bool
	Sealed      []byte
	Units       []string
	IntervalMin int
	CapCredits  int64
	Spend       spend
	Pending     *pending
	HoldUntil   time.Time
	LastRun     time.Time
	LastStatus  string
	LastPost    time.Time
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Vault == nil || cfg.StepUp == nil || cfg.Targets == nil {
		return nil, errors.New("bsbord: Store, Vault, StepUp and Targets are required")
	}
	if cfg.Edge && cfg.RoundLease == nil {
		return nil, errors.New("bsbord: edge RoundLease is required")
	}
	if cfg.HTTPClient == nil {
		timeout := 100 * time.Second
		if cfg.Edge {
			timeout = 40 * time.Second
		}
		cfg.HTTPClient = &http.Client{Timeout: timeout}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, log: cfg.Log, now: cfg.Now}, nil
}

func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewBsbordServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(32 << 10)}, opts...)...)
}

func (s *Service) load(ctx context.Context) (settings, error) {
	keys := []string{settingEnabled, settingKey, settingUnits, settingInterval, settingCap, settingSpend, settingPending,
		settingHold, settingLastRun, settingStatus, settingLastPost}
	kv, err := s.cfg.Store.SettingValues(ctx, keys)
	if err != nil {
		return settings{}, err
	}
	st := settings{Enabled: kv[settingEnabled] == "1", IntervalMin: DefaultIntervalMin, CapCredits: DefaultCapCredits}
	if v := kv[settingKey]; v != "" {
		st.Sealed, err = base64.StdEncoding.DecodeString(v)
		if err != nil {
			return settings{}, errors.New("bsbord: stored settings unreadable")
		}
	}
	if v := kv[settingUnits]; v != "" {
		if err := json.Unmarshal([]byte(v), &st.Units); err != nil {
			return settings{}, errors.New("bsbord: stored settings unreadable")
		}
	}
	if n, err := strconv.Atoi(kv[settingInterval]); err == nil && n >= MinIntervalMin && n <= MaxIntervalMin {
		st.IntervalMin = n
	}
	if n, err := strconv.ParseInt(kv[settingCap], 10, 64); err == nil && n > 0 && n <= MaxCapCredits {
		st.CapCredits = n
	}
	if v := kv[settingSpend]; v != "" && json.Unmarshal([]byte(v), &st.Spend) != nil {
		return settings{}, errors.New("bsbord: stored settings unreadable")
	}
	if v := kv[settingPending]; v != "" && json.Unmarshal([]byte(v), &st.Pending) != nil {
		return settings{}, errors.New("bsbord: stored settings unreadable")
	}
	st.HoldUntil = parseUnix(kv[settingHold])
	st.LastRun = parseUnix(kv[settingLastRun])
	st.LastPost = parseUnixNano(kv[settingLastPost])
	st.LastStatus = safeStatus(kv[settingStatus])
	return st, nil
}

func parseUnix(v string) time.Time {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

func parseUnixNano(v string) time.Time {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func (st settings) proto(now time.Time) *adminv1.BsbordSettings {
	month := now.UTC().Format("2006-01")
	spent := int64(0)
	spendMonth := ""
	if st.Spend.Month == month {
		spent = max(int64(0), st.Spend.Credits)
		spendMonth = st.Spend.Month
	}
	reserved := int64(0)
	if st.Pending != nil {
		reserved = max(int64(0), st.Pending.Reserve)
	}
	return &adminv1.BsbordSettings{
		Enabled: st.Enabled, HasKey: len(st.Sealed) > 0, IntervalMin: uint32(st.IntervalMin), CapCredits: uint64(st.CapCredits),
		Units: slices.Clone(st.Units), SpendMonth: spendMonth, SpentCredits: uint64(spent), ReservedCredits: uint64(reserved),
		LastStatus: st.LastStatus, LastRunUnix: unixValue(st.LastRun), HoldUntilUnix: unixValue(st.HoldUntil), Pending: st.Pending != nil,
	}
}

func unixValue(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *Service) current(ctx context.Context) (*adminv1.BsbordSettings, error) {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Error("bsbord: read settings")
		return nil, errInternal
	}
	return st.proto(s.now()), nil
}

func (s *Service) secret(st settings) (vault.Redacted, error) {
	if len(st.Sealed) == 0 {
		return "", nil
	}
	b, err := s.cfg.Vault.Open(st.Sealed, keyAAD)
	if err != nil {
		s.log.Warn("bsbord: the stored key cannot be opened; it is ignored")
		return "", errors.New("bsbord: stored key unreadable")
	}
	return vault.Redacted(b), nil
}

func (s *Service) client(key vault.Redacted) *client {
	return &client{hc: s.cfg.HTTPClient, base: s.cfg.BaseURL, key: key, onVersion: s.observeVersion}
}

func (s *Service) observeVersion(version string) {
	version = cleanText(version, 40)
	if version == "" {
		return
	}
	s.mu.Lock()
	prev := s.version
	s.version = version
	s.mu.Unlock()
	if prev != "" && prev != version {
		s.log.Warn("bsbord: API version changed", "api_version", version)
	}
}

func (s *Service) generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

func (s *Service) track(ctx context.Context, gen uint64) (context.Context, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	return ctx, func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}, true
}

func (s *Service) stop() {
	s.mu.Lock()
	s.gen++
	if cancel := s.cancel; cancel != nil {
		cancel()
	}
	s.mu.Unlock()
}

// State is the settings-only view of this source for health.Config.External: one batched settings read, no network. It is read on
// every checks, history and evaluator pass.
func (s *Service) State(ctx context.Context) health.ExternalState {
	kv, err := s.cfg.Store.SettingValues(ctx, StateKeys())
	if err != nil {
		s.log.Warn("bsbord: read source state")
		return health.ExternalState{Source: Source}
	}
	return StateFrom(kv)
}

// StateKeys are the settings State reads; StateFrom builds it from their values.
func StateKeys() []string { return []string{settingEnabled, settingInterval, settingCountry} }

// StateFrom is State from the values of StateKeys.
func StateFrom(kv map[string]string) health.ExternalState {
	country := strings.ToUpper(strings.TrimSpace(kv[settingCountry]))
	if country == "" {
		country = countryreach.DefaultCountry
	}
	if country != countryreach.TSPUCountry {
		return health.ExternalState{Source: Source}
	}
	interval := DefaultIntervalMin
	if n, err := strconv.Atoi(kv[settingInterval]); err == nil && n >= MinIntervalMin && n <= MaxIntervalMin {
		interval = n
	}
	return health.ExternalState{Source: Source, On: kv[settingEnabled] == "1", MaxAge: 3 * time.Duration(interval) * time.Minute}
}

func (s *Service) country(ctx context.Context) string {
	if s.cfg.Country == nil {
		return countryreach.TSPUCountry
	}
	return strings.ToUpper(strings.TrimSpace(s.cfg.Country(ctx)))
}

func (s *Service) lease(ctx context.Context) bool {
	if !s.cfg.Edge {
		return true
	}
	return s.cfg.RoundLease(ctx, "bsbord-reach", 2*time.Minute)
}

// Tick runs at most one paid request. A large fleet's remaining chunks stay in the pending record for the next tick.
func (s *Service) Tick(ctx context.Context) {
	if !s.tickMu.TryLock() {
		return
	}
	defer s.tickMu.Unlock()
	gen := s.generation()
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("bsbord: read settings")
		return
	}
	if !st.Enabled || s.country(ctx) != countryreach.TSPUCountry {
		return
	}
	now := s.now().UTC()
	if st.HoldUntil.After(now) {
		return
	}
	if st.Pending == nil && now.Before(st.LastRun.Add(time.Duration(st.IntervalMin)*time.Minute)) {
		return
	}
	if len(st.Sealed) == 0 || len(st.Units) == 0 {
		return
	}
	if !s.lease(ctx) {
		return
	}
	rctx, done, ok := s.track(ctx, gen)
	if !ok {
		return
	}
	defer done()
	secret, err := s.secret(st)
	if err != nil || secret == "" {
		return
	}
	if st.Pending == nil {
		s.startRound(rctx, st, secret, gen, now)
		return
	}
	if st.Pending.At <= 0 || now.Sub(time.Unix(st.Pending.At, 0)) > maxPendingAge {
		s.expirePending(ctx, st, now, st.Pending.Key)
		return
	}
	s.runPending(rctx, st, secret, gen, now, st.Pending.Key)
}

// startRound prepares and persists the exact request body before previewing or submitting it.
func (s *Service) startRound(ctx context.Context, st settings, secret vault.Redacted, gen uint64, now time.Time) {
	targets, err := s.cfg.Targets(ctx)
	if err != nil {
		s.log.Warn("bsbord: read targets")
		return
	}
	calls, skipped, skippedNodes, err := makeCalls(targets, st.Units)
	if err != nil {
		s.log.Warn("bsbord: prepare targets")
		return
	}
	if len(calls) == 0 {
		if s.generation() != gen {
			return
		}
		rows := skippedRows(skippedNodes, skipped, now)
		if !s.tickCurrent(ctx, "") {
			return
		}
		if err := s.cfg.Store.InsertExtChecks(ctx, rows); err != nil {
			s.log.Warn("bsbord: store skipped targets", "err", err)
			return
		}
		st.LastRun, st.LastStatus = now, "no_targets"
		if err := s.saveTickState(ctx, st, ""); err == nil {
			s.changed(ctx)
		}
		return
	}
	key, err := idempotencyKey()
	if err != nil {
		s.log.Warn("bsbord: create request key")
		return
	}
	if s.generation() != gen {
		return
	}
	p := &pending{Key: key, Body: calls[0].Body, Targets: calls[0].Targets, At: now.Unix(), RoundAt: now.Unix(),
		Remaining: calls[1:], SkippedNodes: skippedNodes}
	st.Pending, st.LastStatus = p, "pending"
	// The free preview does not persist a request; the exact body and key are saved only before the paid POST.
	s.runPending(ctx, st, secret, gen, now, "")
}

func (s *Service) runPending(ctx context.Context, st settings, secret vault.Redacted, gen uint64, now time.Time, currentKey string) {
	p := st.Pending
	if p == nil || s.generation() != gen {
		return
	}
	c := s.client(secret)
	reserve := p.Reserve
	if !p.Sent {
		preview, _, err := c.preview(ctx, p.Body)
		if err != nil {
			if s.generation() != gen {
				return
			}
			s.handleFailure(ctx, st, err, now, false, currentKey)
			return
		}
		if !s.tickCurrent(ctx, currentKey) {
			return
		}
		reserve = max(reserve, preview.CostCredits)
		spent := st.Spend.Credits
		if st.Spend.Month != now.UTC().Format("2006-01") {
			spent = 0
		}
		if spent+reserve > st.CapCredits {
			st.LastStatus = "cap_reached"
			st.HoldUntil = firstOfNextMonth(now)
			st.Pending = nil
			if err := s.saveTickState(ctx, st, currentKey); err != nil && !errors.Is(err, errTickSuperseded) {
				s.log.Warn("bsbord: save cap hold", "err", err)
			}
			return
		}
	}
	if wait := st.LastPost.Add(requestGap).Sub(s.now()); !st.LastPost.IsZero() && wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
	if s.generation() != gen {
		return
	}
	if !s.tickCurrent(ctx, currentKey) {
		return
	}
	p.Reserve, p.Sent = reserve, true
	st.Pending, st.LastStatus = p, "pending"
	st.HoldUntil = time.Time{}
	st.LastPost = s.now().UTC()
	if err := s.saveTickState(ctx, st, currentKey); err != nil {
		if errors.Is(err, errTickSuperseded) {
			return
		}
		s.log.Warn("bsbord: save request state", "err", err)
		return
	}
	if s.generation() != gen {
		return
	}
	if !s.tickCurrent(ctx, p.Key) {
		return
	}
	result, _, err := c.probe(ctx, p.Body, p.Key)
	if s.generation() != gen {
		return
	}
	if err != nil {
		s.handleFailure(ctx, st, err, now, true, p.Key)
		return
	}
	if !s.tickCurrent(ctx, p.Key) {
		return
	}
	if result.Outcome == "no_dpi_on" {
		st.Pending, st.LastRun, st.LastStatus = nil, now, "no_dpi_on"
		st.HoldUntil = now.Add(time.Duration(st.IntervalMin) * time.Minute)
		if err := s.saveTickState(ctx, st, p.Key); err != nil && !errors.Is(err, errTickSuperseded) {
			s.log.Warn("bsbord: save no-dpi result", "err", err)
		}
		return
	}
	if result.Outcome != "done" {
		st.LastStatus, st.HoldUntil = "pending", now.Add(time.Minute)
		s.keepPending(ctx, st, p.Key)
		return
	}
	s.completeChunk(ctx, st, result, now, p.Key)
}

type failureAction struct {
	status  string
	hold    time.Time
	keep    bool
	running bool
	unsent  bool
	reserve bool
}

func classifyError(err error, st settings, now time.Time, paid bool) failureAction {
	var ae *apiError
	if !errors.As(err, &ae) {
		return failureAction{status: "pending", hold: now.Add(time.Minute), keep: true, running: paid}
	}
	status := "rejected"
	switch {
	case ae.Status == http.StatusUnauthorized:
		status = "unauthorized"
	case ae.Status == http.StatusPaymentRequired:
		status = "no_balance"
	case ae.Status == http.StatusForbidden && ae.Code == "tier_too_low":
		status = "tier_too_low"
	case ae.Status == http.StatusTooManyRequests:
		status = "rate_limited"
	case ae.Status == http.StatusBadRequest && ae.Code == "no_dpi_on":
		status = "no_dpi_on"
	case ae.Status == http.StatusConflict && (ae.Code == "busy" || ae.Code == "too_many_active" || ae.Code == "request_in_progress"):
		status = "pending"
	case ae.Status == 524:
		status = "pending"
	case ae.Status >= 500:
		status = "unavailable"
	}
	if ae.Status == http.StatusConflict && ae.Code == "request_in_progress" {
		return failureAction{status: "pending", hold: now.Add(retryDelay(ae, time.Minute)), keep: true, running: true}
	}
	if ae.Status == http.StatusConflict && (ae.Code == "busy" || ae.Code == "too_many_active") {
		return failureAction{status: "pending", hold: now.Add(retryDelay(ae, time.Minute)), keep: true, unsent: true}
	}
	if ae.Status == http.StatusRequestTimeout || ae.Status == 524 {
		return failureAction{status: "pending", hold: now.Add(retryDelay(ae, time.Minute)), keep: true, running: paid}
	}
	if ae.Retryable {
		if paid && st.Pending != nil && st.Pending.Running {
			return failureAction{status: status, hold: now.Add(retryDelay(ae, time.Minute)), keep: true, running: true}
		}
		// No paid call is known to be running. A later round can preview a fresh request after the upstream delay.
		return failureAction{status: status, hold: now.Add(retryDelay(ae, time.Minute))}
	}
	if ae.Status == http.StatusUnauthorized {
		return failureAction{status: status, hold: time.Unix(1<<62, 0).UTC(), reserve: paid}
	}
	if ae.Status == http.StatusForbidden && ae.Code == "tier_too_low" {
		return failureAction{status: status, hold: time.Unix(1<<62, 0).UTC(), reserve: paid}
	}
	if ae.Status == http.StatusPaymentRequired {
		return failureAction{status: status, hold: now.Add(6 * time.Hour), reserve: paid}
	}
	if ae.Status == http.StatusBadRequest && ae.Code == "no_dpi_on" {
		return failureAction{status: status, hold: now.Add(time.Duration(st.IntervalMin) * time.Minute)}
	}
	if paid && st.Pending != nil && st.Pending.Running {
		return failureAction{status: status, hold: now.Add(retryDelay(ae, time.Minute)), keep: true, running: true}
	}
	hold := now.Add(time.Minute)
	if ae.Status >= 400 && ae.Status < 500 {
		hold = now.Add(time.Duration(st.IntervalMin) * time.Minute)
	}
	// Keep the paid preview reserve for 401/403 and other non-retryable 4xx; the upstream may already have charged it.
	return failureAction{status: status, hold: hold, reserve: paid && ae.Status >= 400 && ae.Status < 500}
}

func (s *Service) handleFailure(ctx context.Context, st settings, err error, now time.Time, paid bool, currentKey string) {
	a := classifyError(err, st, now, paid)
	st.LastStatus, st.HoldUntil = a.status, a.hold
	if a.keep && st.Pending != nil {
		st.Pending.Running = a.running
		if a.unsent {
			st.Pending.Sent, st.Pending.Reserve = false, 0
		}
		s.keepPending(ctx, st, currentKey)
		return
	}
	s.dropPending(ctx, st, now, a.status, a.reserve, currentKey)
}

func (s *Service) keepPending(ctx context.Context, st settings, currentKey string) {
	if err := s.saveTickState(ctx, st, currentKey); err != nil && !errors.Is(err, errTickSuperseded) {
		s.log.Warn("bsbord: save retry state", "err", err)
	}
}

func (s *Service) dropPending(ctx context.Context, st settings, now time.Time, status string, reserve bool, currentKey string) {
	if reserve && st.Pending != nil && st.Pending.Reserve > 0 {
		st.Spend = addSpend(st.Spend, st.Pending.Reserve, now)
	}
	st.Pending, st.LastRun, st.LastStatus = nil, now, status
	if err := s.saveTickState(ctx, st, currentKey); err != nil && !errors.Is(err, errTickSuperseded) {
		s.log.Warn("bsbord: save failed request", "err", err)
	}
}

func (s *Service) expirePending(ctx context.Context, st settings, now time.Time, currentKey string) {
	p := st.Pending
	if p != nil && p.Reserve > 0 {
		st.Spend = addSpend(st.Spend, p.Reserve, now)
	}
	st.Pending, st.LastRun, st.LastStatus = nil, now, "lost"
	st.HoldUntil = time.Time{}
	if err := s.saveTickState(ctx, st, currentKey); err != nil && !errors.Is(err, errTickSuperseded) {
		s.log.Warn("bsbord: save expired request", "err", err)
	}
}

func (s *Service) completeChunk(ctx context.Context, st settings, result probeResponse, now time.Time, currentKey string) {
	p := st.Pending
	if p == nil {
		return
	}
	rows := rowsForResult(result, p, now)
	if !s.tickCurrent(ctx, currentKey) {
		return
	}
	if err := s.cfg.Store.InsertExtChecks(ctx, rows); err != nil {
		s.log.Warn("bsbord: store probe results", "err", err)
		return
	}
	if result.Refunded < 0 {
		result.Refunded = 0
	}
	actual := max(int64(0), result.CostCredits-result.Refunded)
	st.Spend = addSpend(st.Spend, actual, now)
	if len(p.Remaining) > 0 {
		key, err := idempotencyKey()
		if err != nil {
			s.log.Warn("bsbord: create next request key")
			return
		}
		next := p.Remaining[0]
		st.Pending = &pending{Key: key, Body: next.Body, Targets: next.Targets, At: now.Unix(), RoundAt: p.RoundAt,
			Remaining: p.Remaining[1:], SkippedNodes: p.SkippedNodes}
		st.LastStatus = "pending"
	} else {
		st.Pending, st.LastRun, st.LastStatus = nil, now, "ok"
		st.HoldUntil = time.Time{}
	}
	if err := s.saveTickState(ctx, st, currentKey); err != nil {
		if errors.Is(err, errTickSuperseded) {
			return
		}
		s.log.Warn("bsbord: save probe spend", "err", err)
		return
	}
	s.changed(ctx)
}

func (s *Service) saveConfig(ctx context.Context, st settings) error {
	units, err := json.Marshal(st.Units)
	if err != nil {
		return errors.New("bsbord: encode settings")
	}
	return s.cfg.Store.SetSettings(ctx, map[string]string{
		settingEnabled:  boolSetting(st.Enabled),
		settingKey:      base64.StdEncoding.EncodeToString(st.Sealed),
		settingUnits:    string(units),
		settingInterval: strconv.Itoa(st.IntervalMin),
		settingCap:      strconv.FormatInt(st.CapCredits, 10),
	})
}

func (s *Service) saveConfigAndState(ctx context.Context, st settings) error {
	units, err := json.Marshal(st.Units)
	if err != nil {
		return errors.New("bsbord: encode settings")
	}
	spendJSON, err := json.Marshal(st.Spend)
	if err != nil {
		return errors.New("bsbord: encode state")
	}
	var pendingJSON string
	if st.Pending != nil {
		b, err := json.Marshal(st.Pending)
		if err != nil {
			return errors.New("bsbord: encode state")
		}
		pendingJSON = string(b)
	}
	return s.cfg.Store.SetSettings(ctx, map[string]string{
		settingEnabled:  boolSetting(st.Enabled),
		settingKey:      base64.StdEncoding.EncodeToString(st.Sealed),
		settingUnits:    string(units),
		settingInterval: strconv.Itoa(st.IntervalMin),
		settingCap:      strconv.FormatInt(st.CapCredits, 10),
		settingSpend:    string(spendJSON),
		settingPending:  pendingJSON,
		settingHold:     unixSetting(st.HoldUntil),
		settingLastRun:  unixSetting(st.LastRun),
		settingStatus:   safeStatus(st.LastStatus),
		settingLastPost: unixNanoSetting(st.LastPost),
	})
}

func (s *Service) saveState(ctx context.Context, st settings) error {
	spendJSON, err := json.Marshal(st.Spend)
	if err != nil {
		return errors.New("bsbord: encode state")
	}
	var pendingJSON string
	if st.Pending != nil {
		b, err := json.Marshal(st.Pending)
		if err != nil {
			return errors.New("bsbord: encode state")
		}
		pendingJSON = string(b)
	}
	kv := map[string]string{
		settingSpend:    string(spendJSON),
		settingPending:  pendingJSON,
		settingHold:     unixSetting(st.HoldUntil),
		settingLastRun:  unixSetting(st.LastRun),
		settingStatus:   safeStatus(st.LastStatus),
		settingLastPost: unixNanoSetting(st.LastPost),
	}
	return s.cfg.Store.SetSettings(ctx, kv)
}

func (s *Service) tickCurrent(ctx context.Context, expectedKey string) bool {
	kv, err := s.cfg.Store.SettingValues(ctx, []string{settingEnabled, settingPending})
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("bsbord: verify request state")
		}
		return false
	}
	if kv[settingEnabled] != "1" {
		return false
	}
	key, ok := storedPendingKey(kv[settingPending])
	return ok && key == expectedKey
}

func (s *Service) saveTickState(ctx context.Context, st settings, expectedKey string) error {
	if !s.tickCurrent(ctx, expectedKey) {
		return errTickSuperseded
	}
	// ponytail: an isolate can change these keys between this read and SetSettings. A versioned conditional write would close
	// that remaining millisecond window once the store seam supports it.
	return s.saveState(ctx, st)
}

func storedPendingKey(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	var p pending
	if json.Unmarshal([]byte(value), &p) != nil || p.Key == "" {
		return "", false
	}
	return p.Key, true
}

func boolSetting(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func unixSetting(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.Unix(), 10)
}

func unixNanoSetting(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

func addSpend(cur spend, amount int64, now time.Time) spend {
	month := now.UTC().Format("2006-01")
	if cur.Month != month {
		cur = spend{Month: month}
	}
	cur.Credits += max(int64(0), amount)
	return cur
}

func firstOfNextMonth(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}

func retryDelay(ae *apiError, fallback time.Duration) time.Duration {
	if ae != nil && ae.RetryAfter > 0 {
		return ae.RetryAfter
	}
	return fallback
}

func safeStatus(v string) string {
	switch v {
	case "", "idle", "pending", "ok", "cap_reached", "no_balance", "tier_too_low", "lost", "unauthorized", "rate_limited", "unavailable", "no_dpi_on", "no_targets", "no_units", "rejected", "cancelled":
		return v
	default:
		return "unavailable"
	}
}

func idempotencyKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16]), nil
}

func cleanText(v string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range v {
		if unicode.IsControl(r) {
			continue
		}
		if n >= limit {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func cleanUnitKey(v string) (string, bool) {
	if v == "" || len(v) > 256 || v != strings.TrimSpace(v) || strings.Contains(v, "*") ||
		strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", false
	}
	parts := strings.Split(v, "|")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || (parts[2] != "on" && parts[2] != "off") {
		return "", false
	}
	for _, part := range parts[:2] {
		for _, r := range part {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
				return "", false
			}
		}
	}
	return v, true
}

func validateKey(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", true
	}
	if len(v) > maxKey || !strings.HasPrefix(v, "bsk_live_") || strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", false
	}
	return v, true
}

func (s *Service) changed(ctx context.Context) {
	if s.cfg.OnChange != nil {
		s.cfg.OnChange(ctx)
	}
}

type rpc struct{ s *Service }

func fail(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

var errInternal = fail(connect.CodeInternal, "internal error")

func owner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

func (r rpc) GetBsbord(ctx context.Context, _ *connect.Request[adminv1.GetBsbordRequest]) (*connect.Response[adminv1.GetBsbordResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	if r.s.country(ctx) != countryreach.TSPUCountry {
		return nil, fail(connect.CodeUnavailable, "bsbord_country_unavailable")
	}
	st, err := r.s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetBsbordResponse{Settings: st}), nil
}

func (r rpc) UpdateBsbord(ctx context.Context, req *connect.Request[adminv1.UpdateBsbordRequest]) (*connect.Response[adminv1.UpdateBsbordResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s, m := r.s, req.Msg
	st, err := s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	updated := st
	keyChanged, unitsChanged := false, false
	keyValue := ""
	if m.Key != nil {
		var ok bool
		keyValue, ok = validateKey(*m.Key)
		if !ok {
			return nil, fail(connect.CodeInvalidArgument, "key_invalid")
		}
		keyChanged = keyValue != ""
		if keyValue == "" {
			updated.Sealed = nil
			keyChanged = len(st.Sealed) > 0
		} else {
			old, _ := s.secret(st)
			keyChanged = old.Reveal() != keyValue
			updated.Sealed = s.cfg.Vault.Seal([]byte(keyValue), keyAAD)
		}
	}
	if m.IntervalMin != nil {
		if *m.IntervalMin < MinIntervalMin || *m.IntervalMin > MaxIntervalMin {
			return nil, fail(connect.CodeInvalidArgument, "interval_min: 60 to 1440")
		}
		updated.IntervalMin = int(*m.IntervalMin)
	}
	if m.CapCredits != nil {
		if *m.CapCredits == 0 || *m.CapCredits > MaxCapCredits {
			return nil, fail(connect.CodeInvalidArgument, "cap_credits: 1 to 1000000000")
		}
		updated.CapCredits = int64(*m.CapCredits)
	}
	if m.Units != nil {
		units, ok := cleanUnits(m.Units.OpKeys)
		if !ok {
			return nil, fail(connect.CodeInvalidArgument, "units_invalid")
		}
		if len(units) > 0 {
			key, err := s.secret(updated)
			if err != nil || key == "" {
				return nil, fail(connect.CodeFailedPrecondition, "bsbord_key_required")
			}
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			operators, _, err := s.client(key).operators(cctx)
			cancel()
			if err != nil {
				return nil, rpcAPIError(err)
			}
			allowed := make(map[string]bool, len(operators.Units))
			for _, unit := range operators.Units {
				if opKey, ok := cleanUnitKey(unit.OpKey); ok {
					allowed[opKey] = true
				}
			}
			for _, unit := range units {
				if !allowed[unit] {
					return nil, fail(connect.CodeInvalidArgument, "units_invalid")
				}
			}
		}
		unitsChanged = !slices.Equal(units, st.Units)
		updated.Units = units
	}
	if m.Enabled != nil {
		updated.Enabled = *m.Enabled
	}
	if updated.Enabled {
		if len(updated.Sealed) == 0 {
			return nil, fail(connect.CodeFailedPrecondition, "bsbord_key_required")
		}
		if len(updated.Units) == 0 {
			return nil, fail(connect.CodeFailedPrecondition, "bsbord_units_required")
		}
	}
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	turningOff := m.Enabled != nil && !*m.Enabled
	cancelPending := st.Pending != nil && (turningOff || keyChanged || unitsChanged)
	if turningOff || cancelPending {
		s.stop()
	}
	if cancelPending {
		if st.Pending.Sent && st.Pending.Reserve > 0 {
			updated.Spend = addSpend(updated.Spend, st.Pending.Reserve, s.now())
		}
		updated.Pending = nil
		updated.LastStatus = "cancelled"
		updated.LastRun = s.now()
	}
	var saveErr error
	if cancelPending {
		saveErr = s.saveConfigAndState(ctx, updated)
	} else {
		saveErr = s.saveConfig(ctx, updated)
	}
	if saveErr != nil {
		s.log.Error("bsbord: save settings")
		return nil, errInternal
	}
	if cancelPending {
		s.cancelPending(ctx, st)
	}
	params, _ := json.Marshal(struct {
		KeyChanged   bool `json:"key_changed"`
		UnitsChanged bool `json:"units_changed"`
	}{keyChanged, unitsChanged})
	s.audit(ctx, admin, "bsbord_update", string(params))
	s.changed(ctx)
	current, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateBsbordResponse{Settings: current}), nil
}

func cleanUnits(in []string) ([]string, bool) {
	if len(in) > maxUnits {
		return nil, false
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		k, ok := cleanUnitKey(v)
		if !ok {
			return nil, false
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out, true
}

func (s *Service) cancelPending(ctx context.Context, st settings) {
	if st.Pending == nil || !st.Pending.Sent {
		return
	}
	key, err := s.secret(st)
	if err != nil || key == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.client(key).cancel(cctx, st.Pending.Key); err != nil {
		s.log.Warn("bsbord: cancel pending request")
	}
}

func (r rpc) DisconnectBsbord(ctx context.Context, _ *connect.Request[adminv1.DisconnectBsbordRequest]) (*connect.Response[adminv1.DisconnectBsbordResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	st, err := s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	old := st
	s.stop()
	if st.Pending != nil && st.Pending.Sent && st.Pending.Reserve > 0 {
		st.Spend = addSpend(st.Spend, st.Pending.Reserve, s.now())
	}
	st.Enabled, st.Sealed, st.Units = false, nil, nil
	hadPending := st.Pending != nil
	st.Pending, st.HoldUntil, st.LastStatus = nil, time.Time{}, "idle"
	if hadPending {
		st.LastRun = s.now()
	}
	var saveErr error
	if hadPending {
		saveErr = s.saveConfigAndState(ctx, st)
	} else {
		saveErr = s.saveConfig(ctx, st)
	}
	if saveErr != nil {
		s.log.Error("bsbord: disconnect")
		return nil, errInternal
	}
	if hadPending {
		s.cancelPending(ctx, old)
	}
	s.audit(ctx, admin, "bsbord_disconnect", "")
	s.changed(ctx)
	current, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.DisconnectBsbordResponse{Settings: current}), nil
}

func (r rpc) TestBsbord(ctx context.Context, _ *connect.Request[adminv1.TestBsbordRequest]) (*connect.Response[adminv1.TestBsbordResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	s := r.s
	if err := s.testRate(); err != nil {
		return nil, err
	}
	st, err := s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	key, err := s.secret(st)
	if err != nil {
		return nil, fail(connect.CodeFailedPrecondition, "bsbord_key_unreadable")
	}
	if key == "" {
		return nil, fail(connect.CodeFailedPrecondition, "bsbord_key_required")
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c := s.client(key)
	account, _, err := c.getAccount(cctx)
	if err != nil {
		return nil, rpcAPIError(err)
	}
	units, _, err := c.operators(cctx)
	if err != nil {
		return nil, rpcAPIError(err)
	}
	return connect.NewResponse(&adminv1.TestBsbordResponse{Reachable: true, Tier: cleanText(account.Tier, 32),
		BalanceCredits: uint64(max(int64(0), account.BalanceCredits)), BonusCredits: uint64(max(int64(0), account.BonusCredits)),
		BalanceTotal: uint64(max(int64(0), account.BalanceTotal)), NUnits: uint32(max(int64(0), units.NUnits)),
		NProbeable: uint32(max(int64(0), units.NProbeable)), MinIntervalSec: uint32(max(int64(0), account.MinIntervalSec)),
		ApiVersion: s.apiVersion()}), nil
}

func (s *Service) apiVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

func (s *Service) testRate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wait := s.lastTest.Add(testEvery).Sub(s.now())
	if wait > 0 {
		return fail(connect.CodeResourceExhausted, "tested a moment ago: try again in "+strconv.Itoa(int(wait.Seconds())+1)+" s")
	}
	s.lastTest = s.now()
	return nil
}

func (r rpc) ListBsbordUnits(ctx context.Context, _ *connect.Request[adminv1.ListBsbordUnitsRequest]) (*connect.Response[adminv1.ListBsbordUnitsResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	st, err := r.s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	key, err := r.s.secret(st)
	if err != nil {
		return nil, fail(connect.CodeFailedPrecondition, "bsbord_key_unreadable")
	}
	if key == "" {
		return nil, fail(connect.CodeFailedPrecondition, "bsbord_key_required")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, _, err := r.s.client(key).operators(ctx)
	if err != nil {
		return nil, rpcAPIError(err)
	}
	out := &adminv1.ListBsbordUnitsResponse{NUnits: uint32(max(int64(0), resp.NUnits)), NProbeable: uint32(max(int64(0), resp.NProbeable))}
	for _, u := range resp.Units {
		opKey, ok := cleanUnitKey(u.OpKey)
		if !ok {
			continue
		}
		selected := slices.Contains(st.Units, opKey)
		if len(st.Units) == 0 && strings.EqualFold(u.RegionCode, "krym") {
			selected = true
		}
		out.Units = append(out.Units, &adminv1.BsbordUnit{OpKey: opKey, Operator: cleanText(u.Operator, 64), Name: cleanText(u.Name, 64),
			Region: cleanText(u.Region, 64), RegionCode: cleanText(u.RegionCode, 32), Dpi: cleanText(u.DPI, 8),
			ChannelState: cleanText(u.ChannelState, 32), Probeable: u.Probeable, Selected: selected})
	}
	return connect.NewResponse(out), nil
}

func (r rpc) PreviewBsbord(ctx context.Context, _ *connect.Request[adminv1.PreviewBsbordRequest]) (*connect.Response[adminv1.PreviewBsbordResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	s := r.s
	if s.country(ctx) != countryreach.TSPUCountry {
		return nil, fail(connect.CodeUnavailable, "bsbord_country_unavailable")
	}
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	st, err := s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	if len(st.Units) == 0 {
		return nil, fail(connect.CodeFailedPrecondition, "bsbord_units_required")
	}
	key, err := s.secret(st)
	if err != nil || key == "" {
		return nil, fail(connect.CodeFailedPrecondition, "bsbord_key_required")
	}
	targets, err := s.cfg.Targets(ctx)
	if err != nil {
		return nil, fail(connect.CodeUnavailable, "bsbord_targets_unavailable")
	}
	calls, _, _, err := makeCalls(targets, st.Units)
	if err != nil {
		return nil, fail(connect.CodeInternal, "bsbord_preview_unavailable")
	}
	if len(calls) == 0 {
		return nil, fail(connect.CodeFailedPrecondition, "bsbord_no_targets")
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	c := s.client(key)
	var cost int64
	for _, call := range calls {
		p, _, err := c.preview(cctx, call.Body)
		if err != nil {
			return nil, rpcAPIError(err)
		}
		cost += max(int64(0), p.CostCredits)
	}
	monthly := cost * int64((30*24*60)/st.IntervalMin)
	spent := st.Spend.Credits
	if st.Spend.Month != s.now().UTC().Format("2006-01") {
		spent = 0
	}
	return connect.NewResponse(&adminv1.PreviewBsbordResponse{CostCredits: uint64(cost), MonthlyCredits: uint64(max(int64(0), monthly)),
		TargetCount: uint32(countTargets(calls)), UnitCount: uint32(len(st.Units)), CapReached: spent+cost > st.CapCredits}), nil
}

func countTargets(calls []plannedCall) int {
	n := 0
	for _, c := range calls {
		n += len(c.Targets)
	}
	return n
}

func rpcAPIError(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		switch {
		case ae.Status == http.StatusUnauthorized:
			return fail(connect.CodeFailedPrecondition, "bsbord_unauthorized")
		case ae.Status == http.StatusForbidden && ae.Code == "tier_too_low":
			return fail(connect.CodeFailedPrecondition, "bsbord_tier_too_low")
		case ae.Status == http.StatusPaymentRequired:
			return fail(connect.CodeFailedPrecondition, "bsbord_no_balance")
		case ae.Status == http.StatusTooManyRequests:
			return fail(connect.CodeResourceExhausted, "bsbord_rate_limited")
		}
	}
	return fail(connect.CodeUnavailable, "bsbord_unreachable")
}

func (s *Service) audit(ctx context.Context, admin store.Admin, action, params string) {
	if err := s.cfg.Store.Audit(ctx, s.now(), store.AuditEntry{Actor: admin.ID, Action: action, Params: params, Result: "ok"}); err != nil {
		s.log.Warn("bsbord: audit", "action", action, "err", err)
	}
}

func makeCalls(targets []health.CountryTarget, units []string) ([]plannedCall, []string, []skippedNode, error) {
	if len(units) == 0 {
		return nil, nil, nil, errors.New("bsbord: no units")
	}
	ordered := slices.Clone(targets)
	slices.SortFunc(ordered, func(a, b health.CountryTarget) int { return strings.Compare(a.Key, b.Key) })
	var refs []targetRef
	var skippedNodes []skippedNode
	byAddress := map[string]int{}
	for _, target := range ordered {
		if target.Key == "" {
			continue
		}
		ip, err := netip.ParseAddr(strings.TrimSpace(target.IP))
		if err != nil || !ip.Is4() {
			skippedNodes = append(skippedNodes, skippedNode{NodeID: cleanText(target.Key, 64), Reason: "no_ipv4"})
			continue
		}
		port := preferredPort(target.Ports, target.PreferredPort)
		if port == 0 {
			skippedNodes = append(skippedNodes, skippedNode{NodeID: cleanText(target.Key, 64), Reason: "no_tcp_port"})
			continue
		}
		addr := net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
		if i, ok := byAddress[addr]; ok {
			refs[i].NodeIDs = append(refs[i].NodeIDs, cleanText(target.Key, 64))
			continue
		}
		byAddress[addr] = len(refs)
		refs = append(refs, targetRef{Target: addr, NodeIDs: []string{cleanText(target.Key, 64)}})
	}
	var skipped []string
	for _, n := range skippedNodes {
		skipped = append(skipped, n.NodeID+":"+n.Reason)
	}
	if len(refs) == 0 {
		return nil, skipped, skippedNodes, nil
	}
	var calls []plannedCall
	for start := 0; start < len(refs); start += maxTargets {
		end := min(start+maxTargets, len(refs))
		chunk := slices.Clone(refs[start:end])
		targetList := make([]string, len(chunk))
		for i := range chunk {
			targetList[i] = chunk[i].Target
		}
		body, err := json.Marshal(probeRequest{Targets: targetList, Operators: units, DPI: "any"})
		if err != nil {
			return nil, nil, nil, errors.New("bsbord: encode request")
		}
		calls = append(calls, plannedCall{Body: string(body), Targets: chunk})
	}
	return calls, skipped, skippedNodes, nil
}

func preferredPort(ports []uint16, tlsPort uint16) uint16 {
	if tlsPort != 0 && slices.Contains(ports, tlsPort) {
		return tlsPort
	}
	var other uint16
	for _, p := range ports {
		if p == 443 {
			return 443
		}
		if p != 0 && (other == 0 || p < other) {
			other = p
		}
	}
	return other
}

func rowsForResult(result probeResponse, p *pending, now time.Time) []store.ExtCheck {
	var rows []store.ExtCheck
	at := time.Unix(p.RoundAt, 0).UTC()
	if p.RoundAt <= 0 {
		at = now
	}
	cost := int(min(int64(1<<31-1), max(int64(0), result.CostCredits)))
	for _, target := range p.Targets {
		observed := result.ByTarget[target.Target]
		byOp := observed.ByOperator
		for _, nodeID := range target.NodeIDs {
			vantages := make([]countryreach.Vantage, 0, len(byOp))
			for _, key := range slices.Sorted(maps.Keys(byOp)) {
				vantages = append(vantages, mapLeg(key, byOp[key]))
			}
			verdict, reason := countryreach.JudgeVantages(vantages, 1, countryreach.VerdictBlocked)
			b, _ := json.Marshal(countryreach.ExtDetail{Reason: reason, Vantages: vantages, Skipped: rowSkipped(result, p), Cost: cost})
			rows = append(rows, store.ExtCheck{NodeID: nodeID, Source: Source, At: at, Verdict: verdict, DetailJSON: string(b)})
		}
	}
	rows = append(rows, skippedRows(p.SkippedNodes, nil, at)...)
	return rows
}

func mapLeg(key string, leg probeLeg) countryreach.Vantage {
	v := countryreach.Vantage{Key: key, Network: cleanText(leg.Operator, 64), Region: cleanText(leg.Region, 64), Kind: countryreach.VantageKindMobile,
		TCPOK: leg.TCP != nil && leg.TCP.OK, ICMPOK: leg.ICMP != nil && leg.ICMP.OK, TLS: leg.TCPIsTLS, State: countryreach.VantageStateUncertain}
	if !leg.OK || leg.DNSFail || leg.Error != nil && strings.TrimSpace(*leg.Error) != "" || leg.ICMP == nil || leg.TCP == nil {
		return v
	}
	if leg.DPI == "on" {
		if v.ICMPOK || v.TCPOK {
			v.State = countryreach.VantageStateReach
		} else {
			v.State = countryreach.VantageStateWhitelist
		}
		return v
	}
	if leg.DPI != "off" {
		return v
	}
	if v.ICMPOK || v.TCPOK {
		v.State = countryreach.VantageStateReach
	} else {
		v.State = countryreach.VantageStateDark
	}
	return v
}

func rowSkipped(result probeResponse, p *pending) []string {
	var out []string
	for _, v := range result.Skipped {
		if v = cleanText(v, 256); v != "" {
			out = append(out, "target:"+v)
		}
	}
	for _, n := range p.SkippedNodes {
		out = append(out, n.NodeID+":"+n.Reason)
	}
	for _, u := range result.SkippedUnavailable {
		if key, ok := cleanUnitKey(u.OpKey); ok {
			out = append(out, "unavailable:"+key)
		}
	}
	for _, u := range result.SkippedDPIOff {
		if key, ok := cleanUnitKey(u.OpKey); ok {
			out = append(out, "dpi_off:"+key)
			continue
		}
		parts := []string{cleanText(u.Target, 128), cleanText(u.Operator, 64), cleanText(u.Region, 64), cleanText(u.Reason, 64)}
		parts = slices.DeleteFunc(parts, func(v string) bool { return v == "" })
		if len(parts) > 0 {
			out = append(out, "dpi_off:"+strings.Join(parts, ":"))
		}
	}
	return out
}

func skippedRows(skipped []skippedNode, details []string, at time.Time) []store.ExtCheck {
	rows := make([]store.ExtCheck, 0, len(skipped))
	for _, n := range skipped {
		b, _ := json.Marshal(countryreach.ExtDetail{Reason: n.Reason, Vantages: []countryreach.Vantage{}, Skipped: details})
		rows = append(rows, store.ExtCheck{NodeID: n.NodeID, Source: Source, At: at, Verdict: countryreach.VerdictUnknown, DetailJSON: string(b)})
	}
	return rows
}
