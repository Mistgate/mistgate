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
	s, host := v.Settings, v.host
	name := fmt.Sprintf("%s · vless · %d", nodeName(in.Inbound.Node, host), in.Inbound.Port)
	user := url.User(in.Secret).String()
	if in.MaskSecrets {
		user = protocols.MaskedSecret
	}
	q := make(url.Values)
	q.Set("encryption", "none")
	if s.Security == "reality" && s.Transport == "tcp" {
		q.Set("flow", "xtls-rprx-vision")
	}
	q.Set("fp", s.Reality.Fingerprint)
	if s.Transport == "xhttp" {
		if extra := xhttpExtra(s.XHTTP); extra != "" {
			q.Set("extra", extra)
		}
		if v.clientHost != "" {
			q.Set("host", v.clientHost)
		}
		q.Set("mode", clientXHTTPMode(s))
		q.Set("path", s.XHTTP.Path)
	} else if s.Transport == "ws" {
		if v.clientHost != "" {
			q.Set("host", v.clientHost)
		}
		q.Set("path", s.WS.Path)
	}
	q.Set("security", s.Security)
	if s.Security == "reality" {
		q.Set("pbk", v.publicKey)
		q.Set("sid", v.shortID)
		q.Set("spx", spx(in.DeviceID))
	}
	q.Set("sni", v.serverName)
	q.Set("type", s.Transport)
	uri := "vless://" + user + "@" + address(host, v.port) + "?" + q.Encode() + "#" + (&url.URL{Fragment: name}).EscapedFragment()
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
	s, host := v.Settings, v.host
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
	add("server", quoted(host))
	add("port", intNode(int(v.port)))
	add("uuid", quoted(uuid))
	add("udp", boolNode(true))
	add("packet-encoding", strNode("xudp"))
	add("tls", boolNode(true))
	add("servername", quoted(v.serverName))
	add("client-fingerprint", strNode(s.Reality.Fingerprint))
	add("skip-cert-verify", boolNode(false))
	if s.Security == "reality" && s.Transport == "tcp" {
		add("network", strNode("tcp"))
		add("flow", strNode("xtls-rprx-vision"))
	} else if s.Transport == "xhttp" {
		add("network", strNode("xhttp"))
		xopts := []*yaml.Node{strNode("path"), quoted(s.XHTTP.Path), strNode("mode"), strNode(clientXHTTPMode(s))}
		if v.clientHost != "" {
			xopts = append(xopts, strNode("host"), quoted(v.clientHost))
		}
		padding := defaultString(s.XHTTP.XPaddingBytes, defaultXPaddingBytes)
		if padding != defaultXPaddingBytes {
			xopts = append(xopts, strNode("x-padding-bytes"), quoted(padding))
		}
		add("xhttp-opts", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: xopts})
	} else if s.Transport == "ws" {
		add("network", strNode("ws"))
		wsOpts := []*yaml.Node{strNode("path"), quoted(s.WS.Path)}
		if v.clientHost != "" {
			wsOpts = append(wsOpts, strNode("headers"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{strNode("Host"), quoted(v.clientHost)}})
		}
		add("ws-opts", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: wsOpts})
	}
	if s.Security == "reality" {
		add("reality-opts", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			strNode("public-key"), quoted(v.publicKey), strNode("short-id"), quoted(v.shortID),
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
