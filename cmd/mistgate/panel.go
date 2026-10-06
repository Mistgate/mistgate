package main

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/backup"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	nodeprovision "github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/store"
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

// panel is the VPS listener and background-loop adapter around the shared assembly.
type panel struct {
	srv        *httpserver.Server
	fleet      *fleet.Fleet
	access     *access.Service
	health     *health.Service
	update     *update.Service
	warp       *warp.Service
	provision  *nodeprovision.Service
	backup     *backup.Service
	telegram   *telegram.Service
	st         *store.Store // for the sweep of MCP plans
	log        *slog.Logger
	background []app.BackgroundJob
	// desired is the desired-state source of the fleet (tests read it).
	desired func(ctx context.Context, nodeID string) ([]statehash.Inbound, error)
}

// newPanel keeps the VPS setup seam while delegating service and handler assembly to the shared app package.
func newPanel(st *store.Store, vlt *vault.Vault, authSvc *auth.Service, o panelOpts, log *slog.Logger) (*panel, error) {
	// A configured installation gets this prefix in setup/ensureSecrets. Keep direct in-process panel fixtures usable too.
	if o.in.LinkPrefix == "" {
		o.in.LinkPrefix = newSecretPrefix()
	}
	var healthBlipWindow time.Duration
	if d, err := time.ParseDuration(os.Getenv("MISTGATE_HEALTH_BLIP_WINDOW")); err == nil && d > 0 {
		healthBlipWindow = d
	}
	built, err := app.Build(app.Config{
		Store: st, Vault: vlt, Auth: authSvc, MasterKey: o.masterKey,
		Logger: log, Instance: app.InstanceConfig{
			PublicURL: o.in.PublicURL, AdminHost: o.in.AdminHost, AdminPrefix: o.in.AdminPrefix,
			AdminListen: o.in.AdminListen, RPID: o.in.RPID, RPOrigins: o.in.RPOrigins,
			AgentSNI: o.in.AgentSNI, SubPrefix: o.in.SubPrefix, LinkPrefix: o.in.LinkPrefix,
		},
		DecoyDir: o.decoyDir, PanelAddr: o.panelAddr, DataDir: o.dataDir,
		UpdateService: o.updateService, Title: o.title, HealthBlipWindow: healthBlipWindow,
	})
	if err != nil {
		return nil, err
	}
	return &panel{
		srv: built.Server, fleet: built.Fleet, access: built.Access, health: built.Health,
		update: built.Update, warp: built.Warp, provision: built.Provision, backup: built.Backup,
		telegram: built.Telegram, st: built.Store, log: log, desired: built.Desired,
		background: built.BackgroundJobs,
	}, nil
}

// run starts the shared background loops and serves until ctx ends or a listener fails.
func (p *panel) run(ctx context.Context, o httpserver.ServeOptions) error {
	bg, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(len(p.background))
	for _, job := range p.background {
		job := job
		go func() { defer wg.Done(); job.Run(bg) }()
	}
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
