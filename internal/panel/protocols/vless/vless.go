// Package vless is the panel half of the VLESS protocol plugin. It offers REALITY over TCP (RAW + Vision) and XHTTP, plus
// XHTTP+TLS and WebSocket+TLS through the node's TLS front.
package vless

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ID is the plugin id.
const ID = "vless"

// Settings is the merged profile settings document. Private keys are per inbound and never live here.
type Settings struct {
	Port      int             `json:"port"`
	Transport string          `json:"transport"`
	Security  string          `json:"security"`
	TLSMode   string          `json:"tls_mode,omitempty"`
	SNI       string          `json:"sni,omitempty"`
	CDN       *CDNSettings    `json:"cdn,omitempty"`
	Reality   RealitySettings `json:"reality"`
	XHTTP     XHTTPSettings   `json:"xhttp"`
	WS        *WSSettings     `json:"ws,omitempty"`
	Egress    string          `json:"egress"`
}

// Target kinds: where REALITY sends everyone who is not a client. self is the node's own site (its name is the profile's
// server name for the inbound, see BuildInbound), site is another site named by Target.
const (
	targetSelf = "self"
	targetSite = "site"
)

type RealitySettings struct {
	// TargetKind is self or site. A document that predates the field (a profile made before the node could serve its own
	// site) carries a target and no kind: it reads as site, see kind().
	TargetKind  string   `json:"target_kind"`
	Target      string   `json:"target"`
	ServerNames []string `json:"server_names"`
	ShortIDs    []string `json:"short_ids"`
	Fingerprint string   `json:"fingerprint"`
}

type XHTTPSettings struct {
	Path          string `json:"path"`
	Host          string `json:"host"`
	Mode          string `json:"mode"`
	XPaddingBytes string `json:"x_padding_bytes"`
}

type CDNSettings struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
}

type WSSettings struct {
	Path string `json:"path"`
	Host string `json:"host"`
}

// kind is the target kind of a document: the one it names, else site when it has a target (what every profile did before
// the field existed), else self.
func (r RealitySettings) kind() string {
	switch {
	case r.TargetKind != "":
		return r.TargetKind
	case strings.TrimSpace(r.Target) != "":
		return targetSite
	}
	return targetSelf
}

// NodeSettings is the deterministic settings_json contract consumed by internal/node/vless.parseSpec.
// Field order is kept in sync with the node wireSettings type.
type NodeSettings struct {
	Transport string        `json:"transport"`
	Security  string        `json:"security"`
	Flow      string        `json:"flow"`
	CDN       *NodeCDN      `json:"cdn,omitempty"`
	Reality   NodeReality   `json:"reality"`
	XHTTP     NodeXHTTP     `json:"xhttp"`
	WS        NodeWebSocket `json:"ws"`
}

type NodeCDN struct {
	Host string `json:"host"`
}

type NodeReality struct {
	Target      string   `json:"target"`
	ServerNames []string `json:"server_names"`
	ShortIDs    []string `json:"short_ids"`
	PrivateKey  string   `json:"private_key"`
}

type NodeXHTTP struct {
	Path          string `json:"path"`
	Host          string `json:"host"`
	Mode          string `json:"mode"`
	XPaddingBytes string `json:"x_padding_bytes"`
}

type NodeWebSocket struct {
	Path string `json:"path"`
	Host string `json:"host"`
}

// Protocol implements protocols.Protocol, InboundInitializer and SettingsNormalizer.
type Protocol struct{}

func New() *Protocol { return &Protocol{} }

var (
	_ protocols.Protocol           = (*Protocol)(nil)
	_ protocols.InboundInitializer = (*Protocol)(nil)
	_ protocols.SettingsNormalizer = (*Protocol)(nil)
)

func (*Protocol) ID() string             { return ID }
func (*Protocol) DisplayName() string    { return "VLESS" }
func (*Protocol) SettingsSchema() []byte { return []byte(settingsSchema) }

