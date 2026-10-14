package vless

import (
	"encoding/json"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

type singboxVless struct {
	Type           string            `json:"type"`
	Server         string            `json:"server"`
	ServerPort     uint16            `json:"server_port"`
	UUID           string            `json:"uuid"`
	Flow           string            `json:"flow,omitempty"`
	PacketEncoding string            `json:"packet_encoding"`
	TLS            singboxTLS        `json:"tls"`
	Transport      *singboxTransport `json:"transport,omitempty"`
}

type singboxTLS struct {
	Enabled    bool            `json:"enabled"`
	ServerName string          `json:"server_name"`
	ALPN       []string        `json:"alpn,omitempty"`
	UTLS       singboxUTLS     `json:"utls"`
	Reality    *singboxReality `json:"reality,omitempty"`
}

type singboxUTLS struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

type singboxReality struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}

type singboxTransport struct {
	Type    string            `json:"type"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
}

// renderSingbox returns the VLESS outbound of a sing-box profile (schema of 1.12 and later). It carries REALITY over
// TCP with Vision and TLS over WebSocket, direct or through a CDN. XHTTP has no sing-box implementation (only forks),
// so every XHTTP server, REALITY or TLS, is not rendered. REALITY needs uTLS in sing-box, so uTLS is always on, with
// the fingerprint the profile names; spiderX has no counterpart there and is left out.
func renderSingbox(in protocols.RenderInput) (plugin.Fragment, bool) {
	v, ok := renderView(in)
	if !ok {
		return plugin.Fragment{}, false
	}
	p := v.params
	uuid := p.UUID
	if in.MaskSecrets {
		uuid = protocols.MaskedSecret
	}
	o := singboxVless{
		Type: "vless", Server: p.Host, ServerPort: p.Port, UUID: uuid, Flow: p.Flow, PacketEncoding: "xudp",
		TLS: singboxTLS{Enabled: true, ServerName: p.SNI, UTLS: singboxUTLS{Enabled: true, Fingerprint: p.Fingerprint}},
	}
	switch {
	case p.Security == "reality" && p.Transport == "tcp":
		o.TLS.Reality = &singboxReality{Enabled: true, PublicKey: p.PublicKey, ShortID: p.ShortID}
	case p.Security == "tls" && p.Transport == "ws":
		o.TLS.ALPN = p.ALPN
		t := &singboxTransport{Type: "ws", Path: p.WSPath}
		if p.ClientHost != "" {
			t.Headers = map[string]string{"Host": p.ClientHost}
		}
		o.Transport = t
	case p.Transport == "xhttp":
		return plugin.Skipped(plugin.NoteXHTTP)
	default:
		return plugin.Fragment{}, false
	}
	data, err := json.Marshal(o)
	if err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatSingbox, Name: jsonName(in, p), Data: data}, true
}
