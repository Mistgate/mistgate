package plugin

import (
	"encoding/json"
	"net"
	"strings"
)

// What the settings of an inbound say about the node. The panel and the agent both ask these, and the answers are the node's:
// this is where its engines' rules are read, so nothing else parses `masquerade.tcp_port`, `reality.target`, `server_names` or
// `cdn.host`. The engines (internal/node/vless, internal/node/hysteria2) validate what they are given; these only read it.

// Hy2SitePort is the TCP port of a Hysteria2 inbound's HTTPS site, 0 = none: settings.masquerade.tcp_port, 443 when omitted. The
// node listens there whatever masquerade.type says (type none answers 404, it still answers), and not at all without a TLS mode
// (no certificate), with a port outside 1-65535 or with settings it cannot read (the inbound does not start).
func Hy2SitePort(s InboundSpec) uint16 {
	if s.Protocol != "hysteria2" || s.TLS.Mode == 0 {
		return 0
	}
	var j struct {
		Masquerade struct {
			TCPPort *int `json:"tcp_port"`
		} `json:"masquerade"`
	}
	if len(s.Settings) > 0 && json.Unmarshal(s.Settings, &j) != nil {
		return 0
	}
	port := 443
	if j.Masquerade.TCPPort != nil {
		port = *j.Masquerade.TCPPort
	}
	if port < 0 || port > 65535 {
		return 0
	}
	return uint16(port)
}

// VLESSView is what the settings of a VLESS inbound say, read as the node reads them: Target is trimmed, CDNHost is lower case
// without a closing dot, ServerNames are as written.
type VLESSView struct {
	Security, Transport string // "reality" | "tls"; "tcp" | "xhttp" | "ws"
	Target              string // REALITY: "self" (the node's own site) or "host:port" of another site
	ServerNames         []string
	CDNHost             string // the name people reach a TLS inbound by through a CDN, "" = none
}

// VLESSOf reads the settings of a VLESS inbound; ok is false for any other protocol and for settings that cannot be read (the
// view is then empty). Settings that are absent read as an empty view.
func VLESSOf(s InboundSpec) (v VLESSView, ok bool) {
	if s.Protocol != "vless" {
		return VLESSView{}, false
	}
	var j struct {
		Security  string `json:"security"`
		Transport string `json:"transport"`
		Reality   struct {
			Target      string   `json:"target"`
			ServerNames []string `json:"server_names"`
		} `json:"reality"`
		CDN struct {
			Host string `json:"host"`
		} `json:"cdn"`
	}
	if len(s.Settings) > 0 && json.Unmarshal(s.Settings, &j) != nil {
		return VLESSView{}, false
	}
	return VLESSView{
		Security: j.Security, Transport: j.Transport, Target: strings.TrimSpace(j.Reality.Target), ServerNames: j.Reality.ServerNames,
		CDNHost: strings.ToLower(strings.TrimSuffix(strings.TrimSpace(j.CDN.Host), ".")),
	}, true
}

// TLS says the inbound is a TLS server (its own certificate), not REALITY.
func (v VLESSView) TLS() bool { return v.Security == "tls" }

// TargetSelf says the REALITY target is the node's own site (wire value "self").
func (v VLESSView) TargetSelf() bool { return v.Target == "self" }

// TargetHost is the host of the REALITY target, "" when it is not "host:port".
func (v VLESSView) TargetHost() string {
	host, _, err := net.SplitHostPort(v.Target)
	if err != nil {
		return ""
	}
	return strings.Trim(host, "[]")
}

// OwnSite says a stranger who finds the port gets the node's own HTTPS site: a TLS inbound, or REALITY with target "self" (settings
// that name no security are read as REALITY).
func (v VLESSView) OwnSite() bool {
	return v.TLS() || (v.Security == "reality" || v.Security == "") && v.TargetSelf()
}

// ServerCDN says people reach this server through a CDN host, not the node's address.
func ServerCDN(s InboundSpec) bool {
	v, ok := VLESSOf(s)
	return ok && v.CDNHost != ""
}

// MTProtoWeb says an MTProto inbound is a Telegram Web Proxy (settings.mode "web"); every other protocol, an absent mode and
// settings that cannot be read are the Fake TLS proxy. The agent, the panel and the edge all ask this.
func MTProtoWeb(s InboundSpec) bool {
	var j struct {
		Mode string `json:"mode"`
	}
	return s.Protocol == "mtproto" && len(s.Settings) > 0 && json.Unmarshal(s.Settings, &j) == nil && j.Mode == "web"
}

// SharesTCPPort says the TCP inbounds of the protocol can stand on one port, told apart by the name in the first ClientHello
// (TCPSharePlan). A TCP inbound of any other protocol keeps the port to itself.
func SharesTCPPort(protocol string) bool {
	switch protocol {
	case "vless", "trusttunnel", "mtproto":
		return true
	}
	return false
}
