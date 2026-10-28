// Package health is the panel's health module: the synthetic checker (the panel
// dials every inbound as a client and probes through the tunnel), the alerts derived from node state, doctor
// reports and check results with their lifecycle, the storage and relay of the node doctor, and the retention
// jobs of all of it. It serves the admin HealthService and implements the small fleet.Health seam.
//
// It imports neither the fleet nor the access module: the fleet is reached through the Fleet interface below,
// and cmd/mistgate wires both directions.
package health

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// Fleet is what the module needs from the fleet module (*fleet.Fleet implements it).
type Fleet interface {
	// Live reads the durable view once for a pass; an error must stop that pass before alerts are reconciled.
	Live(ctx context.Context) ([]store.NodeLiveRow, error)
	// NodeLive reads one node's durable projection for a node-scoped check.
	NodeLive(ctx context.Context, nodeID string) (store.NodeLiveRow, error)
	// NodeStatusWithLive derives a node's status from its projection and the summaries (Service.NodeHealthSummaries) the
	// caller read once for the request.
	NodeStatusWithLive(ctx context.Context, n store.NodeRow, live store.NodeLiveRow, summaries map[string]store.NodeHealthSummary) adminv1.NodeStatus
	// RunDoctor asks a node to run checks (nil = all) and waits for the report. FAILED_PRECONDITION: not
	// connected, or an agent without "doctor/1".
	RunDoctor(ctx context.Context, nodeID string, checks []string, wait time.Duration) (*agentv1.DoctorReport, error)
	// ApplyFix sends a fix (or its dry run) and waits for the CommandResult; same preconditions.
	ApplyFix(ctx context.Context, nodeID, fixID string, dryRun bool, params map[string]string) (*agentv1.CommandResult, error)
	// CheckRelay performs the bounded leg/path diagnosis that refines the stage of a failed client-eye relay check.
	CheckRelay(ctx context.Context, relayID string, stages ...string) (store.RelayCheck, error)
	// OnlineByInbound counts the open sessions of every inbound of the connected nodes (the restart dialogs say how
	// many connections drop).
	OnlineByInbound(ctx context.Context) map[string]int
}

func (s *Service) liveNode(ctx context.Context, nodeID string) (store.NodeLiveRow, error) {
	return s.fl.NodeLive(ctx, nodeID)
}

// Timing of the module. Everything a test needs to shorten is a Config field; these are the defaults.
const (
	defaultInterval         = 300 * time.Second // health.check_interval_s
	minInterval             = 60 * time.Second
	intervalSetting         = "health.check_interval_s"
	defaultRelayDownloadURL = "https://speed.cloudflare.com/__down?bytes=65536"

	evalEvery     = 10 * time.Second // the evaluator also runs after every round and every doctor report
	reopenWindow  = time.Hour        // a resolved alert that fires again within this re-opens
	blipWindow    = 10 * time.Minute // same as fleet.blipWindow: a shorter silence is a blip, not an outage
	doctorStale   = 25 * time.Minute // a report older than 2.5 periods is stale (health.proto NodeDoctor)
	fixResolveFor = 5 * time.Minute  // a resolve this soon after ApplyFix is "fix_applied"
	failStreakMin = 2                // failed rounds before a synthetic alert opens

	sampleKeep = 25 * time.Hour
	dailyKeep  = 90 * 24 * time.Hour
	alertKeep  = 90 * 24 * time.Hour
	infoEvents = 90 * 24 * time.Hour  // 00002: events of severity info
	warnEvents = 400 * 24 * time.Hour // warning and error
)

