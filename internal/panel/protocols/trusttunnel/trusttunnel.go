// Package trusttunnel is the panel half of the TrustTunnel protocol plugin.
package trusttunnel

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

const ID = "trusttunnel"

// Settings is the merged profile settings document.
type Settings struct {
	Port    int    `json:"port"`
	H3      bool   `json:"h3"`
	SNI     string `json:"sni"`
	TLSMode string `json:"tls_mode"` // acme_domain | self_signed
	Egress  string `json:"egress"`   // direct | warp
}

// NodeSettings is the byte-stable JSON consumed by internal/node/trusttunnel.
type NodeSettings struct {
	H3 bool `json:"h3"`
}

// NodeCredential is the byte-stable verifier shipped to internal/node/trusttunnel.
// Keep this field order: the exact JSON bytes participate in the credential hash.
type NodeCredential struct {
	User       string `json:"user"`
	PassSHA256 string `json:"pass_sha256"`
}

type Protocol struct{}

func New() *Protocol                        { return &Protocol{} }
func (*Protocol) ID() string                { return ID }
func (*Protocol) DisplayName() string       { return "TrustTunnel" }
func (*Protocol) Experimental() bool        { return true }
func (*Protocol) SettingsSchema() []byte    { return []byte(settingsSchema) }
func (*Protocol) Doctor() []protocols.Check { return nil }
func (*Protocol) Clients() []plugin.ClientSupport {
	return []plugin.ClientSupport{{Client: plugin.ClientMistgate, Formats: []plugin.ClientFormat{plugin.FormatMistgate}}}
}

var _ protocols.Protocol = (*Protocol)(nil)