// DefaultSettings leaves out what must differ from one profile to the next: the short ids and the XHTTP path. The panel
// UI keeps these defaults for the whole session, so a value drawn here would be shared by every profile made in it;
// NormalizeSettings draws them when a profile is made, the way the AmneziaWG plugin does for its obfuscation block.
func (*Protocol) DefaultSettings() (json.RawMessage, error) {
	return json.Marshal(Settings{
		Port: 443, Transport: "xhttp", Security: "reality",
		Reality: RealitySettings{TargetKind: targetSelf, ServerNames: []string{}, ShortIDs: []string{}, Fingerprint: "chrome"},
		XHTTP:   XHTTPSettings{Mode: "auto", XPaddingBytes: defaultXPaddingBytes},
		Egress:  "direct",
	})
}

// NormalizeSettings draws the short ids (one of 8 and one of 16 hex characters) and the XHTTP path of a profile that has
// none. A document that gives a target and does not say what kind it is (a script written for the panel before the node
// could serve its own site) means another site, as it always did: the default "self" would otherwise drop its target in
// silence. It returns Merged itself when there is nothing to change.
func (*Protocol) NormalizeSettings(in protocols.NormalizeInput) (json.RawMessage, error) {
	s, errs := parse(in.Merged)
	if len(errs) != 0 {
		return in.Merged, nil // Validate answers a document it cannot read
	}
	changed := false
	switch {
	case s.Reality.TargetKind == targetSelf && namesTargetOnly(in.Input) && !hasTargetKind(in.Old):
		s.Reality.TargetKind, changed = targetSite, true
	case s.Reality.TargetKind == "": // stored before the field existed: say what it has always meant
		s.Reality.TargetKind, changed = s.Reality.kind(), true
	}
	if s.Security == "tls" && s.Transport == "xhttp" && s.CDN != nil && s.CDN.Host != "" && (s.XHTTP.Mode == "" || s.XHTTP.Mode == "auto") {
		s.XHTTP.Mode, changed = "packet-up", true
	}
	if s.Transport == "ws" && (s.WS == nil || s.WS.Path == "") {
		if s.WS == nil {
			s.WS = &WSSettings{}
		}
		path, err := randomPath(10)
		if err != nil {
			return nil, err
		}
		s.WS.Path, changed = path, true
	}
	if len(s.Reality.ShortIDs) != 0 && s.XHTTP.Path != "" {
		if !changed {
			return in.Merged, nil
		}
		return json.Marshal(s)
	}
	if len(s.Reality.ShortIDs) == 0 {
		ids, err := generatedShortIDs()
		if err != nil {
			return nil, err
		}
		s.Reality.ShortIDs = ids
	}
	if s.XHTTP.Path == "" {
		path, err := randomPath(10)
		if err != nil {
			return nil, err
		}
		s.XHTTP.Path = path
	}
	return json.Marshal(s)
}

// realityKeys is the reality object of a settings document as raw members, so that "said nothing" and "said self" differ.
func realityKeys(raw json.RawMessage) map[string]json.RawMessage {
	var doc struct {
		Reality map[string]json.RawMessage `json:"reality"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return doc.Reality
}

// namesTargetOnly: the client's document gives a target and no target kind.
func namesTargetOnly(input json.RawMessage) bool {
	r := realityKeys(input)
	var target string
	_ = json.Unmarshal(r["target"], &target)
	_, hasKind := r["target_kind"]
	return strings.TrimSpace(target) != "" && !hasKind
}

func hasTargetKind(stored json.RawMessage) bool {
	_, ok := realityKeys(stored)["target_kind"]
	return ok
}

const defaultXPaddingBytes = "100-1000"

func generatedShortIDs() ([]string, error) {
	for {
		short := make([]byte, 4)
		long := make([]byte, 8)
		if _, err := rand.Read(short); err != nil {
			return nil, err
		}
		if _, err := rand.Read(long); err != nil {
			return nil, err
		}
		a, b := hex.EncodeToString(short), hex.EncodeToString(long)
		if a != b {
			return []string{a, b}, nil
		}
	}
}

func randomPath(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "/" + string(b), nil
}

func parse(raw json.RawMessage) (Settings, []protocols.FieldError) {
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return s, []protocols.FieldError{{Pointer: "/" + strings.ReplaceAll(te.Field, ".", "/"), Code: "invalid_type", Message: "wrong type"}}
		}
		message := err.Error()
		if strings.HasPrefix(message, "json: unknown field ") {
			field := strings.Trim(strings.TrimPrefix(message, "json: unknown field "), `"`)
			return s, []protocols.FieldError{{Pointer: "/" + strings.ReplaceAll(field, ".", "/"), Code: "unsupported", Message: "this field is not supported"}}
		}
		return s, []protocols.FieldError{{Pointer: "", Code: "invalid_json", Message: message}}
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return s, []protocols.FieldError{{Pointer: "", Code: "invalid_json", Message: "multiple JSON values"}}
		}
		return s, []protocols.FieldError{{Pointer: "", Code: "invalid_json", Message: err.Error()}}
	}
	return s, nil
}

