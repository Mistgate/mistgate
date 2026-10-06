package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/backup"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	settings "github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/pagepass"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	nodeprovision "github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/panel/telegram"
	"github.com/mistgate/mistgate/internal/panel/update"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/panel/warp"
	"github.com/mistgate/mistgate/internal/statehash"
)

// panelOpts is what newPanel needs besides the open store, vault and auth service.
type panelOpts struct {
	in            instance
	decoyDir      string
	panelAddr     string // host:port agents dial (goes into the install command); "" = not configured
	dataDir       string // holds dist/, the release bundle of the node-agent updates
	updateService string // systemd unit restarted by the GitHub panel updater
	masterKey     []byte // the effective key (including systemd credentials) used by this panel process
	title         string // subscription title apps show (the brand name)
}

// panel is the assembled panel: the HTTP server and the modules whose background loops run starts.
type panel struct {
	srv       *httpserver.Server
	fleet     *fleet.Fleet
	access    *access.Service
	health    *health.Service
	update    *update.Service
	warp      *warp.Service
	provision *nodeprovision.Service
	backup    *backup.Service
	telegram  *telegram.Service
	st        *store.Store // for the sweep of MCP plans
	log       *slog.Logger
	// desired is the desired-state source of the fleet (tests read it).
	desired func(ctx context.Context, nodeID string) ([]statehash.Inbound, error)
}

