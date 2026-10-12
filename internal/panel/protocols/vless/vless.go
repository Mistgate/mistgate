// Package vless is the panel half of the VLESS protocol plugin. It offers REALITY with an external target over TCP
// (RAW + Vision) and XHTTP. TLS, WebSocket and the CDN fields come with the node's TLS front: the schema does not
// list them and Validate refuses them, so the node never sees a spec it cannot serve.
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
	Reality   RealitySettings `json:"reality"`
	XHTTP     XHTTPSettings   `json:"xhttp"`
	Egress    string          `json:"egress"`
}

type RealitySettings struct {
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

// NodeSettings is the deterministic settings_json contract consumed by internal/node/vless.parseSpec.
// Field order is kept in sync with the node wireSettings type.
type NodeSettings struct {
	Transport string        `json:"transport"`
	Security  string        `json:"security"`
	Flow      string        `json:"flow"`
	Reality   NodeReality   `json:"reality"`
	XHTTP     NodeXHTTP     `json:"xhttp"`
	WS        NodeWebSocket `json:"ws"`
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
		Reality: RealitySettings{ServerNames: []string{}, ShortIDs: []string{}, Fingerprint: "chrome"},
		XHTTP:   XHTTPSettings{Mode: "auto", XPaddingBytes: defaultXPaddingBytes},
		Egress:  "direct",
	})
}

// NormalizeSettings draws the short ids (one of 8 and one of 16 hex characters) and the XHTTP path of a profile that has
// none. It returns Merged itself when there is nothing to draw.
func (*Protocol) NormalizeSettings(in protocols.NormalizeInput) (json.RawMessage, error) {
	s, errs := parse(in.Merged)
	if len(errs) != 0 || (len(s.Reality.ShortIDs) != 0 && s.XHTTP.Path != "") {
		return in.Merged, nil // Validate answers a document it cannot read
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
	case "tcp", "xhttp":
	case "ws":
		add("/transport", "unsupported", "WebSocket is not available yet; use xhttp or tcp")
	default:
		add("/transport", "invalid_enum", "transport must be xhttp or tcp")
	}
	switch s.Security {
	case "reality":
	case "tls":
		add("/security", "unsupported", "TLS is not available yet; use reality")
	default:
		add("/security", "invalid_enum", "security must be reality")
	}
	targetHost, targetErr := targetHostPort(s.Reality.Target)
	if targetErr != nil {
		add("/reality/target", "invalid", targetErr.Error())
	}
	names := s.Reality.ServerNames
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
	switch s.Reality.Fingerprint {
	case "chrome", "firefox", "safari", "ios", "edge", "random":
	default:
		add("/reality/fingerprint", "invalid_enum", "must be chrome, firefox, safari, ios, edge or random")
	}
	if s.Transport == "xhttp" {
		errs = append(errs, validateXHTTP(s.XHTTP)...)
	}
	switch s.Egress {
	case "direct", "warp":
	default:
		add("/egress", "invalid_enum", "must be direct or warp")
	}
	return errs
}

