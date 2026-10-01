package protocols

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

// Protocol is the panel half of a protocol plugin. Settings are always the MERGED JSON (secrets in
// place); the framework masks/splits secrets using the schema's "x-secret" markers.
type Protocol interface {
	ID() string
	DisplayName() string

	// SettingsSchema is the JSON Schema the admin UI renders the profile form from (vocabulary: see
	// proto/admin profile.proto).
	SettingsSchema() []byte
	// DefaultSettings returns settings for a new profile with secrets already generated.
	DefaultSettings() (json.RawMessage, error)
	// Validate returns per-field errors (JSON pointers); nil = valid. Must be pure and fast: the UI calls
	// it on every edit through PreviewProfile.
	Validate(settings json.RawMessage) []FieldError
	// Summary is the mono parameter line on the profile card: "UDP 443 · Salamander · Let's Encrypt".
	Summary(settings json.RawMessage) string

	// BuildInbound translates profile settings + node + overrides into the node-side spec (ports, TLS
	// mode, egress are lifted out of the settings into first-class fields; the rest goes to Settings).
	BuildInbound(in InboundInput) (plugin.InboundSpec, error)

	// IssueCredential creates the credential of one device: Secret is what the client presents (stored
	// encrypted, shown in subscriptions), NodeData is the verifier shipped to nodes.
	IssueCredential(in IssueInput) (Issued, error)

	// Clients lists the client apps that can consume this protocol. A user gets the protocol when one of
	// their enabled apps appears here.
	Clients() []plugin.ClientSupport
	// Render returns this protocol's piece of a subscription in the requested format, or false if the
	// format/client cannot carry the protocol (Happ cannot carry AWG and vice versa).
	Render(in RenderInput) (plugin.Fragment, bool)

	// Doctor returns the protocol-specific checks for the fleet doctor.
	Doctor() []Check
}

type FieldError struct {
	Pointer string // "/obfs/password"
	Code    string
	Message string
}

type NodeView struct {
	ID, Name, Address, CountryCode string
}

type ProfileView struct {
	ID       string
	Version  uint32
	Settings json.RawMessage
}

type InboundInput struct {
	Profile               ProfileView
	Node                  NodeView
	PortOverride          uint16 // 0 = none
	TLSServerNameOverride string
	SpecVersion           uint64
	Enabled               bool
	// InboundID and PluginState/PluginPublic carry what InitInbound made for this inbound (AWG: the server
	// key pair). PluginState is already decrypted; empty for a protocol without InboundInitializer.
	InboundID    string
	PluginState  []byte
	PluginPublic json.RawMessage
}

type IssueInput struct {
	UserID, DeviceID string
	// L3 protocols (PerDevice): the profile the credential belongs to, its merged settings and the peer index
	// the IPAM handed out (address = subnet base + PeerIndex). Zero for hysteria2.
	ProfileID string
	Settings  json.RawMessage
	PeerIndex int
}

// InboundInitializer is an optional interface: a plugin that needs per-inbound key material implements it.
// The framework calls InitInbound once when an inbound is created, stores State vault-encrypted (AAD = the
// inbound id) and Public in clear, and hands both back through InboundInput and RenderInput.
type InboundInitializer interface {
	InitInbound(in InboundInitInput) (InboundInit, error)
}

type InboundInitInput struct {
	InboundID string
	Profile   ProfileView
	Node      NodeView
}

type InboundInit struct {
	State  []byte          // secret (AWG: the server private key); encrypted by the framework
	Public json.RawMessage // not secret (AWG: {"public_key": ...})
}

// PerDevice is an optional interface. A plugin answering true issues its credential on demand, bound to ONE
// profile (several live credentials per device, one per profile), so the generic "every device gets one" pass
// skips it. hysteria2 does not implement it.
type PerDevice interface{ PerDevice() bool }

// ClientReq is the minimum version of one client app for a profile's settings.
type ClientReq struct {
	Client plugin.ClientID
	App    string // display name: "AmneziaVPN"
	Min    string // tag or version: "5.0.1.5"
}

