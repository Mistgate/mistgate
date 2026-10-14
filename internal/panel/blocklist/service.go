// Package blocklist is the second source of the country check ("is the node reachable from Russia"): the public lists of
// blocked addresses, downloaded by the panel and compared with every node's IPv4 address in memory. No node address leaves the
// panel, which is what sets it apart from Globalping.
//
// A hit means "listed" (the address is written in the RKN registry, or is in a subnet that is cut wholesale), not that the
// packets are dropped; so the result is a context signal that the screens show and that never opens an alert (see
// countryreach.Combine).
//
// It is an integration of its own and off by default. Off: nothing is downloaded and the results are not shown. Disconnect:
// also forgets the downloaded lists and the stored results. Only Russia has lists; with another country selected it does nothing.
//
// The lists are refreshed every 6 hours (conditional requests) and kept in memory only; each process downloads them again after
// a restart when a check is due. The per-node result is stored (country_blocklist_check, 7 days). On the VPS Run drives it, on
// the Cloudflare edition the cron calls Tick.
package blocklist

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	settingEnabled = "blocklist.enabled"  // "1" or "0"; absent = off
	settingRetryAt = "blocklist.retry_at" // unix seconds: no download before it (set after every list failed)

	// refreshEvery: the lists change often, a node's status rarely.
	refreshEvery = 6 * time.Hour
	retryAfter   = 15 * time.Minute
	testEvery    = 10 * time.Second
	testTimeout  = 45 * time.Second
)

// Config configures Service. Store, StepUp and Address are required.
type Config struct {
	Store *store.Store
	// StepUp is auth.Service.RequireStepUp: changing the settings asks for it.
	StepUp func(context.Context) error
	// HTTPClient downloads the lists (tests use a fake server's). Default: a client with a 20 s timeout.
	HTTPClient *http.Client
	// URLs of the lists; zero fields take the defaults.
	URLs URLs
	// Country is the shared country of the country check (countryreach.Service.Country). Default RU.
	Country func(context.Context) string
	// Address is a node's IPv4 address: its address, or for a name what the panel resolves it to (health.Service.NodeIPv4).
	Address func(context.Context, store.NodeRow) (netip.Addr, bool)
	Log     *slog.Logger
	Now     func() time.Time
}

// Service is the integration.
type Service struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	runMu sync.Mutex // one check at a time; also guards the lists below
	// the downloaded lists, in memory only
	ipsum, community, cdn *list

	mu       sync.Mutex
	gen      uint64 // bumped when the integration is switched off or disconnected; a check of an older one stores nothing
	inflight map[uint64]context.CancelFunc
	next     uint64
	lastTest time.Time
}

// New builds the service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.StepUp == nil || cfg.Address == nil {
		return nil, errors.New("blocklist: Store, StepUp and Address are required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.URLs = cfg.URLs.withDefaults()
	s := &Service{cfg: cfg, log: cfg.Log, now: cfg.Now, inflight: map[uint64]context.CancelFunc{}}
	s.reset()
	return s, nil
}

// reset drops the downloaded lists. runMu must be held (or the service not yet shared).
func (s *Service) reset() {
	u := s.cfg.URLs
	s.ipsum = &list{max: maxIPSum, urls: []string{u.IPSum, u.IPSumMirror}, parse: parseCIDRs}
	s.community = &list{max: maxCommunity, urls: []string{u.Community}, parse: parseCIDRs}
	s.cdn = &list{max: maxCDN, urls: []string{u.CDN}, parse: parseCDN}
}

// Handler is the admin BlocklistService.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewBlocklistServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(4 << 10)}, opts...)...)
}

// ---------------------------------------------------------------------------------------------------
// Settings

type settings struct {
	enabled bool
	retryAt time.Time
}

func (s *Service) load(ctx context.Context) (settings, error) {
	kv, err := s.cfg.Store.SettingValues(ctx, []string{settingEnabled, settingRetryAt})
	if err != nil {
		return settings{}, err
	}
	st := settings{enabled: kv[settingEnabled] == "1"}
	if n, err := strconv.ParseInt(kv[settingRetryAt], 10, 64); err == nil && n > 0 {
		st.retryAt = time.Unix(n, 0)
	}
	return st, nil
}

