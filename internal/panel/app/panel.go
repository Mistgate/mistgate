// Package app assembles the shared panel services and HTTP surface for each edition.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
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

// InstanceConfig is the stored reachability and WebAuthn configuration used by the panel.
type InstanceConfig struct {
	PublicURL   string
	AdminHost   string
	AdminPrefix string
	AdminListen string
	RPID        string
	RPOrigins   []string
	AgentSNI    string
	SubPrefix   string
	LinkPrefix  string // secret path prefix of the signed agent WebSocket link; empty = not served by this handler
}

// AdminURL is the address the owner opens in a browser.
func (in InstanceConfig) AdminURL() string {
	if len(in.RPOrigins) == 0 {
		return ""
	}
	return in.RPOrigins[0] + in.AdminPrefix
}

// MuxPrefix is empty unless the admin lives under a secret path prefix.
func (in InstanceConfig) MuxPrefix() string {
	if in.AdminPrefix == "/" {
		return ""
	}
	return in.AdminPrefix
}

// BackgroundJob is one long-lived VPS worker. Edge callers receive the list but do not run it.
type BackgroundJob struct {
	Name string
	Run  func(context.Context)
}

// Config contains the edition-specific dependencies and settings for the shared panel.
type Config struct {
	Store              *store.Store
	Vault              *vault.Vault
	Auth               *auth.Service
	Limiter            securitylimit.Limiter
	MasterKey          []byte
	Clock              func() time.Time
	Logger             *slog.Logger
	Instance           InstanceConfig
	DecoyDir           string
	PanelAddr          string
	DataDir            string
	UpdateService      string
	Title              string
	HealthBlipWindow   time.Duration
	BackgroundStarters []BackgroundJob
}

// Panel is the assembled panel: its public HTTP handler, VPS listener adapter and services.
type Panel struct {
	Handler        http.Handler
	Server         *httpserver.Server
	Fleet          *fleet.Fleet
	Access         *access.Service
	Health         *health.Service
	Update         *update.Service
	Warp           *warp.Service
	Provision      *nodeprovision.Service
	Backup         *backup.Service
	Telegram       *telegram.Service
	Store          *store.Store
	Desired        func(ctx context.Context, nodeID string) ([]statehash.Inbound, error)
	BackgroundJobs []BackgroundJob
}

