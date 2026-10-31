package subs

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The Xray JSON subscription: a JSON array, one complete Xray config per server, each with its `remarks` (the name the app
// lists). Xray-based apps (v2rayNG, v2rayN, Happ, ...) show every element as a server of its own. Like the Mihomo profile it is
// assembled from typed structs and encoding/json only: names (node, profile, brand) are hostile input, and a string template
// would let one of them add an outbound or a rule. The plugins render the one outbound (plugin.Fragment); everything else is here.
//
// What each config holds: one socks inbound on 127.0.0.1:10808 (the port v2rayNG waits for; no http inbound, no API, no stats:
// a listening local proxy is what the VPN checks of other apps look for), the proxy outbound tagged "proxy", `direct` and, when
// there is a dns section, the dns outbound that answers the queries of the OS (port 53), so that the preset (ads, family) acts
// on the apps' queries and not only on routing.

const (
	xraySocksPort = 10808
	xrayProxyTag  = "proxy"
	xrayDirectTag = "direct"
	xrayDNSOutTag = "dns-out"
	xrayBlockTag  = "block"
)

// xrayPrivate are the destinations that never go through the proxy; written out, not geoip:private, so that no geo file is needed.
var xrayPrivate = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "fc00::/7", "fe80::/10"}

// xrayProfile is what the assembler needs about one user's subscription.
type xrayProfile struct {
	lines  []string // outbounds, each one JSON object without a tag (plugin.Fragment.Data)
	names  []string // the remarks of each line, parallel to lines ("" or missing = a numbered default)
	preset *dns.Preset
	// note: the one entry that says why there are no servers (no access, a refused device); "" = build the servers.
	// none: the entry for a person whose servers all failed to render (never an empty answer).
	note, none string
	log        *slog.Logger
}

type xrayConfig struct {
	Remarks   string            `json:"remarks"`
	Log       xrayLog           `json:"log"`
	DNS       *dns.XraySection  `json:"dns,omitempty"`
	Inbounds  []xrayInbound     `json:"inbounds"`
	Outbounds []json.RawMessage `json:"outbounds"`
	Routing   xrayRouting       `json:"routing"`
}

type xrayLog struct {
	Level string `json:"loglevel"`
}

type xrayInbound struct {
	Tag      string       `json:"tag"`
	Protocol string       `json:"protocol"`
	Listen   string       `json:"listen"`
	Port     int          `json:"port"`
	Settings xraySocks    `json:"settings"`
	Sniffing xraySniffing `json:"sniffing"`
}

type xraySocks struct {
	Auth string `json:"auth"`
	UDP  bool   `json:"udp"`
}

type xraySniffing struct {
	Enabled      bool     `json:"enabled"`
	DestOverride []string `json:"destOverride"`
	RouteOnly    bool     `json:"routeOnly"`
}

type xrayRouting struct {
	DomainStrategy string     `json:"domainStrategy"`
	Rules          []xrayRule `json:"rules"`
}

type xrayRule struct {
	Type        string   `json:"type"`
	InboundTag  []string `json:"inboundTag,omitempty"`
	IP          []string `json:"ip,omitempty"`
	Domain      []string `json:"domain,omitempty"`
	Port        string   `json:"port,omitempty"`
	Network     string   `json:"network,omitempty"`
	OutboundTag string   `json:"outboundTag"`
}

func xrayLoopback(tag string, port int) xrayInbound {
	return xrayInbound{
		Tag: tag, Protocol: "socks", Listen: "127.0.0.1", Port: port, Settings: xraySocks{Auth: "noauth", UDP: true},
		Sniffing: xraySniffing{Enabled: true, DestOverride: []string{"http", "tls", "quic"}, RouteOnly: true},
	}
}

// tagged sets the tag of a rendered outbound: the fragment is decoded into raw members, so nothing of it is interpreted, the tag
// is added and the object is written again (keys sorted, the output is deterministic). key is the member every outbound of the
// format has ("protocol" for Xray, "type" for sing-box); a fragment without it is not an outbound of that format.
func tagged(frag []byte, key, tag string) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(frag, &m); err != nil {
		return nil, false
	}
	if k := m[key]; len(k) < 3 || k[0] != '"' {
		return nil, false
	}
	t, err := json.Marshal(tag)
	if err != nil {
		return nil, false
	}
	m["tag"] = t
	b, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return b, true
}

func staticOutbound(tag, protocol string) json.RawMessage {
	b, _ := json.Marshal(struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
	}{tag, protocol})
	return b
}

