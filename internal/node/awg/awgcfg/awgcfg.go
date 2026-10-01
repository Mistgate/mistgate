// Package awgcfg is the pure data model of an AmneziaWG interface shared by the node engine, the wire
// clients (awgnl, awguapi) and the panel plugin: settings JSON, the strict validator and
// the peer types. It has no dependencies and no OS-specific code, so the panel can import it too.
package awgcfg

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Versions of the protocol a profile can speak.
const (
	Version20 = "2.0"
	Version31 = "3.1"
)

// Range is an "n" or "lo-hi" value. The zero value means "unset". In JSON it is a string; a bare number and ""
// are accepted on input.
type Range struct {
	Lo, Hi uint32
	bad    bool // the text did not parse; Validate reports it at the field's pointer
}

// IsZero reports whether the range is unset (or an explicit 0).
func (r Range) IsZero() bool { return r.Lo == 0 && r.Hi == 0 && !r.bad }

// String renders "n" or "lo-hi" (what UAPI and .conf files take); callers skip zero ranges.
func (r Range) String() string {
	if r.Lo == r.Hi {
		return strconv.FormatUint(uint64(r.Lo), 10)
	}
	return strconv.FormatUint(uint64(r.Lo), 10) + "-" + strconv.FormatUint(uint64(r.Hi), 10)
}

// ParseRange parses "n" or "lo-hi" (both ends uint32); "" is the zero Range.
func ParseRange(s string) (Range, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Range{}, nil
	}
	lo, hi, found := strings.Cut(s, "-")
	a, err := strconv.ParseUint(lo, 10, 32)
	if err != nil {
		return Range{bad: true}, fmt.Errorf("range %q: not an unsigned 32-bit number or lo-hi", s)
	}
	if !found {
		return Range{Lo: uint32(a), Hi: uint32(a)}, nil
	}
	b, err := strconv.ParseUint(hi, 10, 32)
	if err != nil {
		return Range{bad: true}, fmt.Errorf("range %q: not an unsigned 32-bit number or lo-hi", s)
	}
	return Range{Lo: uint32(a), Hi: uint32(b)}, nil
}

func (r Range) MarshalJSON() ([]byte, error) {
	if r.IsZero() {
		return []byte(`""`), nil
	}
	return json.Marshal(r.String())
}

func (r *Range) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*r = Range{}
		return nil
	}
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	// A malformed value is remembered, not returned: the whole document still parses and Validate points at the field.
	v, _ := ParseRange(s)
	*r = v
	return nil
}

// Obfuscation is the "obfuscation" object of a profile. Field order is the order the node JSON is
// serialized in (it is hashed). Client-only fields (i1..i5, preset, persistent_keepalive) are part of the type
// because the panel uses it too; the node ignores what it does not need.
type Obfuscation struct {
	Preset string `json:"preset,omitempty"` // panel only: input of the generator, label in the UI

	Jc   int   `json:"jc"`
	Jmin int   `json:"jmin"`
	Jmax int   `json:"jmax"`
	S1   int   `json:"s1"`
	S2   int   `json:"s2"`
	S3   int   `json:"s3"`
	S4   int   `json:"s4"`
	H1   Range `json:"h1"`
	H2   Range `json:"h2"`
	H3   Range `json:"h3"`
	H4   Range `json:"h4"`

	I1 string `json:"i1,omitempty"` // client side only
	I2 string `json:"i2,omitempty"`
	I3 string `json:"i3,omitempty"`
	I4 string `json:"i4,omitempty"`
	I5 string `json:"i5,omitempty"`

	HeaderProtectionKey string `json:"header_protection_key,omitempty"` // base64, 32 bytes; 3.1 only; a secret
	RandomTrailers      bool   `json:"random_trailers,omitempty"`       // 3.1 only
	DisableCookies      bool   `json:"disable_cookies,omitempty"`       // 3.1 only, server side only

	ContentPaddingAddition Range `json:"content_padding_addition,omitzero"` // 3.1
	RekeyAfterTime         Range `json:"rekey_after_time,omitzero"`         // 3.1
	RekeyTimeout           Range `json:"rekey_timeout,omitzero"`            // 3.1
	RejectAfterTime        Range `json:"reject_after_time,omitzero"`        // 3.1
	KeepaliveTimeout       Range `json:"keepalive_timeout,omitzero"`        // 3.1
	MaxHandshakeAttempts   Range `json:"max_handshake_attempts,omitzero"`   // 3.1

	PersistentKeepalive Range `json:"persistent_keepalive,omitzero"` // client [Peer] only
}