// Config configures Service. The zero value is production: only Actor and Log are usually set.
type Config struct {
	// Edge runs alert evaluation from EdgeTick and leaves one-off doctor actions to that evaluator in the jobs isolate.
	Edge bool
	// Dialers maps a protocol plugin id to its tunnel client. Default: hysteria2, awg and vless. A protocol without one is
	// SKIPPED (client_unsupported).
	Dialers map[string]Dialer
	// Probe URLs of the client-eye checks; hostnames go into the tunnel unresolved. A relay also downloads the bounded
	// ProbeDownloadURL body to detect transfers that stall after the handshake.
	Probe204URL, ProbeTraceURL string
	ProbeDownloadURL           string
	// ProbeTLS is the TLS configuration of the probes (tests with their own CA). Default: system roots.
	ProbeTLS *tls.Config

	HandshakeTimeout time.Duration // QUIC handshake + auth; default 8 s
	ProbeTimeout     time.Duration // one probe request; default 10 s
	RetryDelay       time.Duration // between the attempt and its retry inside a round; default 15 s
	FailedEvery      time.Duration // probe period while an inbound fails; default 60 s
	// Interval between rounds of one inbound. 0 = the setting health.check_interval_s, else 300 s.
	Interval time.Duration
	Workers  int // concurrent rounds; default 4
	// Tick is how often the scheduler looks for inbounds that are due; default 1 s.
	Tick time.Duration
	// SnapshotTTL is how long the list of probe targets is reused; default 5 s.
	SnapshotTTL time.Duration
	// StartGrace after Run starts: no NODE_DOWN is opened, agents are still reconnecting after a panel
	// restart. Default 90 s.
	StartGrace time.Duration
	// BlipWindow: a silence shorter than this is a blip, not an outage. Default 10 min (fleet.blipWindow);
	// only the end-to-end test shortens it (MISTGATE_HEALTH_BLIP_WINDOW).
	BlipWindow time.Duration

	// Actor returns the admin id for audit rows. Default: the signed-in admin from the auth interceptor.
	Actor func(context.Context) string
	// CheckPorts runs the fleet's bounded UDP delivery check. When set, the VPS scheduler uses it every six hours.
	CheckPorts func(context.Context, string, []uint16) ([]store.PortCheck, string, string)
	// CheckPortsUDP is the same without the TCP half, for the confirming second sample after UDP loss. nil = CheckPorts.
	CheckPortsUDP func(context.Context, string, []uint16) ([]store.PortCheck, string, string)
	// Country returns the owner's choice for the country check and the source that measures it (the Globalping integration),
	// read on every use so that a change applies at once. nil, or Enabled false, or a nil source: the check is off.
	Country func(context.Context) (CountryConfig, CountrySource)
	// Lists says whether the block-list source of the country check (internal/panel/blocklist) is on and covers the country,
	// read on every use. When it is, GetChecks adds what the lists say of each node to the verdict (countryreach.Combine).
	// nil: it is off. The lists never open an alert.
	Lists func(context.Context) bool
	// TSPU says whether the TSPU probe source (internal/panel/countryprobe) is on and covers the country, which probe node's rows to
	// read and how old a row may be, read on every use. When it is, the probe node's verdicts join the live round in the combined
	// verdict (countryreach.Combine) and in the COUNTRY_BLOCKED alert. nil: it is off.
	TSPU func(context.Context) TSPUState
	// External says which external source rows are enabled and how long their newest row stays fresh. nil is no external source.
	External func(context.Context) []ExternalState
	// Resolve looks a node's name up on the panel's side, for the country check. Default: the system resolver (DNS over
	// HTTPS on the Cloudflare edition, which has none).
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// CountryRoundTimeout bounds one whole country round. Default 60 s.
	CountryRoundTimeout time.Duration
	// RoundLease, when set, must grant a due edge pass before it runs, for ttl. The Cloudflare edition takes a D1 lease
	// (store.TakeLease): cron ticks can overlap in two isolates, each with its own local mutex. Nil on the VPS.
	RoundLease func(ctx context.Context, name string, ttl time.Duration) bool
	// OnTransition is called after an alert was opened or resolved (notifications attach here).
	OnTransition func(Transition)
	Log          *slog.Logger
	Now          func() time.Time // the clock (tests)
}

// ExternalState configures one external source for the combined country verdict.
type ExternalState struct {
	Source string
	On     bool
	MaxAge time.Duration
}

// Transition is one alert changing state, for the notification hook.
type Transition struct {
	Alert    store.HealthAlert
	Resolved bool // false: opened (or re-opened)
}

// Service is the health module.
type Service struct {
	st  *store.Store
	v   *vault.Vault
	reg *protocols.Registry
	fl  Fleet
	cfg Config
	log *slog.Logger
	now func() time.Time

	startedAt time.Time

	credMu sync.Mutex
	creds  map[string]store.ProbeCredRow // inbound id -> credential (cache of the table)

	snapMu sync.Mutex
	snap   *snapshot

	cellMu sync.Mutex
	cells  map[string]*cell // inbound id -> last result and fail streak

	schedMu sync.Mutex
	sched   map[string]*schedule // inbound id -> when it is probed next
	work    chan string          // inbound ids to probe now
	queued  map[string]bool

	countryMu        sync.Mutex // one country round at a time
	countryStretched bool       // the stretched interval was logged
	// entry6 says whether a node's IPv6 entry for clients is on (step 7 of the IPv6 plan sets it); nil: no node has one, so no
	// IPv6 country alert is ever opened.
	entry6 func(store.NodeRow) bool

	evalMu sync.Mutex // serialises evaluate
	kick   chan struct{}

	portCheckRun      chan struct{} // one scheduled CheckPorts run at a time
	portCheckAttempts map[string]int64

	nhMu             sync.RWMutex
	nodeHealth       map[string]nodeHealth // computed by the last evaluation
	nodeHealthJSON   string                // last edge summary stored by this isolate; evaluator passes are serialised by evalMu
	nodeHealthStored bool

	repMu   sync.Mutex
	waiters map[string][]chan struct{} // node id -> channels closed at the next doctor report

	ext condSources // conditions other modules raise (updatehooks.go)

	fixMu     sync.Mutex
	plans     map[string]fixPlan // plan id -> plan (ApplyFix step 1)
	fixBusy   map[string]bool    // node id -> a fix is running
	recentFix map[string]time.Time
}