func (*Protocol) DefaultSettings() (json.RawMessage, error) {
	return json.Marshal(Settings{Port: 443, TLSMode: "acme_domain", Egress: "direct"})
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

func fieldError(ptr, code, message string) protocols.FieldError {
	return protocols.FieldError{Pointer: ptr, Code: code, Message: message}
}

func (*Protocol) Validate(raw json.RawMessage) []protocols.FieldError {
	s, errs := parse(raw)
	if errs != nil {
		return errs
	}
	var out []protocols.FieldError
	add := func(ptr, code, message string) { out = append(out, fieldError(ptr, code, message)) }
	if s.Port < 1 || s.Port > 65535 {
		add("/port", "out_of_range", "port must be 1-65535")
	}
	switch s.TLSMode {
	case "acme_domain", "self_signed":
	default:
		add("/tls_mode", "invalid_enum", "must be acme_domain or self_signed")
	}
	if len(s.SNI) > 253 {
		add("/sni", "length", "server name must be at most 253 characters")
	} else if s.SNI != "" {
		if !validServerName(s.SNI, s.TLSMode == "self_signed") {
			if s.TLSMode == "acme_domain" {
				add("/sni", "invalid", "must be a host name (not an IP address) for a Let's Encrypt certificate")
			} else {
				add("/sni", "invalid", "must be a host name or an IP address")
			}
		}
	}
	switch s.Egress {
	case "direct", "warp":
	default:
		add("/egress", "invalid_enum", "must be direct or warp")
	}
	return out
}

func (p *Protocol) Summary(raw json.RawMessage) string {
	s, errs := parse(raw)
	if errs != nil || len(validateSettings(s)) != 0 {
		return ""
	}
	cert := "Let's Encrypt"
	if s.TLSMode == "self_signed" {
		cert = "Self-signed"
	}
	port, transport := "TCP "+strconv.Itoa(s.Port), "HTTP/2"
	if s.H3 {
		port, transport = port+" + UDP "+strconv.Itoa(s.Port), transport+" + HTTP/3"
	}
	parts := []string{port, transport, cert}
	if s.Egress == "warp" {
		parts = append(parts, "WARP")
	}
	return strings.Join(parts, " · ")
}

func validateSettings(s Settings) []protocols.FieldError {
	var out []protocols.FieldError
	if s.Port < 1 || s.Port > 65535 {
		out = append(out, fieldError("/port", "out_of_range", "port must be 1-65535"))
	}
	if s.TLSMode != "acme_domain" && s.TLSMode != "self_signed" {
		out = append(out, fieldError("/tls_mode", "invalid_enum", "must be acme_domain or self_signed"))
	}
	if len(s.SNI) > 253 {
		out = append(out, fieldError("/sni", "length", "server name must be at most 253 characters"))
	} else if s.SNI != "" && !validServerName(s.SNI, s.TLSMode == "self_signed") {
		out = append(out, fieldError("/sni", "invalid", "server name is not valid for the selected certificate"))
	}
	if s.Egress != "direct" && s.Egress != "warp" {
		out = append(out, fieldError("/egress", "invalid_enum", "must be direct or warp"))
	}
	return out
}

func validServerName(name string, allowIP bool) bool {
	if protocols.IsHostname(name) {
		return true
	}
	if !allowIP {
		return false
	}
	_, err := netip.ParseAddr(strings.Trim(name, "[]"))
	return err == nil
}

func (p *Protocol) BuildInbound(in protocols.InboundInput) (plugin.InboundSpec, error) {
	s, errs := parse(in.Profile.Settings)
	if errs == nil {
		errs = validateSettings(s)
	}
	if len(errs) > 0 {
		return plugin.InboundSpec{}, fmt.Errorf("invalid profile settings: %s %s", errs[0].Pointer, errs[0].Message)
	}
	port := s.Port
	if in.PortOverride != 0 {
		port = int(in.PortOverride)
	}

	name := in.TLSServerNameOverride
	if name == "" {
		name = s.SNI
	}
	if name == "" {
		name = strings.Trim(in.Node.Address, "[]")
	}
	mode := plugin.TLSAcmeDomain
	allowIP := false
	if s.TLSMode == "self_signed" {
		mode, allowIP = plugin.TLSSelfSigned, true
	}
	if name == "" || !validServerName(name, allowIP) {
		if in.TLSServerNameOverride != "" {
			return plugin.InboundSpec{}, &protocols.ServerNameError{Name: name, DomainOnly: !allowIP}
		}
		if !allowIP {
			return plugin.InboundSpec{}, &protocols.NeedsDomainError{Address: in.Node.Address}
		}
		return plugin.InboundSpec{}, &protocols.ServerNameError{Name: name}
	}
	if in.TLSServerNameOverride != "" && len(name) > 253 {
		return plugin.InboundSpec{}, &protocols.ServerNameError{Name: name, DomainOnly: !allowIP}
	}

	settings, err := json.Marshal(NodeSettings{H3: s.H3})
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	return plugin.InboundSpec{
		Protocol: ID, ProfileID: in.Profile.ID, Version: in.SpecVersion, Enabled: in.Enabled,
		Listen: plugin.Listen{Network: "tcp", Port: uint16(port)},
		TLS:    plugin.TLS{Mode: mode, ServerName: name}, Egress: s.Egress, Settings: settings,
	}, nil
}

func (*Protocol) IssueCredential(protocols.IssueInput) (protocols.Issued, error) {
	var entropy [10]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return protocols.Issued{}, err
	}
	username := "u" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(entropy[:]))[:15]
	password, err := randomBase62(32)
	if err != nil {
		return protocols.Issued{}, err
	}
	data, err := encodeNodeCredential(username, password)
	if err != nil {
		return protocols.Issued{}, err
	}
	return protocols.Issued{Secret: username + ":" + password, NodeData: data}, nil
}

func randomBase62(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const cutoff = byte(248) // 248 is the largest multiple of 62 below 256; this avoids modulo bias.
	out := make([]byte, 0, n)
	for len(out) < n {
		buf := make([]byte, n-len(out))
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b < cutoff {
				out = append(out, alphabet[int(b)%len(alphabet)])
				if len(out) == n {
					break
				}
			}
		}
	}
	return string(out), nil
}

func encodeNodeCredential(username, password string) (json.RawMessage, error) {
	sum := sha256.Sum256([]byte(password))
	return json.Marshal(NodeCredential{User: username, PassSHA256: hex.EncodeToString(sum[:])})
}

func (*Protocol) Render(in protocols.RenderInput) (plugin.Fragment, bool) {
	if in.Format != plugin.FormatMistgate {
		return plugin.Fragment{}, false
	}
	return renderMistgate(in)
}
