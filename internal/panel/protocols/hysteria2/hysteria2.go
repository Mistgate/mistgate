// Package hysteria2 is the panel half of the Hysteria2 protocol plugin: the settings schema, validation,
// translation of a profile into a node-side inbound spec, credential issuing and the hysteria2:// URI.
//
// Node-side settings (plugin.InboundSpec.Settings, settings_json on the wire) are the profile settings
// minus what became first-class fields (port, hop, sni, tls_mode, egress): see NodeSettings.
package hysteria2

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ID is the plugin id.
const ID = "hysteria2"

// Settings is the merged profile settings document (secrets in place).
type Settings struct {
	Port                  int        `json:"port"`
	Hop                   Hop        `json:"hop"`
	SNI                   string     `json:"sni"`
	TLSMode               string     `json:"tls_mode"` // acme_domain | self_signed (acme_ip: later)
	Obfs                  Obfs       `json:"obfs"`
	Masquerade            Masquerade `json:"masquerade"`
	IgnoreClientBandwidth bool       `json:"ignore_client_bandwidth"`
	BBRProfile            string     `json:"bbr_profile"` // standard | conservative | aggressive
	UpMbps                int        `json:"up_mbps"`
	DownMbps              int        `json:"down_mbps"`
	UDP                   bool       `json:"udp"`
	Egress                string     `json:"egress"` // direct | warp
}

type Hop struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type Obfs struct {
	Type     string `json:"type"` // none | salamander | gecko
	Password string `json:"password"`
}

type Masquerade struct {
	Type string `json:"type"` // decoy | none (proxy, file, string: when the node supports them)
}

// NodeSettings is the JSON the node engine receives in InboundSpec.Settings. Field order and omitempty
// rules are part of the contract: the bytes feed the state hash, so they must be deterministic.
// Obfs.Password is present only when Obfs.Type is not "none". The node's optional masquerade.tcp_port is
// not sent: the node default (TCP 443 next to the QUIC port) applies.
type NodeSettings struct {
	Obfs                  NodeObfs `json:"obfs"`
	Masquerade            NodeMasq `json:"masquerade"`
	IgnoreClientBandwidth bool     `json:"ignore_client_bandwidth"`
	BBRProfile            string   `json:"bbr_profile"`
	UpMbps                int      `json:"up_mbps"`
	DownMbps              int      `json:"down_mbps"`
	UDP                   bool     `json:"udp"`
}

type NodeObfs struct {
	Type     string `json:"type"`
	Password string `json:"password,omitempty"`
}

type NodeMasq struct {
	Type string `json:"type"`
}

// Protocol implements protocols.Protocol.
type Protocol struct{}

// New returns the plugin.
func New() *Protocol { return &Protocol{} }

var _ protocols.Protocol = (*Protocol)(nil)

func (*Protocol) ID() string          { return ID }
func (*Protocol) DisplayName() string { return "Hysteria2" }

func (*Protocol) SettingsSchema() []byte { return []byte(settingsSchema) }

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (*Protocol) DefaultSettings() (json.RawMessage, error) {
	pw, err := randomToken(24) // 32 URL-safe chars
	if err != nil {
		return nil, err
	}
	// A new profile always arrives with its Salamander password generated (the form never shows an empty secret)
	// and with the client-declared speed ignored: otherwise one client can ask for Brutal at a huge rate and take
	// the whole node link. Stored profiles keep whatever they have.
	return json.Marshal(Settings{
		Port:                  443,
		TLSMode:               "acme_domain",
		Obfs:                  Obfs{Type: "salamander", Password: pw},
		Masquerade:            Masquerade{Type: "decoy"},
		IgnoreClientBandwidth: true,
		BBRProfile:            "standard",
		UDP:                   true,
		Egress:                "direct",
	})
}

func parse(raw json.RawMessage) (Settings, []protocols.FieldError) {
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return s, []protocols.FieldError{{Pointer: "/" + strings.ReplaceAll(te.Field, ".", "/"), Code: "invalid_type", Message: "wrong type"}}
		}
		return s, []protocols.FieldError{{Pointer: "", Code: "invalid_json", Message: err.Error()}}
	}
	return s, nil
}

func fe(ptr, code, msg string) protocols.FieldError {
	return protocols.FieldError{Pointer: ptr, Code: code, Message: msg}
}