func fe(ptr, code, message string) protocols.FieldError {
	return protocols.FieldError{Pointer: ptr, Code: code, Message: message}
}

func (*Protocol) Validate(raw json.RawMessage) []protocols.FieldError {
	s, errs := parse(raw)
	if len(errs) != 0 {
		return errs
	}
	return validate(s)
}

func validate(s Settings) []protocols.FieldError {
	var errs []protocols.FieldError
	add := func(ptr, code, message string) { errs = append(errs, fe(ptr, code, message)) }
	if s.Port < 1 || s.Port > 65535 {
		add("/port", "out_of_range", "port must be 1-65535")
	} else if s.Port == 80 {
		add("/port", "reserved", "port 80 is reserved for the Let's Encrypt HTTP check")
	}
	switch s.Transport {
	case "tcp", "xhttp", "ws":
	default:
		add("/transport", "invalid_enum", "transport must be xhttp, tcp or ws")
	}
	switch s.Security {
	case "reality", "tls":
	default:
		add("/security", "invalid_enum", "security must be reality or tls")
	}
	if s.Security == "reality" {
		if s.Transport == "ws" {
			add("/transport", "requires_tls", "WebSocket requires TLS security")
		}
		if s.TLSMode != "" {
			add("/tls_mode", "unsupported", "certificate mode is only available with TLS security")
		}
		if s.SNI != "" {
			add("/sni", "unsupported", "server name is only available with TLS security")
		}
		if s.CDN != nil {
			add("/cdn", "unsupported", "CDN settings are only available with TLS security")
		}
		switch s.Reality.kind() {
		case targetSelf:
			// The node's own site: its name comes from the node or the inbound (BuildInbound).
		case targetSite:
			errs = append(errs, validateSiteTarget(s.Reality)...)
		default:
			add("/reality/target_kind", "invalid_enum", "target_kind must be self or site")
		}
		if len(s.Reality.ShortIDs) < 1 || len(s.Reality.ShortIDs) > 8 {
			add("/reality/short_ids", "length", "must contain 1-8 values")
		} else {
			seen := make(map[string]struct{}, len(s.Reality.ShortIDs))
			for _, value := range s.Reality.ShortIDs {
				if len(value) > 16 || len(value)%2 != 0 {
					add("/reality/short_ids", "invalid", "values must be even-length hex of at most 16 characters")
					break
				}
				decoded, err := hex.DecodeString(value)
				if err != nil {
					add("/reality/short_ids", "invalid", "values must contain hexadecimal characters only")
					break
				}
				padded := make([]byte, 8) // the node compares the zero-padded 8 bytes: "01" and "0100" are one id
				copy(padded, decoded)
				key := string(padded)
				if _, ok := seen[key]; ok {
					add("/reality/short_ids", "duplicate", "values must be unique")
					break
				}
				seen[key] = struct{}{}
			}
		}
	} else if s.Security == "tls" {
		if s.Transport == "tcp" {
			add("/transport", "unsupported", "TCP with Vision requires REALITY security")
		}
		switch s.TLSMode {
		case "acme_domain", "self_signed":
		case "":
			add("/tls_mode", "required", "choose an origin certificate mode")
		default:
			add("/tls_mode", "invalid_enum", "must be acme_domain or self_signed")
		}
		if s.SNI != "" {
			if s.TLSMode == "acme_domain" && (!isHostname(s.SNI) || !strings.Contains(s.SNI, ".")) || s.TLSMode != "acme_domain" && !validHost(s.SNI) {
				add("/sni", "format", "must be a domain for ACME or a host name for a self-signed certificate")
			}
		}
		cdnHost := ""
		if s.CDN != nil {
			cdnHost = strings.TrimSpace(s.CDN.Host)
			if cdnHost != "" && (!isHostname(cdnHost) || !strings.Contains(cdnHost, ".")) {
				add("/cdn/host", "format", "must be a plain domain name")
			}
			if s.CDN.Port < 0 || s.CDN.Port > 65535 {
				add("/cdn/port", "out_of_range", "must be 1-65535")
			}
		}
		if s.TLSMode == "self_signed" && cdnHost == "" {
			add("/cdn/host", "required", "a self-signed origin needs a CDN host")
		}
		if s.Transport == "ws" {
			if s.WS == nil || s.WS.Path == "" || len(s.WS.Path) > 64 || s.WS.Path[0] != '/' || strings.IndexFunc(s.WS.Path, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '/' || r == '_' || r == '-')
			}) >= 0 {
				add("/ws/path", "format", "must start with / and contain only letters, digits, / _ -")
			}
			if s.WS != nil && s.WS.Host != "" && !validHost(s.WS.Host) {
				add("/ws/host", "format", "must be empty or a host name")
			}
		}
	}
	if s.Security == "reality" {
		switch s.Reality.Fingerprint {
		case "chrome", "firefox", "safari", "ios", "edge", "random":
		default:
			add("/reality/fingerprint", "invalid_enum", "must be chrome, firefox, safari, ios, edge or random")
		}
	}
	if s.Transport == "xhttp" {
		errs = append(errs, validateXHTTP(s.XHTTP)...)
		if s.Security == "tls" && s.XHTTP.Host != "" {
			add("/xhttp/host", "unsupported", "TLS clients use the origin or CDN host")
		}
	}
	switch s.Egress {
	case "direct", "warp":
	default:
		add("/egress", "invalid_enum", "must be direct or warp")
	}
	return errs
}