// newPanel wires the modules: fleet and access know each other only through the small
// interfaces and callbacks below, this is the one place that connects them.
func newPanel(st *store.Store, vlt *vault.Vault, authSvc *auth.Service, o panelOpts, log *slog.Logger) (*panel, error) {
	// A configured installation gets this prefix in setup/ensureSecrets. Keep direct in-process panel fixtures usable too.
	if o.in.LinkPrefix == "" {
		o.in.LinkPrefix = newSecretPrefix()
	}
	reg := builtin.Registry()
	// Telegram alerts: everything below that has news reports it here; the admin links of the news carry the admin address
	// only when it is a public one (a separate loopback admin listener is not).
	alertsURL := ""
	if o.in.AdminListen == "" && len(o.in.RPOrigins) > 0 {
		alertsURL = o.in.adminURL()
	}
	tg, err := telegram.New(telegram.Config{Store: st, Vault: vlt, StepUp: authSvc.RequireStepUp, AdminURL: alertsURL, Log: log})
	if err != nil {
		return nil, err
	}
	authSvc.SetEventHook(tg.Security)
	var acc *access.Service // set below; the fleet calls back into it only after serving started
	var hl *health.Service  // likewise
	// One source of truth for the access rule: access.Desired (adapter NodeInbound -> statehash.Inbound).
	desired := func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
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
	fl, err := fleet.New(st, vlt, reg, fleet.Config{
		AgentSNI:             o.in.AgentSNI,
		PanelAddr:            o.panelAddr,
		ExpectedAgentVersion: buildinfo.Version,
		Desired:              desired,
		// A quota overrun takes effect right after the stats batch that crossed it.
		OnUsage: func(ctx context.Context, userIDs []string) {
			if err := acc.Recompute(ctx, userIDs); err != nil && ctx.Err() == nil {
				log.Warn("recompute user status after usage", "err", err)
			}
		},
		OnAwgPrepareFailed: tg.AwgPrepareFailed,
		Log:                log,
	})
	if err != nil {
		return nil, err
	}
	subBase := ""
	if o.in.PublicURL != "" {
		subBase = strings.TrimRight(o.in.PublicURL, "/") + strings.TrimRight(o.in.SubPrefix, "/")
	}
	acc, err = access.New(st, vlt, reg, fl, fl, access.Config{SubscriptionBaseURL: subBase, Log: log})
	if err != nil {
		return nil, err
	}
	// Health: the synthetic checker, alerts, the node doctor and retention. The fleet hands it doctor reports and
	// returning nodes; it gives the fleet the node status and the alert badge.
	hcfg := health.Config{Log: log, OnTransition: func(t health.Transition) { tg.AlertTransition(t.Alert, t.Resolved) }}
	// Test hook: scripts/e2e-wsl.sh cannot wait ten minutes for NODE_DOWN.
	if d, err := time.ParseDuration(os.Getenv("MISTGATE_HEALTH_BLIP_WINDOW")); err == nil && d > 0 {
		hcfg.BlipWindow = d
	}
	hl = health.New(st, vlt, reg, fl, hcfg)
	fl.SetHealth(hl)

	// Node-agent updates: the signed bundle in <data-dir>/dist, its distribution through the agent endpoint and
	// the staged rollout. The installation's release key is kept in dataDir; a binary with another compiled-in key
	// trusts nothing until the owner runs `mistgate release trust-key`.
	relKey, err := buildinfo.ReleasePublicKey()
	if o.dataDir != "" {
		relKey, err = buildinfo.LoadReleasePublicKey(o.dataDir)
	}
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
		CurrentBuilt: buildinfo.BuiltUnix(), Key: compiledKey, DataDir: o.dataDir,
		ServiceUnit: o.updateService, Enabled: update.PanelUpdateHostSupported(),
		UseRootHelperService: update.PanelUpdateUsesRootHelperService(), Log: log,
	})
	var nodeBundleSource update.NodeBundleSource
	if len(relKey) > 0 && o.dataDir != "" {
		nodeBundleSource = update.NewGitHubNodeBundleSource(update.GitHubNodeBundleConfig{
			DataDir: o.dataDir, Key: relKey, Log: log,
		})
	}
	upd, err := update.New(st, fl, hl, update.Config{
		DataDir: o.dataDir, Key: relKey, PanelVersion: buildinfo.Version, PanelBuilt: buildinfo.BuiltUnix(),
		StepUp: authSvc.RequireStepUp, PanelUpdater: panelUpdater, NodeBundleSource: nodeBundleSource, Log: log,
	})
	if err != nil {
		return nil, err
	}
	fl.SetUpdates(upd)
	hl.AddConditionSource(upd.Conditions)
	tg.SetSources(telegram.Sources{
		PanelRelease: func() (string, bool) {
			s := panelUpdater.Status()
			return s.Version, s.Available && s.ErrorKey == ""
		},
		OutdatedAgents: upd.OutdatedAgents,
	})
	prov, err := nodeprovision.NewService(st, vlt, nodeprovision.Config{
		StepUp: authSvc.RequireStepUp, Nodes: fl, Binaries: upd,
		PanelAddr: o.panelAddr, AgentSNI: o.in.AgentSNI, Log: log,
	})
	if err != nil {
		return nil, err
	}
	bkp, err := backup.New(backup.Config{
		Store: st, Vault: vlt, DataDir: o.dataDir, MasterKey: o.masterKey,
		StepUp: authSvc.RequireStepUp, OnResult: backupResult(tg), Log: log,
	})
	if err != nil {
		return nil, err
	}

	// WARP: the account of a node, registered at the owner's click or imported. The fleet asks it for the node's
	// WarpSpec (only for agents that list warp/1) and hands it what the node reports; it asks the fleet whether the node
	// is connected and for a recompute after every change. Register, import and delete need a fresh step-up.
	wsv, err := warp.New(st, vlt, fl, warp.Config{StepUp: authSvc.RequireStepUp, Log: log})
	if err != nil {
		return nil, err
	}
	fl.SetWarp(warpForFleet{wsv})

	// The public subscription endpoint (and the brand logo, which the apps show next to it) lives under
	// the secret prefix. Nothing in it logs a path or a token: subs answers unknown tokens with the decoy
	// and its errors with a bare 500 (the access module logs causes without the token).
	//
	// Everything a subscription shows comes from the settings and the brand of the moment of the request
	// (a rename or an edit needs no restart): subsettings.Cache is shared by the handler and the admin
	// service that edits it, and the DNS module supplies the Happ routing header of each user.
	dnsSvc := dns.New(st)
	cache := subsettings.NewCache(st, log)
	brand := func(ctx context.Context) (settings.Settings, error) { return settings.Load(ctx, st) }
	subCfg := subs.Config{
		Title: o.title, BaseURL: subBase, Settings: cache, Brand: brand,
		Routing: subs.HappRouting(dnsSvc), Events: st, Log: log,
		PageKey: vlt.Derive(pagepass.KeyLabel), // the page password of every user: computed from the token, nothing stored
	}
	sub := http.NewServeMux()
	sub.Handle("GET /brand/logo.svg", httpserver.NewLogoHandler(st, log))
	sub.Handle("/", subs.Handler(acc, httpserver.NewDecoy(o.decoyDir), subCfg))
	subSvc := subs.NewService(st, cache, reg, brand, dnsSvc, log, fl)

	var admin []httpserver.AdminHandler
	for _, h := range []func() (string, http.Handler){
		func() (string, http.Handler) { return fl.NodeHandler() },
		func() (string, http.Handler) { return fl.FleetHandler() },
		acc.ProfileHandler, acc.UserHandler, acc.GroupHandler, acc.DeviceHandler, acc.AwgHandler, subSvc.Handler, dnsSvc.Handler,
		func() (string, http.Handler) { return wsv.Handler() },
		func() (string, http.Handler) { return prov.Handler() },
		func() (string, http.Handler) { return hl.Handler() },
		func() (string, http.Handler) { return upd.Handler() },
		func() (string, http.Handler) { return bkp.Handler() },
		func() (string, http.Handler) { return tg.Handler() },
	} {
		path, handler := h()
		admin = append(admin, httpserver.AdminHandler{Path: path, Handler: handler})
	}
	// API tokens and the owner's approvals, then the MCP endpoint over the same admin API.
	admin = append(admin, integrationHandlers(authSvc)...)
	// The Instance service is built into httpserver (it is part of the sign-in flow); Auth likewise.
	srv, err := httpserver.New(httpserver.Config{
		AdminHost:        o.in.AdminHost,
		AdminPrefix:      o.in.muxPrefix(),
		DecoyDir:         o.decoyDir,
		TrustedOrigins:   o.in.RPOrigins,
		Log:              log,
		AdminHandlers:    admin,
		AdminPages:       []httpserver.AdminPage{{Path: nodeprovision.AdminPagePath, Handler: prov.PageHandler()}},
		MCP:              mcpEndpoint(authSvc, st, log, tg.PlanWaiting),
		PublicMounts:     map[string]http.Handler{o.in.SubPrefix: sub},
		AgentSNI:         o.in.AgentSNI,
		AgentTLS:         fl.AgentTLSConfig,
		AgentHandler:     fl.AgentHandler(),
		AgentLinkPrefix:  o.in.LinkPrefix,
		AgentLinkHandler: fl.LinkHandler(),
		// Settings -> Domains shows the owner both addresses, read-only.
		AdminURL: o.in.adminURL(), SubscriptionBase: subBase,
		// The admin's framed preview of the public user page (session and read role are checked by the server).
		UserPagePreview: subs.PreviewHandler(acc, subCfg),
	}, authSvc, st)
	if err != nil {
		return nil, err
	}
	return &panel{srv: srv, fleet: fl, access: acc, health: hl, update: upd, warp: wsv, provision: prov, backup: bkp, telegram: tg, desired: desired, st: st, log: log}, nil
}