// available: the lists cover the shared country (Russia only).
func (s *Service) available(ctx context.Context) bool {
	if s.cfg.Country == nil {
		return true
	}
	return strings.EqualFold(s.cfg.Country(ctx), countryreach.BlocklistCountry)
}

// Active says whether the source is on and covers the country: the health module shows its results only then. An off source
// (the default, or any trouble reading the settings) costs one read.
func (s *Service) Active(ctx context.Context) bool {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("blocklist: read settings", "err", err)
		return false
	}
	return st.enabled && s.available(ctx)
}

// ---------------------------------------------------------------------------------------------------
// The check

var errSwitchedOff = errors.New("blocklist: switched off")

// track registers a check in flight so that switching the source off can cancel it; gen is the generation to store under.
func (s *Service) track(ctx context.Context) (context.Context, uint64, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	id := s.next
	s.next++
	s.inflight[id] = cancel
	return ctx, s.gen, func() {
		s.mu.Lock()
		delete(s.inflight, id)
		s.mu.Unlock()
		cancel()
	}
}

// stop cancels every check in flight and keeps a check that started earlier from storing anything.
func (s *Service) stop() {
	s.mu.Lock()
	s.gen++
	for _, cancel := range s.inflight {
		cancel()
	}
	s.mu.Unlock()
}

// forget stops everything and drops the downloaded lists (Disconnect): it waits for a cancelled check to end.
func (s *Service) forget() {
	s.stop()
	s.runMu.Lock()
	s.reset()
	s.runMu.Unlock()
}

// release drops the lists when no check is running (the source is off); a running one is left for the next call.
func (s *Service) release() {
	if s.runMu.TryLock() {
		if s.ipsum.loaded || s.community.loaded || s.cdn.loaded {
			s.reset()
		}
		s.runMu.Unlock()
	}
}

// activeNodes are the nodes that are not retired.
func (s *Service) activeNodes(ctx context.Context) ([]store.NodeRow, error) {
	all, err := s.cfg.Store.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, n := range all {
		if n.State == "active" {
			out = append(out, n)
		}
	}
	return out, nil
}

// refreshLists brings the lists up to date: every list that is older than refreshEvery (or all of them, with force) is asked
// with a conditional request. A list that fails keeps its old copy; only a missing registry list (ipsum) fails the check.
func (s *Service) refreshLists(ctx context.Context, force bool) error {
	now := s.now()
	for _, l := range []struct {
		name string
		l    *list
	}{{ListIPSum, s.ipsum}, {ListCommunity, s.community}, {"cdn", s.cdn}} {
		if !force && l.l.loaded && now.Sub(l.l.checked) < refreshEvery {
			continue
		}
		if _, err := l.l.refresh(ctx, s.cfg.HTTPClient, now); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if l.l == s.ipsum && !l.l.loaded {
				s.log.Warn("blocklist: the registry list could not be downloaded", "list", l.name, "err", err)
				return errUnreachable
			}
			s.log.Warn("blocklist: a list could not be refreshed; the copy in memory stays", "list", l.name, "err", err)
		}
	}
	return nil
}

var errUnreachable = errors.New("blocklist: no list could be downloaded")

// listsDate is the date of the newest registry list in use.
func (s *Service) listsDate() time.Time {
	d := s.ipsum.date
	if s.community.loaded && s.community.date.After(d) {
		d = s.community.date
	}
	return d
}

// result is what a check found.
type result struct {
	listsAt                   time.Time
	prefixes, cdn             int
	nodesChecked, nodesListed int
	mirror                    bool
}

