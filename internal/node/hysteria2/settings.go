package hysteria2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mistgate/mistgate/internal/plugin"
)

// settingsJSON is InboundSpec.Settings for hysteria2. Port, hop range, TLS mode,
// server name and egress are first-class InboundSpec fields and are not read from here. Unknown keys are
// ignored so the panel plugin can add fields without a lock-step node upgrade; known keys are validated.
type settingsJSON struct {
	Obfs struct {
		Type     string `json:"type"` // "" | "none" | "salamander" | "gecko"
		Password string `json:"password"`
	} `json:"obfs"`
	Masquerade struct {
		Type    string `json:"type"`     // "" | "decoy" | "none"  (proxy/file/string come later)
		TCPPort *int   `json:"tcp_port"` // absent = 443, 0 = no HTTPS listener
	} `json:"masquerade"`
	IgnoreClientBandwidth bool   `json:"ignore_client_bandwidth"`
	BBRProfile            string `json:"bbr_profile"` // "" | standard | conservative | aggressive
	UpMbps                uint64 `json:"up_mbps"`     // client upload ceiling, 0 = unset
	DownMbps              uint64 `json:"down_mbps"`   // client download ceiling, 0 = unset
	UDP                   *bool  `json:"udp"`         // absent = true
}

type obfsKind int

const (
	obfsNone obfsKind = iota
	obfsSalamander
	obfsGecko
)

// settings is settingsJSON after validation.
type settings struct {
	obfs       obfsKind
	obfsKey    []byte
	masqNone   bool // masquerade "none": everything is the plain 404
	tcpPort    int  // 0 = off
	ignoreBW   bool
	bbrProfile string
	maxRx      uint64 // bytes/s the client may send us (up_mbps)
	maxTx      uint64 // bytes/s we may send the client (down_mbps)
	udp        bool
}

const (
	defaultTCPPort = 443
	maxMbps        = 1_000_000
	bytesPerMbps   = 125_000 // 1 Mbit/s
)

// parseSpec validates everything the engine will rely on. Enabled=false still parses settings (the panel
// validated them, this is the second line) but does not insist on a port.
func parseSpec(spec plugin.InboundSpec) (settings, error) {
	var s settings
	if spec.ID == "" {
		return s, errors.New("inbound id is empty")
	}
	if spec.Protocol != Protocol {
		return s, fmt.Errorf("protocol %q is not %s", spec.Protocol, Protocol)
	}
	if spec.Enabled {
		if n := spec.Listen.Network; n != "" && n != "udp" {
			return s, fmt.Errorf("listen network %q: hysteria2 is udp", n)
		}
		if spec.Listen.Port == 0 {
			return s, errors.New("listen port is 0")
		}
	}

	var j settingsJSON
	if b := bytes.TrimSpace(spec.Settings); len(b) > 0 && !bytes.Equal(b, []byte("null")) {
		if err := json.Unmarshal(b, &j); err != nil {
			return s, fmt.Errorf("settings: %w", err)
		}
	}

	switch strings.ToLower(j.Obfs.Type) {
	case "", "none": // a stored password with type none is fine: the UI keeps the secret when obfs is toggled off
	case "salamander", "gecko":
		if len(j.Obfs.Password) < 4 {
			return s, errors.New("settings: obfs password must be at least 4 bytes")
		}
		s.obfs = obfsSalamander
		if strings.EqualFold(j.Obfs.Type, "gecko") {
			s.obfs = obfsGecko
		}
		s.obfsKey = []byte(j.Obfs.Password)
	default:
		return s, fmt.Errorf("settings: unknown obfs type %q", j.Obfs.Type)
	}

	switch strings.ToLower(j.Masquerade.Type) {
	case "", "decoy":
	case "none":
		s.masqNone = true
	default:
		return s, fmt.Errorf("settings: masquerade type %q is not supported yet (decoy, none)", j.Masquerade.Type)
	}
	s.tcpPort = defaultTCPPort
	if p := j.Masquerade.TCPPort; p != nil {
		if *p < 0 || *p > 65535 {
			return s, fmt.Errorf("settings: masquerade tcp_port %d out of range", *p)
		}
		s.tcpPort = *p
	}

	switch strings.ToLower(j.BBRProfile) {
	case "", "standard", "conservative", "aggressive":
		s.bbrProfile = strings.ToLower(j.BBRProfile)
	default:
		return s, fmt.Errorf("settings: unknown bbr_profile %q", j.BBRProfile)
	}
	if j.UpMbps > maxMbps || j.DownMbps > maxMbps {
		return s, fmt.Errorf("settings: up_mbps/down_mbps above %d", maxMbps)
	}
	s.maxRx, s.maxTx = j.UpMbps*bytesPerMbps, j.DownMbps*bytesPerMbps
	s.ignoreBW = j.IgnoreClientBandwidth
	s.udp = j.UDP == nil || *j.UDP
	return s, nil
}
