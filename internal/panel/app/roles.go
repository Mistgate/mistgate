package app

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/backup"
	"github.com/mistgate/mistgate/internal/panel/clientprobe"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	nodeprovision "github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The edge edition runs the panel as several Workers, one per role, each with its own wasm: only what a role wires is in it.
// Build is the whole panel (the VPS process, and the single-Worker edge); the functions below split it:
//
//	BuildPublic     subscriptions of every format, the person's page, devices, the brand logo, the decoy
//	BuildLink       the agent link: the Go side of a node's session (and link-only enrolment)
//	BuildJobs       the scheduled work (EdgeTick)
//	BuildAdmin      the SPA, the admin API, sign-in and setup, the install marker for the Provisioner
//	BuildProvision  the Provisioner: SSH installs
//
// Every one of them is made of wireCore (or, for the public Worker, of the fleet and access it needs) and the pieces Build
// also uses, so a service or a handler is wired in one place. TestEdgeRolesCoverBuild fails when a role and Build drift apart.

// Surface lists what a Panel serves over HTTP.
type Surface struct {
	AdminAPI     []string // the Connect services behind the session, as AdminHandler.Path (without the built-in auth and instance services)
	AdminPages   []string
	PublicMounts []string // secret prefixes
	Link         string   // secret prefix of the agent link, "" = none
	MCP          bool
	Preview      bool // the admin's preview of the person's page
}

func surfaceOf(cfg httpserver.Config) Surface {
	var s Surface
	for _, h := range cfg.AdminHandlers {
		s.AdminAPI = append(s.AdminAPI, h.Path)
	}
	for _, p := range cfg.AdminPages {
		s.AdminPages = append(s.AdminPages, p.Path)
	}
	for prefix := range cfg.PublicMounts {
		s.PublicMounts = append(s.PublicMounts, prefix)
	}
	sort.Strings(s.AdminAPI)
	sort.Strings(s.AdminPages)
	sort.Strings(s.PublicMounts)
	s.Link, s.MCP, s.Preview = cfg.AgentLinkPrefix, cfg.MCP != nil, cfg.UserPagePreview != nil
	return s
}

// Build wires services into the HTTP handler shared by the VPS and edge editions: every role in one process.
func Build(c Config) (*Panel, error) {
	k, err := wireCore(c)
	if err != nil {
		return nil, err
	}
	c = k.c
	parts := k.base.newSubParts(true)
	prov, bkp, err := k.provisionAndBackup()
	if err != nil {
		return nil, err
	}
	var link http.Handler // the signed WebSocket agent link, served only under its secret prefix
	if k.in.LinkPrefix != "" {
		link = k.fl.LinkHandler()
	}
	cfg := k.adminConfig(parts, prov, bkp, c.ProvisionForward)
	cfg.PublicMounts = map[string]http.Handler{k.in.SubPrefix: parts.mux(k.base, k.acc)}
	cfg.AgentSNI, cfg.AgentTLS, cfg.AgentHandler = k.in.AgentSNI, k.fl.AgentTLSConfig, k.fl.AgentHandler()
	cfg.AgentLinkPrefix, cfg.AgentLinkHandler = k.in.LinkPrefix, link
	return k.finish(cfg, prov, bkp, true)
}

// BuildAdmin is the admin Worker: Build without the public mounts, the agent link and the agent endpoint, which other
// Workers serve (their prefixes are only checked), and with the install calls answered by the marker for the Provisioner.
func BuildAdmin(c Config) (*Panel, error) {
	k, err := wireCore(c)
	if err != nil {
		return nil, err
	}
	parts := k.base.newSubParts(true)
	prov, bkp, err := k.provisionAndBackup()
	if err != nil {
		return nil, err
	}
	cfg := k.adminConfig(parts, prov, bkp, true)
	cfg.ReservedPrefixes = []string{k.in.SubPrefix, k.in.LinkPrefix}
	return k.finish(cfg, prov, bkp, false)
}

// BuildProvision is the Provisioner Worker: the admin surface with the ProvisioningService and the install page only, served
// for real, with the SSH client of c. The admin Worker forwards the calls to it after its own sign-in check.
func BuildProvision(c Config) (*Panel, error) {
	if c.SSHClient == nil {
		return nil, errors.New("panel app: the Provisioner needs an SSH client")
	}
	k, err := wireCore(c)
	if err != nil {
		return nil, err
	}
	prov, err := k.newProvision()
	if err != nil {
		return nil, err
	}
	cfg := k.baseHTTP()
	path, handler := forwardProvision(false, prov.Handler)
	cfg.AdminHandlers = []httpserver.AdminHandler{{Path: path, Handler: handler}}
	cfg.AdminPages = []httpserver.AdminPage{{Path: nodeprovision.AdminPagePath, Handler: forwardPage(false, prov.PageHandler())}}
	cfg.ReservedPrefixes = []string{k.in.SubPrefix, k.in.LinkPrefix}
	return k.finish(cfg, prov, nil, false)
}