func (*Protocol) Validate(raw json.RawMessage) []protocols.FieldError {
	s, errs := parse(raw)
	if errs != nil {
		return errs
	}
	return append(validate(s), validateHop(s)...)
}

// The hop limits the node enforces (hostctl.ValidateHop, F12), checked here so that the form shows a field
// error instead of a node-side refusal. Starting at 1024 also keeps SSH port 22 (and every well-known port)
// out of the range.
const (
	minHopPort = 1024
	maxHopSpan = 20000 // ports in the range
)

// validateHop is the strict part of the hop check. It is applied when a profile is written (Validate) and not
// when an inbound is built from a stored profile, so a profile saved before these limits existed keeps
// building; the node refuses such a range on its own (F12).
func validateHop(s Settings) []protocols.FieldError {
	h := s.Hop
	if h.From < 1 || h.To > 65535 || h.From >= h.To { // the base check already said what is wrong
		return nil
	}
	if h.From < minHopPort {
		return []protocols.FieldError{fe("/hop/from", "out_of_range", "hop range must start at 1024 or above (it must not cover SSH port 22 or other well-known ports)")}
	}
	if h.To-h.From+1 > maxHopSpan {
		return []protocols.FieldError{fe("/hop/to", "too_wide", "hop range may hold at most 20000 ports")}
	}
	return nil
}

func validate(s Settings) []protocols.FieldError {
	var errs []protocols.FieldError
	add := func(ptr, code, msg string) { errs = append(errs, fe(ptr, code, msg)) }

	if s.Port < 1 || s.Port > 65535 {
		add("/port", "out_of_range", "port must be 1-65535")
	}
	if s.Hop.From != 0 || s.Hop.To != 0 {
		okFrom, okTo := s.Hop.From >= 1 && s.Hop.From <= 65535, s.Hop.To >= 1 && s.Hop.To <= 65535
		if !okFrom {
			add("/hop/from", "out_of_range", "hop range start must be 1-65535 (or 0 and 0 to turn hopping off)")
		}
		if !okTo {
			add("/hop/to", "out_of_range", "hop range end must be 1-65535 (or 0 and 0 to turn hopping off)")
		}
		if okFrom && okTo {
			if s.Hop.From >= s.Hop.To {
				add("/hop/to", "invalid", "hop range end must be greater than its start")
			} else if s.Port >= s.Hop.From && s.Port <= s.Hop.To {
				add("/hop/from", "invalid", "the port must be outside the hop range")
			}
		}
	}
	switch s.TLSMode {
	case "acme_domain", "self_signed":
	case "acme_ip":
		add("/tls_mode", "unsupported", "IP certificates are not available yet")
	default:
		add("/tls_mode", "invalid_enum", "must be acme_domain or self_signed")
	}
	if s.SNI != "" {
		if err := checkServerName(s.SNI, s.TLSMode == "acme_domain"); err != nil {
			add("/sni", "invalid", err.Error())
		}
	}
	switch s.Obfs.Type {
	case "none":
	case "salamander", "gecko":
		if n := len(s.Obfs.Password); n < 8 || n > 128 {
			add("/obfs/password", "length", "password must be 8-128 characters")
		} else if strings.ContainsFunc(s.Obfs.Password, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			add("/obfs/password", "invalid", "password must not contain control characters")
		}
	default:
		add("/obfs/type", "invalid_enum", "must be none, salamander or gecko")
	}
	switch s.Masquerade.Type {
	case "decoy", "none":
	default:
		add("/masquerade/type", "invalid_enum", "must be decoy or none")
	}
	switch s.BBRProfile {
	case "standard", "conservative", "aggressive":
	default:
		add("/bbr_profile", "invalid_enum", "must be standard, conservative or aggressive")
	}
	if s.UpMbps < 0 || s.UpMbps > 100000 {
		add("/up_mbps", "out_of_range", "must be 0-100000")
	}
	if s.DownMbps < 0 || s.DownMbps > 100000 {
		add("/down_mbps", "out_of_range", "must be 0-100000")
	}
	switch s.Egress {
	case "direct", "warp": // warp needs a WARP account on the node; without one the node refuses to start the inbound (fail closed)
	default:
		add("/egress", "invalid_enum", "must be direct or warp")
	}
	return errs
}

