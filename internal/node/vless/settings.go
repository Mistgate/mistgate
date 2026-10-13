package vless

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/plugin"
)

type wireSettings struct {
	Transport string `json:"transport"`
	Security  string `json:"security"`
	Flow      string `json:"flow"`
	Reality   struct {
		Target      string   `json:"target"`
		ServerNames []string `json:"server_names"`
		ShortIDs    []string `json:"short_ids"`
		PrivateKey  string   `json:"private_key"`
	} `json:"reality"`
	XHTTP struct {
		Path          string `json:"path"`
		Host          string `json:"host"`
		Mode          string `json:"mode"`
		XPaddingBytes string `json:"x_padding_bytes"`
	} `json:"xhttp"`
	WS struct {
		Path string `json:"path"`
		Host string `json:"host"`
	} `json:"ws"`
	CDN struct {
		Host string `json:"host"`
		Port uint16 `json:"port"`
	} `json:"cdn"`
}

type settings struct {
	transport string
	security  string
	flow      string
	reality   realitySettings
	xhttp     xhttpSettings
	wsPath    string
	wsHost    string
	cdnHost   string
}

type realitySettings struct {
	self bool // target "self": the node's own site behind REALITY
	// dialNet and dialAddr are where REALITY forwards non-clients: the target for a site, the inbound's own site socket for
	// self (filled in when the inbound starts).
	dialNet, dialAddr string
	target            string
	serverNames       []string
	shortIDs          [][]byte
	privateKey        []byte
}

type xhttpSettings struct {
	path         string
	host         string
	mode         string
	xPaddingFrom uint32
	xPaddingTo   uint32
}

func parseSpec(spec plugin.InboundSpec) (settings, error) {
	var out settings
	if spec.Protocol != Protocol {
		return out, fmt.Errorf("protocol must be %q", Protocol)
	}
	if spec.Listen.Network != "tcp" {
		return out, errors.New("listen network must be tcp")
	}
	if spec.Listen.Port == 0 {
		return out, errors.New("listen port must be 1-65535")
	}
	if spec.Listen.Port == 80 {
		return out, errors.New("port 80 is reserved for ACME HTTP-01")
	}
	var raw wireSettings
	if len(spec.Settings) == 0 {
		return out, errors.New("settings_json is required")
	}
	if err := json.Unmarshal(spec.Settings, &raw); err != nil {
		return out, fmt.Errorf("settings_json: %w", err)
	}
	switch raw.Security {
	case "reality":
		if raw.Transport == "ws" {
			return out, errors.New("ws requires tls")
		}
		if raw.Transport != "tcp" && raw.Transport != "xhttp" {
			return out, fmt.Errorf("transport %q is not supported with reality", raw.Transport)
		}
		if raw.CDN.Host != "" || raw.CDN.Port != 0 {
			return out, errors.New("cdn settings require security tls")
		}
	case "tls":
		if raw.Flow != "" {
			return out, errors.New("flow is only derived for TCP with reality")
		}
		if raw.Reality.Target != "" || len(raw.Reality.ServerNames) != 0 || len(raw.Reality.ShortIDs) != 0 || raw.Reality.PrivateKey != "" {
			return out, errors.New("reality settings require security reality")
		}
		if raw.CDN.Port != 0 {
			return out, errors.New("cdn.port is a client setting and is not sent to the TLS origin")
		}
		if raw.Transport == "tcp" {
			return out, errors.New("tcp requires reality")
		}
		if raw.Transport != "xhttp" && raw.Transport != "ws" {
			return out, fmt.Errorf("transport %q is not supported with tls", raw.Transport)
		}
		if spec.TLS.ServerName == "" || (spec.TLS.Mode != plugin.TLSAcmeDomain && spec.TLS.Mode != plugin.TLSSelfSigned) {
			return out, errors.New("TLS requires a certificate mode and server name")
		}
		serverName := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(spec.TLS.ServerName), "."))
		if !validServerName(serverName) || (spec.TLS.Mode == plugin.TLSAcmeDomain && !validDomainName(serverName)) {
			return out, errors.New("TLS server name is invalid for its certificate mode")
		}
		out.cdnHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw.CDN.Host), "."))
		if raw.CDN.Host != "" && !validDomainName(out.cdnHost) {
			return out, errors.New("cdn.host must be a domain name")
		}
		if spec.TLS.Mode == plugin.TLSSelfSigned && out.cdnHost == "" {
			return out, errors.New("self_signed requires cdn.host")
		}
	default:
		return out, errors.New("security must be reality or tls")
	}
	out.transport, out.security = raw.Transport, raw.Security
	out.flow = ""
	if raw.Security == "reality" && raw.Transport == "tcp" {
		out.flow = "xtls-rprx-vision"
	}
	if raw.Security == "reality" {
		out.reality.target = strings.TrimSpace(raw.Reality.Target)
		if out.reality.target == selfTarget {
			// The node's own site: its name is the TLS name of the inbound, the one certificate name and the only REALITY
			// server name (design/vless.md section 7.5).
			out.reality.self = true
			if spec.TLS.Mode != plugin.TLSAcmeDomain {
				return settings{}, errors.New("reality.target self needs tls mode acme_domain")
			}
			name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(spec.TLS.ServerName), "."))
			if name == "" || !validServerName(name) || net.ParseIP(name) != nil || !strings.Contains(name, ".") {
				return settings{}, errors.New("reality.target self needs a tls server name that is a domain")
			}
			if len(raw.Reality.ServerNames) != 1 || !strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(raw.Reality.ServerNames[0]), "."), name) {
				return settings{}, errors.New("reality.server_names must be exactly the tls server name for target self")
			}
			out.reality.serverNames = []string{name}
		} else {
			if spec.TLS.Mode != 0 || spec.TLS.ServerName != "" {
				return out, errors.New("TLS fields are only supported with security tls or reality target self")
			}
			if err := validateTarget(out.reality.target); err != nil {
				return settings{}, fmt.Errorf("reality.target: %w", err)
			}
			if len(raw.Reality.ServerNames) == 0 || len(raw.Reality.ServerNames) > 8 {
				return settings{}, errors.New("reality.server_names must contain 1-8 names")
			}
			seenNames := make(map[string]struct{}, len(raw.Reality.ServerNames))
			for _, name := range raw.Reality.ServerNames {
				name = strings.TrimSpace(name)
				if !validServerName(name) {
					return settings{}, errors.New("reality.server_names contains an invalid name")
				}
				key := strings.ToLower(name)
				if _, ok := seenNames[key]; ok {
					return settings{}, errors.New("reality.server_names contains a duplicate name")
				}
				seenNames[key] = struct{}{}
				out.reality.serverNames = append(out.reality.serverNames, name)
			}
		}
		if len(raw.Reality.ShortIDs) == 0 || len(raw.Reality.ShortIDs) > 8 {
			return settings{}, errors.New("reality.short_ids must contain 1-8 values")
		}
		seenIDs := make(map[string]struct{}, len(raw.Reality.ShortIDs))
		for _, value := range raw.Reality.ShortIDs {
			if len(value) > 16 || len(value)%2 != 0 {
				return settings{}, errors.New("reality.short_ids values must be even-length hex of at most 16 chars")
			}
			decoded, err := hex.DecodeString(value)
			if err != nil {
				return settings{}, errors.New("reality.short_ids contains non-hex characters")
			}
			id := make([]byte, 8)
			copy(id, decoded)
			key := hex.EncodeToString(id)
			if _, ok := seenIDs[key]; ok {
				return settings{}, errors.New("reality.short_ids contains a duplicate value")
			}
			seenIDs[key] = struct{}{}
			out.reality.shortIDs = append(out.reality.shortIDs, id)
		}
		key, err := base64.RawURLEncoding.DecodeString(raw.Reality.PrivateKey)
		if err != nil || len(key) != 32 {
			return settings{}, errors.New("reality.private_key must be a base64url X25519 key")
		}
		out.reality.privateKey = key
	}

	out.xhttp = xhttpSettings{
		path: raw.XHTTP.Path, host: raw.XHTTP.Host, mode: raw.XHTTP.Mode,
	}
	if raw.Transport == "xhttp" {
		var err error
		out.xhttp, err = validateXHTTP(out.xhttp, raw.XHTTP.XPaddingBytes)
		if err != nil {
			return settings{}, err
		}
		if raw.Security == "tls" && raw.XHTTP.Host != "" {
			return settings{}, errors.New("xhttp.host must be empty on the TLS origin")
		}
	}
	out.wsPath, out.wsHost = raw.WS.Path, raw.WS.Host
	if raw.Security == "tls" && raw.Transport == "ws" {
		if err := validateWSPath(raw.WS.Path); err != nil {
			return settings{}, err
		}
		if raw.WS.Host != "" {
			return settings{}, errors.New("ws.host must be empty on the TLS origin")
		}
	} else if raw.Security == "tls" && (raw.WS.Path != "" || raw.WS.Host != "") {
		return settings{}, errors.New("ws settings require the ws transport")
	}
	return out, nil
}