// validateSiteTarget checks the target of another site and the names REALITY accepts for it.
func validateSiteTarget(r RealitySettings) []protocols.FieldError {
	var errs []protocols.FieldError
	add := func(ptr, code, message string) { errs = append(errs, fe(ptr, code, message)) }
	targetHost, targetErr := targetHostPort(r.Target)
	if targetErr != nil {
		add("/reality/target", targetErr.code, targetErr.message)
	}
	names := r.ServerNames
	switch {
	case len(names) > 8:
		add("/reality/server_names", "length", "at most 8 host names")
	case len(names) == 0:
		// empty = the target's host; an IP target has no host name to fall back to
		if targetErr == nil && !isHostname(targetHost) {
			add("/reality/server_names", "required", "the target is an IP address: list the host names its certificate is valid for")
		}
	default:
		seen := make(map[string]struct{}, len(names))
		for _, name := range names {
			if !isHostname(name) {
				add("/reality/server_names", "invalid", "names must be host names without wildcards")
				break
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				add("/reality/server_names", "duplicate", "names must be unique")
				break
			}
			seen[key] = struct{}{}
		}
	}
	return errs
}

// targetError is a refusal of the target field: a code the admin UI words itself (field.reality.target.<code>) and the
// English sentence that stands in until it does.
type targetError struct{ code, message string }

func (e *targetError) Error() string { return e.message }

func targetHostPort(target string) (string, *targetError) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", &targetError{"required", "enter the site's host:port"}
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", &targetError{"format", "must be an external host:port"}
	}
	if !validHost(host) {
		return "", &targetError{"host", "must use a plain host name or IP address"}
	}
	if portText == "" || strings.IndexFunc(portText, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return "", &targetError{"port_range", "port must be 1-65535"}
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", &targetError{"port_range", "port must be 1-65535"}
	}
	return host, nil
}