func (p *Protocol) Summary(raw json.RawMessage) string {
	s, errs := parse(raw)
	if errs != nil {
		return ""
	}
	parts := []string{"UDP " + strconv.Itoa(s.Port)}
	if s.Hop.From != 0 && s.Hop.To != 0 {
		parts = append(parts, fmt.Sprintf("hop %d-%d", s.Hop.From, s.Hop.To))
	}
	switch s.Obfs.Type {
	case "salamander":
		parts = append(parts, "Salamander")
	case "gecko":
		parts = append(parts, "Gecko")
	default:
		parts = append(parts, "No obfuscation")
	}
	switch s.TLSMode {
	case "acme_domain":
		parts = append(parts, "Let's Encrypt")
	case "acme_ip":
		parts = append(parts, "Let's Encrypt IP")
	case "self_signed":
		parts = append(parts, "Self-signed")
	}
	if s.Egress == "warp" {
		parts = append(parts, "WARP")
	}
	return strings.Join(parts, " · ")
}

// BuildInbound lifts port, hop, TLS and egress out of the settings into first-class spec fields.
// The caller (access) sets InboundSpec.ID: InboundInput does not carry it.
func (p *Protocol) BuildInbound(in protocols.InboundInput) (plugin.InboundSpec, error) {
	s, errs := parse(in.Profile.Settings)
	if errs == nil {
		errs = validate(s)
	}
	if len(errs) > 0 {
		return plugin.InboundSpec{}, fmt.Errorf("invalid profile settings: %s %s", errs[0].Pointer, errs[0].Message)
	}
	port := s.Port
	if in.PortOverride != 0 {
		port = int(in.PortOverride)
		if s.Hop.From != 0 && port >= s.Hop.From && port <= s.Hop.To {
			return plugin.InboundSpec{}, &protocols.PortInHopError{From: s.Hop.From, To: s.Hop.To}
		}
	}
	name := in.TLSServerNameOverride
	if name == "" {
		name = s.SNI
	}
	if name == "" && isHostname(in.Node.Address) {
		name = in.Node.Address
	}
	mode := plugin.TLSAcmeDomain
	switch s.TLSMode {
	case "acme_domain":
		if in.TLSServerNameOverride != "" && !isHostname(in.TLSServerNameOverride) {
			return plugin.InboundSpec{}, &protocols.ServerNameError{Name: in.TLSServerNameOverride, DomainOnly: true}
		}
		if name == "" || !isHostname(name) {
			return plugin.InboundSpec{}, &protocols.NeedsDomainError{Address: in.Node.Address}
		}
	case "self_signed":
		mode = plugin.TLSSelfSigned
		if name == "" {
			name = in.Node.Address // the agent puts the IP into the certificate
		}
	}
	if in.TLSServerNameOverride != "" {
		if err := checkServerName(name, s.TLSMode == "acme_domain"); err != nil {
			return plugin.InboundSpec{}, &protocols.ServerNameError{Name: name, DomainOnly: s.TLSMode == "acme_domain"}
		}
	}
	ns := NodeSettings{
		Obfs:                  NodeObfs{Type: s.Obfs.Type},
		Masquerade:            NodeMasq{Type: s.Masquerade.Type},
		IgnoreClientBandwidth: s.IgnoreClientBandwidth,
		BBRProfile:            s.BBRProfile,
		UpMbps:                s.UpMbps,
		DownMbps:              s.DownMbps,
		UDP:                   s.UDP,
	}
	if s.Obfs.Type != "none" {
		ns.Obfs.Password = s.Obfs.Password
	}
	settings, err := json.Marshal(ns)
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	return plugin.InboundSpec{
		Protocol:  ID,
		ProfileID: in.Profile.ID,
		Version:   in.SpecVersion,
		Enabled:   in.Enabled,
		Listen:    plugin.Listen{Network: "udp", Port: uint16(port), HopFrom: uint16(s.Hop.From), HopTo: uint16(s.Hop.To)},
		TLS:       plugin.TLS{Mode: mode, ServerName: name},
		Egress:    s.Egress,
		Settings:  settings,
	}, nil
}

// IssueCredential makes a random 32-byte token. The client keeps the token; nodes get sha256(token) hex,
// which is what the engine computes from the auth string of a connecting client.
func (*Protocol) IssueCredential(protocols.IssueInput) (protocols.Issued, error) {
	tok, err := randomToken(32)
	if err != nil {
		return protocols.Issued{}, err
	}
	sum := sha256.Sum256([]byte(tok))
	data, err := json.Marshal(map[string]string{"auth_sha256": hex.EncodeToString(sum[:])})
	if err != nil {
		return protocols.Issued{}, err
	}
	return protocols.Issued{Secret: tok, NodeData: data}, nil
}

