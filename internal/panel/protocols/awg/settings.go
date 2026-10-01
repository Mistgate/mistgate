package awg

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
)

// Protocol versions of a profile. 3.0 is 3.1 without RandomTrailers and is not offered: Amnezia itself calls
// everything 3.x "3.1".
const (
	Version31 = "3.1"
	Version20 = "2.0"
)

// Settings is the merged profile settings document (the secret, header_protection_key, in place). JSON names
// are the schema property names; the schema test keeps the two in step.
type Settings struct {
	Version     string      `json:"version"`
	Port        int         `json:"port"`
	MTU         int         `json:"mtu"`
	Egress      string      `json:"egress"` // direct | warp
	Subnet4     string      `json:"subnet4"`
	Subnet6     string      `json:"subnet6"` // "" = no IPv6 inside the tunnel
	Obfuscation Obfuscation `json:"obfuscation"`
}

// Obfuscation is the `obfuscation` object of the settings. The fields marked client are only written into
// client configs (the node never initiates a handshake); the rest the node needs as well.
type Obfuscation struct {
	Preset string `json:"preset"` // quic|dns|stun|...|custom: the generator's input and a label in the UI

	Jc   int `json:"jc"`   // client
	Jmin int `json:"jmin"` // client
	Jmax int `json:"jmax"` // client

	S1 int `json:"s1"`
	S2 int `json:"s2"`
	S3 int `json:"s3"`
	S4 int `json:"s4"`

	H1 string `json:"h1"`
	H2 string `json:"h2"`
	H3 string `json:"h3"`
	H4 string `json:"h4"`

	I1 string `json:"i1"` // client
	I2 string `json:"i2"` // client
	I3 string `json:"i3"` // client
	I4 string `json:"i4"` // client
	I5 string `json:"i5"` // client

	HeaderProtectionKey string `json:"header_protection_key"` // 3.1, secret
	RandomTrailers      bool   `json:"random_trailers"`       // 3.1
	DisableCookies      bool   `json:"disable_cookies"`       // 3.1, node side only

	ContentPaddingAddition string `json:"content_padding_addition"` // 3.1, ranges
	RekeyAfterTime         string `json:"rekey_after_time"`         // 3.1
	RekeyTimeout           string `json:"rekey_timeout"`            // 3.1
	RejectAfterTime        string `json:"reject_after_time"`        // 3.1
	KeepaliveTimeout       string `json:"keepalive_timeout"`        // 3.1
	MaxHandshakeAttempts   string `json:"max_handshake_attempts"`   // 3.1

	PersistentKeepalive string `json:"persistent_keepalive"` // client [Peer]; 2.0: a single number

	// Where I1..I5 come from. All three are client only and never reach the node. A profile stored before they
	// existed has them zero: per-device signatures off, the profile's I1..I5 for everyone.
	Domain             string `json:"domain"`               // host name in the packets (UsesDomain presets); "" = one from the pool
	PerDeviceSignature bool   `json:"per_device_signature"` // every device gets its own I1..I5, drawn from SignatureSeed and its id
	SignatureSeed      string `json:"signature_seed"`       // random, not a secret: it only makes the per-device chains reproducible
}

// I returns I1..I5 in order.
func (o Obfuscation) I() [5]string { return [5]string{o.I1, o.I2, o.I3, o.I4, o.I5} }

// H returns H1..H4 in order.
func (o Obfuscation) H() [4]string { return [4]string{o.H1, o.H2, o.H3, o.H4} }

// S returns S1..S4 in order.
func (o Obfuscation) S() [4]int { return [4]int{o.S1, o.S2, o.S3, o.S4} }

// hpk is the header protection key the profile actually uses: only 3.1 has one (see validate: a 2.0 profile may
// carry a stored key that is never sent anywhere).
func (s Settings) hpk() string {
	if s.Version == Version31 {
		return s.Obfuscation.HeaderProtectionKey
	}
	return ""
}

// NodeSettings is the JSON the node engine receives in InboundSpec.Settings: the profile settings without what
// became first-class fields (port, mtu, egress, subnets), without the client-only I1..I5, preset and
// persistent keepalive, plus the server key of this inbound. Field order and omitempty are part of the
// contract: the bytes feed the state hash.
type NodeSettings struct {
	Version     string          `json:"version"`
	PrivateKey  string          `json:"private_key"`
	Obfuscation NodeObfuscation `json:"obfuscation"`
}

// NodeObfuscation is the "obfuscation" object of NodeSettings. The node reads the document as awgcfg.Settings,
// which keeps these fields under "obfuscation": a flat document makes every inbound fail with
// /obfuscation/h1: required.
type NodeObfuscation struct {
	Jc   int `json:"jc"`
	Jmin int `json:"jmin"`
	Jmax int `json:"jmax"`
	S1   int `json:"s1"`
	S2   int `json:"s2"`
	S3   int `json:"s3"`
	S4   int `json:"s4"`

	H1 string `json:"h1"`
	H2 string `json:"h2"`
	H3 string `json:"h3"`
	H4 string `json:"h4"`

	HeaderProtectionKey    string `json:"header_protection_key,omitempty"`
	RandomTrailers         bool   `json:"random_trailers,omitempty"`
	DisableCookies         bool   `json:"disable_cookies,omitempty"`
	ContentPaddingAddition string `json:"content_padding_addition,omitempty"`
	RekeyAfterTime         string `json:"rekey_after_time,omitempty"`
	RekeyTimeout           string `json:"rekey_timeout,omitempty"`
	RejectAfterTime        string `json:"reject_after_time,omitempty"`
	KeepaliveTimeout       string `json:"keepalive_timeout,omitempty"`
	MaxHandshakeAttempts   string `json:"max_handshake_attempts,omitempty"`
}

func nodeSettings(s Settings, privateKey string) NodeSettings {
	o := s.Obfuscation
	n := NodeObfuscation{
		Jc: o.Jc, Jmin: o.Jmin, Jmax: o.Jmax,
		S1: o.S1, S2: o.S2, S3: o.S3, S4: o.S4,
		H1: o.H1, H2: o.H2, H3: o.H3, H4: o.H4,
	}
	if s.Version == Version31 { // a 2.0 server does not know any of these, whatever the profile stores
		n.HeaderProtectionKey = o.HeaderProtectionKey
		n.RandomTrailers = o.RandomTrailers
		n.DisableCookies = o.DisableCookies
		n.ContentPaddingAddition = o.ContentPaddingAddition
		n.RekeyAfterTime = o.RekeyAfterTime
		n.RekeyTimeout = o.RekeyTimeout
		n.RejectAfterTime = o.RejectAfterTime
		n.KeepaliveTimeout = o.KeepaliveTimeout
		n.MaxHandshakeAttempts = o.MaxHandshakeAttempts
	}
	return NodeSettings{Version: s.Version, PrivateKey: privateKey, Obfuscation: n}
}

// parse decodes the settings strictly: an unknown key is an error, as it would be silently lost otherwise.
func parse(raw json.RawMessage) (Settings, []protocols.FieldError) {
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return s, []protocols.FieldError{fe("/"+strings.ReplaceAll(te.Field, ".", "/"), "invalid_type", "wrong type")}
		}
		return s, []protocols.FieldError{fe("", "invalid_json", err.Error())}
	}
	return s, nil
}

func fe(ptr, code, msg string) protocols.FieldError {
	return protocols.FieldError{Pointer: ptr, Code: code, Message: msg}
}
