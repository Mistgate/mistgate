// Package cheburcheck checks whether volunteer networks can send a SYN through the TSPU box to a node's IPv4 address.
// The site sees the node address, panel address and Mistgate User-Agent. The integration is off by default; own-probe
// credentials are sealed in the vault and are only sent to the site's probe endpoint.
package cheburcheck

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"sort"
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
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	SourcePublic = "cheburcheck"
	SourceOwn    = "cheburcheck_own"

	settingEnabled      = "cheburcheck.enabled"
	settingPublic       = "cheburcheck.public"
	settingToken        = "cheburcheck.token"
	settingInterval     = "cheburcheck.interval_min"
	settingOwnInterval  = "cheburcheck.own_interval_min"
	settingHoldUntil    = "cheburcheck.hold_until"
	settingBackoff      = "cheburcheck.backoff_min"
	settingStatus       = "cheburcheck.status"
	settingTokenUnknown = "cheburcheck.token_unknown"
	settingCountry      = "country_reachability.country"
	tokenAAD            = "setting/cheburcheck.token"

	defaultIntervalMin    = 180
	minimumIntervalMin    = 180
	defaultOwnIntervalMin = 60
	minimumOwnIntervalMin = 30
	maximumIntervalMin    = 10080
	maximumToken          = 256
	defaultBackoffMin     = 2

	callSpacing     = 30 * time.Second
	rateHoldStart   = 2 * time.Minute
	rateHoldMax     = 30 * time.Minute
	probesOffHold   = 6 * time.Hour
	testEvery       = 10 * time.Second
	testTimeout     = 15 * time.Second
	vpsProbeBudget  = 45 * time.Second
	edgeProbeBudget = 35 * time.Second
	publicMinValid  = 2
	ownMinValid     = 1
)

// Config configures Service. Store, Vault, StepUp and Address are required.
type Config struct {
	Store *store.Store
	Vault *vault.Vault
	// StepUp is auth.Service.RequireStepUp: changing the settings asks for it.
	StepUp func(context.Context) error
	// Address resolves a node the way health.Service.NodeIPv4 does. It must return an IPv4 address.
	Address func(context.Context, store.NodeRow) (netip.Addr, bool)
	// HTTPClient and BaseURL are transport overrides. Tests use an httptest server; BaseURL defaults to the public site.
	HTTPClient *http.Client
	BaseURL    string
	// Edge uses the shorter tick budget. RoundLease coordinates the shared site limit across edge isolates.
	Edge       bool
	RoundLease func(context.Context, string, time.Duration) bool
	// ProbeTimeout overrides the whole check+SSE budget in tests. Production uses the VPS or edge default.
	ProbeTimeout time.Duration
	// OnChange refreshes the country alerts after a setting change or stored result.
	OnChange func(context.Context)
	Log      *slog.Logger
	Now      func() time.Time
}

type Service struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	gen      uint64
	inflight map[uint64]context.CancelFunc
	next     uint64
	lastTest time.Time
	lastCall time.Time // VPS spacing; EdgeRoundLease holds the cross-isolate edge spacing.
}

type settings struct {
	enabled, public bool
	intervalMin     int
	ownIntervalMin  int
	country         string
	sealed          []byte
	holdUntil       time.Time
	backoffMin      int
	status          string
	tokenUnknown    bool
}

// State is the cheburcheck source state the health module needs. Its sources have separate intervals and freshness windows.
type State struct {
	On           bool
	PublicOn     bool
	OwnOn        bool
	PublicMaxAge time.Duration
	OwnMaxAge    time.Duration
	Unavailable  string
	Status       string
	TokenUnknown bool
	HoldUntil    time.Time
	LastRun      time.Time
	LastVerdict  string
}

// SourceState is a source name with its current switch and freshness window, ready for health.Config.External.
type SourceState struct {
	Source string
	On     bool
	MaxAge time.Duration
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Vault == nil || cfg.StepUp == nil || cfg.Address == nil {
		return nil, errors.New("cheburcheck: Store, Vault, StepUp and Address are required")
	}
	if cfg.HTTPClient == nil {
		// The service request context is stricter (45 s on VPS and 35 s on edge) and owns the probe deadline.
		cfg.HTTPClient = &http.Client{Timeout: 105 * time.Second}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, log: cfg.Log, now: cfg.Now, inflight: map[uint64]context.CancelFunc{}}, nil
}

