package plugin

import (
	"encoding/json"
	"net/netip"
	"time"
)

// InboundSpec mirrors agent.v1.InboundSpec. Settings is opaque, protocol-specific and already validated by
// the plugin on the panel; the engine validates again (never trust the wire).
type InboundSpec struct {
	ID        string
	Protocol  string
	ProfileID string
	Version   uint64 // agent.v1 spec_version
	Enabled   bool
	Listen    Listen
	TLS       TLS
	Egress    string // "direct" | "warp"
	Settings  json.RawMessage
	// Tunnel is the L3 interface of a tunnel protocol (awg). The zero value means "none" (hysteria2); the
	// state hash includes it only when it is set, so the hashes of protocols without it never change.
	Tunnel Tunnel
}

// Tunnel is what the HOST layer needs for an L3 protocol: the address of the node's interface inside the
// client subnet (the .1 of it) and the MTU. The host derives the client subnets (masquerade source,
// "ip rule from") from the prefixes. Mirrors agent.v1.Tunnel.
type Tunnel struct {
	AddrV4, AddrV6 netip.Prefix // AddrV6 invalid = no IPv6 in the tunnel
	MTU            uint16
}

// IsZero reports whether no tunnel is set.
func (t Tunnel) IsZero() bool { return !t.AddrV4.IsValid() && !t.AddrV6.IsValid() && t.MTU == 0 }

// WarpSpec is the node-level WARP configuration (agent.v1.WarpSpec). A nil *WarpSpec means "this node has no
// WARP"; Enabled=false with a spec present is "paused".
type WarpSpec struct {
	Enabled       bool
	PrivateKey    string // base64; a secret, never logged
	PeerPublicKey string
	EndpointV4    string // IP literal, no port
	EndpointV6    string // "" = never use
	Ports         []uint16
	AddressV4     string // "172.16.0.2/32"
	AddressV6     string // "" = none
	MTU           uint16
	Reserved      []byte // 0 or 3 bytes
	Backend       string // "auto" | "kernel" | "userspace"
}

// Listen is what the HOST layer needs. The engine binds Port only; the agent installs the hop DNAT.
type Listen struct {
	Network        string // "udp" | "tcp"
	Port           uint16
	HopFrom, HopTo uint16 // 0/0 = no port hopping
}

type TLSMode int // values equal agent.v1.TlsMode

const (
	TLSAcmeDomain TLSMode = 1
	TLSAcmeIP     TLSMode = 2
	TLSSelfSigned TLSMode = 3
)

type TLS struct {
	Mode       TLSMode
	ServerName string
}

// UserCred is one device x protocol credential as a node sees it (agent.v1.Credential).
type UserCred struct {
	CredID       string
	UserID       string
	DeviceID     string
	Data         json.RawMessage // verifier, never the raw end-user secret
	RateLimitBps uint64          // 0 = unlimited
	ValidUntil   time.Time       // zero = no expiry; the agent drops the credential itself at this time
}

// UserTraffic is a counter delta. Up = client -> internet, Down = internet -> client (the END USER's
// view; Hysteria core's "tx"/"rx" are server-remote and therefore swapped: core tx = Up, core rx = Down).
type UserTraffic struct {
	CredID    string
	InboundID string
	Up, Down  uint64
}

// Session is one open client connection (QUIC connection for Hysteria2, recent handshake for AWG).
type Session struct {
	CredID    string
	InboundID string
	RemoteIP  netip.Addr
	Since     time.Time
}

type RunState int // values equal agent.v1.InboundRunState

const (
	RunStarting RunState = 1
	RunRunning  RunState = 2
	RunFailed   RunState = 3
	RunStopped  RunState = 4
)

// EngineHealth is per inbound.
type EngineHealth struct {
	InboundID string
	State     RunState
	Detail    string // machine-readable reason when not running
	Restarts  uint32
	Since     time.Time
	// The certificate the inbound serves right now (zero for a protocol without TLS, and for an ACME certificate that is
	// not issued yet). Unlike the apply-time report it follows issuance and renewal.
	CertPinSHA256 string
	CertNotAfter  time.Time
}

// ---- panel-side vocabulary shared with the UI layer ----

// ClientID names a client application, independent of its config format.
type ClientID string

const (
	ClientHapp    ClientID = "happ"    // user toggle "Happ"
	ClientAmnezia ClientID = "amnezia" // user toggle "Amnezia"
	ClientMihomo  ClientID = "mihomo"
)

// ClientFormat is a subscription wire format.
type ClientFormat string

const (
	FormatURIList    ClientFormat = "uri-list"  // base64(lines of scheme:// URIs); hysteria2:// only so far.
	FormatXrayJSON   ClientFormat = "xray-json" // not rendered yet
	FormatSingbox    ClientFormat = "singbox"   // not rendered yet
	FormatMihomo     ClientFormat = "mihomo-yaml"
	FormatAmneziaVPN ClientFormat = "amnezia-vpn" // vpn://
	FormatAWGConf    ClientFormat = "awg-conf"
)

// ClientSupport says which client can consume a protocol, in which formats.
type ClientSupport struct {
	Client     ClientID
	Formats    []ClientFormat
	MinVersion string // "" = any; shown as a warning when the client is older
}

// Fragment is a piece of a subscription in one format. For FormatURIList, Data is exactly one URI (no
// newline, no base64; the subscription assembler joins and encodes).
type Fragment struct {
	Format ClientFormat
	Name   string // display name, e.g. "de1 · hy2 · 443"
	Data   []byte
}
