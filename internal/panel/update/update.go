// Package update is the panel side of the node-agent updates: it reads the
// release bundle the owner signed and dropped into <data-dir>/dist, shows whether it is trusted, serves its files to
// the node agents (only through the agent mTLS endpoint, see fleet.Updates), runs owner-triggered node updates (with a
// health gate and automatic rollback) whose state lives in the database, and serves the admin
// UpdateService.
//
// The panel is a courier, not an authority: the agents verify the signature with the release key compiled into
// their own binary. Everything here that looks like a trust decision (TRUSTED) is for display and for refusing to
// start a pointless rollout.
//
// It imports neither the fleet nor the health module except for one data type: the fleet is reached through the Fleet
// interface below (implemented by *fleet.Fleet), health through Health, and cmd/mistgate wires all directions.
package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Fleet is what the module needs from the fleet module (*fleet.Fleet implements it).
type Fleet interface {
	// Live: whether the node has an agent stream now, the capabilities of its Hello, whether its state drift persists.
	Live(nodeID string) (connected bool, caps []string, drift bool)
	// UpdateAgent sends a signed manifest and waits for the CommandResult (the download is part of it).
	// FAILED_PRECONDITION when the node is not connected or did not list "update/1": nothing was sent.
	UpdateAgent(ctx context.Context, nodeID string, manifest, signature []byte, wait time.Duration) (*agentv1.CommandResult, error)
	// RollbackAgent asks the node to put its previous binary back; same preconditions.
	RollbackAgent(ctx context.Context, nodeID string, wait time.Duration) (*agentv1.CommandResult, error)
	// OnlineUsersByNode is the number of distinct users with an open session on each connected node.
	OnlineUsersByNode() map[string]int
}

// Health is what the rollout gate needs from the health module (*health.Service implements it); nil = no probe gate.
type Health interface {
	// RunChecksNow schedules an immediate synthetic round for the inbounds of a node (rate limited to one per 30 s).
	RunChecksNow(ctx context.Context, nodeID string) (scheduled, skipped int, retry time.Duration, err error)
	// NodeChecksSince counts the probeable inbounds of the node and how many have a round that ended at or after since
	// with OK / with FAILED or DEGRADED.
	NodeChecksSince(ctx context.Context, nodeID string, since time.Time) (probeable, ok, failed int)
}

// Timing of the module. The zero value is production; tests shorten what they need.
type Timing struct {
	Tick          time.Duration // worker period; default 5 s
	Rescan        time.Duration // bundle directory poll; default 60 s
	BundlePoll    time.Duration // GitHub agent bundle poll; default 10 min
	AnswerWait    time.Duration // UpdateAgent: the agent's CommandResult, download included; default 10 min
	ReconnectWait time.Duration // after the answer: the node is back with the new build; default 3 min
	GateWait      time.Duration // after the reconnect: the gate passes or fails; default 5 min
	RollbackWait  time.Duration // RollbackAgent answer; default 60 s
	InboundGrace  time.Duration // an inbound FAILED that long (two evaluations) fails the gate at once; default 30 s
	Prune         time.Duration // retention sweep period; default 6 h
}

func (t *Timing) defaults() {
	for p, d := range map[*time.Duration]time.Duration{
		&t.Tick: 5 * time.Second, &t.Rescan: time.Minute, &t.BundlePoll: 10 * time.Minute,
		&t.AnswerWait: 10 * time.Minute, &t.ReconnectWait: 3 * time.Minute,
		&t.GateWait: 5 * time.Minute, &t.RollbackWait: time.Minute, &t.InboundGrace: 30 * time.Second, &t.Prune: 6 * time.Hour,
	} {
		if *p <= 0 {
			*p = d
		}
	}
}

// Retention of finished rollouts: the newest keepRollouts are always kept, older ones go after rolloutAge.
const (
	keepRollouts = 20
	rolloutAge   = 90 * 24 * time.Hour
)

// Config configures Service.
type Config struct {
	// DataDir holds dist/ (the bundle). Empty = no bundle directory at all (tests).
	DataDir string
	// Key is the release public key of this build (buildinfo.ReleasePublicKey); nil = an unsigned build: bundles are
	// shown but never TRUSTED and no rollout can start.
	Key ed25519.PublicKey
	// PanelVersion and PanelBuilt describe this panel's own build (buildinfo.Version, buildinfo.BuiltUnix()).
	PanelVersion string
	PanelBuilt   int64
	// PanelUpdater checks official GitHub releases and schedules a checked panel binary replacement when available.
	PanelUpdater PanelUpdater
	// NodeBundleSource polls the official GitHub release for a bundle signed by this installation's release key.
	// A trusted newer bundle is installed into dist and made available for owner-triggered node updates.
	NodeBundleSource NodeBundleSource
	// StepUp is auth.Service.RequireStepUp: every change calls it first. Required.
	StepUp func(context.Context) error
	// Actor returns the admin id for audit rows and for the rollout's created_by. Default: the signed-in admin.
	Actor func(context.Context) string
	Timing
	Log *slog.Logger
	Now func() time.Time // the clock (tests)
}