func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewCheburcheckServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(4 << 10)}, opts...)...)
}

func (s *Service) client() *client { return &client{hc: s.cfg.HTTPClient, base: s.cfg.BaseURL} }

func (s *Service) load(ctx context.Context) (settings, error) {
	keys := []string{settingEnabled, settingPublic, settingToken, settingInterval, settingOwnInterval, settingHoldUntil,
		settingBackoff, settingStatus, settingTokenUnknown, settingCountry}
	kv, err := s.cfg.Store.SettingValues(ctx, keys)
	if err != nil {
		return settings{}, err
	}
	st := settings{enabled: kv[settingEnabled] == "1", public: kv[settingPublic] != "0", intervalMin: defaultIntervalMin,
		ownIntervalMin: defaultOwnIntervalMin, country: countryreach.DefaultCountry, backoffMin: defaultBackoffMin, status: kv[settingStatus],
		tokenUnknown: kv[settingTokenUnknown] == "1"}
	if country := strings.ToUpper(kv[settingCountry]); len(country) == 2 {
		st.country = country
	}
	if n, err := strconv.Atoi(kv[settingInterval]); err == nil && n >= minimumIntervalMin && n <= maximumIntervalMin {
		st.intervalMin = n
	}
	if n, err := strconv.Atoi(kv[settingOwnInterval]); err == nil && n >= minimumOwnIntervalMin && n <= maximumIntervalMin {
		st.ownIntervalMin = n
	}
	if b, err := base64.StdEncoding.DecodeString(kv[settingToken]); err == nil && len(b) > 0 {
		st.sealed = b
	}
	if n, err := strconv.ParseInt(kv[settingHoldUntil], 10, 64); err == nil && n > 0 {
		st.holdUntil = time.Unix(n, 0)
	}
	if n, err := strconv.Atoi(kv[settingBackoff]); err == nil && n >= 2 && n <= 30 {
		st.backoffMin = n
	}
	return st, nil
}

func (s *Service) sourceState(st settings) State {
	out := State{PublicMaxAge: 3 * time.Duration(st.intervalMin) * time.Minute,
		OwnMaxAge: 3 * time.Duration(st.ownIntervalMin) * time.Minute, Status: st.status, TokenUnknown: st.tokenUnknown, HoldUntil: st.holdUntil}
	if !strings.EqualFold(st.country, countryreach.DefaultCountry) {
		out.Unavailable = "country"
	}
	out.PublicOn = st.enabled && st.public && out.Unavailable == ""
	out.OwnOn = st.enabled && len(st.sealed) > 0 && !st.tokenUnknown && out.Unavailable == ""
	out.On = out.PublicOn || out.OwnOn
	if out.TokenUnknown && out.Status == "" {
		out.Status = "token_unknown"
	}
	return out
}

func (s *Service) latest(ctx context.Context, state State) (State, error) {
	if !state.On {
		return state, nil
	}
	rows, err := s.cfg.Store.RecentExtChecks(ctx, 1, SourcePublic, SourceOwn)
	if err != nil {
		return state, err
	}
	for _, row := range rows {
		if row.At.After(state.LastRun) {
			state.LastRun, state.LastVerdict = row.At, row.Verdict
		}
	}
	return state, nil
}

// State reports the source switches and freshness windows using settings only.
func (s *Service) State(ctx context.Context) State {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("cheburcheck: read settings", "err", err)
		return State{}
	}
	return s.sourceState(st)
}

// Sources maps State to the source entries consumed by health.Config.External.
func (st State) Sources() []SourceState {
	return []SourceState{{Source: SourcePublic, On: st.PublicOn, MaxAge: st.PublicMaxAge},
		{Source: SourceOwn, On: st.OwnOn, MaxAge: st.OwnMaxAge}}
}