// S returns s1..s4.
func (o Obfuscation) S() [4]int { return [4]int{o.S1, o.S2, o.S3, o.S4} }

// H returns h1..h4.
func (o Obfuscation) H() [4]Range { return [4]Range{o.H1, o.H2, o.H3, o.H4} }

// I returns i1..i5.
func (o Obfuscation) I() [5]string { return [5]string{o.I1, o.I2, o.I3, o.I4, o.I5} }

// Timers returns the six 3.1 range fields in the order: content_padding_addition, rekey_after_time,
// rekey_timeout, reject_after_time, keepalive_timeout, max_handshake_attempts.
func (o Obfuscation) Timers() [6]Range {
	return [6]Range{o.ContentPaddingAddition, o.RekeyAfterTime, o.RekeyTimeout, o.RejectAfterTime, o.KeepaliveTimeout, o.MaxHandshakeAttempts}
}

// HPK decodes the header protection key: nil when unset, ok=false when malformed.
func (o Obfuscation) HPK() (key *[32]byte, ok bool) {
	if o.HeaderProtectionKey == "" {
		return nil, true
	}
	k, ok := DecodeKey(o.HeaderProtectionKey)
	if !ok {
		return nil, false
	}
	return &k, true
}

// Settings is the settings_json of an awg inbound (and, with the client-only fields, of the profile). Port, mtu,
// egress and the subnets are first-class InboundSpec fields (listen, tunnel, egress), not settings.
type Settings struct {
	Version     string      `json:"version"`
	PrivateKey  string      `json:"private_key,omitempty"` // base64; the inbound's server key, a secret; node JSON only
	Obfuscation Obfuscation `json:"obfuscation"`
}

// ParseSettings reads a settings document. Unknown keys are ignored (the panel may add fields without a
// lock-step node upgrade); values are not validated here, see Validate.
func ParseSettings(raw []byte) (Settings, error) {
	var s Settings
	if b := bytes.TrimSpace(raw); len(b) > 0 && !bytes.Equal(b, []byte("null")) {
		if err := json.Unmarshal(b, &s); err != nil {
			return Settings{}, fmt.Errorf("settings: %w", err)
		}
	}
	return s, nil
}

// NodeJSON is the canonical settings_json for a node: the client-only fields (i1..i5, preset,
// persistent_keepalive) are dropped and fields come out in a fixed order. The bytes go into the spec hash.
func (s Settings) NodeJSON() ([]byte, error) {
	o := s.Obfuscation
	o.Preset = ""
	o.I1, o.I2, o.I3, o.I4, o.I5 = "", "", "", "", ""
	o.PersistentKeepalive = Range{}
	s.Obfuscation = o
	return json.Marshal(s)
}

// DecodeKey decodes a standard-base64 32-byte key.
func DecodeKey(s string) (k [32]byte, ok bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return k, false
	}
	copy(k[:], b)
	return k, true
}

// Peer is one peer operation for a backend or a wire client. Endpoint and Keepalive are for the client side
// (tests; the engine never sets them on a server: a server that sets keepalive starts initiating handshakes).
type Peer struct {
	PublicKey  [32]byte
	PSK        *[32]byte
	AllowedIPs []netip.Prefix
	Endpoint   string // "host:port", client side only
	Keepalive  Range  // client side only
	Remove     bool   // the session dies at once; re-adding costs the client up to ~15 s
	UpdateOnly bool   // never creates a peer: the safe way to change a live one
	ReplaceIPs bool   // replace the allowed ips instead of adding
}

// PeerStat is one peer as a device reports it.
type PeerStat struct {
	PublicKey  [32]byte
	RxBytes    uint64    // client -> node (plugin.UserTraffic.Up)
	TxBytes    uint64    // node -> client (plugin.UserTraffic.Down)
	LastHS     time.Time // zero = never
	Endpoint   netip.AddrPort
	AllowedIPs []netip.Prefix
}
