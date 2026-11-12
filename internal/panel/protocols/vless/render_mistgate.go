package vless

import (
	"encoding/json"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// mistgateVLESS is the VLESS path of the first-party client API: explicit fields, not a link, everything a client needs to build
// the outbound of Xray, sing-box or Mihomo without guessing. It is made of the same ClientParams as the other renderers, so the
// address, the port, the CDN host, the REALITY name and short id are the ones every other format carries.
//
//	transport  raw (TCP, with Vision), xhttp or ws
//	security   reality (sni, public_key, short_id, fingerprint, spider_x) or tls (sni, alpn, fingerprint)
//	path, host the request path and Host header of xhttp and ws (host only when there is one)
//	mode, x_padding_bytes   xhttp only; the padding is always written out, never left to a default
type mistgateVLESS struct {
	Server        string   `json:"server"`
	Port          uint16   `json:"port"`
	UUID          string   `json:"uuid"`
	Flow          string   `json:"flow,omitempty"`
	Transport     string   `json:"transport"`
	Path          string   `json:"path,omitempty"`
	Host          string   `json:"host,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	XPaddingBytes string   `json:"x_padding_bytes,omitempty"`
	Security      string   `json:"security"`
	SNI           string   `json:"sni"`
	Fingerprint   string   `json:"fingerprint"`
	PublicKey     string   `json:"public_key,omitempty"`
	ShortID       string   `json:"short_id,omitempty"`
	SpiderX       string   `json:"spider_x,omitempty"`
	ALPN          []string `json:"alpn,omitempty"`
}

// renderMistgate gives a path only to a client that announced VLESS ("vless=1" in X-Mistgate-Caps), the way AmneziaWG goes to a client
// that announced its version: a client from before VLESS is not sent a protocol it does not know.
func renderMistgate(in protocols.RenderInput) (plugin.Fragment, bool) {
	if !protocols.HasCapability(in.Capabilities, "vless", "1") {
		return plugin.Fragment{}, false
	}
	p, ok := ResolveClientParams(in)
	if !ok {
		return plugin.Fragment{}, false
	}
	uuid := p.UUID
	if in.MaskSecrets {
		uuid = protocols.MaskedSecret
	}
	out := mistgateVLESS{
		Server: p.Host, Port: p.Port, UUID: uuid, Flow: p.Flow, Transport: p.Transport, Security: p.Security, SNI: p.SNI,
		Fingerprint: p.Fingerprint, PublicKey: p.PublicKey, ShortID: p.ShortID, SpiderX: p.SpiderX, ALPN: p.ALPN,
	}
	switch p.Transport {
	case "tcp":
		out.Transport = "raw" // what Xray's current cores call it
	case "xhttp":
		out.Path, out.Host, out.Mode, out.XPaddingBytes = p.XHTTPPath, p.ClientHost, p.XHTTPMode, p.XHTTPPadding
	case "ws":
		out.Path, out.Host = p.WSPath, p.ClientHost
	default:
		return plugin.Fragment{}, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatMistgate, Data: b}, true
}