func protoSettings(st settings, state State) *adminv1.CheburcheckSettings {
	return &adminv1.CheburcheckSettings{Enabled: st.enabled, PublicEnabled: st.public, HasToken: len(st.sealed) > 0,
		IntervalMin: uint32(st.intervalMin), OwnIntervalMin: uint32(st.ownIntervalMin), Status: state.Status,
		HoldUntilUnix: unixOrZero(state.HoldUntil), LastRunUnix: unixOrZero(state.LastRun), LastVerdict: state.LastVerdict,
		Unavailable: state.Unavailable, TokenUnknown: state.TokenUnknown}
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *Service) current(ctx context.Context) (*adminv1.CheburcheckSettings, error) {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Error("cheburcheck: read settings", "err", err)
		return nil, errInternal
	}
	state, err := s.latest(ctx, s.sourceState(st))
	if err != nil {
		s.log.Error("cheburcheck: read latest rows", "err", err)
		return nil, errInternal
	}
	return protoSettings(st, state), nil
}

func (s *Service) token(st settings) vault.Redacted {
	if len(st.sealed) == 0 {
		return ""
	}
	b, err := s.cfg.Vault.Open(st.sealed, tokenAAD)
	if err != nil {
		s.log.Warn("cheburcheck: stored token cannot be opened; it is ignored")
		return ""
	}
	return vault.Redacted(b)
}

// Tick runs at most one overdue (source, node) check. It uses the same 30-second site lease on the VPS and edge.
func (s *Service) Tick(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	gen := s.generation()
	st, err := s.load(ctx)
	if err != nil {
		return err
	}
	state := s.sourceState(st)
	if !state.On || (st.holdUntil.After(s.now())) {
		return nil
	}
	var ownToken vault.Redacted
	if state.OwnOn {
		ownToken = s.token(st)
		if ownToken == "" {
			state.OwnOn = false
			state.On = state.PublicOn
		}
	}
	if !state.On {
		return nil
	}
	nodes, err := s.activeNodes(ctx)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}
	rows, err := s.cfg.Store.RecentExtCheckTimes(ctx, SourcePublic, SourceOwn)
	if err != nil {
		return err
	}
	last := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		last[row.NodeID+"\x00"+row.Source] = row.At
	}
	now := s.now()
	var candidates []dueCheck
	for _, n := range nodes {
		for _, lane := range []struct {
			source   string
			interval time.Duration
			enabled  bool
			token    vault.Redacted
		}{{SourcePublic, time.Duration(st.intervalMin) * time.Minute, state.PublicOn, ""},
			{SourceOwn, time.Duration(st.ownIntervalMin) * time.Minute, state.OwnOn, ownToken}} {
			if !lane.enabled {
				continue
			}
			at := last[n.ID+"\x00"+lane.source]
			if !at.IsZero() && (at.After(now) || now.Sub(at) < lane.interval) {
				continue
			}
			candidates = append(candidates, dueCheck{node: node{ID: n.ID}, row: n, source: lane.source, token: lane.token, last: at})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].last.Equal(candidates[j].last) {
			return candidates[i].last.Before(candidates[j].last)
		}
		if candidates[i].source != candidates[j].source {
			return candidates[i].source < candidates[j].source
		}
		return candidates[i].node.ID < candidates[j].node.ID
	})
	var next *dueCheck
	for i := range candidates {
		ip, ok := s.cfg.Address(ctx, candidates[i].row)
		if !ok || !ip.Is4() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		candidates[i].node.IP = ip
		next = &candidates[i]
		break
	}
	if next == nil || !s.takeCallSlot(ctx, now) {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, s.probeTimeout())
	defer cancel()
	callCtx, done, ok := s.track(callCtx, gen)
	if !ok {
		return nil
	}
	defer done()
	run, phase, runErr := s.run(callCtx, *next)
	finished := s.now()
	if runErr != nil {
		if err := s.handleFailure(ctx, gen, st, next.source, phase, runErr); err != nil {
			return err
		}
		if heldFailure(runErr, next.source, phase) {
			return nil
		}
		if phase != "probe" || (len(run.Results) == 0 && !isDeadline(runErr, callCtx)) {
			return s.storeUnknown(ctx, gen, st, *next, finished, failureReason(runErr))
		}
	}
	if callCtx.Err() != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	vantages := make([]countryreach.Vantage, 0, len(run.Results))
	for _, result := range run.Results {
		if v, ok := result.vantage(); ok {
			vantages = append(vantages, v)
		}
	}
	minValid := publicMinValid
	if next.source == SourceOwn {
		minValid = ownMinValid
	}
	verdict, reason := countryreach.JudgeVantages(vantages, minValid, "tcp_blocked")
	switch {
	case run.HasOnline && run.OnlineProbes == 0:
		verdict, reason = countryreach.VerdictUnknown, "no_probe"
	case !run.Done && len(vantages) == 0:
		verdict, reason = countryreach.VerdictUnknown, "timeout"
	case !run.Done && verdict == countryreach.VerdictUnknown:
		reason = "timeout"
	}
	detail, err := json.Marshal(countryreach.ExtDetail{Reason: reason, Vantages: vantages})
	if err != nil {
		return errors.New("cheburcheck: encode result")
	}
	row := store.ExtCheck{NodeID: next.node.ID, Source: next.source, At: finished, Verdict: verdict, DetailJSON: string(detail)}
	stored, err := s.storeResult(ctx, gen, st, row, runErr == nil)
	if stored {
		s.changed(ctx)
	}
	return err
}

