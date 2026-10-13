package vless

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

func renderURI(in protocols.RenderInput) (plugin.Fragment, bool) {
	v, ok := renderView(in)
	if !ok {
		return plugin.Fragment{}, false
	}
	host := v.params.Host
	name := fmt.Sprintf("%s · vless · %d", nodeName(in.Inbound.Node, host), in.Inbound.Port)
	user := url.User(in.Secret).String()
	if in.MaskSecrets {
		user = protocols.MaskedSecret
	}
	q := make(url.Values)
	q.Set("encryption", "none")
	if v.params.Flow != "" {
		q.Set("flow", v.params.Flow)
	}
	q.Set("fp", v.params.Fingerprint) // TLS profiles need not carry a REALITY block
	if v.params.Transport == "xhttp" {
		if v.params.XHTTPExtra != "" {
			q.Set("extra", v.params.XHTTPExtra)
		}
		if v.params.ClientHost != "" {
			q.Set("host", v.params.ClientHost)
		}
		q.Set("mode", v.params.XHTTPMode)
		q.Set("path", v.params.XHTTPPath)
	} else if v.params.Transport == "ws" {
		if v.params.ClientHost != "" {
			q.Set("host", v.params.ClientHost)
		}
		q.Set("path", v.params.WSPath)
	}
	q.Set("security", v.params.Security)
	if v.params.Security == "reality" {
		q.Set("pbk", v.params.PublicKey)
		q.Set("sid", v.params.ShortID)
		q.Set("spx", v.params.SpiderX)
	}
	q.Set("sni", v.params.SNI)
	q.Set("type", v.params.Transport)
	uri := "vless://" + user + "@" + address(host, v.params.Port) + "?" + q.Encode() + "#" + (&url.URL{Fragment: name}).EscapedFragment()
	return plugin.Fragment{Format: plugin.FormatURIList, Name: name, Data: []byte(uri)}, true
}

func xhttpExtra(s XHTTPSettings) string {
	padding := defaultString(s.XPaddingBytes, defaultXPaddingBytes)
	if padding == defaultXPaddingBytes {
		return ""
	}
	b, _ := json.Marshal(struct {
		XPaddingBytes string `json:"xPaddingBytes"`
	}{XPaddingBytes: padding})
	return string(b)
}

func clientXHTTPMode(s Settings) string {
	mode := defaultString(s.XHTTP.Mode, "auto")
	if s.Security == "tls" && s.CDN != nil && s.CDN.Host != "" && mode == "auto" {
		return "packet-up"
	}
	return mode
}

func spx(deviceID string) string {
	// Use the first eight hexadecimal characters of SHA-256 as a stable, device-specific path.
	sum := sha256.Sum256([]byte(deviceID))
	return "/" + hex.EncodeToString(sum[:])[:8]
}

func renderMihomo(in protocols.RenderInput) (plugin.Fragment, bool) {
	v, ok := renderView(in)
	if !ok {
		return plugin.Fragment{}, false
	}
	host := v.params.Host
	name := in.DisplayName
	if name == "" {
		name = fmt.Sprintf("%s · vless · %d", nodeName(in.Inbound.Node, host), in.Inbound.Port)
	}
	uuid := in.Secret
	if in.MaskSecrets {
		uuid = protocols.MaskedSecret
	}

	var kv []*yaml.Node
	add := func(k string, v *yaml.Node) { kv = append(kv, strNode(k), v) }
	add("name", quoted(name))
	add("type", strNode("vless"))
	add("server", quoted(v.params.Host))
	add("port", intNode(int(v.params.Port)))
	add("uuid", quoted(uuid))
	add("udp", boolNode(true))
	add("packet-encoding", strNode("xudp"))
	add("tls", boolNode(true))
	add("servername", quoted(v.params.SNI))
	add("client-fingerprint", strNode(v.params.Fingerprint))
	add("skip-cert-verify", boolNode(false))
	if v.params.Security == "reality" && v.params.Transport == "tcp" {
		add("network", strNode("tcp"))
		add("flow", strNode(v.params.Flow))
	} else if v.params.Transport == "xhttp" {
		add("network", strNode("xhttp"))
		xopts := []*yaml.Node{strNode("path"), quoted(v.params.XHTTPPath), strNode("mode"), strNode(v.params.XHTTPMode)}
		if v.params.ClientHost != "" {
			xopts = append(xopts, strNode("host"), quoted(v.params.ClientHost))
		}
		if v.params.XHTTPPadding != defaultXPaddingBytes {
			xopts = append(xopts, strNode("x-padding-bytes"), quoted(v.params.XHTTPPadding))
		}
		add("xhttp-opts", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: xopts})
	} else if v.params.Transport == "ws" {
		add("network", strNode("ws"))
		wsOpts := []*yaml.Node{strNode("path"), quoted(v.params.WSPath)}
		if v.params.ClientHost != "" {
			wsOpts = append(wsOpts, strNode("headers"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{strNode("Host"), quoted(v.params.ClientHost)}})
		}
		add("ws-opts", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: wsOpts})
	}
	if v.params.Security == "reality" {
		add("reality-opts", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			strNode("public-key"), quoted(v.params.PublicKey), strNode("short-id"), quoted(v.params.ShortID),
		}})
	}

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

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func strNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func quoted(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle}
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

func boolNode(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)}
}