// build writes the array. Lines that do not decode as one outbound are skipped (and logged without their text: it holds a device
// secret).
func (p xrayProfile) build() ([]byte, error) {
	if p.note != "" {
		return xrayPlaceholder(p.note)
	}
	var d *dns.XrayDNS
	if p.preset != nil {
		x, err := p.preset.XrayDNS()
		if err != nil {
			if p.log != nil {
				p.log.Warn("xray json: the dns section was left out", "err", err)
			}
		} else {
			d = &x
		}
	}
	rules := xrayRules(d)
	outbounds := []json.RawMessage{nil, staticOutbound(xrayDirectTag, "freedom")}
	if d != nil {
		outbounds = append(outbounds, staticOutbound(xrayDNSOutTag, "dns"))
		if len(d.Block) > 0 {
			outbounds = append(outbounds, staticOutbound(xrayBlockTag, "blackhole"))
		}
	}
	var configs []xrayConfig
	for i, line := range p.lines {
		ob, ok := tagged([]byte(line), "protocol", xrayProxyTag)
		if !ok {
			if p.log != nil {
				p.log.Warn("xray json: a rendered outbound was skipped", "index", i)
			}
			continue
		}
		name := "server " + strconv.Itoa(len(configs)+1)
		if i < len(p.names) && p.names[i] != "" {
			name = p.names[i]
		}
		c := xrayConfig{
			Remarks: name, Log: xrayLog{Level: "warning"},
			Inbounds:  []xrayInbound{xrayLoopback("socks", xraySocksPort)},
			Outbounds: append([]json.RawMessage{ob}, outbounds[1:]...),
			Routing:   xrayRouting{DomainStrategy: "AsIs", Rules: rules},
		}
		if d != nil {
			c.DNS = &d.Section
		}
		configs = append(configs, c)
	}
	if len(configs) == 0 {
		return xrayPlaceholder(p.none)
	}
	return json.Marshal(configs)
}

// xrayRules is the routing of every config: the resolvers' own traffic (main ones through the proxy, the split ones direct),
// the queries of the OS to the dns outbound, the ad list to a blackhole, private addresses and the split suffixes direct, the rest
// through the proxy.
func xrayRules(d *dns.XrayDNS) []xrayRule {
	var rules []xrayRule
	if d != nil {
		if len(d.DirectIPs) > 0 {
			rules = append(rules, xrayRule{Type: "field", InboundTag: []string{dns.XrayDNSTag}, IP: d.DirectIPs, OutboundTag: xrayDirectTag})
		}
		rules = append(rules,
			xrayRule{Type: "field", InboundTag: []string{dns.XrayDNSTag}, OutboundTag: xrayProxyTag},
			xrayRule{Type: "field", Port: "53", OutboundTag: xrayDNSOutTag})
		if len(d.Block) > 0 { // after the DNS rules (a resolver's own traffic is not a site), before private and direct
			rules = append(rules, xrayRule{Type: "field", Domain: d.Block, OutboundTag: xrayBlockTag})
		}
	}
	rules = append(rules, xrayRule{Type: "field", IP: xrayPrivate, OutboundTag: xrayDirectTag})
	if d != nil && (len(d.Direct) > 0 || len(d.DirectGeo) > 0) {
		domains := make([]string, len(d.Direct))
		for i, s := range d.Direct {
			domains[i] = "domain:" + s
		}
		var ips []string
		for _, g := range d.DirectGeo {
			domains = append(domains, g.Site)
			ips = append(ips, g.IP)
		}
		rules = append(rules, xrayRule{Type: "field", Domain: domains, OutboundTag: xrayDirectTag})
		if len(ips) > 0 {
			rules = append(rules, xrayRule{Type: "field", IP: ips, OutboundTag: xrayDirectTag})
		}
	}
	return append(rules, xrayRule{Type: "field", Network: "tcp,udp", OutboundTag: xrayProxyTag})
}

// xrayPlaceholder is the one entry of a person who gets no server: a config whose remarks say why and whose only outbound is a
// socks proxy nobody listens on (127.0.0.1 port 1), so it never connects.
func xrayPlaceholder(note string) ([]byte, error) {
	off, _ := json.Marshal(struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
		Settings any    `json:"settings"`
	}{xrayProxyTag, "socks", map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": 1}}}})
	return json.Marshal([]xrayConfig{{
		Remarks: note, Log: xrayLog{Level: "warning"},
		Inbounds:  []xrayInbound{xrayLoopback("socks", xraySocksPort)},
		Outbounds: []json.RawMessage{off},
		Routing:   xrayRouting{DomainStrategy: "AsIs", Rules: []xrayRule{{Type: "field", Network: "tcp,udp", OutboundTag: xrayProxyTag}}},
	}})
}

// writeXrayJSON answers with the Xray JSON array: one config per server, named like the base64 list names its servers (the
// load percentage only for Happ), the dns section of the user's effective preset.
func (h *handler) writeXrayJSON(w http.ResponseWriter, r *http.Request, token string, v access.SubView, set *adminv1.SubscriptionSettings) {
	b := h.brand(r.Context())
	p := xrayProfile{lines: v.Lines, log: h.cfg.Log, note: h.appNote(v, b.Language, plugin.FormatXrayJSON), none: noServersNote(b.Language)}
	if p.note == "" {
		p.preset = h.linkPreset(r.Context(), v)
		if len(v.Servers) == len(v.Lines) && len(v.Servers) > 0 {
			p.names = happRemarks(v.Servers, set.GetServerNameTemplate(), b.Language, isHapp(r.UserAgent()))
		}
	}
	body, err := p.build()
	if err != nil {
		h.cfg.Log.Error("xray json", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.writeDocument(w, r, token, v, set, b, headerContentTypeJSON, body, plugin.FormatXrayJSON)
}
