// Package mtproto is the panel half of the Telegram MTProto proxy plugin: the
// profile schema, validation, node-side inbound construction, credential
// issuing and the tg://proxy link.
package mtproto

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

const ID = "mtproto"

const (
	defaultPort = 443
	keyLen      = 16
)

// The choice of the Fake TLS name, as REALITY's target is a choice: the node's own name (the name of the node, or of its
// inbound) or another site that the owner names.
const (
	nameOwn  = "own"
	nameSite = "site"
)

// Settings is the merged profile settings document.
type Settings struct {
	Port    int          `json:"port"`
	FakeTLS FakeTLSNames `json:"fake_tls"`
}

// FakeTLSNames is where the name inside the Fake TLS handshake (the domain at the end of the ee secret) comes from. Kind own:
// the node's own host name, or the one set on the inbound; a node with only an IP address cannot use it. Kind site: Domain,
// a real site that is not this server's. A document that names a domain and no kind (read before the field existed) is site.
type FakeTLSNames struct {
	Kind   string `json:"kind"`
	Domain string `json:"domain"`
}

func (n FakeTLSNames) kind() string {
	switch {
	case n.Kind != "":
		return n.Kind
	case strings.TrimSpace(n.Domain) != "":
		return nameSite
	}
	return nameOwn
}

// What the node does with a connection that is not a client: Fallback decoy answers with the node's own site, site forwards
// it to the chosen site, so a prober sees that site and its certificate.
const (
	FallbackDecoy = "decoy"
	FallbackSite  = "site"
)

// NodeSettings is the JSON the node engine receives in InboundSpec.Settings. Domains are the names a client may put in the
// Fake TLS handshake (one today); Fallback is FallbackDecoy for the node's own name and FallbackSite for another site, which
// the node reaches at Domains[0]:443. Field order is deterministic because the bytes feed the state hash.
type NodeSettings struct {
	Domains  []string `json:"domains"`
	Fallback string   `json:"fallback"`
}

type Protocol struct{}

func New() *Protocol { return &Protocol{} }

var (
	_ protocols.Protocol     = (*Protocol)(nil)
	_ protocols.Experimental = (*Protocol)(nil)
)

// Experimental: the first Go port of the protocol, not proven in the field yet.
func (*Protocol) Experimental() bool { return true }

func (*Protocol) ID() string          { return ID }
func (*Protocol) DisplayName() string { return "MTProto" }

func (*Protocol) SettingsSchema() []byte { return []byte(settingsSchema) }

func (*Protocol) DefaultSettings() (json.RawMessage, error) {
	return json.Marshal(Settings{Port: defaultPort, FakeTLS: FakeTLSNames{Kind: nameOwn}})
}

func parse(raw json.RawMessage) (Settings, []protocols.FieldError) {
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return s, []protocols.FieldError{{
				Pointer: "/" + strings.ReplaceAll(te.Field, ".", "/"),
				Code:    "invalid_type",
				Message: "wrong type",
			}}
		}
		return s, []protocols.FieldError{{Pointer: "", Code: "invalid_json", Message: err.Error()}}
	}
	return s, nil
}

func fe(ptr, code, msg string) protocols.FieldError {
	return protocols.FieldError{Pointer: ptr, Code: code, Message: msg}
}

func validate(s Settings) []protocols.FieldError {
	var errs []protocols.FieldError
	if s.Port < 1 || s.Port > 65535 {
		errs = append(errs, fe("/port", "out_of_range", "port must be 1-65535"))
	}
	switch s.FakeTLS.kind() {
	case nameOwn: // the node's name comes from the node or the inbound (BuildInbound)
	case nameSite:
		if !validSite(s.FakeTLS.Domain) {
			errs = append(errs, fe("/fake_tls/domain", "invalid", "must be a host name such as www.google.com: no IP address, no port, no path"))
		}
	default:
		errs = append(errs, fe("/fake_tls/kind", "invalid_enum", "kind must be own or site"))
	}
	return errs
}

// validSite: a host name with a dot (a TLS name has one), in the form a certificate carries it.
func validSite(domain string) bool {
	d := strings.TrimSpace(domain)
	return protocols.IsHostname(d) && strings.Contains(d, ".")
}