// Build wires services into the HTTP handler shared by the VPS and edge editions.
func Build(c Config) (*Panel, error) {
	if c.Store == nil || c.Vault == nil || c.Auth == nil {
		return nil, errors.New("panel app: store, vault and auth are required")
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}

	st, vlt, authSvc, log, in := c.Store, c.Vault, c.Auth, c.Logger, c.Instance
	limiter := c.Limiter
	if limiter == nil {
		limiter = securitylimit.NewMemoryWithOptions(c.Clock, 10_000)
	}
	reg := builtin.Registry()
	// Telegram alerts: everything below that has news reports it here; the admin links of the news carry the admin address
	// only when it is a public one (a separate loopback admin listener is not).
	alertsURL := ""
	if in.AdminListen == "" && len(in.RPOrigins) > 0 {
		alertsURL = in.AdminURL()
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
		AgentSNI:             in.AgentSNI,
		LinkServed:           in.LinkPrefix != "",
		Limiter:              limiter,
		PanelAddr:            c.PanelAddr,
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
	if in.PublicURL != "" {
		subBase = strings.TrimRight(in.PublicURL, "/") + strings.TrimRight(in.SubPrefix, "/")
	}
	acc, err = access.New(st, vlt, reg, fl, fl, access.Config{SubscriptionBaseURL: subBase, Log: log})
	if err != nil {
		return nil, err
	}
	// Health: the synthetic checker, alerts, the node doctor and retention. The fleet hands it doctor reports and
	// returning nodes; it gives the fleet the node status and the alert badge.
	hcfg := health.Config{Log: log, BlipWindow: c.HealthBlipWindow, OnTransition: func(t health.Transition) { tg.AlertTransition(t.Alert, t.Resolved) }}
	hl = health.New(st, vlt, reg, fl, hcfg)
	fl.SetHealth(hl)

	// Node-agent updates: the signed bundle in <data-dir>/dist, its distribution through the agent endpoint and
	// the staged rollout. The installation's release key is kept in dataDir; a binary with another compiled-in key
	// trusts nothing until the owner runs `mistgate release trust-key`.
	relKey, err := buildinfo.ReleasePublicKey()
	if c.DataDir != "" {
		relKey, err = buildinfo.LoadReleasePublicKey(c.DataDir)
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
		CurrentBuilt: buildinfo.BuiltUnix(), Key: compiledKey, DataDir: c.DataDir,
		ServiceUnit: c.UpdateService, Enabled: update.PanelUpdateHostSupported(),
		UseRootHelperService: update.PanelUpdateUsesRootHelperService(), Log: log,
	})
	var nodeBundleSource update.NodeBundleSource
	if len(relKey) > 0 && c.DataDir != "" {
		nodeBundleSource = update.NewGitHubNodeBundleSource(update.GitHubNodeBundleConfig{
			DataDir: c.DataDir, Key: relKey, Log: log,
		})
	}
	upd, err := update.New(st, fl, hl, update.Config{
		DataDir: c.DataDir, Key: relKey, PanelVersion: buildinfo.Version, PanelBuilt: buildinfo.BuiltUnix(),
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
		PanelAddr: c.PanelAddr, AgentSNI: in.AgentSNI, Log: log,
	})
	if err != nil {
		return nil, err
	}
	bkp, err := backup.New(backup.Config{
		Store: st, Vault: vlt, DataDir: c.DataDir, MasterKey: c.MasterKey,
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
		Title: c.Title, BaseURL: subBase, Settings: cache, Brand: brand,
		Routing: subs.HappRouting(dnsSvc), Events: st, Log: log,
		Limiter: limiter,
		PageKey: vlt.Derive(pagepass.KeyLabel), // the page password of every user: computed from the token, nothing stored
	}
	sub := http.NewServeMux()
	sub.Handle("GET /brand/logo.svg", httpserver.NewLogoHandler(st, log))
	sub.Handle("/", subs.Handler(acc, httpserver.NewDecoy(c.DecoyDir), subCfg))
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
	var link http.Handler // the signed WebSocket agent link, served only under its secret prefix
	if in.LinkPrefix != "" {
		link = fl.LinkHandler()
	}
	// The Instance service is built into httpserver (it is part of the sign-in flow); Auth likewise.
	srv, err := httpserver.New(httpserver.Config{
		AdminHost:        in.AdminHost,
		AdminPrefix:      in.MuxPrefix(),
		DecoyDir:         c.DecoyDir,
		TrustedOrigins:   in.RPOrigins,
		Log:              log,
		AdminHandlers:    admin,
		AdminPages:       []httpserver.AdminPage{{Path: nodeprovision.AdminPagePath, Handler: prov.PageHandler()}},
		MCP:              mcpEndpoint(authSvc, st, log, tg.PlanWaiting, c.Clock),
		PublicMounts:     map[string]http.Handler{in.SubPrefix: sub},
		AgentSNI:         in.AgentSNI,
		AgentTLS:         fl.AgentTLSConfig,
		AgentHandler:     fl.AgentHandler(),
		AgentLinkPrefix:  in.LinkPrefix,
		AgentLinkHandler: link,
		// Settings -> Domains shows the owner both addresses, read-only.
		AdminURL: in.AdminURL(), SubscriptionBase: subBase,
		// The admin's framed preview of the public user page (session and read role are checked by the server).
		UserPagePreview: subs.PreviewHandler(acc, subCfg),
	}, authSvc, st)
	if err != nil {
		return nil, err
	}
	jobs := mcpBackgroundJobs(st, log, c.Clock)
	jobs = append(jobs, []BackgroundJob{
		{Name: "fleet", Run: func(ctx context.Context) { _ = fl.Run(ctx) }},
		{Name: "access", Run: acc.Run},
		{Name: "health", Run: hl.Run},
		{Name: "updates", Run: upd.Run},
		{Name: "backups", Run: bkp.Run},
		{Name: "telegram", Run: tg.Run},
		{Name: "provision", Run: func(ctx context.Context) {
			for ctx.Err() == nil {
				if err := prov.Run(ctx); err != nil && ctx.Err() == nil {
					log.Error("provision worker stopped; retrying", "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}},
	}...)
	jobs = append(jobs, c.BackgroundStarters...)
	return &Panel{
		Handler: srv.Public(), Server: srv, Fleet: fl, Access: acc, Health: hl, Update: upd,
		Warp: wsv, Provision: prov, Backup: bkp, Telegram: tg, Store: st, Desired: desired,
		BackgroundJobs: jobs,
	}, nil
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

type warpForFleet struct{ *warp.Service }

var _ fleet.Warp = warpForFleet{}

func (warpForFleet) Summary(a *store.WarpAccountRow, online bool, now time.Time) *adminv1.WarpSummary {
	return warp.Summary(a, online, now)
}
