package subs

import (
	"slices"
	"strings"
	"unicode/utf8"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The servers of the page and the DNS of each of them (the user page's "Servers" and "DNS for each server"). Everything
// here is data about the person's own servers: no node name, no address, no rate.

// pageNode is one server (one node) of the person. label is the country and the place, the name the page and the keys
// give a server, never the panel's node name. app_names are what the apps that take the link call it (the owner's name
// template): empty when the template has {node}, because the node name must not reach the page.
type pageNode struct {
	ID          string     `json:"id"`
	CountryCode string     `json:"country_code"`
	Place       string     `json:"place"`
	Label       string     `json:"label"`
	AppNames    []string   `json:"app_names"`
	Connections []pageConn `json:"connections"`
	// Online: the node's agent answers (a live session or a fresh sample); the page says "not responding" otherwise.
	Online bool `json:"online"`
	// Load is "low" | "medium" | "high", null when the node has no capacity set or no fresh sample. Never a percentage.
	Load *string      `json:"load"`
	DNS  *pageNodeDNS `json:"dns"` // null: the owner offers no choice of DNS on this server
}

// pageConn is one way to use a server: by the link (app_name is how the apps list it) or by a key (profile_id).
type pageConn struct {
	Way       string `json:"way"`  // "link" | "key"
	Exit      string `json:"exit"` // "direct" | "warp"
	AppName   string `json:"app_name,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	// MihomoOnly: a link that only the Mihomo apps (kl!ck, Clash Verge, FlClash) can use; Happ and the other link-list apps do not get it.
	MihomoOnly bool `json:"mihomo_only,omitempty"`
}

// pageNodeDNS is the DNS of one server: what the person picked ("" = nothing), what applies, what the owner offers (in
// the owner's order), and the AmneziaWG devices whose key on this server still holds an older DNS.
type pageNodeDNS struct {
	Choice        string   `json:"choice"`
	Effective     string   `json:"effective"`
	Options       []string `json:"options"`
	Default       string   `json:"default"` // what applies without a pick (the page names the "default" option by it)
	KeysToRefresh []string `json:"keys_to_refresh"`
}

// pageDNS is the page's DNS settings: whether a person may pick (the owner's switch), where the pick is posted, how often
// the apps refresh and what the apps that take the link have.
type pageDNS struct {
	Enabled      bool        `json:"enabled"`
	Endpoint     string      `json:"endpoint"` // <link>/dns; "" in the owner's preview, when the choice is off and for a user without access
	RefreshHours int         `json:"refresh_hours"`
	Link         pageDNSLink `json:"link"`
}

// pageDNSLink: the apps that take the link (Hysteria2 in Happ and in Mihomo) have one resolver for the whole subscription,
// so a pick per server does not reach them: per_server is false and effective is the preset they all use.
type pageDNSLink struct {
	PerServer bool   `json:"per_server"`
	Effective string `json:"effective"`
}

// pageDNSPreset is a preset the page names, in the language of the page.
type pageDNSPreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"` // "russia" | "regular" | "no_ads" | "family" | "security" | "" (the owner's own)
}

// pageServers builds servers[] and, from it, the old server_loads. Only an active user has servers (Nodes is empty
// otherwise).
func pageServers(v access.SubView, set *adminv1.SubscriptionSettings, lang string) []pageNode {
	out := []pageNode{}
	if len(v.Nodes) == 0 {
		return out
	}
	// The names of the apps: the owner's template. Not with {node} in it (the node name must not reach the page), and never
	// with the node name as a fallback for an empty name: it is blanked first.
	var appNames []string
	if tmpl := set.GetServerNameTemplate(); !strings.Contains(tmpl, "{node}") && len(v.Servers) > 0 {
		safe := slices.Clone(v.Servers)
		for i := range safe {
			safe[i].Node = ""
		}
		appNames = remarks(safe, tmpl, lang)
	}
	label := access.ServerLabeler(lang)
	for _, n := range v.Nodes {
		p := pageNode{
			ID: n.ID, CountryCode: strings.ToUpper(strings.TrimSpace(n.CountryCode)), Place: place(n.Location),
			Label: label(n.CountryCode, n.Location), AppNames: []string{}, Connections: []pageConn{}, Online: n.Online,
		}
		if n.LoadPercent != nil {
			l := loadLevel(*n.LoadPercent)
			p.Load = &l
		}
		for _, c := range n.Conns {
			pc := pageConn{Way: c.Way, Exit: c.Exit, ProfileID: c.ProfileID, MihomoOnly: c.MihomoOnly}
			if c.Way == "link" && !c.MihomoOnly && c.Server < len(appNames) {
				pc.AppName = appNames[c.Server]
				p.AppNames = append(p.AppNames, pc.AppName)
			}
			p.Connections = append(p.Connections, pc)
		}
		if d := n.DNS; d != nil {
			p.DNS = &pageNodeDNS{Choice: d.Choice, Effective: d.Effective, Options: d.Options, Default: d.Default, KeysToRefresh: nonNil(d.KeysToRefresh)}
		}
		out = append(out, p)
	}
	return out
}

// place is the owner's text for the location of a node: one line, at most 100 characters, "" when there is none.
func place(s string) string {
	s = clean(s)
	if utf8.RuneCountInString(s) > 100 {
		s = string([]rune(s)[:100])
	}
	return s
}

// serverLoads is the old server_loads, derived from servers[]: one row for each server that has a load level.
func serverLoadsOf(servers []pageNode) []pageServer {
	out := []pageServer{}
	for _, s := range servers {
		if s.Load != nil {
			out = append(out, pageServer{Name: s.Label, Level: *s.Load})
		}
	}
	return out
}

// pageDNSOf is the page's DNS settings. The pick is offered (endpoint set) only when the owner switched the choice on, the
// page is not the owner's preview and the user has servers.
func pageDNSOf(v access.SubView, set *adminv1.SubscriptionSettings, link string, preview bool) pageDNS {
	hours := int(set.GetUpdateIntervalHours())
	if hours == 0 {
		hours = subsettings.DefaultUpdateHours
	}
	d := pageDNS{Enabled: subsettings.DNSChoice(set), RefreshHours: hours, Link: pageDNSLink{Effective: v.DNSLink}}
	if d.Enabled && !preview && link != "" && v.Status == access.StatusActive {
		d.Endpoint = link + "/dns"
	}
	return d
}

func pageDNSPresets(v access.SubView, lang string) []pageDNSPreset {
	out := []pageDNSPreset{}
	for _, p := range v.DNSPresets {
		out = append(out, pageDNSPreset{ID: p.ID, Name: p.NameIn(lang), Description: p.DescriptionIn(lang), Category: string(dns.CategoryOf(p))})
	}
	return out
}