func validHost(host string) bool {
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return true
	}
	return isHostname(host)
}

func isHostname(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validateXHTTP(s XHTTPSettings) []protocols.FieldError {
	var errs []protocols.FieldError
	add := func(ptr, message string) { errs = append(errs, fe("/xhttp/"+ptr, "invalid", message)) }
	switch {
	case s.Path == "" || len(s.Path) > 64 || s.Path[0] != '/':
		add("path", "must start with / and be at most 64 characters")
	case strings.IndexFunc(s.Path, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '/' || r == '_' || r == '-')
	}) >= 0:
		add("path", "may contain only letters, digits, / _ -")
	}
	if s.Host != "" && !validHost(s.Host) {
		add("host", "must be empty or a host name")
	}
	switch s.Mode {
	case "", "auto", "packet-up", "stream-up", "stream-one":
	default:
		add("mode", "must be auto, packet-up, stream-up or stream-one")
	}
	padding := defaultString(s.XPaddingBytes, defaultXPaddingBytes)
	parts := strings.Split(padding, "-")
	if len(parts) != 2 {
		add("x_padding_bytes", "must be written min-max, for example 100-1000")
	} else {
		from, errFrom := strconv.ParseUint(parts[0], 10, 32)
		to, errTo := strconv.ParseUint(parts[1], 10, 32)
		if errFrom != nil || errTo != nil || from < 1 || from > to || to > 4096 {
			add("x_padding_bytes", "must satisfy 1 <= min <= max <= 4096")
		}
	}
	return errs
}

// effectiveServerNames are the TLS names REALITY accepts for another site: the list, or the target's host when the list is
// empty. Nothing derived is stored, so a changed target keeps the names in step. For the node's own site the one name is
// the inbound's (siteName), not a setting.
func effectiveServerNames(s RealitySettings) []string {
	if len(s.ServerNames) > 0 {
		return s.ServerNames
	}
	host, err := targetHostPort(s.Target)
	if err == nil && isHostname(host) {
		return []string{host}
	}
	return nil
}

// siteName is the name of the node's own site, which is the certificate's name and the only name REALITY accepts: the
// inbound's own server name, else the node's address when that is a host name. A name needs a dot (a certificate authority
// does not issue for a bare label), and an IP address has none, so such a node needs an SNI (NeedsDomainError).
func siteName(in protocols.InboundInput) (string, error) {
	if o := in.TLSServerNameOverride; o != "" {
		name := strings.ToLower(strings.TrimSuffix(o, "."))
		if !isHostname(name) || !strings.Contains(name, ".") {
			return "", &protocols.ServerNameError{Name: o, DomainOnly: true}
		}
		return name, nil
	}
	name := strings.ToLower(strings.TrimSuffix(in.Node.Address, "."))
	if isHostname(name) && strings.Contains(name, ".") {
		return name, nil
	}
	return "", &protocols.NeedsDomainError{Address: in.Node.Address, Reality: true}
}

func (*Protocol) Summary(raw json.RawMessage) string {
	s, errs := parse(raw)
	if len(errs) != 0 {
		return ""
	}
	label := "XHTTP"
	if s.Transport == "tcp" {
		label = "TCP"
	} else if s.Transport == "ws" {
		label = "WebSocket"
	}
	security := strings.ToUpper(s.Security)
	parts := []string{label + " " + strconv.Itoa(s.Port), security}
	if s.Transport == "tcp" {
		parts = append(parts, "Vision")
	}
	if s.Egress == "warp" {
		parts = append(parts, "WARP")
	}
	return strings.Join(parts, " · ")
}

// InitInbound generates an X25519 REALITY key pair for this inbound. State is the private key; Public is safe to
// include in client configurations. The access framework parks State when an inbound is removed and restores it
// when that same profile is added back to the node.
func (*Protocol) InitInbound(protocols.InboundInitInput) (protocols.InboundInit, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return protocols.InboundInit{}, err
	}
	public, err := json.Marshal(struct {
		PublicKey string `json:"public_key"`
	}{PublicKey: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())})
	if err != nil {
		return protocols.InboundInit{}, err
	}
	return protocols.InboundInit{State: priv.Bytes(), Public: public}, nil
}

