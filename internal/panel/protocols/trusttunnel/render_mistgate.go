package trusttunnel

import (
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

type mistgateTrustTunnel struct {
	Server    string `json:"server"`
	Port      uint16 `json:"port"`
	SNI       string `json:"sni"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Transport string `json:"transport"`
	TLS       string `json:"tls"`
	PinSHA256 string `json:"pin_sha256,omitempty"`
}

func renderMistgate(in protocols.RenderInput) (plugin.Fragment, bool) {
	if !protocols.HasCapability(in.Capabilities, "trusttunnel", "1") {
		return plugin.Fragment{}, false
	}
	host := strings.Trim(in.Inbound.Node.Address, "[]")
	sni := in.Inbound.TLSServerName
	if !validHost(host) || !validHost(sni) || in.Inbound.Port == 0 {
		return plugin.Fragment{}, false
	}
	if in.Inbound.TLSMode != plugin.TLSAcmeDomain && in.Inbound.TLSMode != plugin.TLSSelfSigned {
		return plugin.Fragment{}, false
	}
	var s Settings
	if json.Unmarshal(in.Settings, &s) != nil {
		return plugin.Fragment{}, false
	}
	username, password := "", ""
	if in.MaskSecrets {
		username, password = protocols.MaskedSecret, protocols.MaskedSecret
	} else {
		var ok bool
		username, password, ok = strings.Cut(in.Secret, ":")
		if !ok || username == "" || password == "" || strings.Contains(password, ":") {
			return plugin.Fragment{}, false
		}
	}
	transport := "h2"
	if s.H3 && !in.Inbound.TCPOnly {
		transport = "auto"
	}
	settings := mistgateTrustTunnel{
		Server: host, Port: in.Inbound.Port, SNI: sni,
		Username: username, Password: password, Transport: transport, TLS: "ca",
	}
	if in.Inbound.TLSMode == plugin.TLSSelfSigned {
		pin, ok := protocols.NormalizePin(in.Inbound.CertPinSHA256)
		if !ok {
			return plugin.Fragment{}, false
		}
		settings.TLS, settings.PinSHA256 = "pin", pin
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatMistgate, Data: b}, true
}

func validHost(host string) bool {
	if protocols.IsHostname(host) {
		return true
	}
	_, err := netip.ParseAddr(host)
	return err == nil
}