// New builds the module. Call Run to start the background work and SetHealth of the fleet with the service.
func New(st *store.Store, v *vault.Vault, reg *protocols.Registry, fl Fleet, cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Dialers == nil {
		cfg.Dialers = map[string]Dialer{"hysteria2": dialHysteria2, "awg": dialAWG, "vless": dialVLESS}
	}
	if cfg.Probe204URL == "" {
		cfg.Probe204URL = "https://www.gstatic.com/generate_204"
	}
	if cfg.ProbeTraceURL == "" {
		cfg.ProbeTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	}
	if cfg.ProbeDownloadURL == "" {
		cfg.ProbeDownloadURL = defaultRelayDownloadURL
	}
	for p, d := range map[*time.Duration]time.Duration{
		&cfg.HandshakeTimeout: 8 * time.Second, &cfg.ProbeTimeout: 10 * time.Second, &cfg.RetryDelay: 15 * time.Second,
		&cfg.FailedEvery: 60 * time.Second, &cfg.SnapshotTTL: 5 * time.Second, &cfg.StartGrace: 90 * time.Second, &cfg.Tick: time.Second, &cfg.BlipWindow: blipWindow,
	} {
		if *p <= 0 {
			*p = d
		}
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.Resolve == nil {
		cfg.Resolve = defaultResolve
	}
	if cfg.Actor == nil {
		cfg.Actor = func(ctx context.Context) string {
			if a, ok := auth.AdminFrom(ctx); ok {
				return a.ID
			}
			return "unknown"
		}
	}
	return &Service{
		st: st, v: v, reg: reg, fl: fl, cfg: cfg, log: cfg.Log, now: cfg.Now, startedAt: cfg.Now(),
		creds: map[string]store.ProbeCredRow{}, cells: map[string]*cell{}, sched: map[string]*schedule{},
		work: make(chan string, 1024), queued: map[string]bool{}, kick: make(chan struct{}, 1),
		portCheckRun: make(chan struct{}, 1), portCheckAttempts: map[string]int64{},
		nodeHealth: map[string]nodeHealth{}, waiters: map[string][]chan struct{}{},
		plans: map[string]fixPlan{}, fixBusy: map[string]bool{}, recentFix: map[string]time.Time{},
	}
}

// Handler is the admin HealthService (see fleet.NodeHandler for how it is mounted behind the session).
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewHealthServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)...)
}

// Run drives the checker, the evaluator and the retention sweeps until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.startedAt = s.now()
	var wg sync.WaitGroup
	for _, f := range []func(context.Context){s.runChecker, s.runEvaluator, s.runRetention, s.runPortRechecks, s.runCountryChecks} {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	wg.Wait()
}

// evaluateSoon asks the evaluator for a pass without waiting for it.
func (s *Service) evaluateSoon() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// interval is the period between rounds of one inbound.
func (s *Service) interval(ctx context.Context) time.Duration {
	if s.cfg.Interval > 0 {
		return s.cfg.Interval
	}
	if v, err := s.st.Setting(ctx, intervalSetting); err == nil {
		if d, err := time.ParseDuration(v + "s"); err == nil && d >= minInterval {
			return d
		}
	}
	return defaultInterval
}

func (s *Service) audit(ctx context.Context, action string, params map[string]string) {
	b := jsonString(params)
	if err := s.st.Audit(ctx, s.now(), store.AuditEntry{Actor: s.cfg.Actor(ctx), Action: action, Params: b, Result: "ok"}); err != nil {
		s.log.Warn("audit", "action", action, "err", err)
	}
}

var errInternal = connect.NewError(connect.CodeInternal, errors.New("internal error"))

func (s *Service) internal(what string, err error) error {
	s.log.Error("health: "+what, "err", err)
	return errInternal
}