// Clients: Happ (and other Xray/sing-box based apps that take a URI list) carry Hysteria2, and so do the Mihomo
// core and its apps (Clash Verge, FlClash, ...) from a Mihomo YAML profile; Amnezia does not.
// The xray-json and sing-box formats are not rendered.
func (*Protocol) Clients() []plugin.ClientSupport {
	return []plugin.ClientSupport{
		{Client: plugin.ClientHapp, Formats: []plugin.ClientFormat{plugin.FormatURIList}},
		{Client: plugin.ClientMihomo, Formats: []plugin.ClientFormat{plugin.FormatMihomo}},
	}
}

func (*Protocol) Doctor() []protocols.Check { return nil }

// Render returns the hysteria2:// URI. Format: hysteria2://<auth>@<host>:<port>/?insecure=1&obfs=..&obfs-password=..
// &pinSHA256=..&sni=..#<name>, query keys in alphabetical order as in the scheme document
// (https://v2.hysteria.network/docs/developers/URI-Scheme/); pinSHA256 is plain lowercase hex.
// One port only; the hop range is not put into the URI until the multi-port
// form is verified against Happ.
func (*Protocol) Render(in protocols.RenderInput) (plugin.Fragment, bool) {
	switch in.Format {
	case plugin.FormatURIList:
	case plugin.FormatMihomo:
		return renderMihomo(in)
	default:
		return plugin.Fragment{}, false
	}
	var s Settings
	if err := json.Unmarshal(in.Settings, &s); err != nil {
		return plugin.Fragment{}, false
	}
	host := in.Inbound.Node.Address
	if !isIP(host) && !isHostname(host) {
		return plugin.Fragment{}, false // never splice an address that is not a plain host into the authority
	}
	name := in.Inbound.Node.Name
	if name == "" {
		name = host
	}
	frag := fmt.Sprintf("%s · hy2 · %d", name, in.Inbound.Port)

	auth, obfsPw := url.User(in.Secret).String(), url.QueryEscape(s.Obfs.Password)
	if in.MaskSecrets {
		auth, obfsPw = protocols.MaskedSecret, protocols.MaskedSecret // shown as text, not escaped
	}
	var q []string
	// The pin was reported by the node: anything but 64 hex digits is not a pin (then the client gets no
	// insecure=1 either and refuses the certificate, instead of a line the node wrote).
	pin, pinOK := protocols.NormalizePin(in.Inbound.CertPinSHA256)
	pinned := in.Inbound.TLSMode == plugin.TLSSelfSigned && pinOK
	if pinned {
		// The client checks the pin on top of normal verification, so a self-signed certificate needs
		// insecure=1 next to it (hysteria app/cmd/client.go); without a pin that would trust anyone.
		q = append(q, "insecure=1")
	}
	if s.Obfs.Type == "salamander" || s.Obfs.Type == "gecko" {
		q = append(q, "obfs="+s.Obfs.Type, "obfs-password="+obfsPw)
	}
	if pinned {
		q = append(q, "pinSHA256="+url.QueryEscape(pin))
	}
	if sni := in.Inbound.TLSServerName; sni != "" && !isIP(sni) {
		q = append(q, "sni="+url.QueryEscape(sni))
	}
	uri := "hysteria2://" + auth + "@" + net.JoinHostPort(host, strconv.Itoa(int(in.Inbound.Port))) + "/"
	if len(q) > 0 {
		uri += "?" + strings.Join(q, "&")
	}
	uri += "#" + (&url.URL{Fragment: frag}).EscapedFragment()
	return plugin.Fragment{Format: plugin.FormatURIList, Name: frag, Data: []byte(uri)}, true
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(strings.Trim(s, "[]"))
	return err == nil
}

// isHostname: a syntactically valid DNS name that is not an IP literal.
func isHostname(s string) bool {
	if s == "" || len(s) > 253 || isIP(s) {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func checkServerName(s string, hostnameOnly bool) error {
	if hostnameOnly {
		if !isHostname(s) {
			return errors.New("must be a host name (not an IP address) for a Let's Encrypt certificate")
		}
		return nil
	}
	if !isHostname(s) && !isIP(s) {
		return errors.New("must be a host name or an IP address")
	}
	return nil
}