// Service is the updates module.
type Service struct {
	st   *store.Store
	fl   Fleet
	hl   Health
	cfg  Config
	log  *slog.Logger
	now  func() time.Time
	dist string

	bmu    sync.Mutex // guards the bundle state and the file hash cache
	bundle *bundleState
	hashes map[string]hashEntry
	serves int // concurrent downloads, guarded by bmu

	mu                       sync.Mutex // serialises every change of a rollout: the worker pass and the admin calls
	panelInstalling          bool
	bundleSyncing            bool
	githubBundleAvailable    bool
	githubBundleManifestHash string
	panelInstallGeneration   uint64
	panelInstallTimer        *time.Timer
	runCtx                   context.Context
	wg                       sync.WaitGroup
	inflight                 map[string]bool      // node id -> a command goroutine owns its step (UpdateAgent or RollbackAgent)
	orphans                  map[string]bool      // node id -> its step was SENT when this process started (nobody waits for the answer)
	failing                  map[string]time.Time // node id -> since when a new FAILED inbound has been seen in the gate

	updMu    sync.RWMutex
	updating map[string]bool // node id -> has a SENT or GATING step (fleet.Updates.Updating)

	wake chan struct{}
}

// New builds the module and reads the bundle directory once. Call Run to start the worker, fleet.SetUpdates and
// health.AddConditionSource (Conditions) to connect it.
func New(st *store.Store, fl Fleet, hl Health, cfg Config) (*Service, error) {
	if cfg.StepUp == nil {
		return nil, errors.New("update: StepUp is required")
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
	cfg.Timing.defaults()
	s := &Service{st: st, fl: fl, hl: hl, cfg: cfg, log: cfg.Log, now: cfg.Now,
		hashes: map[string]hashEntry{}, runCtx: context.Background(),
		inflight: map[string]bool{}, orphans: map[string]bool{}, failing: map[string]time.Time{},
		updating: map[string]bool{}, wake: make(chan struct{}, 1)}
	if cfg.DataDir != "" { // without one there is no bundle (and no relative "dist" in whatever the working directory is)
		s.dist = filepath.Join(cfg.DataDir, "dist")
	}
	s.rescan()
	s.loadUpdating(context.Background())
	return s, nil
}

// Handler is the admin UpdateService (see fleet.NodeHandler for how it is mounted behind the session).
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewUpdateServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)...)
}

// Run drives the worker, the bundle poll and the retention sweep until ctx ends, then waits for the command
// goroutines it started.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
	s.recover(ctx)
	s.syncNodeBundle(ctx)
	s.processScheduledNodeUpdates(ctx)
	if s.cfg.PanelUpdater != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.cfg.PanelUpdater.Run(ctx)
		}()
	}
	tick := time.NewTicker(s.cfg.Tick)
	defer tick.Stop()
	poll := time.NewTicker(s.cfg.Rescan)
	defer poll.Stop()
	bundlePoll := time.NewTicker(s.cfg.BundlePoll)
	defer bundlePoll.Stop()
	prune := time.NewTicker(s.cfg.Prune)
	defer prune.Stop()
	s.pruneOld(ctx)
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-tick.C:
			s.tick(ctx)
			s.processScheduledNodeUpdates(ctx)
		case <-s.wake:
			s.tick(ctx)
			s.processScheduledNodeUpdates(ctx)
		case <-poll.C:
			s.rescanIfChanged()
			s.processScheduledNodeUpdates(ctx)
		case <-bundlePoll.C:
			s.syncNodeBundle(ctx)
			s.processScheduledNodeUpdates(ctx)
		case <-prune.C:
			s.pruneOld(ctx)
		}
	}
}

func (s *Service) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) pruneOld(ctx context.Context) {
	if n, err := s.st.PruneRollouts(ctx, keepRollouts, s.now().Add(-rolloutAge)); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("update: prune rollouts", "err", err)
		}
	} else if n > 0 {
		s.log.Info("update: old rollouts removed", "count", n)
	}
}

func (s *Service) audit(ctx context.Context, action string, params map[string]string) {
	s.auditAs(ctx, s.cfg.Actor(ctx), action, params)
}

func (s *Service) auditAs(ctx context.Context, actor, action string, params map[string]string) {
	b := jsonString(params)
	if err := s.st.Audit(ctx, s.now(), store.AuditEntry{Actor: actor, Action: action, Params: b, Result: "ok"}); err != nil {
		s.log.Warn("update: audit", "action", action, "err", err)
	}
}

// Updating is fleet.Updates.Updating: a SENT or GATING step of an active rollout exists for the node.
func (s *Service) Updating(nodeID string) bool {
	s.updMu.RLock()
	defer s.updMu.RUnlock()
	return s.updating[nodeID]
}

func (s *Service) setUpdating(ids map[string]bool) {
	s.updMu.Lock()
	s.updating = ids
	s.updMu.Unlock()
}

// loadUpdating rebuilds the in-memory "updating" set from the database (start, and after every change).
func (s *Service) loadUpdating(ctx context.Context) {
	ro, err := s.st.ActiveRollout(ctx)
	if err != nil {
		s.setUpdating(map[string]bool{})
		return
	}
	steps, err := s.st.RolloutSteps(ctx, ro.ID)
	if err != nil {
		return
	}
	s.setUpdating(updatingSet(steps))
}

func updatingSet(steps []store.StepRow) map[string]bool {
	out := map[string]bool{}
	for _, x := range steps {
		if x.State == store.StepSent || x.State == store.StepGating {
			out[x.NodeID] = true
		}
	}
	return out
}
