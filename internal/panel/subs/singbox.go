package subs

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The sing-box subscription: one profile for the official sing-box apps (SFA, SFI, SFM): a TUN inbound (without one the VPN
// carries nothing), a selector over every server and `direct`, the dns section of the user's effective preset, and the route rules
// that go with it. Typed structs and encoding/json only, like the Xray array: the names are hostile input. The schema is the one
// of sing-box 1.12 and later (typed DNS servers, rule actions, default_domain_resolver); nothing the later versions removed.
// No `mixed` inbound and no clash_api: a listening local proxy is what the VPN checks of other apps look for.

const (
	singboxDirectTag = "direct"
	singboxTunTag    = "tun-in"
)

// singboxProfile is what the assembler needs about one user's subscription.
type singboxProfile struct {
	title  string
	lines  []string // outbounds, each one JSON object without a tag (plugin.Fragment.Data)
	names  []string // the tag of each line, parallel to lines ("" or missing = "server")
	preset *dns.Preset
	// note: the one entry that says why there are no servers; "" = build the servers. none: the entry for a person whose servers
	// all failed to render (never an empty answer).
	note, none string
	log        *slog.Logger
}

type singboxConfig struct {
	Log          singboxLog          `json:"log"`
	DNS          *dns.SingboxSection `json:"dns,omitempty"`
	Inbounds     []singboxTun        `json:"inbounds"`
	Outbounds    []json.RawMessage   `json:"outbounds"`
	Route        singboxRoute        `json:"route"`
	Experimental *singboxExp         `json:"experimental,omitempty"`
}

type singboxLog struct {
	Level string `json:"level"`
}

type singboxTun struct {
	Type        string   `json:"type"`
	Tag         string   `json:"tag"`
	Address     []string `json:"address"`
	AutoRoute   bool     `json:"auto_route"`
	StrictRoute bool     `json:"strict_route"`
}

type singboxSelector struct {
	Type      string   `json:"type"`
	Tag       string   `json:"tag"`
	Outbounds []string `json:"outbounds"`
	Default   string   `json:"default,omitempty"`
}

type singboxRoute struct {
	Rules                 []singboxRule `json:"rules,omitempty"`
	Final                 string        `json:"final"`
	AutoDetectInterface   bool          `json:"auto_detect_interface"`
	DefaultDomainResolver string        `json:"default_domain_resolver,omitempty"`
}

type singboxRule struct {
	Protocol     string   `json:"protocol,omitempty"`
	IPIsPrivate  bool     `json:"ip_is_private,omitempty"`
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	Action       string   `json:"action"`
	Outbound     string   `json:"outbound,omitempty"`
}

type singboxExp struct {
	CacheFile struct {
		Enabled bool `json:"enabled"`
	} `json:"cache_file"`
}

func singboxTunInbound() singboxTun {
	return singboxTun{Type: "tun", Tag: singboxTunTag, Address: []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"}, AutoRoute: true, StrictRoute: true}
}

func singboxDirect() json.RawMessage {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Tag  string `json:"tag"`
	}{"direct", singboxDirectTag})
	return b
}