func validDomainName(name string) bool {
	if !validServerName(name) || net.ParseIP(name) != nil || !strings.Contains(name, ".") || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

func validateWSPath(path string) error {
	if path == "" || len(path) > 64 || path[0] != '/' {
		return errors.New("ws.path must start with / and be at most 64 chars")
	}
	for _, r := range path {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/_-", r)) {
			return errors.New("ws.path contains an invalid character")
		}
	}
	return nil
}

func validateTarget(target string) error {
	host, portText, err := net.SplitHostPort(target)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("must be a host:port")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return errors.New("port must be 1-65535")
	}
	return nil
}

func validServerName(name string) bool {
	if name == "" || strings.ContainsAny(name, "*/\\ \t\r\n") {
		return false
	}
	if net.ParseIP(name) != nil {
		return true
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validateXHTTP(s xhttpSettings, padding string) (xhttpSettings, error) {
	if s.path == "" || len(s.path) > 64 || s.path[0] != '/' {
		return s, errors.New("xhttp.path must start with / and be at most 64 chars")
	}
	for _, r := range s.path {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/_-", r)) {
			return s, errors.New("xhttp.path contains an invalid character")
		}
	}
	if s.mode == "" {
		s.mode = "auto"
	}
	switch s.mode {
	case "auto", "packet-up", "stream-up", "stream-one":
	default:
		return s, fmt.Errorf("xhttp.mode %q is invalid", s.mode)
	}
	if padding == "" {
		padding = "100-1000"
	}
	parts := strings.Split(padding, "-")
	if len(parts) != 2 {
		return s, errors.New("xhttp.x_padding_bytes must be min-max")
	}
	from, errFrom := strconv.ParseUint(parts[0], 10, 32)
	to, errTo := strconv.ParseUint(parts[1], 10, 32)
	if errFrom != nil || errTo != nil || from < 1 || from > to || to > 4096 {
		return s, errors.New("xhttp.x_padding_bytes must satisfy 1 <= min <= max <= 4096")
	}
	s.xPaddingFrom, s.xPaddingTo = uint32(from), uint32(to)
	return s, nil
}