type dueCheck struct {
	node   node
	row    store.NodeRow
	source string
	token  vault.Redacted
	last   time.Time
}

type node struct {
	ID string
	IP netip.Addr
}

func (s *Service) activeNodes(ctx context.Context) ([]store.NodeRow, error) {
	rows, err := s.cfg.Store.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]store.NodeRow, 0, len(rows))
	for _, n := range rows {
		if n.State == "active" {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *Service) takeCallSlot(ctx context.Context, now time.Time) bool {
	if s.cfg.RoundLease != nil && !s.cfg.RoundLease(ctx, "cheburcheck-call", max(callSpacing, s.probeTimeout()+5*time.Second)) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Edge && !s.lastCall.IsZero() && now.Sub(s.lastCall) < callSpacing {
		return false
	}
	s.lastCall = now
	return true
}

func (s *Service) probeTimeout() time.Duration {
	if s.cfg.ProbeTimeout > 0 {
		return s.cfg.ProbeTimeout
	}
	if s.cfg.Edge {
		return edgeProbeBudget
	}
	return vpsProbeBudget
}

func (s *Service) run(ctx context.Context, d dueCheck) (probeRun, string, error) {
	id, err := s.client().check(ctx, d.node.IP.String())
	if err != nil {
		return probeRun{}, "check", err
	}
	run, err := s.client().probe(ctx, id, d.token)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		// Check IDs expire. A single fresh check is allowed; do not loop on a bad or expired context.
		id, err = s.client().check(ctx, d.node.IP.String())
		if err != nil {
			return run, "check", err
		}
		run, err = s.client().probe(ctx, id, d.token)
	}
	return run, "probe", err
}

func isDeadline(err error, ctx context.Context) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

func heldFailure(err error, source, phase string) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Status == http.StatusTooManyRequests ||
		(ae.Status == http.StatusBadRequest && phase == "probe") ||
		(ae.Status == http.StatusNotFound && source == SourceOwn && phase == "probe")
}