// BuildLink is the link Worker: the agent link under its secret prefix (the node's Durable Object calls the fleet through
// mgPanel.link; link-only enrolment arrives here as HTTP) and the decoy for everything else.
func BuildLink(c Config) (*Panel, error) {
	k, err := wireCore(c)
	if err != nil {
		return nil, err
	}
	if k.in.LinkPrefix == "" {
		return nil, errors.New("panel app: the link role needs a link prefix")
	}
	cfg := httpserver.Config{
		DecoyDir: k.c.DecoyDir, Log: k.log,
		AgentLinkPrefix: k.in.LinkPrefix, AgentLinkHandler: k.fl.LinkHandler(),
		ReservedPrefixes: []string{k.in.MuxPrefix(), k.in.SubPrefix},
	}
	srv, err := httpserver.NewPublic(cfg, k.st)
	if err != nil {
		return nil, err
	}
	p := k.panel()
	p.Handler, p.Server, p.Surface = srv.Public(), srv, surfaceOf(cfg)
	return p, nil
}

// BuildJobs is the scheduled-work Worker: the services EdgeTick runs, and no HTTP surface.
func BuildJobs(c Config) (*Panel, error) {
	k, err := wireCore(c)
	if err != nil {
		return nil, err
	}
	return k.panel(), nil
}

// BuildPublic is the public Worker: the subscriptions, the person's page, the brand logo and the decoy, under the secret
// subscription prefix. It builds only the fleet (to tell the nodes that state changed, and who is online) and access that
// the subscription handler needs, and no auth service: c.Auth may be nil.
func BuildPublic(c Config) (*Panel, error) {
	b, err := newBase(c)
	if err != nil {
		return nil, err
	}
	var acc *access.Service // the fleet calls back into it only after serving started
	// The public Worker holds no node session: the desired state of a node is asked in the link step, in the link Worker.
	noDesired := func(context.Context, string) ([]statehash.Inbound, error) {
		return nil, errors.New("panel app: the public role serves no node link")
	}
	fl, err := fleet.New(b.st, b.vlt, b.reg, b.fleetConfig(noDesired, b.usageRecompute(func() *access.Service { return acc })))
	if err != nil {
		return nil, err
	}
	acc, err = access.New(b.st, b.vlt, b.reg, fl, fl, b.accessConfig(fl))
	if err != nil {
		return nil, err
	}
	parts := b.newSubParts(false)
	cfg := httpserver.Config{
		DecoyDir: b.c.DecoyDir, Log: b.log,
		PublicMounts:     map[string]http.Handler{b.in.SubPrefix: parts.mux(b, acc)},
		ReservedPrefixes: []string{b.in.MuxPrefix(), b.in.LinkPrefix},
	}
	srv, err := httpserver.NewPublic(cfg, b.st)
	if err != nil {
		return nil, err
	}
	return &Panel{Handler: srv.Public(), Server: srv, Fleet: fl, Access: acc, Store: b.st, Surface: surfaceOf(cfg)}, nil
}

// baseHTTP is the part of the HTTP configuration every role that serves HTTP shares.
func (k *core) baseHTTP() httpserver.Config {
	return httpserver.Config{
		AdminHost:      k.in.AdminHost,
		AdminPrefix:    k.in.MuxPrefix(),
		DecoyDir:       k.c.DecoyDir,
		TrustedOrigins: k.in.RPOrigins,
		Log:            k.log,
		// Settings -> Domains shows the owner both addresses, read-only.
		AdminURL: k.in.AdminURL(), SubscriptionBase: k.subBase,
	}
}

