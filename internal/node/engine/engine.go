package engine

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// Engine runs all inbounds of ONE protocol on the node. Implementations: hysteria2, awg, (v1.x)
// xray. It may live in-process or wrap a child process; the agent does not care.
//
// Concurrency: the agent calls Apply/Remove/Kick from one goroutine (the reconcile loop) and Collect/
// Observed/Health from others; implementations must make these safe. Hot paths (authentication, traffic
// accounting) must not take a global mutex: use an atomic pointer to an immutable credential index and
// per-credential atomic counters.
type Engine interface {
	// Protocol is the plugin id ("hysteria2"), equal to InboundSpec.Protocol it serves.
	Protocol() string
	// Version is shown in Hello and on the node page: "hysteria core v2.12.3".
	Version() string
	Capabilities() Capabilities

	// Apply makes the inbound match (spec, creds). Idempotent.
	//  - New inbound: bind, start serving.
	//  - Same spec_hash, different creds: swap the credential index. NO restart, open sessions of kept
	//    credentials survive, sessions of removed credentials are closed at their next traffic.
	//  - Different spec_hash: restart that inbound (and only it); report Restarted. One exception: an engine MAY
	//    apply the change in place when only fields it changes live differ (awg: mtu) and then reports
	//    Restarted=false. A different port, version, key or obfuscation always recreates the interface.
	//  - spec.Enabled == false: stop the listener, keep nothing.
	// It returns an error only for a failure to reach the target state (bind failed, bad settings);
	// the agent records it in the inbound result and keeps the other inbounds running.
	Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (ApplyReport, error)

	// Remove stops the inbound and forgets it. Unknown id is not an error.
	Remove(ctx context.Context, inboundID string) error

	// Collect returns traffic deltas accumulated since the previous Collect (it resets them) and an
	// absolute snapshot of open sessions. The agent owns delivery: a delta that was handed out is the
	// agent's responsibility (it queues it until the panel acks), so Collect never replays.
	Collect(ctx context.Context) (Collected, error)

	// Kick closes the sessions of these credentials (best effort; Hysteria2 closes at the next packet).
	// Returns how many credentials had an open session.
	Kick(ctx context.Context, credIDs []string) (int, error)

	// Observed reports what the engine holds right now, for the state hash and drift detection.
	Observed() []statehash.Inbound

	Health() []plugin.EngineHealth

	// Close stops everything; bounded by ctx.
	Close(ctx context.Context) error
}

// TCPPortUser lets an engine give up a TCP side listener when another inbound claims that port. claimed maps
// enabled TCP listener ports to their inbound ids and must be treated as read-only.
type TCPPortUser interface {
	TCPClaims(ctx context.Context, claimed map[uint16]string)
}

type Capabilities struct {
	RateLimitPerCred bool // honours UserCred.RateLimitBps
	HardExpiry       bool // honours UserCred.ValidUntil itself (the agent also enforces it, this is an optimisation)
}

type ApplyReport struct {
	SpecHash  string // statehash.Spec of the spec it now runs
	CredCount int
	Restarted bool
	Cert      CertInfo // what the inbound serves; zero if the protocol has no TLS
}

type CertInfo struct {
	PinSHA256 string // hex of sha256(DER leaf)
	NotAfter  time.Time
}

type Collected struct {
	Traffic  []plugin.UserTraffic // non-zero deltas only
	Sessions []plugin.Session
}

// Env is everything the agent (the owner of host state) gives an engine at construction. Engines never
// touch nftables, sysctl, the resolver configuration or certificates directly.
type Env struct {
	Log *slog.Logger
	// Event reports protocol-level detections to the owning agent. Implementations must keep event parameters
	// bounded and must not include credentials or other secrets.
	Event func(Event)
	// Certs issues/renews certificates per inbound (ACME domain, ACME IP, self-signed).
	Certs CertSource
	// Egress returns the outbound for InboundSpec.Egress: "direct", or "warp" (the WARP manager's, bound to the
	// mgwarp device). "warp" while the node has no WARP configuration is an error: there is no silent direct exit.
	Egress func(name string) (Egress, error)
	// DNS returns the node's resolvers (NodeSettings.dns_resolvers or the geo default); called per use so
	// a settings change needs no restart.
	DNS func() []string
	// Masquerade serves unauthenticated requests: the decoy site. The same handler is used
	// for HTTP/3 (the core's MasqHandler) and for TCP 443 (hysteria extras/masq.MasqTCPServer).
	Masquerade func(inboundID string) http.Handler
	Now        func() time.Time
}

// Event is a small, protocol-neutral signal from an engine to the node agent.
type Event struct {
	Code      string
	InboundID string
	Warning   bool
	Params    map[string]string
}

// Factory builds an engine; registered from main: engine.Register("hysteria2", hysteria2.New).
type Factory func(Env) (Engine, error)

type CertSource interface {
	// Acquire returns a live certificate source for an inbound; renewals replace it transparently.
	Acquire(ctx context.Context, inboundID string, t plugin.TLS) (Cert, error)
	Release(inboundID string)
}

type Cert struct {
	GetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	Info           func() CertInfo
}

// Egress has the exact shape of hysteria server.Outbound, so a WARP/netstack dialer drops in without an
// adapter for Hysteria2, and other engines can wrap it.
type Egress interface {
	TCP(addr string) (net.Conn, error)
	UDP(addr string) (EgressUDP, error)
	CheckUDP(addr string) error
}

type EgressUDP interface {
	ReadFrom(b []byte) (int, string, error)
	WriteTo(b []byte, addr string) (int, error)
	Close() error
}
