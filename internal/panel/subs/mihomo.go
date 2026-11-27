package subs

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The Mihomo YAML profile: the proxies the protocol plugins rendered, one select
// group over all of them, the dns section of the user's effective preset and the rules that go with it. It is
// assembled from yaml.v3 nodes only: names (node, profile, brand) are hostile input and a string template would let
// one of them add a proxy or a rule. Some Mihomo-based apps take only `proxies:` and ignore the rest; others merge it.

// mihomoProfile is what the assembler needs about one user's subscription.
type mihomoProfile struct {
	title      string
	groupID    string // primary group, or empty when previewing only the common part
	lines      []string // proxies, each a YAML list with one element (plugin.Fragment.Data)
	names      []string // the server name of each line, parallel to lines ("" or missing = keep the plugin's own)
	preset     *dns.Preset
	autoSelect bool
	autoSecs   int  // seconds between the checks of the Auto group; 0 = the default
	note       bool // lines hold the one entry that says why there are no servers: the profile is the built-in one, never a template
	log        *slog.Logger
}

func strNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}
func boolNode(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(b)}
}
func intNode(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(i)}
}
func seqNode(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}
func quotedSeq(vs []string) *yaml.Node {
	n := seqNode()
	for _, v := range vs {
		n.Content = append(n.Content, quoted(v))
	}
	return n
}

type mapNode struct{ n *yaml.Node }

func newMap() *mapNode { return &mapNode{&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}} }
func (m *mapNode) set(k string, v *yaml.Node) *mapNode {
	m.n.Content = append(m.n.Content, strNode(k), v)
	return m
}

// proxyNode parses one rendered fragment and returns its proxy mapping and the name it carries.
func proxyNode(line string) (*yaml.Node, string, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(line), &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, "", false
	}
	seq := doc.Content[0]
	if seq.Kind != yaml.SequenceNode || len(seq.Content) != 1 || seq.Content[0].Kind != yaml.MappingNode {
		return nil, "", false
	}
	m := seq.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "name" {
			return m, m.Content[i+1].Value, true
		}
	}
	return nil, "", false
}

// uniqueName makes a proxy name safe: not one of the core's adapter names and not taken yet (subsettings.CoreNames).
func uniqueName(name string, taken map[string]bool) string {
	return subsettings.UniqueName(name, taken)
}

// groupName is the name of the select group: the subscription title without what would break a rule line
// ("MATCH,<group>" is split at commas) or the "#group" fragment of a nameserver (split at "&" and "=").
func groupName(title string, taken map[string]bool) string {
	title = strings.Map(func(r rune) rune {
		if strings.ContainsRune(",#&=\"'%", r) {
			return ' '
		}
		return r
	}, clean(title))
	return uniqueName(strings.Join(strings.Fields(title), " "), taken)
}

// servers are the proxies of the lines with the names they want (the name of the server, else the one the plugin gave it). A line
// that does not parse is skipped.
func (p mihomoProfile) servers() []subsettings.MihomoServer {
	var out []subsettings.MihomoServer
	for i, line := range p.lines {
		m, own, ok := proxyNode(line)
		if !ok {
			if p.log != nil {
				p.log.Warn("mihomo profile: a rendered proxy was skipped", "code", "mihomo_proxy_skipped", "index", i)
			}
			continue
		}
		name := own
		if i < len(p.names) && p.names[i] != "" {
			name = p.names[i]
		}
		out = append(out, subsettings.MihomoServer{Node: m, Name: name})
	}
	return out
}

// render is the profile of one person: the template of their group around their servers, else the built-in profile. The built-in one
// is what a person gets when there is no template (tplText ""), when they get a note instead of servers (the note must be seen: a
// template's groups would hide it), when none of their servers can be used, and when the template does not build (a stored text that
// this version cannot read: a log line with the template's name, never its text). tplName is for that line only.
// selected is the source after group selection, for preview warnings even if no servers could be inserted.
func (p mihomoProfile) render(tplName, tplText string) (body []byte, selected string, templated bool, err error) {
	if tplText != "" && !p.note {
		body, selected, err = p.buildFromTemplate(tplText)
		switch {
		case err == nil:
			return body, selected, true, nil
		case errors.Is(err, subsettings.ErrNoServers):
		default:
			if p.log != nil {
				p.log.Warn("mihomo profile: a template could not be used, the built-in profile is sent", "code", "mihomo_template_unusable", "template", tplName)
			}
		}
	}
	body, err = p.build()
	return body, selected, false, err
}

// buildFromTemplate selects the group variant, then puts the person's servers into it. The dns section the template
// lacks is the resolvers of the person's preset with respect-rules on: no group name is known here for a "#group" fragment, and the
// split, ad and country rules of the preset (and the policy and providers they need) are the built-in profile's, not a template's.
func (p mihomoProfile) buildFromTemplate(text string) (body []byte, selected string, err error) {
	selected, err = subsettings.ResolveMihomoGroup(text, p.groupID)
	if err != nil {
		return nil, "", err
	}
	opt := subsettings.MihomoOptions{AutoSelect: p.autoSelect, AutoInterval: p.autoSecs}
	if p.preset != nil {
		if d, err := p.preset.MihomoDNS(""); err != nil {
			if p.log != nil {
				p.log.Warn("mihomo profile: the dns section was left out", "code", "mihomo_dns_render_failed", "err", err)
			}
		} else {
			d.Policy = nil
			opt.DNS = dnsNode(d, true)
		}
	}
	body, err = subsettings.ApplyMihomoTemplate(selected, p.servers(), opt)
	return body, selected, err
}

