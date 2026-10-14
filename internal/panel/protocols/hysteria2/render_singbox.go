package hysteria2

import (
	"encoding/json"
	"strconv"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

type singboxHy2 struct {
	Type        string        `json:"type"`
	Server      string        `json:"server"`
	ServerPort  uint16        `json:"server_port,omitempty"`
	ServerPorts []string      `json:"server_ports,omitempty"`
	HopInterval string        `json:"hop_interval,omitempty"`
	Password    string        `json:"password"`
	Obfs        *singboxObfs  `json:"obfs,omitempty"`
	TLS         singboxHy2TLS `json:"tls"`
}

type singboxObfs struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type singboxHy2TLS struct {
	Enabled    bool     `json:"enabled"`
	ServerName string   `json:"server_name,omitempty"`
	ALPN       []string `json:"alpn"`
}

// renderSingbox returns the Hysteria2 outbound of a sing-box profile (schema of 1.12 and later). A self-signed node
// is not rendered: sing-box pins a certificate by its public key (certificate_public_key_sha256, 1.13) and takes the
// pin of the whole certificate only from 1.15, and the pin the node reports is the whole certificate's. Without a
// pin the client could only skip verification, which the panel never does. Gecko is not rendered either (see
// newJSONView). A hop range goes to `server_ports` in sing-box's "from:to" form with `hop_interval`; the docs say
// `server_port` is ignored next to it, so it is left out (the node forwards the range to the listening port).
func renderSingbox(in protocols.RenderInput) (plugin.Fragment, bool) {
	v, ok := newJSONView(in)
	if !ok {
		return plugin.Skipped(v.note)
	}
	if v.selfSigned {
		return plugin.Skipped(plugin.NoteSelfSigned)
	}
	o := singboxHy2{
		Type: "hysteria2", Server: v.host, Password: v.password,
		TLS: singboxHy2TLS{Enabled: true, ServerName: sni(in), ALPN: []string{"h3"}},
	}
	if from, to := in.Inbound.HopFrom, in.Inbound.HopTo; from != 0 && to != 0 {
		o.ServerPorts = []string{strconv.Itoa(int(from)) + ":" + strconv.Itoa(int(to))}
		o.HopInterval = strconv.Itoa(hopIntervalSeconds) + "s"
	} else {
		o.ServerPort = in.Inbound.Port
	}
	if v.salamander {
		o.Obfs = &singboxObfs{Type: "salamander", Password: v.obfsPw}
	}
	data, err := json.Marshal(o)
	if err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatSingbox, Name: v.name, Data: data}, true
}