// check refreshes the lists, looks every active node's address up and stores the rows. runMu must be held.
func (s *Service) check(ctx context.Context, force bool) (result, error) {
	ctx, gen, done := s.track(ctx)
	defer done()
	nodes, err := s.activeNodes(ctx)
	if err != nil {
		return result{}, err
	}
	if err := s.refreshLists(ctx, force); err != nil {
		return result{}, err
	}
	now, listsAt := s.now(), s.listsDate()
	res := result{listsAt: listsAt, prefixes: len(s.ipsum.set.e) + len(s.community.set.e), cdn: len(s.cdn.set.e),
		mirror: s.ipsum.url == s.cfg.URLs.IPSumMirror}
	rows := make([]store.BlocklistCheck, 0, len(nodes))
	for _, n := range nodes {
		row := store.BlocklistCheck{NodeID: n.ID, At: now, ListsAt: listsAt}
		// A node without an IPv4 address (a name that does not resolve, or IPv6 only) is stored as "not listed": the
		// screens read that as "nothing to say", the same as no row, and the node is not asked again before the next round.
		if a, ok := s.cfg.Address(ctx, n); ok {
			m := match(s.ipsum, s.community, s.cdn, a)
			row.InRKN, row.ListedBy, row.CDNProvider, row.CDNRegion = m.inRKN, strings.Join(m.listedBy, ","), m.cdnProvider, m.cdnRegion
			if m.subnet.IsValid() {
				row.RKNSubnet = m.subnet.String()
			}
			if m.inRKN {
				res.nodesListed++
			}
		}
		rows = append(rows, row)
	}
	if err := ctx.Err(); err != nil {
		return result{}, err
	}
	res.nodesChecked = len(rows)
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return result{}, errSwitchedOff
	}
	return res, s.cfg.Store.InsertBlocklistChecks(ctx, rows)
}

// due says whether a node has no result yet or its newest one is older than the refresh period.
func due(nodes []store.NodeRow, rows []store.BlocklistCheck, now time.Time) bool {
	at := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		at[r.NodeID] = r.At
	}
	for _, n := range nodes {
		if t, ok := at[n.ID]; !ok || now.Sub(t) >= refreshEvery {
			return true
		}
	}
	return false
}

// Tick runs a check when one is due and reports whether it ran. The VPS loop and the edge cron both call it; it is safe to
// call often. Nothing happens while the source is off or the country is not Russia: no download, no request of any kind, and
// whatever lists are held are dropped. A node that has no result yet (or whose result is 6 hours old) makes a check due, so a
// node added a minute ago is looked up at once without asking the list servers again.
func (s *Service) Tick(ctx context.Context) bool {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("blocklist: read settings", "err", err)
		return false
	}
	if !st.enabled || !s.available(ctx) {
		s.release()
		return false
	}
	now := s.now()
	if now.Before(st.retryAt) {
		return false
	}
	nodes, err := s.activeNodes(ctx)
	if err != nil {
		s.log.Warn("blocklist: read nodes", "err", err)
		return false
	}
	rows, err := s.cfg.Store.RecentBlocklistChecks(ctx)
	if err != nil {
		s.log.Warn("blocklist: read results", "err", err)
		return false
	}
	if !due(nodes, rows, now) || !s.runMu.TryLock() {
		return false
	}
	defer s.runMu.Unlock()
	if _, err := s.check(ctx, false); err != nil {
		if ctx.Err() == nil && !errors.Is(err, errSwitchedOff) {
			s.log.Warn("blocklist: check failed; trying again later", "err", err)
			if serr := s.cfg.Store.SetSettings(ctx, map[string]string{settingRetryAt: strconv.FormatInt(now.Add(retryAfter).Unix(), 10)}); serr != nil {
				s.log.Warn("blocklist: save retry time", "err", serr)
			}
		}
		return false
	}
	if !st.retryAt.IsZero() {
		_ = s.cfg.Store.SetSettings(ctx, map[string]string{settingRetryAt: ""})
	}
	return true
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

// current is the settings as an answer. The time of the last check and the date of its lists come from the stored results, so
// they are the same on every process; an off source shows none.
func (s *Service) current(ctx context.Context) (*adminv1.BlocklistSettings, error) {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Error("blocklist: read settings", "err", err)
		return nil, errInternal
	}
	m := &adminv1.BlocklistSettings{Enabled: st.enabled, Available: s.available(ctx)}
	if !st.enabled {
		return m, nil
	}
	rows, err := s.cfg.Store.RecentBlocklistChecks(ctx)
	if err != nil {
		s.log.Error("blocklist: read results", "err", err)
		return nil, errInternal
	}
	for _, r := range rows {
		m.CheckedUnix, m.ListsUnix = max(m.CheckedUnix, r.At.Unix()), max(m.ListsUnix, r.ListsAt.Unix())
	}
	return m, nil
}