// BuildInbound lifts the TCP listen port, TLS certificate and egress out of settings. The node receives only the origin
// fields needed to build the TLS front; the CDN endpoint stays in the panel profile for client rendering.
func (*Protocol) BuildInbound(in protocols.InboundInput) (plugin.InboundSpec, error) {
	s, errs := parse(in.Profile.Settings)
	if len(errs) == 0 {
		errs = validate(s)
	}
	if len(errs) > 0 {
		return plugin.InboundSpec{}, fmt.Errorf("invalid profile settings: %s %s", errs[0].Pointer, errs[0].Message)
	}
	port := s.Port
	if in.PortOverride != 0 {
		port = int(in.PortOverride)
	}
	if port < 1 || port > 65535 || port == 80 {
		return plugin.InboundSpec{}, errors.New("VLESS TCP port must be 1-65535 and not 80")
	}
	var privateKey []byte
	target, names, tlsSpec := "", []string(nil), plugin.TLS{}
	if s.Security == "reality" {
		priv, err := ecdh.X25519().NewPrivateKey(in.PluginState)
		if err != nil {
			return plugin.InboundSpec{}, errors.New("the REALITY private key of this inbound is missing or damaged")
		}
		privateKey = priv.Bytes()
		target, names = strings.TrimSpace(s.Reality.Target), effectiveServerNames(s.Reality)
		if s.Reality.kind() == targetSelf {
			name, err := siteName(in)
			if err != nil {
				return plugin.InboundSpec{}, err
			}
			target, names, tlsSpec = "self", []string{name}, plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: name}
		}
	} else {
		name := strings.TrimSpace(in.TLSServerNameOverride)
		if name == "" {
			name = strings.TrimSpace(s.SNI)
		}
		if name == "" && (isHostname(in.Node.Address) || net.ParseIP(strings.Trim(in.Node.Address, "[]")) != nil) {
			name = strings.Trim(in.Node.Address, "[]")
		}
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		mode := plugin.TLSAcmeDomain
		if s.TLSMode == "self_signed" {
			mode = plugin.TLSSelfSigned
		}
		if mode == plugin.TLSAcmeDomain {
			if name == "" || !isHostname(name) || !strings.Contains(name, ".") {
				if in.TLSServerNameOverride != "" {
					return plugin.InboundSpec{}, &protocols.ServerNameError{Name: in.TLSServerNameOverride, DomainOnly: true}
				}
				return plugin.InboundSpec{}, &protocols.NeedsDomainError{Address: in.Node.Address}
			}
		} else if name == "" || !validHost(name) {
			if in.TLSServerNameOverride != "" {
				return plugin.InboundSpec{}, &protocols.ServerNameError{Name: in.TLSServerNameOverride}
			}
			return plugin.InboundSpec{}, &protocols.NeedsDomainError{Address: in.Node.Address}
		}
		tlsSpec = plugin.TLS{Mode: mode, ServerName: name}
	}
	var shortIDs []string
	if s.Security == "reality" {
		shortIDs = make([]string, len(s.Reality.ShortIDs))
		for i, id := range s.Reality.ShortIDs {
			shortIDs[i] = strings.ToLower(id)
		}
	}
	xhttp := s.XHTTP
	xhttp.Mode = defaultString(xhttp.Mode, "auto")
	if s.Security == "tls" && s.CDN != nil && s.CDN.Host != "" && xhttp.Mode == "auto" {
		xhttp.Mode = "packet-up"
	}
	xhttp.XPaddingBytes = defaultString(xhttp.XPaddingBytes, defaultXPaddingBytes)
	if s.Security == "tls" {
		xhttp.Host = ""
	}
	flow := ""
	if s.Transport == "tcp" {
		flow = "xtls-rprx-vision"
	}
	var cdn *NodeCDN
	if s.Security == "tls" && s.CDN != nil && s.CDN.Host != "" {
		cdn = &NodeCDN{Host: strings.ToLower(strings.TrimSpace(s.CDN.Host))}
	}
	var ws NodeWebSocket
	if s.Security == "tls" && s.Transport == "ws" && s.WS != nil {
		ws.Path = s.WS.Path
	}
	settings, err := json.Marshal(NodeSettings{
		Transport: s.Transport, Security: s.Security, Flow: flow,
		CDN: cdn,
		Reality: NodeReality{
			Target: target, ServerNames: names, ShortIDs: shortIDs,
			PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey),
		},
		XHTTP: NodeXHTTP{Path: xhttp.Path, Host: xhttp.Host, Mode: xhttp.Mode, XPaddingBytes: xhttp.XPaddingBytes},
		WS:    ws,
	})
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	return plugin.InboundSpec{
		Protocol: ID, ProfileID: in.Profile.ID, Version: in.SpecVersion, Enabled: in.Enabled,
		Listen: plugin.Listen{Network: "tcp", Port: uint16(port)}, TLS: tlsSpec, Egress: s.Egress, Settings: settings,
	}, nil
}