// build writes the profile. Lines that do not decode as one outbound are skipped (and logged without their text: it holds a device
// secret).
func (p singboxProfile) build() ([]byte, error) {
	if p.note != "" {
		return p.placeholder(p.note)
	}
	taken := map[string]bool{}
	var servers []json.RawMessage
	var tags []string
	for i, line := range p.lines {
		name := ""
		if i < len(p.names) {
			name = p.names[i]
		}
		tag := uniqueName(name, taken)
		ob, ok := tagged([]byte(line), "type", tag)
		if !ok {
			delete(taken, tag)
			if p.log != nil {
				p.log.Warn("sing-box profile: a rendered outbound was skipped", "index", i)
			}
			continue
		}
		servers = append(servers, ob)
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		return p.placeholder(p.none)
	}
	group := groupName(p.title, taken)
	if group == "" {
		group = "Proxy"
	}
	sel, _ := json.Marshal(singboxSelector{Type: "selector", Tag: group, Outbounds: append(slices.Clone(tags), singboxDirectTag), Default: tags[0]})

	cfg := singboxConfig{
		Log:      singboxLog{Level: "warn"},
		Inbounds: []singboxTun{singboxTunInbound()},
		Route: singboxRoute{Final: group, AutoDetectInterface: true, DefaultDomainResolver: dns.SingboxLocalTag, Rules: []singboxRule{
			{Action: "sniff"},
			{Protocol: "dns", Action: "hijack-dns"},
			{IPIsPrivate: true, Action: "route", Outbound: singboxDirectTag},
		}},
		Experimental: &singboxExp{},
	}
	cfg.Experimental.CacheFile.Enabled = true
	// The resolvers: the preset's, else (no preset or one a sing-box profile cannot carry) the system resolver alone, which is also
	// what default_domain_resolver names, so that the servers' own host names resolve.
	var d *dns.SingboxDNS
	if p.preset != nil {
		x, err := p.preset.SingboxDNS(group)
		if err != nil {
			if p.log != nil {
				p.log.Warn("sing-box profile: the dns section was left out", "err", err)
			}
		} else {
			d = &x
		}
	}
	if d != nil {
		cfg.DNS = &d.Section
		if len(d.Direct) > 0 {
			cfg.Route.Rules = append(cfg.Route.Rules, singboxRule{DomainSuffix: d.Direct, Action: "route", Outbound: singboxDirectTag})
		}
	} else {
		cfg.DNS = &dns.SingboxSection{
			Servers: []dns.SingboxServer{{Type: "local", Tag: dns.SingboxLocalTag}}, Final: dns.SingboxLocalTag, Strategy: "ipv4_only",
		}
	}
	cfg.Outbounds = append(append([]json.RawMessage{sel}, servers...), singboxDirect())
	return json.Marshal(cfg)
}

// placeholder is the profile of a person who gets no server: the selector holds one socks outbound nobody listens on (127.0.0.1
// port 1), tagged with the words that say why, so it never connects and the app shows the reason where it shows the server.
func (p singboxProfile) placeholder(note string) ([]byte, error) {
	taken := map[string]bool{singboxDirectTag: true}
	tag := uniqueName(note, taken)
	group := groupName(p.title, taken)
	if group == "" {
		group = "Proxy"
	}
	sel, _ := json.Marshal(singboxSelector{Type: "selector", Tag: group, Outbounds: []string{tag}, Default: tag})
	off, _ := json.Marshal(struct {
		Type       string `json:"type"`
		Tag        string `json:"tag"`
		Server     string `json:"server"`
		ServerPort int    `json:"server_port"`
	}{"socks", tag, "127.0.0.1", 1})
	return json.Marshal(singboxConfig{
		Log:       singboxLog{Level: "warn"},
		Inbounds:  []singboxTun{singboxTunInbound()},
		Outbounds: []json.RawMessage{sel, off, singboxDirect()},
		Route:     singboxRoute{Final: group, AutoDetectInterface: true},
	})
}

// writeSingbox answers with the sing-box profile: the servers named like the Mihomo profile names them (stable, never with the
// load percentage: the selector remembers the chosen server by its tag), the dns section of the user's effective preset.
func (h *handler) writeSingbox(w http.ResponseWriter, r *http.Request, token string, v access.SubView, set *adminv1.SubscriptionSettings) {
	b := h.brand(r.Context())
	title := h.title(set, b)
	p := singboxProfile{title: title, lines: v.Lines, log: h.cfg.Log, note: h.appNote(v, b.Language, plugin.FormatSingbox), none: noServersNote(b.Language)}
	if p.note == "" {
		p.preset = h.linkPreset(r.Context(), v)
		if len(v.Servers) == len(v.Lines) && len(v.Servers) > 0 {
			p.names = remarks(v.Servers, set.GetServerNameTemplate(), b.Language)
		}
	}
	body, err := p.build()
	if err != nil {
		h.cfg.Log.Error("sing-box profile", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.writeDocument(w, r, token, v, set, b, headerContentTypeJSON, body, plugin.FormatSingbox)
}