func (r rpc) GetBlocklist(ctx context.Context, _ *connect.Request[adminv1.GetBlocklistRequest]) (*connect.Response[adminv1.GetBlocklistResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	cur, err := r.s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetBlocklistResponse{Settings: cur}), nil
}

func (r rpc) UpdateBlocklist(ctx context.Context, req *connect.Request[adminv1.UpdateBlocklistRequest]) (*connect.Response[adminv1.UpdateBlocklistResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if e := req.Msg.Enabled; e != nil {
		v := "0"
		if *e {
			v = "1"
		}
		kv := map[string]string{settingEnabled: v}
		if *e {
			kv[settingRetryAt] = "" // switching it on again tries at once
		}
		if err := s.cfg.Store.SetSettings(ctx, kv); err != nil {
			s.log.Error("blocklist: save settings", "err", err)
			return nil, errInternal
		}
		if !*e {
			s.stop()
			s.release()
		}
	}
	s.audit(ctx, admin, "blocklist_update", "")
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateBlocklistResponse{Settings: cur}), nil
}

func (r rpc) DisconnectBlocklist(ctx context.Context, _ *connect.Request[adminv1.DisconnectBlocklistRequest]) (*connect.Response[adminv1.DisconnectBlocklistResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	s.stop() // first: nothing new may be stored while the settings change
	if err := s.cfg.Store.SetSettings(ctx, map[string]string{settingEnabled: "0", settingRetryAt: ""}); err != nil {
		s.log.Error("blocklist: disconnect", "err", err)
		return nil, errInternal
	}
	s.forget()
	if err := s.cfg.Store.DeleteBlocklistChecks(ctx); err != nil {
		s.log.Error("blocklist: forget the results", "err", err)
		return nil, errInternal
	}
	s.audit(ctx, admin, "blocklist_disconnect", "")
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.DisconnectBlocklistResponse{Settings: cur}), nil
}

// TestBlocklist downloads the lists now (conditional requests) and checks every node, storing the result. It sends no address.
func (r rpc) TestBlocklist(ctx context.Context, _ *connect.Request[adminv1.TestBlocklistRequest]) (*connect.Response[adminv1.TestBlocklistResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	s := r.s
	st, err := s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	switch {
	case !st.enabled:
		return nil, fail(connect.CodeFailedPrecondition, "blocklist_off")
	case !s.available(ctx):
		return nil, fail(connect.CodeFailedPrecondition, "blocklist_unavailable_for_country")
	}
	s.mu.Lock()
	wait := s.lastTest.Add(testEvery).Sub(s.now())
	if wait <= 0 {
		s.lastTest = s.now()
	}
	s.mu.Unlock()
	if wait > 0 {
		return nil, fail(connect.CodeResourceExhausted, "tested a moment ago: try again in "+strconv.Itoa(int(wait.Seconds())+1)+" s")
	}
	cctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	s.runMu.Lock()
	defer s.runMu.Unlock()
	res, err := s.check(cctx, true)
	switch {
	case errors.Is(err, errSwitchedOff):
		return nil, fail(connect.CodeFailedPrecondition, "blocklist_off")
	case errors.Is(err, errUnreachable), errors.Is(err, context.DeadlineExceeded):
		return nil, fail(connect.CodeUnavailable, "blocklist_unreachable")
	case err != nil:
		s.log.Error("blocklist: test", "err", err)
		return nil, errInternal
	}
	return connect.NewResponse(&adminv1.TestBlocklistResponse{ListsUnix: res.listsAt.Unix(), Prefixes: uint32(res.prefixes), CdnRanges: uint32(res.cdn),
		NodesChecked: uint32(res.nodesChecked), NodesListed: uint32(res.nodesListed), MirrorUsed: res.mirror}), nil
}

// audit records who did what; params is a JSON object.
func (s *Service) audit(ctx context.Context, admin store.Admin, action, params string) {
	if err := s.cfg.Store.Audit(ctx, s.now(), store.AuditEntry{Actor: admin.ID, Action: action, Params: params, Result: "ok"}); err != nil {
		s.log.Warn("blocklist: audit", "action", action, "err", err)
	}
}
