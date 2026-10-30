package app

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/blocklist"
	"github.com/mistgate/mistgate/internal/panel/bsbord"
	"github.com/mistgate/mistgate/internal/panel/cheburcheck"
	"github.com/mistgate/mistgate/internal/panel/countryprobe"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/globalping"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	settings "github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/pagepass"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/panel/telegram"
	"github.com/mistgate/mistgate/internal/panel/update"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/panel/warp"
	"github.com/mistgate/mistgate/internal/statehash"
)

func nodeUpdateReleaseKey(dataDir string, onEdge bool) (ed25519.PublicKey, error) {
	key, err := buildinfo.ReleasePublicKey()
	if dataDir != "" && !onEdge {
		return buildinfo.LoadReleasePublicKey(dataDir)
	}
	return key, err
}

func nodeUpdateConfig(dataDir string, edge, onEdge bool, key ed25519.PublicKey, stepUp func(context.Context) error,
	panelUpdater update.PanelUpdater, source update.NodeBundleSource, gate update.Health, log *slog.Logger) (update.Config, update.Health) {
	if onEdge {
		dataDir = ""
		source = nil
	}
	var gateHealth update.Health
	if !slices.Contains(edition.Unavailable(edge), edition.HealthChecks) {
		gateHealth = gate
	}
	return update.Config{DataDir: dataDir, Key: key, PanelVersion: buildinfo.Version, PanelBuilt: buildinfo.BuiltUnix(),
		Edge: edge, StepUp: stepUp, PanelUpdater: panelUpdater, NodeBundleSource: source, Log: log}, gateHealth
}

// base is what every role needs from its Config: the defaults filled in, and the few values derived from it.
type base struct {
	c       Config
	st      *store.Store
	vlt     *vault.Vault
	log     *slog.Logger
	in      InstanceConfig
	edge    bool // the edge edition: the node link is set
	limiter securitylimit.Limiter
	reg     *protocols.Registry
	subBase string // the subscription links start with this
}