// run starts the background loops of the modules and serves until ctx ends or a listener fails.
func (p *panel) run(ctx context.Context, o httpserver.ServeOptions) error {
	bg, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(8)
	go func() { defer wg.Done(); sweepPlans(bg, p.st, p.log) }()
	go func() { defer wg.Done(); p.fleet.Run(bg) }()
	go func() { defer wg.Done(); p.access.Run(bg) }()
	go func() { defer wg.Done(); p.health.Run(bg) }()
	go func() { defer wg.Done(); p.update.Run(bg) }()
	go func() { defer wg.Done(); p.backup.Run(bg) }()
	go func() { defer wg.Done(); p.telegram.Run(bg) }()
	go func() {
		defer wg.Done()
		for bg.Err() == nil {
			if err := p.provision.Run(bg); err != nil && bg.Err() == nil {
				p.log.Error("provision worker stopped; retrying", "err", err)
			}
			select {
			case <-bg.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	err := p.srv.Serve(ctx, o)
	stop()
	wg.Wait() // the store must not be closed under a running sweep
	return err
}

// agentAddress is the host:port agents dial, for the install command: --agent-addr, else the separate
// agent listener if it names a concrete host, else the public URL's host and port. "" = unknown.
func agentAddress(flagAddr, agentListen, publicURL string) string {
	if flagAddr != "" {
		return flagAddr
	}
	if host, _, err := net.SplitHostPort(agentListen); err == nil && host != "" {
		if ip, err := netip.ParseAddr(host); err != nil || !ip.IsUnspecified() {
			return agentListen
		}
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443" // the agent endpoint is TLS
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// backupResult is backup.Config.OnResult: a failed backup is announced once, the next one that works resolves it.
func backupResult(tg *telegram.Service) func(code string) {
	return func(code string) {
		if code == "" {
			tg.BackupOK()
			return
		}
		tg.BackupFailed(code)
	}
}