func targetHostPort(target string) (string, error) {
	target = strings.TrimSpace(target)
	host, portText, err := net.SplitHostPort(target)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("must be an external host:port")
	}
	if !validHost(host) {
		return "", errors.New("must use a plain host name or IP address")
	}
	if portText == "" || strings.IndexFunc(portText, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return "", errors.New("port must be 1-65535")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", errors.New("port must be 1-65535")
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

// effectiveServerNames are the TLS names REALITY accepts: the list, or the target's host when the list is empty.
// Nothing derived is stored, so a changed target keeps the names in step.
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

func (*Protocol) Summary(raw json.RawMessage) string {
	s, errs := parse(raw)
	if len(errs) != 0 {
		return ""
	}
	label := "XHTTP"
	if s.Transport == "tcp" {
		label = "TCP"
	}
	parts := []string{label + " " + strconv.Itoa(s.Port), "REALITY"}
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

// BuildInbound lifts the TCP listen port and egress out of settings. The rest is the node's vless wireSettings.
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
	priv, err := ecdh.X25519().NewPrivateKey(in.PluginState)
	if err != nil {
		return plugin.InboundSpec{}, errors.New("the REALITY private key of this inbound is missing or damaged")
	}
	names := effectiveServerNames(s.Reality)
	shortIDs := make([]string, len(s.Reality.ShortIDs))
	for i, id := range s.Reality.ShortIDs {
		shortIDs[i] = strings.ToLower(id)
	}
	xhttp := s.XHTTP
	xhttp.Mode = defaultString(xhttp.Mode, "auto")
	xhttp.XPaddingBytes = defaultString(xhttp.XPaddingBytes, defaultXPaddingBytes)
	flow := ""
	if s.Transport == "tcp" {
		flow = "xtls-rprx-vision"
	}
	settings, err := json.Marshal(NodeSettings{
		Transport: s.Transport, Security: s.Security, Flow: flow,
		Reality: NodeReality{
			Target: strings.TrimSpace(s.Reality.Target), ServerNames: names, ShortIDs: shortIDs,
			PrivateKey: base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		},
		XHTTP: NodeXHTTP{Path: xhttp.Path, Host: xhttp.Host, Mode: xhttp.Mode, XPaddingBytes: xhttp.XPaddingBytes},
		WS:    NodeWebSocket{},
	})
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	return plugin.InboundSpec{
		Protocol: ID, ProfileID: in.Profile.ID, Version: in.SpecVersion, Enabled: in.Enabled,
		Listen: plugin.Listen{Network: "tcp", Port: uint16(port)}, Egress: s.Egress, Settings: settings,
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
		{Client: plugin.ClientHapp, Formats: []plugin.ClientFormat{plugin.FormatURIList}},
		{Client: plugin.ClientMihomo, Formats: []plugin.ClientFormat{plugin.FormatMihomo}},
	}
}

func (*Protocol) Doctor() []protocols.Check { return nil }

func (*Protocol) Render(in protocols.RenderInput) (plugin.Fragment, bool) {
	switch in.Format {
	case plugin.FormatURIList:
		return renderURI(in)
	case plugin.FormatMihomo:
		return renderMihomo(in)
	default:
		return plugin.Fragment{}, false
	}
}

// view is what both renderers need of one inbound: the settings, the address, and the per-device picks.
type view struct {
	Settings
	host, publicKey, serverName, shortID string
}

// renderView checks the inputs a client link depends on and makes the device's picks: the server name and the short
// id are chosen by fnv32(device id), so the bytes of a subscription stay the same from one fetch to the next.
func renderView(in protocols.RenderInput) (view, bool) {
	s, errs := parse(in.Settings)
	if len(errs) == 0 {
		errs = validate(s)
	}
	if len(errs) != 0 {
		return view{}, false
	}
	host, ok := plainHost(in.Inbound.Node.Address)
	if !ok || in.Inbound.Port == 0 || in.Inbound.Port == 80 {
		return view{}, false
	}
	var public struct {
		PublicKey string `json:"public_key"`
	}
	if json.Unmarshal(in.InboundPublic, &public) != nil {
		return view{}, false
	}
	if key, err := base64.RawURLEncoding.DecodeString(public.PublicKey); err != nil || len(key) != 32 {
		return view{}, false
	}
	names := effectiveServerNames(s.Reality)
	if len(names) == 0 || len(s.Reality.ShortIDs) == 0 {
		return view{}, false
	}
	hash := fnv32(in.DeviceID)
	return view{
		Settings: s, host: host, publicKey: public.PublicKey,
		serverName: names[hash%uint32(len(names))],
		shortID:    strings.ToLower(s.Reality.ShortIDs[hash%uint32(len(s.Reality.ShortIDs))]),
	}, true
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