// fakeName is the name a profile and an inbound put into the Fake TLS handshake, and what the node does with a stranger.
// Own: the inbound's name, else the node address when it is a host name; an IP-only node has none (NeedsDomainError).
func fakeName(s Settings, in protocols.InboundInput) (name, fallback string, err error) {
	if s.FakeTLS.kind() == nameSite {
		return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s.FakeTLS.Domain), ".")), FallbackSite, nil
	}
	if o := strings.TrimSpace(in.TLSServerNameOverride); o != "" {
		name = strings.ToLower(strings.TrimSuffix(o, "."))
		if !validSite(name) {
			return "", "", &protocols.ServerNameError{Name: o, DomainOnly: true}
		}
		return name, FallbackDecoy, nil
	}
	name = strings.ToLower(strings.TrimSuffix(in.Node.Address, "."))
	if validSite(name) {
		return name, FallbackDecoy, nil
	}
	return "", "", &protocols.NeedsDomainError{Address: in.Node.Address, MTProto: true}
}

func (*Protocol) Validate(raw json.RawMessage) []protocols.FieldError {
	s, errs := parse(raw)
	if errs != nil {
		return errs
	}
	return validate(s)
}

func (p *Protocol) Summary(raw json.RawMessage) string {
	s, errs := parse(raw)
	if errs != nil {
		return ""
	}
	if s.FakeTLS.kind() == nameSite && validSite(s.FakeTLS.Domain) {
		return fmt.Sprintf("TCP %d · Fake TLS · %s", s.Port, strings.ToLower(strings.TrimSpace(s.FakeTLS.Domain)))
	}
	return fmt.Sprintf("TCP %d · Fake TLS", s.Port)
}

func (*Protocol) BuildInbound(in protocols.InboundInput) (plugin.InboundSpec, error) {
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
	}
	name, fallback, err := fakeName(s, in)
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	settings, err := json.Marshal(NodeSettings{Domains: []string{name}, Fallback: fallback})
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	return plugin.InboundSpec{
		Listen: plugin.Listen{Network: "tcp", Port: uint16(port)},
		// The name is also the inbound's server name: it is what the link carries, also through a relay (the entry's address is
		// another, the name stays the exit's). No certificate is made for it.
		TLS:      plugin.TLS{ServerName: name},
		Egress:   "direct",
		Settings: settings,
	}, nil
}

// IssueCredential makes one MTProto key. Secret is the key in hex (what the panel stores sealed); the client link
// secret (ee + key + SNI in hex) is built from it when the link is rendered, because the SNI depends on the node.
// NodeData is what the node stores.
func (*Protocol) IssueCredential(protocols.IssueInput) (protocols.Issued, error) {
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return protocols.Issued{}, err
	}
	keyHex := hex.EncodeToString(key)
	data, err := json.Marshal(map[string]string{"key": keyHex})
	if err != nil {
		return protocols.Issued{}, err
	}
	return protocols.Issued{Secret: keyHex, NodeData: data}, nil
}

func (*Protocol) Clients() []plugin.ClientSupport {
	return []plugin.ClientSupport{{
		Client:  plugin.ClientTelegram,
		Formats: []plugin.ClientFormat{plugin.FormatTelegramLink},
	}}
}

func (*Protocol) Doctor() []protocols.Check { return nil }

// Render returns one tg://proxy link, for FormatTelegramLink only. Every subscription format answers false: a
// tg:// line would be a bogus server in Happ and in every other app of the link.
func (*Protocol) Render(in protocols.RenderInput) (plugin.Fragment, bool) {
	if in.Format != plugin.FormatTelegramLink {
		return plugin.Fragment{}, false
	}
	host := in.NodeAddr
	if host == "" {
		host = in.Inbound.Node.Address
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n/#?") {
		return plugin.Fragment{}, false
	}
	secret := in.Secret
	if in.MaskSecrets { // the profile editor's preview has no credential: it shows the link with the secret hidden
		secret = protocols.MaskedSecret
	}
	if !in.MaskSecrets {
		key, err := hex.DecodeString(secret)
		if err != nil || len(key) != keyLen || in.Inbound.TLSServerName == "" {
			return plugin.Fragment{}, false // no key, or no name for the handshake: no link
		}
		secret = "ee" + secret + hex.EncodeToString([]byte(in.Inbound.TLSServerName))
	}
	if secret == "" {
		return plugin.Fragment{}, false
	}
	q := url.Values{}
	q.Set("server", host)
	q.Set("port", strconv.Itoa(int(in.Inbound.Port)))
	q.Set("secret", secret)
	link := "tg://proxy?" + q.Encode()

	name := in.DisplayName
	if name == "" {
		name = in.Inbound.Node.Name
	}
	if name == "" {
		name = host
	}
	label := fmt.Sprintf("%s - Telegram - %d", name, in.Inbound.Port)
	return plugin.Fragment{
		Format: plugin.FormatTelegramLink,
		Name:   label,
		Data:   []byte(link),
	}, true
}
