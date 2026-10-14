// Package app assembles the shared panel services and HTTP surface for each edition.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/backup"
	"github.com/mistgate/mistgate/internal/panel/blocklist"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	nodeprovision "github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
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

// linkURL is the base of the signed agent link (and of link-only enrolment): wss:// + the public host + the secret
// prefix. "" when the panel has no public URL or no link prefix.
func linkURL(in InstanceConfig) string {
	u, err := url.Parse(in.PublicURL)
	if err != nil || u.Host == "" || in.LinkPrefix == "" {
		return ""
	}
	return "wss://" + u.Host + in.LinkPrefix
}

// provisionLinkURL is the link an SSH install enrols the node over. Edge agents are link-only (no mTLS endpoint to dial),
// so an edge install uses the link, like fleet's own install command; the VPS edition keeps the mTLS enrolment.
func provisionLinkURL(edge bool, in InstanceConfig) string {
	if !edge {
		return ""
	}
	return linkURL(in)
}

// forwardProvision is the ProvisioningService mount: the marker that sends the call to the Provisioner when forwarding, else
// the service itself.
func forwardProvision(forward bool, service func(opts ...connect.HandlerOption) (string, http.Handler)) (string, http.Handler) {
	path, handler := service()
	if forward {
		handler = nodeprovision.ForwardMarker(buildinfo.Version)
	}
	return path, handler
}

// forwardPage is the install page mount, likewise.
func forwardPage(forward bool, page http.Handler) http.Handler {
	if forward {
		return nodeprovision.ForwardMarker(buildinfo.Version)
	}
	return page
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

// BackgroundJob is one long-lived VPS worker. The edge runs only the bounded phases listed in edge_tick.go.
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
	AfterResponse      access.AfterResponseRunner
	// Remote is the edge edition's node link. Setting it makes this the edge edition (fleet does the same): Me says so,
	// and the RPCs that start work the edge does not run yet refuse with unavailable_in_edition.
	Remote fleet.Remote
	// ProvisionForward makes this panel hand SSH installs to the Provisioner (the edge Worker that holds the sockets): the
	// ProvisioningService calls and the install page are answered, after the sign-in check, with a marker that the Worker
	// turns into a request to that object. Without it, and without SSHClient, they are served here (and refused on the edge).
	ProvisionForward bool
	// ProvisionKick wakes the Provisioner (edge: set its alarm). The cron pass calls it while installs are pending; a
	// Provisioner calls it when an install is queued. Nil: nothing to wake.
	ProvisionKick func(context.Context) error
	// SSHClient makes this panel the one that runs the installs (the Provisioner): it dials through it. Nil: the default
	// client of the VPS edition.
	SSHClient *nodeprovision.Client
	// NodeBinaries is where an install gets the agent binary; nil: the update service (the signed bundle on disk).
	NodeBinaries nodeprovision.BinarySource
}

// Panel is the assembled panel: its public HTTP handler, VPS listener adapter and services.
type Panel struct {
	Handler        http.Handler
	Server         *httpserver.Server
	Fleet          *fleet.Fleet
	Access         *access.Service
	Health         *health.Service
	Blocklist      *blocklist.Service
	Update         *update.Service
	Warp           *warp.Service
	Provision      *nodeprovision.Service
	Backup         *backup.Service
	Telegram       *telegram.Service
	Store          *store.Store
	Desired        func(ctx context.Context, nodeID string) ([]statehash.Inbound, error)
	BackgroundJobs []BackgroundJob
	// Surface is what Handler serves (see roles.go).
	Surface Surface
	// provisionKick is Config.ProvisionKick; EdgeTick uses it.
	provisionKick func(context.Context) error
}

// jobServices are the services the background jobs of the VPS edition run (see backgroundJobs).
type jobServices struct {
	fleet     *fleet.Fleet
	access    *access.Service
	health    *health.Service
	blocklist *blocklist.Service
	update    *update.Service
	backup    *backup.Service
	telegram  *telegram.Service
	provision *nodeprovision.Service
	store     *store.Store
	log       *slog.Logger
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