// ClientRequirements is an optional interface: the minimum client versions for THIS profile's settings
// (shown next to every config: an old AmneziaVPN silently drops keys it does not know).
type ClientRequirements interface {
	MinClients(settings json.RawMessage) []ClientReq
}

// NormalizeInput is what a SettingsNormalizer sees: the client's document, what it was laid over and the result.
type NormalizeInput struct {
	Input  json.RawMessage // the document the client sent
	Old    json.RawMessage // the stored merged settings of the profile; nil when the profile is being created
	Merged json.RawMessage // Input laid over Old (over the defaults on create), secrets resolved, not yet validated
	Save   bool            // the result will be stored; false for a live preview of a form
}

// SettingsNormalizer is an optional interface: it fixes merged settings before they are validated and stored.
// AWG uses it so that a schema default never becomes a live config: a profile made without an obfuscation block
// gets a generated one. It must return Merged itself, byte for byte, when it has nothing to change (the caller
// compares the bytes to see whether anything changed).
type SettingsNormalizer interface {
	NormalizeSettings(in NormalizeInput) (json.RawMessage, error)
}

// IsPerDevice reports whether p issues per-device, per-profile credentials.
func IsPerDevice(p Protocol) bool {
	pd, ok := p.(PerDevice)
	return ok && pd.PerDevice()
}

// MinClientsOf returns the client requirements of p for the given settings, nil when it has none.
func MinClientsOf(p Protocol, settings json.RawMessage) []ClientReq {
	if cr, ok := p.(ClientRequirements); ok {
		return cr.MinClients(settings)
	}
	return nil
}

type Issued struct {
	Secret   string          // client-side secret (hy2 auth token); never sent to a node
	NodeData json.RawMessage // verifier for UserCred.Data (hy2: {"auth_sha256":"<hex>"})
}

// InboundView is the effective facts about one deployed inbound needed to write a client config.
type InboundView struct {
	ID             string
	Node           NodeView
	Port           uint16
	HopFrom, HopTo uint16
	TLSServerName  string // effective SNI
	TLSMode        plugin.TLSMode
	CertPinSHA256  string // reported by the node after apply; empty until then
}

type RenderInput struct {
	Format      plugin.ClientFormat
	Settings    json.RawMessage // merged, secrets in place
	Inbound     InboundView
	UserID      string
	UserName    string
	DeviceID    string
	Secret      string // decrypted credential secret
	MaskSecrets bool   // profile preview: render secrets as "••••"

	// L3 protocols (awg). For FormatMihomo, Fragment.Data is ONE proxy as an element of a YAML list built
	// from yaml.v3 nodes (never string templates: node and user names are hostile input); the assembler
	// adds groups, dns and rules.
	Label         string          // device label, for the config's comment / file name
	DisplayName   string          // server name as the subscription shows it (the assembler's remarks())
	Peer          json.RawMessage // decrypted credential secret of the device ({"priv","psk"} for awg)
	InboundPublic json.RawMessage // InboundInit.Public of this inbound
	DNS           []string        // at most 2 plain IPv4 servers (the pair an AWG client carries)
	NodeAddr      string          // host clients connect to (Inbound.Node.Address)
}

// Check is one doctor rule. The runner calls Run for every node that has an inbound of this protocol
// (Scope node) or once (Scope fleet).
type Check struct {
	ID    string
	Scope CheckScope
	Run   func(ctx context.Context, in CheckInput) []Finding
}

type CheckScope int

const (
	ScopeNode CheckScope = iota + 1
	ScopeFleet
)

type CheckInput struct {
	Node     NodeView
	Inbounds []InboundView
	Now      time.Time
	// Observations is filled by the health module: last apply results, latest metrics, synthetic check
	// results. Kept a map so the first version does not freeze its shape.
	Observations map[string]json.RawMessage
}

type Finding struct {
	Severity int // 1 info, 2 warning, 3 error
	Code     string
	Params   map[string]string
	FixID    string // "" = no one-click fix
}