// build writes the profile. Lines that do not parse as one proxy are skipped (and logged without their text: it holds
// a device secret).
func (p mihomoProfile) build() ([]byte, error) {
	proxies := seqNode()
	taken := map[string]bool{}
	var names []string
	for _, s := range p.servers() {
		name := uniqueName(s.Name, taken)
		subsettings.SetProxyName(s.Node, name)
		proxies.Content = append(proxies.Content, s.Node)
		names = append(names, name)
	}
	group := groupName(p.title, taken)
	if group == "" {
		group = "Proxy"
	}
	selectMembers := slices.Clone(names)
	var autoGroup *yaml.Node
	if p.autoSelect && len(names) > 1 {
		auto := uniqueName(subsettings.AutoName, taken)
		selectMembers = append([]string{auto}, selectMembers...)
		autoGroup = subsettings.MihomoAutoGroup(auto, names, p.autoSecs)
	}
	members := append(selectMembers, "DIRECT")

	root := newMap()
	root.set("proxies", proxies)
	groups := seqNode(newMap().
		set("name", quoted(group)).set("type", strNode("select")).set("proxies", quotedSeq(members)).n)
	if autoGroup != nil {
		groups.Content = append(groups.Content, autoGroup)
	}
	root.set("proxy-groups", groups)

	var direct []string
	var providers, block []dns.MihomoProvider
	var blockDomains, torrentApps []string
	var torrent []dns.MihomoProvider
	if p.preset != nil {
		d, err := p.preset.MihomoDNS(group)
		if err != nil {
			if p.log != nil {
				p.log.Warn("mihomo profile: the dns section was left out", "code", "mihomo_dns_render_failed", "err", err)
			}
		} else {
			root.set("dns", dnsNode(d, false))
			direct = d.Direct
			providers = d.Providers
			block = d.Block
			blockDomains = d.BlockDomains
			torrentApps, torrent = d.TorrentApps, d.Torrent
		}
	}
	if len(providers)+len(block)+len(torrent) > 0 {
		// The lists of a country (dns.SplitRule.Geo): downloaded through the group, so that they arrive where the sites they
		// name are the point. The key "proxy" is the group's name, which groupName kept free of anything YAML or a rule line would read.
		rp := newMap()
		for _, pr := range append(append(slices.Clone(block), torrent...), providers...) {
			rp.set(pr.Name, newMap().
				set("type", strNode("http")).set("behavior", strNode(pr.Behavior)).set("format", strNode("mrs")).
				set("interval", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(dns.MihomoProviderInterval)}).
				set("proxy", quoted(group)).set("url", quoted(pr.URL)).n)
		}
		root.set("rule-providers", rp.n)
	}
	rules := seqNode()
	for _, s := range blockDomains { // Max: the very first rules, ahead of DOMAIN-SUFFIX,ru,DIRECT
		rules.Content = append(rules.Content, quoted("DOMAIN-SUFFIX,"+s+",REJECT"))
	}
	for _, pr := range block { // first of all: the ad list is dropped whatever else matches
		rules.Content = append(rules.Content, quoted(pr.RuleTo("REJECT")))
	}
	for _, a := range torrentApps { // torrent clients by their program, then the public trackers: after the blocks, before the split
		rules.Content = append(rules.Content, quoted("PROCESS-NAME,"+a+",DIRECT"))
	}
	for _, pr := range torrent {
		rules.Content = append(rules.Content, quoted(pr.Rule()))
	}
	for _, s := range direct {
		rules.Content = append(rules.Content, quoted("DOMAIN-SUFFIX,"+s+",DIRECT"))
	}
	for _, pr := range providers {
		rules.Content = append(rules.Content, quoted(pr.Rule()))
	}
	rules.Content = append(rules.Content, quoted("MATCH,"+group))
	root.set("rules", rules)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root.n); err != nil {
		return nil, fmt.Errorf("mihomo profile: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// dnsNode is the dns section: fake-ip so that rules see names, IPv6 answers off (the profile's tunnel and the
// resolvers are IPv4), the servers of the preset (dns.MihomoDNS says why each is where it is).
func dnsNode(d dns.MihomoDNS, respectRules bool) *yaml.Node {
	m := newMap()
	m.set("enable", boolNode(true)).set("ipv6", boolNode(false))
	m.set("enhanced-mode", strNode("fake-ip")).set("fake-ip-range", strNode("198.18.0.1/16"))
	if len(d.DefaultNameserver) > 0 {
		m.set("default-nameserver", quotedSeq(d.DefaultNameserver))
	}
	m.set("nameserver", quotedSeq(d.Nameserver))
	if len(d.Policy) > 0 {
		pol := newMap()
		for _, e := range d.Policy {
			pol.n.Content = append(pol.n.Content, quoted(e.Domain), quotedSeq(e.Servers))
		}
		m.set("nameserver-policy", pol.n)
	}
	m.set("proxy-server-nameserver", quotedSeq(d.ProxyServerNameserver))
	if respectRules { // the resolvers' own traffic follows the rules (it needs proxy-server-nameserver, which is always there)
		m.set("respect-rules", boolNode(true))
	}
	return m.n
}

// acceptsGzip reports whether the request lists gzip (q=0 means "not acceptable").
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		tok, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(tok), "gzip") {
			continue
		}
		q = strings.ReplaceAll(strings.ToLower(q), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}

// gzipped compresses b.
var mihomoGzipWriters = sync.Pool{New: func() any {
	w, err := gzip.NewWriterLevel(io.Discard, gzip.BestCompression)
	if err != nil {
		panic(err) // gzip.BestCompression is a valid level.
	}
	return w
}}

func gzipped(b []byte) []byte {
	var buf bytes.Buffer
	zw := mihomoGzipWriters.Get().(*gzip.Writer)
	zw.Reset(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	mihomoGzipWriters.Put(zw)
	return buf.Bytes()
}
