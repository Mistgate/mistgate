package hysteria2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// hopIntervalSeconds is how often a Mihomo client moves to another port of the hop range (the core's own default;
// written out so that the profile says it).
const hopIntervalSeconds = 30

// renderMihomo returns ONE proxy as an element of a YAML list (the subscription assembler adds groups, dns and
// rules). It is built from yaml.v3 nodes, never from a string template: the node name is hostile input.
//
// Keys per the core's adapter/outbound/hysteria2.go: `obfs` salamander|gecko with
// `obfs-password`, `sni`, `skip-cert-verify: false`, and for a self-signed node `fingerprint` = the pin the node
// reported (the sha256 of the certificate, which is what the core pins; with a fingerprint the core skips the chain
// check, so the pin is what protects the connection). A self-signed node that has not reported its pin yet gives no
// proxy: a client could not verify it. `ports` + `hop-interval` carry the hop range.
func renderMihomo(in protocols.RenderInput) (plugin.Fragment, bool) {
	var s Settings
	if err := json.Unmarshal(in.Settings, &s); err != nil {
		return plugin.Fragment{}, false
	}
	host := strings.Trim(in.Inbound.Node.Address, "[]")
	if !isIP(host) && !isHostname(host) {
		return plugin.Fragment{}, false // never splice an address that is not a plain host into the proxy
	}
	pin, pinOK := protocols.NormalizePin(in.Inbound.CertPinSHA256)
	if in.Inbound.TLSMode == plugin.TLSSelfSigned && !pinOK {
		return plugin.Fragment{}, false
	}
	name := in.DisplayName
	if name == "" {
		name = in.Inbound.Node.Name
		if name == "" {
			name = host
		}
		name = fmt.Sprintf("%s · hy2 · %d", name, in.Inbound.Port)
	}
	password, obfsPw := in.Secret, s.Obfs.Password
	if in.MaskSecrets {
		password, obfsPw = protocols.MaskedSecret, protocols.MaskedSecret
	}

	var kv []*yaml.Node
	add := func(k string, v *yaml.Node) { kv = append(kv, strNode(k), v) }
	add("name", quoted(name))
	add("type", strNode("hysteria2"))
	add("server", quoted(host))
	add("port", intNode(int(in.Inbound.Port)))
	if from, to := in.Inbound.HopFrom, in.Inbound.HopTo; from != 0 && to != 0 {
		add("ports", quoted(strconv.Itoa(int(from))+"-"+strconv.Itoa(int(to))))
		add("hop-interval", quoted(strconv.Itoa(hopIntervalSeconds)))
	}
	add("password", quoted(password))
	if s.Obfs.Type == "salamander" || s.Obfs.Type == "gecko" {
		add("obfs", strNode(s.Obfs.Type))
		add("obfs-password", quoted(obfsPw))
	}
	if sni := in.Inbound.TLSServerName; sni != "" && !isIP(sni) {
		add("sni", quoted(sni))
	}
	add("skip-cert-verify", boolNode(false))
	if in.Inbound.TLSMode == plugin.TLSSelfSigned {
		add("fingerprint", quoted(pin))
	}
	add("alpn", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle, Content: []*yaml.Node{strNode("h3")}})

	proxy := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: kv}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{proxy}}); err != nil {
		return plugin.Fragment{}, false
	}
	if err := enc.Close(); err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatMihomo, Name: name, Data: buf.Bytes()}, true
}

func strNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}
func intNode(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
}
func boolNode(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(b)}
}