// IssueCredential makes the UUID v4 VLESS uses as both the client secret and its node-side identity.
func (*Protocol) IssueCredential(protocols.IssueInput) (protocols.Issued, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return protocols.Issued{}, err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	id := encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
	data, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		return protocols.Issued{}, err
	}
	return protocols.Issued{Secret: id, NodeData: data}, nil
}

func (*Protocol) Clients() []plugin.ClientSupport {
	return []plugin.ClientSupport{
		{Client: plugin.ClientHapp, Formats: []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatXrayJSON}},
		{Client: plugin.ClientMihomo, Formats: []plugin.ClientFormat{plugin.FormatMihomo}},
		{Client: plugin.ClientSingbox, Formats: []plugin.ClientFormat{plugin.FormatSingbox}},
	}
}

func (*Protocol) Doctor() []protocols.Check { return nil }

func (*Protocol) Render(in protocols.RenderInput) (plugin.Fragment, bool) {
	switch in.Format {
	case plugin.FormatURIList:
		return renderURI(in)
	case plugin.FormatMihomo:
		return renderMihomo(in)
	case plugin.FormatXrayJSON:
		return renderXray(in)
	case plugin.FormatSingbox:
		return renderSingbox(in)
	default:
		return plugin.Fragment{}, false
	}
}

// ClientParams are the effective VLESS settings a client consumes after profile and per-credential values are resolved.
// Both subscription renderers and the panel's synthetic client use this result.
type ClientParams struct {
	Host, UUID, Flow, Security, SNI, Fingerprint string
	PublicKey, ShortID, SpiderX                  string
	Transport, ClientHost                        string
	XHTTPPath, XHTTPMode, XHTTPPadding           string
	XHTTPExtra                                   string
	WSPath                                       string
	Port                                         uint16
	ALPN                                         []string
}

