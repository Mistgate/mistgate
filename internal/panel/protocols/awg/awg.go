// Package awg is the panel half of the AmneziaWG protocol plugin: the
// settings schema, a validator stricter than both implementations, the obfuscation generator with its mimicry
// presets, the translation of a profile into a node-side inbound, the per-device X25519 credentials and the three
// client formats (.conf, vpn://, Mihomo proxy).
//
// One profile is one interface on one UDP port with one protocol version. A device is a peer bound to ONE profile
// and valid on every inbound of that profile; the server key is per inbound (InitInbound).
package awg

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/mimicry"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ID is the plugin id.
const ID = "awg"

// Protocol implements protocols.Protocol and the optional InboundInitializer, PerDevice and ClientRequirements.
type Protocol struct{}

// New returns the plugin.
func New() *Protocol { return &Protocol{} }

var (
	_ protocols.Protocol           = (*Protocol)(nil)
	_ protocols.InboundInitializer = (*Protocol)(nil)
	_ protocols.PerDevice          = (*Protocol)(nil)
	_ protocols.ClientRequirements = (*Protocol)(nil)
)

func (*Protocol) ID() string          { return ID }
func (*Protocol) DisplayName() string { return "AmneziaWG" }

func (*Protocol) SettingsSchema() []byte { return []byte(settingsSchema) }

// PerDevice: a credential belongs to one profile and is issued on request, not for every device.
func (*Protocol) PerDevice() bool { return true }

// Clients: the Amnezia apps take vpn:// and .conf, Mihomo-based apps the YAML proxy. Happ cannot carry AWG and
// is deliberately absent; the minimum versions depend on the profile (MinClients).
func (*Protocol) Clients() []plugin.ClientSupport {
	return []plugin.ClientSupport{
		{Client: plugin.ClientAmnezia, Formats: []plugin.ClientFormat{plugin.FormatAmneziaVPN, plugin.FormatAWGConf}},
		{Client: plugin.ClientMihomo, Formats: []plugin.ClientFormat{plugin.FormatMihomo}},
	}
}

func (*Protocol) Doctor() []protocols.Check { return nil } // the data is in AwgHealth

// Summary: "UDP 51842 · AWG 3.1 · QUIC".
func (*Protocol) Summary(raw json.RawMessage) string {
	s, errs := parse(raw)
	if errs != nil {
		return ""
	}
	parts := []string{"UDP " + strconv.Itoa(s.Port), "AWG " + s.Version}
	if l := mimicry.Label(s.Obfuscation.Preset); l != "" && s.Obfuscation.Preset != mimicry.Custom {
		parts = append(parts, l)
	}
	if s.Egress == "warp" {
		parts = append(parts, "WARP")
	}
	return strings.Join(parts, " · ")
}

// InitInbound makes the server key pair of one inbound (a hacked node must not give away the others).
// State is the private key in base64, Public is {"public_key": ...}.
func (*Protocol) InitInbound(protocols.InboundInitInput) (protocols.InboundInit, error) {
	priv, pub, err := genKeyPair()
	if err != nil {
		return protocols.InboundInit{}, err
	}
	public, err := json.Marshal(struct {
		PublicKey string `json:"public_key"`
	}{pub})
	if err != nil {
		return protocols.InboundInit{}, err
	}
	return protocols.InboundInit{State: []byte(priv), Public: public}, nil
}

// BuildInbound lifts port, egress and the tunnel (node address and MTU) out of the settings; the rest goes to the
// node as NodeSettings together with the server private key of this inbound. The caller sets InboundSpec.ID.
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
	priv := strings.TrimSpace(string(in.PluginState))
	if !validKey(priv) {
		return plugin.InboundSpec{}, fmt.Errorf("the server key of the inbound is missing or damaged")
	}
	v4, v6, err := serverAddrs(s)
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	settings, err := json.Marshal(nodeSettings(s, priv))
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	return plugin.InboundSpec{
		Protocol:  ID,
		ProfileID: in.Profile.ID,
		Version:   in.SpecVersion,
		Enabled:   in.Enabled,
		Listen:    plugin.Listen{Network: "udp", Port: uint16(port)},
		Egress:    s.Egress,
		Settings:  settings,
		Tunnel:    plugin.Tunnel{AddrV4: v4, AddrV6: v6, MTU: uint16(s.MTU)},
	}, nil
}

// IssueCredential makes the keys and the addresses of one device on one profile: X25519 key pair and a pre-shared
// key, addresses from the profile's networks by peer index. Secret (vault-sealed by the caller) keeps the private
// key, the PSK and the addresses; NodeData is what the nodes get and has no private key.
func (*Protocol) IssueCredential(in protocols.IssueInput) (protocols.Issued, error) {
	if in.ProfileID == "" || in.PeerIndex < 2 {
		return protocols.Issued{}, fmt.Errorf("an AWG credential belongs to one profile and needs a peer index")
	}
	s, errs := parse(in.Settings)
	if errs != nil {
		return protocols.Issued{}, fmt.Errorf("invalid profile settings: %s", errs[0].Message)
	}
	v4, v6, err := peerAddrs(s, in.PeerIndex)
	if err != nil {
		return protocols.Issued{}, err
	}
	priv, pub, err := genKeyPair()
	if err != nil {
		return protocols.Issued{}, err
	}
	psk, err := randomKey()
	if err != nil {
		return protocols.Issued{}, err
	}
	sec := peerSecret{Priv: priv, PSK: psk, IP4: v4.String()}
	nd := NodeData{PublicKey: pub, PSK: psk, AllowedIPs: []string{v4.String() + "/32"}}
	if v6.IsValid() {
		sec.IP6 = v6.String()
		nd.AllowedIPs = append(nd.AllowedIPs, v6.String()+"/128")
	}
	secret, err := json.Marshal(sec)
	if err != nil {
		return protocols.Issued{}, err
	}
	data, err := json.Marshal(nd)
	if err != nil {
		return protocols.Issued{}, err
	}
	return protocols.Issued{Secret: string(secret), NodeData: data}, nil
}