func failureReason(err error) string {
	if errors.Is(err, errNoID) {
		return "no_id"
	}
	var ae *apiError
	if errors.As(err, &ae) {
		if ae.Status >= 500 && ae.Status <= 599 {
			return "http_5xx"
		}
		return "http_" + strconv.Itoa(ae.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "error"
}

func (s *Service) storeUnknown(ctx context.Context, gen uint64, st settings, d dueCheck, at time.Time, reason string) error {
	detail, err := json.Marshal(countryreach.ExtDetail{Reason: reason, Vantages: []countryreach.Vantage{}})
	if err != nil {
		return errors.New("cheburcheck: encode failure result")
	}
	row := store.ExtCheck{NodeID: d.node.ID, Source: d.source, At: at, Verdict: countryreach.VerdictUnknown, DetailJSON: string(detail)}
	stored, err := s.storeResult(ctx, gen, st, row, false)
	if stored {
		s.changed(ctx)
	}
	return err
}

func (s *Service) handleFailure(ctx context.Context, gen uint64, st settings, source, phase string, err error) error {
	var ae *apiError
	api := errors.As(err, &ae)
	values := map[string]string{}
	switch {
	case api && ae.Status == http.StatusTooManyRequests:
		delay := time.Duration(st.backoffMin) * time.Minute
		if delay < rateHoldStart {
			delay = rateHoldStart
		}
		if delay > rateHoldMax {
			delay = rateHoldMax
		}
		next := min(int(delay/time.Minute)*2, int(rateHoldMax/time.Minute))
		values[settingStatus] = "rate_limited"
		values[settingHoldUntil] = strconv.FormatInt(s.now().Add(delay).Unix(), 10)
		values[settingBackoff] = strconv.Itoa(next)
	case api && ae.Status == http.StatusBadRequest && phase == "probe":
		values[settingStatus] = "ip_probes_off"
		values[settingHoldUntil] = strconv.FormatInt(s.now().Add(probesOffHold).Unix(), 10)
		values[settingBackoff] = strconv.Itoa(int(rateHoldStart / time.Minute))
	case api && ae.Status == http.StatusNotFound && source == SourceOwn && phase == "probe":
		values[settingStatus] = "token_unknown"
		values[settingTokenUnknown] = "1"
	default:
		values[settingStatus] = "unreachable"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return nil
	}
	if err := s.cfg.Store.SetSettings(ctx, values); err != nil {
		s.log.Warn("cheburcheck: save failure state", "status", values[settingStatus], "err", err)
		return err
	}
	s.log.Warn("cheburcheck: request failed", "status", values[settingStatus], "err", err)
	return nil
}

func (s *Service) storeResult(ctx context.Context, gen uint64, st settings, row store.ExtCheck, clearStatus bool) (bool, error) {
	s.mu.Lock()
	// Edge cron and admin isolates have separate generations, so an in-flight cron call can still write after an admin change.
	if gen != s.gen {
		s.mu.Unlock()
		return false, nil
	}
	if err := s.cfg.Store.InsertExtChecks(ctx, []store.ExtCheck{row}); err != nil {
		s.mu.Unlock()
		return false, err
	}
	if clearStatus && (st.status != "" || !st.holdUntil.IsZero() || st.backoffMin != defaultBackoffMin) {
		clear := map[string]string{settingStatus: "", settingHoldUntil: "", settingBackoff: strconv.Itoa(defaultBackoffMin)}
		if err := s.cfg.Store.SetSettings(ctx, clear); err != nil {
			s.mu.Unlock()
			return true, err
		}
	}
	s.mu.Unlock()
	return true, nil
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
	cctx, cancel := context.WithCancel(ctx)
	id := s.next
	s.next++
	s.inflight[id] = cancel
	return cctx, func() {
		s.mu.Lock()
		delete(s.inflight, id)
		s.mu.Unlock()
		cancel()
	}, true
}

func (s *Service) stop() {
	s.mu.Lock()
	s.gen++
	for _, cancel := range s.inflight {
		cancel()
	}
	s.mu.Unlock()
}

func (s *Service) changed(ctx context.Context) {
	if s.cfg.OnChange != nil {
		s.cfg.OnChange(ctx)
	}
}

type rpc struct{ s *Service }

var (
	errInternal = connect.NewError(connect.CodeInternal, errors.New("internal error"))
)

func fail(code connect.Code, message string) error {
	return connect.NewError(code, errors.New(message))
}

func owner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

func (r rpc) GetCheburcheck(ctx context.Context, _ *connect.Request[adminv1.GetCheburcheckRequest]) (*connect.Response[adminv1.GetCheburcheckResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	settings, err := r.s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetCheburcheckResponse{Settings: settings}), nil
}

func (r rpc) UpdateCheburcheck(ctx context.Context, req *connect.Request[adminv1.UpdateCheburcheckRequest]) (*connect.Response[adminv1.UpdateCheburcheckResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s, m := r.s, req.Msg
	values := map[string]string{}
	if m.IntervalMin != nil {
		if *m.IntervalMin < minimumIntervalMin || *m.IntervalMin > maximumIntervalMin {
			return nil, fail(connect.CodeInvalidArgument, "interval_min: at least 180")
		}
		values[settingInterval] = strconv.Itoa(int(*m.IntervalMin))
	}
	if m.OwnIntervalMin != nil {
		if *m.OwnIntervalMin < minimumOwnIntervalMin || *m.OwnIntervalMin > maximumIntervalMin {
			return nil, fail(connect.CodeInvalidArgument, "own_interval_min: 30 to 10080")
		}
		values[settingOwnInterval] = strconv.Itoa(int(*m.OwnIntervalMin))
	}
	if m.Enabled != nil {
		values[settingEnabled] = boolSetting(*m.Enabled)
	}
	if m.PublicEnabled != nil {
		values[settingPublic] = boolSetting(*m.PublicEnabled)
	}
	tokenChanged := m.Token != nil
	if tokenChanged {
		values[settingToken] = ""
		values[settingTokenUnknown] = "0"
		if token := vault.Redacted(strings.TrimSpace(*m.Token)); token != "" {
			if len(token) > maximumToken || strings.IndexFunc(token.Reveal(), func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
				return nil, fail(connect.CodeInvalidArgument, "token_invalid")
			}
			values[settingToken] = base64.StdEncoding.EncodeToString(s.cfg.Vault.Seal([]byte(token.Reveal()), tokenAAD))
		}
	}
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if len(values) > 0 {
		s.stop()
		if err := s.cfg.Store.SetSettings(ctx, values); err != nil {
			s.log.Error("cheburcheck: save settings", "err", err)
			return nil, errInternal
		}
	}
	params := `{"token_changed":` + strconv.FormatBool(tokenChanged) + `}`
	if err := s.cfg.Store.Audit(ctx, s.now(), store.AuditEntry{Actor: admin.ID, Action: "cheburcheck_update", Params: params, Result: "ok"}); err != nil {
		s.log.Warn("cheburcheck: audit", "err", err)
	}
	s.changed(ctx)
	current, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateCheburcheckResponse{Settings: current}), nil
}

func boolSetting(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func (r rpc) DisconnectCheburcheck(ctx context.Context, _ *connect.Request[adminv1.DisconnectCheburcheckRequest]) (*connect.Response[adminv1.DisconnectCheburcheckResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	s.stop()
	values := map[string]string{settingEnabled: "0", settingPublic: "1", settingToken: "", settingHoldUntil: "", settingBackoff: strconv.Itoa(defaultBackoffMin),
		settingStatus: "", settingTokenUnknown: "0"}
	if err := s.cfg.Store.SetSettings(ctx, values); err != nil {
		s.log.Error("cheburcheck: disconnect", "err", err)
		return nil, errInternal
	}
	if err := s.cfg.Store.DeleteExtChecks(ctx, SourcePublic, SourceOwn); err != nil {
		s.log.Error("cheburcheck: delete rows", "err", err)
		return nil, errInternal
	}
	if err := s.cfg.Store.Audit(ctx, s.now(), store.AuditEntry{Actor: admin.ID, Action: "cheburcheck_disconnect", Result: "ok"}); err != nil {
		s.log.Warn("cheburcheck: audit", "err", err)
	}
	s.changed(ctx)
	current, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.DisconnectCheburcheckResponse{Settings: current}), nil
}

func (r rpc) TestCheburcheck(ctx context.Context, _ *connect.Request[adminv1.TestCheburcheckRequest]) (*connect.Response[adminv1.TestCheburcheckResponse], error) {
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
	testCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	status, err := s.client().status(testCtx)
	if err != nil {
		return nil, fail(connect.CodeUnavailable, "cheburcheck_unreachable")
	}
	return connect.NewResponse(&adminv1.TestCheburcheckResponse{Reachable: true, Version: status.Version, LastUpdate: status.LastUpdate,
		DomainCount: status.DomainCount, V4Count: status.V4Count}), nil
}