// ResolveClientParams checks the inputs a client link depends on and makes the per-credential picks: the REALITY
// server name and short id are chosen by fnv32(device id), so the bytes of a subscription stay the same from one fetch
// to the next. XHTTP and WebSocket ALPN follow the node's TLS front; the URI format leaves those defaults implicit.
func ResolveClientParams(in protocols.RenderInput) (ClientParams, bool) {
	s, errs := parse(in.Settings)
	if len(errs) == 0 {
		errs = validate(s)
	}
	if len(errs) != 0 {
		return ClientParams{}, false
	}
	host, ok := plainHost(in.Inbound.Node.Address)
	if !ok || in.Inbound.Port == 0 || in.Inbound.Port == 80 {
		return ClientParams{}, false
	}
	v := ClientParams{
		Host: host, Port: in.Inbound.Port, UUID: in.Secret, Security: s.Security, Transport: s.Transport,
		Fingerprint: defaultString(s.Reality.Fingerprint, "chrome"),
	}
	if s.Security == "reality" && s.Transport == "tcp" {
		v.Flow = "xtls-rprx-vision"
	}
	if s.Transport == "xhttp" {
		v.XHTTPPath = s.XHTTP.Path
		v.XHTTPMode = clientXHTTPMode(s)
		v.XHTTPPadding = defaultString(s.XHTTP.XPaddingBytes, defaultXPaddingBytes)
		v.XHTTPExtra = xhttpExtra(s.XHTTP)
	}
	if s.Transport == "ws" {
		v.WSPath = s.WS.Path
	}
	if s.Security == "tls" {
		name := in.Inbound.TLSServerName
		if name == "" {
			name = s.SNI
		}
		if name == "" && isHostname(in.Inbound.Node.Address) {
			name = in.Inbound.Node.Address
		}
		if !validHost(name) {
			return ClientParams{}, false
		}
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		v.SNI = name
		if s.CDN != nil && s.CDN.Host != "" {
			// Clients dial the CDN host and profile port override, and use the CDN host for TLS SNI and HTTP Host,
			// exactly as the subscription link does. Without a CDN, they dial the node address and inbound port.
			cdnHost := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s.CDN.Host), "."))
			if !isHostname(cdnHost) || !strings.Contains(cdnHost, ".") {
				return ClientParams{}, false
			}
			v.Host = cdnHost
			v.Port = uint16(s.CDN.Port)
			if v.Port == 0 {
				v.Port = 443
			}
			v.SNI = cdnHost
			v.ClientHost = cdnHost
		} else if s.Transport == "xhttp" {
			v.ClientHost = name
		} else if s.WS != nil {
			v.ClientHost = s.WS.Host
		}
		v.ALPN = clientALPN(s.Transport)
		return v, true
	}
	var public struct {
		PublicKey string `json:"public_key"`
	}
	if json.Unmarshal(in.InboundPublic, &public) != nil {
		return ClientParams{}, false
	}
	if key, err := base64.RawURLEncoding.DecodeString(public.PublicKey); err != nil || len(key) != 32 {
		return ClientParams{}, false
	}
	names := effectiveServerNames(s.Reality)
	if s.Reality.kind() == targetSelf {
		names = nil // the node's own site: the one name the inbound was built with
		if in.Inbound.TLSServerName != "" {
			names = []string{in.Inbound.TLSServerName}
		}
	}
	if len(names) == 0 || len(s.Reality.ShortIDs) == 0 {
		return ClientParams{}, false
	}
	hash := fnv32(in.DeviceID)
	v.PublicKey = public.PublicKey
	v.SNI = names[hash%uint32(len(names))]
	v.ShortID = strings.ToLower(s.Reality.ShortIDs[hash%uint32(len(s.Reality.ShortIDs))])
	v.SpiderX = spx(in.DeviceID)
	if s.Transport == "xhttp" {
		v.ClientHost = s.XHTTP.Host
	}
	return v, true
}

func clientALPN(transport string) []string {
	if transport == "ws" {
		return []string{"http/1.1"}
	}
	return []string{"h2", "http/1.1"}
}

// view adapts the shared parameter set to the fields the existing renderers use. The renderer output depends on
// this resolved set, so the synthetic health client and subscriptions select the same address and per-client values.
type view struct {
	params ClientParams
}

func renderView(in protocols.RenderInput) (view, bool) {
	p, ok := ResolveClientParams(in)
	if !ok {
		return view{}, false
	}
	return view{params: p}, true
}

func plainHost(address string) (string, bool) {
	host := strings.Trim(address, "[]")
	if net.ParseIP(host) != nil || isHostname(host) {
		return host, true
	}
	return "", false
}

func fnv32(value string) uint32 {
	var hash uint32 = 2166136261
	for i := 0; i < len(value); i++ {
		hash ^= uint32(value[i])
		hash *= 16777619
	}
	return hash
}

func address(host string, port uint16) string {
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

func nodeName(node protocols.NodeView, fallback string) string {
	if node.Name != "" {
		return node.Name
	}
	return fallback
}
