package subs

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/dns"
)

// The Mihomo YAML profile: the proxies the protocol plugins rendered, one select
// group over all of them, the dns section of the user's effective preset and the rules that go with it. It is
// assembled from yaml.v3 nodes only: names (node, profile, brand) are hostile input and a string template would let
// one of them add a proxy or a rule. Some Mihomo-based apps take only `proxies:` and ignore the rest; others merge it.

// reservedNames are the adapters of the core: a proxy or group with one of these names would be shadowed by it.
var reservedNames = []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"}

// mihomoProfile is what the assembler needs about one user's subscription.
type mihomoProfile struct {
	title  string
	lines  []string // proxies, each a YAML list with one element (plugin.Fragment.Data)
	names  []string // the server name of each line, parallel to lines ("" or missing = keep the plugin's own)
	preset *dns.Preset
	log    *slog.Logger
}

func strNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}
func boolNode(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(b)}
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

// setName replaces the name of a proxy mapping (the key exists: proxyNode found it).
func setName(m *yaml.Node, name string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "name" {
			m.Content[i+1] = quoted(name)
			return
		}
	}
}

// uniqueName makes a proxy name safe: not one of the core's adapter names and not taken yet.
func uniqueName(name string, taken map[string]bool) string {
	if name = strings.TrimSpace(name); name == "" {
		name = "server"
	}
	for slices.Contains(reservedNames, strings.ToUpper(name)) || taken[name] {
		name += " ·"
	}
	taken[name] = true
	return name
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

// build writes the profile. Lines that do not parse as one proxy are skipped (and logged without their text: it holds
// a device secret).
func (p mihomoProfile) build() ([]byte, error) {
	proxies := seqNode()
	taken := map[string]bool{}
	var names []string
	for i, line := range p.lines {
		m, own, ok := proxyNode(line)
		if !ok {
			if p.log != nil {
				p.log.Warn("mihomo profile: a rendered proxy was skipped", "index", i)
			}
			continue
		}
		name := own
		if i < len(p.names) && p.names[i] != "" {
			name = p.names[i]
		}
		name = uniqueName(name, taken)
		setName(m, name)
		proxies.Content = append(proxies.Content, m)
		names = append(names, name)
	}
	group := groupName(p.title, taken)
	if group == "" {
		group = "Proxy"
	}
	members := append(slices.Clone(names), "DIRECT")

	root := newMap()
	root.set("proxies", proxies)
	root.set("proxy-groups", seqNode(newMap().
		set("name", quoted(group)).set("type", strNode("select")).set("proxies", quotedSeq(members)).n))

	var direct []string
	if p.preset != nil {
		d, err := p.preset.MihomoDNS(group)
		if err != nil {
			if p.log != nil {
				p.log.Warn("mihomo profile: the dns section was left out", "err", err)
			}
		} else {
			root.set("dns", dnsNode(d))
			direct = d.Direct
		}
	}
	rules := seqNode()
	for _, s := range direct {
		rules.Content = append(rules.Content, quoted("DOMAIN-SUFFIX,"+s+",DIRECT"))
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
func dnsNode(d dns.MihomoDNS) *yaml.Node {
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
func gzipped(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression) // a valid level
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}
