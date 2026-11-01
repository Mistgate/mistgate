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
	defaultSNI  = "www.google.com"
	keyLen      = 16
)

// Settings is the merged profile settings document. The Fake TLS SNI is
// fixed for the first version so one credential works on every MTProto
// profile of the user's groups; per-profile SNI needs profile-scoped
// credentials and comes later.
type Settings struct {
	Port int `json:"port"`
}

// NodeSettings is the JSON the node engine receives in InboundSpec.Settings.
// Field order is deterministic because the bytes feed the state hash.
type NodeSettings struct {
	SNI string `json:"sni"`
}

type Protocol struct{}

func New() *Protocol { return &Protocol{} }

var _ protocols.Protocol = (*Protocol)(nil)

func (*Protocol) ID() string          { return ID }
func (*Protocol) DisplayName() string { return "MTProto" }

func (*Protocol) SettingsSchema() []byte { return []byte(settingsSchema) }

func (*Protocol) DefaultSettings() (json.RawMessage, error) {
	return json.Marshal(Settings{Port: defaultPort})
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
	return errs
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
	return fmt.Sprintf("TCP %d - Fake TLS", s.Port)
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
	settings, err := json.Marshal(NodeSettings{SNI: defaultSNI})
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	return plugin.InboundSpec{
		Listen:   plugin.Listen{Network: "tcp", Port: uint16(port)},
		Egress:   "direct",
		Settings: settings,
	}, nil
}

// IssueCredential makes one MTProto key. Secret is the client link secret
// (ee + key + SNI in hex); NodeData is the verifier the node stores.
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
	secret := "ee" + keyHex + hex.EncodeToString([]byte(defaultSNI))
	return protocols.Issued{Secret: secret, NodeData: data}, nil
}

func (*Protocol) Clients() []plugin.ClientSupport {
	return []plugin.ClientSupport{{
		Client:  plugin.ClientTelegram,
		Formats: []plugin.ClientFormat{plugin.FormatURIList},
	}}
}

func (*Protocol) Doctor() []protocols.Check { return nil }

// Render returns one tg://proxy link. Telegram clients read a URI list the same
// way they read a shared proxy link.
func (*Protocol) Render(in protocols.RenderInput) (plugin.Fragment, bool) {
	if in.Format != plugin.FormatURIList {
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
	if secret == "" {
		return plugin.Fragment{}, false
	}
	if in.MaskSecrets {
		secret = protocols.MaskedSecret
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
		Format: plugin.FormatURIList,
		Name:   label,
		Data:   []byte(link),
	}, true
}