// adminConfig is the HTTP configuration of the admin surface: every admin service, the install page, MCP and the preview of
// the person's page.
func (k *core) adminConfig(parts subParts, prov *nodeprovision.Service, bkp *backup.Service, forward bool) httpserver.Config {
	cfg := k.baseHTTP()
	subSvc := subs.NewService(k.st, parts.cache, k.reg, parts.brand, parts.dns, k.log)
	subSvc.NotifyWith(k.fl) // a switch to STRICT revokes the shared devices
	// The kl!ck reports: the owner's switch (it rides with the subscription settings, hence their cache) and the counts.
	probe := clientprobe.New(clientprobe.Config{Store: k.st, StepUp: k.authSvc.RequireStepUp, Invalidate: parts.cache.Invalidate, Log: k.log, Now: k.c.Clock})
	for _, h := range []func() (string, http.Handler){
		func() (string, http.Handler) { return k.fl.NodeHandler() },
		func() (string, http.Handler) { return k.fl.FleetHandler() },
		k.acc.ProfileHandler, k.acc.UserHandler, k.acc.GroupHandler, k.acc.DeviceHandler, k.acc.AwgHandler, subSvc.Handler, parts.dns.Handler,
		func() (string, http.Handler) { return k.wsv.Handler() },
		func() (string, http.Handler) { return forwardProvision(forward, prov.Handler) },
		func() (string, http.Handler) { return k.hl.Handler() },
		func() (string, http.Handler) { return k.upd.Handler() },
		func() (string, http.Handler) { return bkp.Handler() },
		func() (string, http.Handler) { return k.tg.Handler() },
		func() (string, http.Handler) { return k.gp.Handler() },
		func() (string, http.Handler) { return k.cc.Handler() },
		func() (string, http.Handler) { return k.bs.Handler() },
		func() (string, http.Handler) { return k.cr.Handler() },
		func() (string, http.Handler) { return k.bl.Handler() },
		func() (string, http.Handler) { return k.cp.Handler() },
		func() (string, http.Handler) { return probe.Handler() },
	} {
		path, handler := h()
		cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: handler})
	}
	// API tokens and the owner's approvals, then the MCP endpoint over the same admin API.
	cfg.AdminHandlers = append(cfg.AdminHandlers, integrationHandlers(k.authSvc)...)
	cfg.AdminPages = []httpserver.AdminPage{{Path: nodeprovision.AdminPagePath, Handler: forwardPage(forward, prov.PageHandler())}}
	cfg.MCP = mcpEndpoint(k.authSvc, k.st, k.log, k.tg.PlanWaiting, k.c.Clock)
	// The admin's framed preview of the public user page (session and read role are checked by the server).
	cfg.UserPagePreview = subs.PreviewHandler(k.acc, parts.cfg)
	return cfg
}

// provisionAndBackup builds the two services only an admin surface has.
func (k *core) provisionAndBackup() (*nodeprovision.Service, *backup.Service, error) {
	prov, err := k.newProvision()
	if err != nil {
		return nil, nil, err
	}
	bkp, err := backup.New(backup.Config{
		Store: k.st, Vault: k.vlt, DataDir: k.c.DataDir, MasterKey: k.c.MasterKey,
		Edge: k.edge, StepUp: k.authSvc.RequireStepUp, OnResult: backupResult(k.tg), Log: k.log,
	})
	if err != nil {
		return nil, nil, err
	}
	return prov, bkp, nil
}

func (k *core) newProvision() (*nodeprovision.Service, error) {
	c := k.c
	var binaries nodeprovision.BinarySource = k.upd
	if c.NodeBinaries != nil {
		binaries = c.NodeBinaries
	}
	provCfg := nodeprovision.Config{
		StepUp: k.authSvc.RequireStepUp, Nodes: k.fl, Binaries: binaries, SSH: c.SSHClient,
		PanelAddr: c.PanelAddr, LinkURL: provisionLinkURL(k.edge, k.in), AgentSNI: k.in.AgentSNI, Log: k.log,
	}
	if c.ProvisionKick != nil {
		provCfg.Kick = func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := c.ProvisionKick(ctx); err != nil {
				k.log.Warn("wake the provisioner", "err", err)
			}
		}
	}
	return nodeprovision.NewService(k.st, k.vlt, provCfg)
}

// finish builds the HTTP server of an admin surface from cfg and the Panel that goes with it. jobs says whether the
// process runs the background loops of the VPS edition (Build does; the Workers never start them).
func (k *core) finish(cfg httpserver.Config, prov *nodeprovision.Service, bkp *backup.Service, jobs bool) (*Panel, error) {
	// The Instance service is built into httpserver (it is part of the sign-in flow); Auth likewise.
	srv, err := httpserver.New(cfg, k.authSvc, k.st)
	if err != nil {
		return nil, err
	}
	p := k.panel()
	p.Handler, p.Server, p.Provision, p.Backup, p.Surface = srv.Public(), srv, prov, bkp, surfaceOf(cfg)
	if jobs {
		p.BackgroundJobs = backgroundJobs(k.c, jobServices{fleet: k.fl, access: k.acc, health: k.hl, blocklist: k.bl, probe: k.cp,
			cheburcheck: k.cc, bsbord: k.bs, update: k.upd, backup: bkp, telegram: k.tg, provision: prov, store: k.st, log: k.log})
	}
	return p, nil
}
