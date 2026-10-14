package vless

import (
	"encoding/json"
	"fmt"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

type xrayVless struct {
	Protocol       string          `json:"protocol"`
	Settings       xrayVlessConfig `json:"settings"`
	StreamSettings xrayStream      `json:"streamSettings"`
}

type xrayVlessConfig struct {
	Vnext []xrayVnext `json:"vnext"`
}

type xrayVnext struct {
	Address string     `json:"address"`
	Port    uint16     `json:"port"`
	Users   []xrayUser `json:"users"`
}

type xrayUser struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption"`
	Flow       string `json:"flow,omitempty"`
}

type xrayStream struct {
	Network         string       `json:"network"`
	Security        string       `json:"security"`
	TLSSettings     *xrayTLS     `json:"tlsSettings,omitempty"`
	RealitySettings *xrayReality `json:"realitySettings,omitempty"`
	XHTTPSettings   *xrayXHTTP   `json:"xhttpSettings,omitempty"`
	WSSettings      *xrayWS      `json:"wsSettings,omitempty"`
}

type xrayTLS struct {
	ServerName  string   `json:"serverName"`
	Fingerprint string   `json:"fingerprint"`
	ALPN        []string `json:"alpn,omitempty"`
}

type xrayReality struct {
	ServerName  string `json:"serverName"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	ShortID     string `json:"shortId"`
	SpiderX     string `json:"spiderX"`
}

type xrayXHTTP struct {
	Path          string `json:"path"`
	Host          string `json:"host,omitempty"`
	Mode          string `json:"mode"`
	XPaddingBytes string `json:"xPaddingBytes,omitempty"`
}

type xrayWS struct {
	Path string `json:"path"`
	Host string `json:"host,omitempty"`
}

// renderXray returns the VLESS outbound of an Xray config in the `vnext` form, which the old and the new cores read.
// Networks: tcp (REALITY + Vision), xhttp (REALITY or TLS) and ws (TLS). The XHTTP padding is written as the direct
// `xPaddingBytes` key of xhttpSettings, not inside `extra`: the URI needs `extra` because a link has no such key,
// the JSON does, and conf.SplitHTTPConfig.Build replaces the whole object by `extra` when it is present (only host,
// path and mode survive from the outer one), so the two are the same to the core and the direct one is simpler.
// The default padding is left out, as in the other formats.
func renderXray(in protocols.RenderInput) (plugin.Fragment, bool) {
	v, ok := renderView(in)
	if !ok {
		return plugin.Fragment{}, false
	}
	p := v.params
	uuid := p.UUID
	if in.MaskSecrets {
		uuid = protocols.MaskedSecret
	}
	o := xrayVless{
		Protocol: "vless",
		Settings: xrayVlessConfig{Vnext: []xrayVnext{{
			Address: p.Host, Port: p.Port,
			Users: []xrayUser{{ID: uuid, Encryption: "none", Flow: p.Flow}},
		}}},
		StreamSettings: xrayStream{Network: p.Transport, Security: p.Security},
	}
	st := &o.StreamSettings
	switch p.Security {
	case "reality":
		st.RealitySettings = &xrayReality{ServerName: p.SNI, Fingerprint: p.Fingerprint, PublicKey: p.PublicKey, ShortID: p.ShortID, SpiderX: p.SpiderX}
	case "tls":
		st.TLSSettings = &xrayTLS{ServerName: p.SNI, Fingerprint: p.Fingerprint, ALPN: p.ALPN}
	default:
		return plugin.Fragment{}, false
	}
	switch p.Transport {
	case "tcp":
	case "xhttp":
		x := &xrayXHTTP{Path: p.XHTTPPath, Host: p.ClientHost, Mode: p.XHTTPMode}
		if p.XHTTPPadding != defaultXPaddingBytes {
			x.XPaddingBytes = p.XHTTPPadding
		}
		st.XHTTPSettings = x
	case "ws":
		st.WSSettings = &xrayWS{Path: p.WSPath, Host: p.ClientHost}
	default:
		return plugin.Fragment{}, false
	}
	data, err := json.Marshal(o)
	if err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatXrayJSON, Name: jsonName(in, p), Data: data}, true
}

// jsonName is the server name the assembler shows when it does not pass one: the same fallback the other formats use.
func jsonName(in protocols.RenderInput, p ClientParams) string {
	if in.DisplayName != "" {
		return in.DisplayName
	}
	return fmt.Sprintf("%s · vless · %d", nodeName(in.Inbound.Node, p.Host), in.Inbound.Port)
}
