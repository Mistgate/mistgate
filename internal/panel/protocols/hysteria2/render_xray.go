package hysteria2

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// jsonView is what the Xray and sing-box renderers share: the checked host, the pin, the secrets (masked on request)
// and the fallback server name. ok == false from newJSONView means the server is not rendered in these formats; note
// is then the code of the reason, when it has one.
type jsonView struct {
	note             string
	s                Settings
	host, pin, name  string
	password, obfsPw string
	selfSigned       bool
	salamander       bool
}

func newJSONView(in protocols.RenderInput) (jsonView, bool) {
	var v jsonView
	if err := json.Unmarshal(in.Settings, &v.s); err != nil {
		return v, false
	}
	// Gecko is in neither format: Xray's finalmask has no such mask, and sing-box carries it from 1.14 only.
	if v.s.Obfs.Type == "gecko" {
		v.note = plugin.NoteGecko
		return v, false
	}
	v.host = strings.Trim(in.Inbound.Node.Address, "[]")
	if !isIP(v.host) && !isHostname(v.host) {
		return v, false // never splice an address that is not a plain host into the outbound
	}
	pin, pinOK := protocols.NormalizePin(in.Inbound.CertPinSHA256)
	v.selfSigned = in.Inbound.TLSMode == plugin.TLSSelfSigned
	if v.selfSigned && !pinOK {
		return v, false // a client could not verify the node
	}
	v.pin = pin
	v.name = in.DisplayName
	if v.name == "" {
		n := in.Inbound.Node.Name
		if n == "" {
			n = v.host
		}
		v.name = fmt.Sprintf("%s · hy2 · %d", n, in.Inbound.Port)
	}
	v.password, v.obfsPw = in.Secret, v.s.Obfs.Password
	if in.MaskSecrets {
		v.password, v.obfsPw = protocols.MaskedSecret, protocols.MaskedSecret
	}
	v.salamander = v.s.Obfs.Type == "salamander"
	return v, true
}

// sni is the server name the client sends: the inbound's, unless it is an IP address.
func sni(in protocols.RenderInput) string {
	if n := in.Inbound.TLSServerName; n != "" && !isIP(n) {
		return n
	}
	return ""
}

type xrayHy2 struct {
	Protocol       string        `json:"protocol"`
	Settings       xrayHy2Server `json:"settings"`
	StreamSettings xrayHy2Stream `json:"streamSettings"`
}

type xrayHy2Server struct {
	Version int    `json:"version"`
	Address string `json:"address"`
	Port    uint16 `json:"port"`
}

type xrayHy2Stream struct {
	Network          string        `json:"network"`
	Security         string        `json:"security"`
	TLSSettings      xrayHy2TLS    `json:"tlsSettings"`
	HysteriaSettings xrayHy2Auth   `json:"hysteriaSettings"`
	FinalMask        *xrayHy2Masks `json:"finalmask,omitempty"`
}

type xrayHy2TLS struct {
	ServerName           string   `json:"serverName,omitempty"`
	ALPN                 []string `json:"alpn"`
	PinnedPeerCertSha256 string   `json:"pinnedPeerCertSha256,omitempty"`
}

type xrayHy2Auth struct {
	Version int    `json:"version"`
	Auth    string `json:"auth"`
}

type xrayHy2Masks struct {
	UDP []xrayHy2Mask `json:"udp"`
}

type xrayHy2Mask struct {
	Type     string            `json:"type"`
	Settings map[string]string `json:"settings"`
}

// renderXray returns the Hysteria2 outbound of an Xray config (xray-core infra/conf, v26.3.27 and later): protocol
// "hysteria" version 2, the auth string in hysteriaSettings, Salamander as a udp finalmask, and for a self-signed
// node `pinnedPeerCertSha256` (the sha256 of the whole certificate, the pin the node reported; with a pin the core
// skips the chain check, and it refuses `allowInsecure` after 2026-06-01). A hop range is not written: the udphop
// schema moved in Xray 26.9.9 and the old key is silently ignored, so the client gets the one port, as in the URI
// list.
func renderXray(in protocols.RenderInput) (plugin.Fragment, bool) {
	v, ok := newJSONView(in)
	if !ok {
		return plugin.Skipped(v.note)
	}
	o := xrayHy2{
		Protocol: "hysteria",
		Settings: xrayHy2Server{Version: 2, Address: v.host, Port: in.Inbound.Port},
		StreamSettings: xrayHy2Stream{
			Network: "hysteria", Security: "tls",
			TLSSettings:      xrayHy2TLS{ServerName: sni(in), ALPN: []string{"h3"}},
			HysteriaSettings: xrayHy2Auth{Version: 2, Auth: v.password},
		},
	}
	if v.selfSigned {
		o.StreamSettings.TLSSettings.PinnedPeerCertSha256 = v.pin
	}
	if v.salamander {
		o.StreamSettings.FinalMask = &xrayHy2Masks{UDP: []xrayHy2Mask{{Type: "salamander", Settings: map[string]string{"password": v.obfsPw}}}}
	}
	data, err := json.Marshal(o)
	if err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatXrayJSON, Name: v.name, Data: data, Note: hopNote(in)}, true
}