func newBase(c Config) (base, error) {
	if c.Store == nil || c.Vault == nil {
		return base{}, errors.New("panel app: store and vault are required")
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	b := base{c: c, st: c.Store, vlt: c.Vault, log: c.Logger, in: c.Instance, edge: c.Remote != nil, limiter: c.Limiter, reg: builtin.Registry()}
	if b.limiter == nil {
		b.limiter = securitylimit.NewMemory(c.Clock, 0)
	}
	if b.in.PublicURL != "" {
		b.subBase = strings.TrimRight(b.in.PublicURL, "/") + strings.TrimRight(b.in.SubPrefix, "/")
	}
	return b, nil
}

// fleetConfig is the part of the fleet's configuration that every role that builds a fleet shares.
func (b base) fleetConfig(desired func(ctx context.Context, nodeID string) ([]statehash.Inbound, error), onUsage func(ctx context.Context, userIDs []string)) fleet.Config {
	agentSNIPrefix, _, _ := strings.Cut(b.in.AgentSNI, ".")
	return fleet.Config{
		AgentSNI:             b.in.AgentSNI,
		JournalSecrets:       []string{b.in.AgentSNI, agentSNIPrefix, b.in.AdminPrefix, b.in.SubPrefix, b.in.LinkPrefix},
		LinkServed:           b.in.LinkPrefix != "",
		LinkURL:              linkURL(b.in),
		Remote:               b.c.Remote,
		AfterResponse:        b.c.AfterResponse,
		Limiter:              b.limiter,
		PanelAddr:            b.c.PanelAddr,
		ExpectedAgentVersion: buildinfo.Version,
		DecoyDir:             b.c.DecoyDir,
		Desired:              desired,
		OnUsage:              onUsage,
		Log:                  b.log,
	}
}

// accessConfig is the access service's configuration, the same in every role.
func (b base) accessConfig(fl *fleet.Fleet) access.Config {
	return access.Config{SubscriptionBaseURL: b.subBase, Log: b.log, AfterResponse: b.c.AfterResponse, CheckPorts: fl.CheckPorts, CheckRelay: fl.CheckRelay}
}

// usageRecompute is fleet.Config.OnUsage: a quota overrun takes effect right after the stats batch that crossed it.
func (b base) usageRecompute(acc func() *access.Service) func(context.Context, []string) {
	return func(ctx context.Context, userIDs []string) {
		if err := acc().Recompute(ctx, userIDs); err != nil && ctx.Err() == nil {
			b.log.Warn("recompute user status after usage", "err", err)
		}
	}
}

// subParts are the pieces of the subscription surface. Everything a subscription shows comes from the settings and the
// brand of the moment of the request (a rename or an edit needs no restart): the cache is shared by the handler and the admin
// service that edits it, and the DNS module supplies the Happ routing header of each user.
type subParts struct {
	dns   *dns.Service
	cache *subsettings.Cache
	brand func(ctx context.Context) (settings.Settings, error)
	cfg   subs.Config
}

// newSubParts builds them. migrate says whether this Worker owns the data migrations of the settings (the admin and the VPS
// process do; a public Worker only reads).
func (b base) newSubParts(migrate bool) subParts {
	p := subParts{dns: dns.New(b.st)}
	if migrate {
		p.cache = subsettings.NewCache(b.st, b.log)
	} else {
		p.cache = subsettings.NewCacheWithoutMigrations(b.st, b.log)
	}
	p.brand = func(ctx context.Context) (settings.Settings, error) { return settings.Load(ctx, b.st) }
	p.cfg = subs.Config{
		Title: b.c.Title, BaseURL: b.subBase, Settings: p.cache, Brand: p.brand,
		Routing: subs.HappRouting(p.dns), Events: b.st, Log: b.log,
		Limiter: b.limiter, AfterResponse: b.c.AfterResponse, ProbeReports: b.st,
		PageKey: b.vlt.Derive(pagepass.KeyLabel), // the page password of every user: computed from the token, nothing stored
	}
	return p
}

// mux is the public subscription endpoint (and the brand logo, which the apps show next to it) that lives under the secret
// prefix. Nothing in it logs a path or a token: subs answers unknown tokens with the decoy and its errors with a bare 500 (the
// access module logs causes without the token).
func (p subParts) mux(b base, acc *access.Service) http.Handler {
	sub := http.NewServeMux()
	sub.Handle("GET /brand/logo.svg", httpserver.NewLogoHandler(b.st, b.log))
	sub.Handle("/", subs.Handler(acc, httpserver.NewDecoy(b.c.DecoyDir), p.cfg))
	return sub
}

// core is the wiring the roles share (wireCore): the services that call into each other, and the callbacks between them.
// Only the public Worker does without it: it builds the fleet and access it needs on their own.
type core struct {
	base
	authSvc *auth.Service
	tg      *telegram.Service
	fl      *fleet.Fleet
	acc     *access.Service
	cr      *countryreach.Service
	gp      *globalping.Service
	cc      *cheburcheck.Service
	bs      *bsbord.Service
	bl      *blocklist.Service
	cp      *countryprobe.Service
	hl      *health.Service
	upd     *update.Service
	wsv     *warp.Service
	desired func(ctx context.Context, nodeID string) ([]statehash.Inbound, error)
}

// wireCore builds the telegram alerts, the fleet, access, Globalping and the health module, the node-agent updates and WARP,
// with every callback between them. It is the first half of the old Build; every role but the public one starts from it.
func wireCore(c Config) (*core, error) {
	b, err := newBase(c)
	if err != nil {
		return nil, err
	}
	if c.Auth == nil {
		return nil, errors.New("panel app: auth is required")
	}
	k := &core{base: b, authSvc: c.Auth}
	st, vlt, authSvc, log, in := b.st, b.vlt, k.authSvc, b.log, b.in
	c = b.c
	edge := b.edge
	authSvc.SetEdge(edge)
	// Telegram alerts: everything below that has news reports it here; the admin links of the news carry the admin address
	// only when it is a public one (a separate loopback admin listener is not).
	alertsURL := ""
	if in.AdminListen == "" && len(in.RPOrigins) > 0 {
		alertsURL = in.AdminURL()
	}
	k.tg, err = telegram.New(telegram.Config{Store: st, Vault: vlt, StepUp: authSvc.RequireStepUp, AdminURL: alertsURL, Edge: edge,
		AfterResponse: c.AfterResponse, APIBase: c.TelegramAPIBase, Log: log})
	if err != nil {
		return nil, err
	}
	tg := k.tg
	authSvc.SetEventHook(tg.Security)
	if c.AfterResponse != nil {
		authSvc.SetEventRunner(c.AfterResponse)
	}
	var acc *access.Service // set below; the fleet calls back into it only after serving started
	var hl *health.Service  // likewise
	// One source of truth for the access rule: access.Desired (adapter NodeInbound -> statehash.Inbound).
	k.desired = func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		in, err := acc.Desired(ctx, nodeID)
		out := make([]statehash.Inbound, len(in))
		for i, x := range in {
			out[i] = statehash.Inbound(x)
		}
		if err != nil {
			return out, err
		}
		// The hidden system credential of the synthetic checker rides along with every inbound (no user, no device).
		return hl.WithProbeCreds(ctx, out), nil
	}
	fcfg := b.fleetConfig(k.desired, b.usageRecompute(func() *access.Service { return acc }))
	fcfg.OnAwgPrepareFailed = tg.AwgPrepareFailed
	// the last resort of the IPv6 check of a node; Globalping is built after the fleet, hence the late binding
	fcfg.GlobalpingTCP6 = func(ctx context.Context, addr string, port uint16) (int, int, error) {
		return k.gp.ProbeTCP6(ctx, addr, port)
	}
	k.fl, err = fleet.New(st, vlt, b.reg, fcfg)
	if err != nil {
		return nil, err
	}
	fl := k.fl
	acc, err = access.New(st, vlt, b.reg, fl, fl, b.accessConfig(fl))
	if err != nil {
		return nil, err
	}
	k.acc = acc
	// Globalping, an integration of its own: off until the owner switches it on. The country check of the health module
	// measures through it and does nothing while it is off. The country it looks from is the shared one of countryreach.
	// Its history is read by the health module, which knows which sources are on: late binding to hl.
	k.cr = countryreach.New(st, log, c.Clock, func(ctx context.Context, since time.Time, family int) ([]string, []*adminv1.NodeReachabilityHistory, error) {
		return hl.CountryHistory(ctx, since, family)
	})
	k.gp, err = globalping.New(globalping.Config{Store: st, Vault: vlt, StepUp: authSvc.RequireStepUp, Log: log, Now: c.Clock, Country: k.cr.Country,
		OnChange: func(ctx context.Context) { hl.ReconcileCountry(ctx) }})
	if err != nil {
		return nil, err
	}
	var callLease func(context.Context, string, time.Duration) bool
	if edge {
		callLease = EdgeRoundLease(st, log)
	}
	k.cc, err = cheburcheck.New(cheburcheck.Config{Store: st, Vault: vlt, StepUp: authSvc.RequireStepUp, Edge: edge,
		Log: log, Now: c.Clock, RoundLease: callLease,
		Address:  func(ctx context.Context, n store.NodeRow) (netip.Addr, bool) { return hl.NodeIPv4(ctx, n) },
		OnChange: func(ctx context.Context) { hl.ReconcileCountry(ctx) }})
	if err != nil {
		return nil, err
	}
	// Bsbord, the paid mobile-operator source: the online nodes' addresses and TLS ports are read the way the health module reads
	// them, hence the late binding to hl.
	k.bs, err = bsbord.New(bsbord.Config{Store: st, Vault: vlt, StepUp: authSvc.RequireStepUp, Log: log, Now: c.Clock, Country: k.cr.Country,
		Targets:  func(ctx context.Context) ([]health.CountryTarget, error) { return hl.CountryTargets(ctx) },
		OnChange: func(ctx context.Context) { hl.ReconcileCountry(ctx) }, Edge: edge, RoundLease: callLease})
	if err != nil {
		return nil, err
	}
	// Block lists, the second source of the country check: the panel downloads the public lists and compares its nodes' addresses
	// with them, so no address leaves it. Off until the owner switches it on; Russia only. A node's address is read the way the
	// health module reads it (the panel resolves a name itself), hence the late binding to hl.
	k.bl, err = blocklist.New(blocklist.Config{Store: st, StepUp: authSvc.RequireStepUp, Log: log, Now: c.Clock, Country: k.cr.Country,
		Address: func(ctx context.Context, n store.NodeRow) (netip.Addr, bool) { return hl.NodeIPv4(ctx, n) }})
	if err != nil {
		return nil, err
	}
	// The TSPU probe, the third source: one of the owner's own nodes inside the country tests whether SYNs to the other nodes
	// survive the national DPI box. Off until the owner switches it on; Russia only; not built for the edge edition (a run takes
	// minutes). The nodes and their ports are read the way the health module reads them, hence the late binding to hl.
	k.cp, err = countryprobe.New(countryprobe.Config{Store: st, StepUp: authSvc.RequireStepUp, Fleet: fl, Edge: edge, Log: log, Now: c.Clock, Country: k.cr.Country,
		Nodes: func(ctx context.Context) ([]countryprobe.Node, error) {
			nodes, err := hl.ProbeNodes(ctx)
			out := make([]countryprobe.Node, len(nodes))
			for i, n := range nodes {
				out[i] = countryprobe.Node(n)
			}
			return out, err
		},
		OnChange: func(ctx context.Context) { hl.ReconcileCountry(ctx) }})
	if err != nil {
		return nil, err
	}
	// Health: the synthetic checker, alerts, the node doctor and retention. The fleet hands it doctor reports and
	// returning nodes; it gives the fleet the node status and the alert badge.
	hcfg := health.Config{Edge: edge, Log: log, BlipWindow: c.HealthBlipWindow, CheckPorts: fl.CheckPortsAsSystem, CheckPortsUDP: fl.CheckPortsAsSystemUDP,
		Country: k.gp.Country,
		Lists:   k.bl.Active,
		// One read for both external sources: the evaluator asks on every pass, also while both are off.
		External: func(ctx context.Context) []health.ExternalState {
			kv, err := st.SettingValues(ctx, append(cheburcheck.StateKeys(), bsbord.StateKeys()...))
			if err != nil {
				log.Warn("health: read the external sources", "err", err)
				return nil
			}
			var out []health.ExternalState
			for _, s := range cheburcheck.StateFrom(kv).Sources() {
				out = append(out, health.ExternalState(s))
			}
			return append(out, bsbord.StateFrom(kv))
		},
		TSPU: func(ctx context.Context) health.TSPUState {
			st := k.cp.State(ctx)
			return health.TSPUState(st)
		},
		OnTransition: func(t health.Transition) {
			tg.AlertTransition(t.Alert, t.Resolved)
			if c.OnAlertTransition != nil {
				c.OnAlertTransition(t)
			}
		}}
	if edge {
		hcfg.RoundLease = EdgeRoundLease(st, log)
	}
	hl = health.New(st, vlt, b.reg, fl, hcfg)
	k.hl = hl
	acc.SetRelayHints(hl.RelayHints) // the candidates of a relay carry what the country check and the TSPU probe know
	fl.SetHealth(hl)

	// Node-agent updates: the signed bundle in <data-dir>/dist, its distribution through the agent endpoint and
	// the staged rollout. The installation's release key is kept in dataDir; a binary with another compiled-in key
	// trusts nothing until the owner runs `mistgate release trust-key`.
	// The edge (and any js build, with or without a node link) uses the compiled-in key as it is and never tries release.pub.
	onEdge := edge || runtime.GOOS == "js"
	relKey, err := nodeUpdateReleaseKey(c.DataDir, onEdge)
	switch {
	case errors.Is(err, buildinfo.ErrReleaseKeyMismatch):
		log.Error("release key mismatch: node bundles are not trusted and rollouts are off", "err", err)
	case err != nil && !errors.Is(err, buildinfo.ErrUnsignedBuild):
		log.Warn("release key is unusable, node updates are off", "err", err)
	}
	// A panel release is verified with the compiled-in key only, as the root helper does: release.pub is writable by
	// the panel's service user. Without one the Updates page says the panel cannot verify releases.
	compiledKey, _ := buildinfo.ReleasePublicKey()
	panelUpdater := update.NewGitHubPanelUpdater(update.PanelUpdateConfig{
		CurrentBuilt: buildinfo.BuiltUnix(), Key: compiledKey, DataDir: c.DataDir, NotesURL: update.GitHubReleaseNotesURL,
		ServiceUnit: c.UpdateService, Enabled: update.PanelUpdateHostSupported(),
		UseRootHelperService: update.PanelUpdateUsesRootHelperService(), Log: log,
	})
	if !onEdge {
		stars := update.NewRepoStars(authSvc.SourceURL(), log)
		if stars != nil {
			authSvc.SetSourceStars(stars.Stars)
			k.c.BackgroundStarters = append(k.c.BackgroundStarters, BackgroundJob{Name: "repo-stars", Run: stars.Run})
		}
	}
	var nodeBundleSource update.NodeBundleSource
	if len(relKey) > 0 && c.DataDir != "" && !onEdge {
		nodeBundleSource = update.NewGitHubNodeBundleSource(update.GitHubNodeBundleConfig{
			DataDir: c.DataDir, Key: relKey, Log: log,
		})
	}
	updateConfig, gateHealth := nodeUpdateConfig(c.DataDir, edge, onEdge, relKey, authSvc.RequireStepUp, panelUpdater, nodeBundleSource, hl, log)
	k.upd, err = update.New(st, fl, gateHealth, updateConfig)
	if err != nil {
		return nil, err
	}
	upd := k.upd
	fl.SetUpdates(upd)
	hl.AddConditionSource(upd.Conditions)
	tg.SetSources(telegram.Sources{
		PanelRelease: func() (string, bool) {
			s := panelUpdater.Status()
			return s.Version, s.Available && s.ErrorKey == ""
		},
		OutdatedAgents: upd.OutdatedAgents,
	})

	// WARP: the account of a node, registered at the owner's click or imported. The fleet asks it for the node's
	// WarpSpec (only for agents that list warp/1) and hands it what the node reports; it asks the fleet whether the node
	// is connected and for a recompute after every change. Register, import and delete need a fresh step-up.
	k.wsv, err = warp.New(st, vlt, fl, warp.Config{StepUp: authSvc.RequireStepUp, Log: log})
	if err != nil {
		return nil, err
	}
	fl.SetWarp(warpForFleet{k.wsv})
	return k, nil
}

// panel is the Panel of the services the core holds; a role adds what it serves.
func (k *core) panel() *Panel {
	return &Panel{
		Fleet: k.fl, Access: k.acc, Health: k.hl, Blocklist: k.bl, CountryProbe: k.cp, Cheburcheck: k.cc, Bsbord: k.bs, Update: k.upd, Warp: k.wsv, Telegram: k.tg, Store: k.st, Desired: k.desired,
		provisionKick: k.c.ProvisionKick,
	}
}
