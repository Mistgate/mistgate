package hysteria2

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

type mistgateObfs struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type mistgateHysteria2 struct {
	Server       string        `json:"server"`
	Port         uint16        `json:"port"`
	Ports        string        `json:"ports,omitempty"`
	HopIntervalS int           `json:"hop_interval_s,omitempty"`
	Auth         string        `json:"auth"`
	TLS          string        `json:"tls"`
	PinSHA256    string        `json:"pin_sha256,omitempty"`
	SNI          string        `json:"sni,omitempty"`
	ALPN         []string      `json:"alpn"`
	Obfs         *mistgateObfs `json:"obfs,omitempty"`
}

func renderMistgate(in protocols.RenderInput) (plugin.Fragment, bool) {
	var s Settings
	if err := json.Unmarshal(in.Settings, &s); err != nil {
		return plugin.Fragment{}, false
	}
	host := strings.Trim(in.Inbound.Node.Address, "[]")
	if !isIP(host) && !isHostname(host) {
		return plugin.Fragment{}, false
	}
	if s.Obfs.Type == "gecko" && !protocols.HasCapability(in.Capabilities, "gecko", "1") {
		return plugin.Fragment{}, false
	}
	pin, pinOK := protocols.NormalizePin(in.Inbound.CertPinSHA256)
	settings := mistgateHysteria2{
		Server: host, Port: in.Inbound.Port, Auth: in.Secret, TLS: "ca", ALPN: []string{"h3"},
	}
	if in.Inbound.TLSMode == plugin.TLSSelfSigned {
		if !pinOK {
			return plugin.Fragment{}, false
		}
		settings.TLS, settings.PinSHA256 = "pin", pin
	}
	if from, to := in.Inbound.HopFrom, in.Inbound.HopTo; from != 0 && to != 0 {
		settings.Ports = strconv.Itoa(int(from)) + "-" + strconv.Itoa(int(to))
		settings.HopIntervalS = hopIntervalSeconds
	}
	if sni := in.Inbound.TLSServerName; sni != "" && !isIP(sni) {
		settings.SNI = sni
	}
	if s.Obfs.Type == "salamander" || s.Obfs.Type == "gecko" {
		settings.Obfs = &mistgateObfs{Type: s.Obfs.Type, Password: s.Obfs.Password}
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatMistgate, Data: b}, true
}
