package awg

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/mimicry"
)

// The validator is stricter than both implementations: the
// module answers EINVAL without a reason, amneziawg-go accepts jmin > jmax, <t><t> and <r 100000>, the module
// accepts <c> that every client rejects. Rules whose violation is a hard error are in validate; things worth a
// remark but legal are in Warnings.
//
// Not checked here, because they need more than one profile or the node: that the port is free on the node,
// that the subnets do not overlap another profile or the node's own networks (panel: CreateInbound, agent:
// preflight), that the subnets never change once used, and whether the node's AWG backend knows a 3.1 profile
// (agent: awg_module_too_old).

const (
	maxJc         = 128
	maxJunk       = 1280
	minSWithKey   = 12  // S1..S4 with a header protection key: the packet sizes go into a ChaCha20 nonce
	maxS12        = 150 // Amnezia UI limits
	maxS34        = 64
	minMTU        = 1200
	maxMTU        = 1420
	defaultMTU    = 1280
	maxUint16     = 1<<16 - 1
	maxUint32     = 1<<32 - 1
	hpkBytes      = 32
	jcWarnAbove   = 12
	maxChainTotal = mimicry.MaxChainChars
	maxSeedLen    = 64

	// The outer packet of a data message is MTU + 32 (WG header and tag) + S4 + 8 (UDP) + 40 (IPv6, the larger
	// outer header) and must fit the 1500 of an Ethernet path. maxS4 and the mtu_headroom warning hold to it.
	pathMTU      = 1500
	wireOverhead = 32 + 8 + 40
	// A NAT forgets an idle UDP mapping after about this many seconds; a keepalive must come sooner.
	natTimeoutSeconds = 30
)

// Amnezia's own values of the 3.x timers. All of them together are a profile that was never randomised.
var amneziaTimers = [5]string{"100-120", "3-7", "150-180", "5-15", "15-20"}

// Validate implements protocols.Protocol.
func (*Protocol) Validate(raw json.RawMessage) []protocols.FieldError {
	s, errs := parse(raw)
	if errs != nil {
		return errs
	}
	return validate(s)
}

func validate(s Settings) []protocols.FieldError {
	var errs []protocols.FieldError
	add := func(ptr, code, msg string) { errs = append(errs, fe(ptr, code, msg)) }
	o := s.Obfuscation

	// 1. port, version, basics
	if s.Port < 1 || s.Port > 65535 {
		add("/port", "out_of_range", "port must be 1-65535")
	}
	switch s.Version {
	case Version31, Version20:
	default:
		add("/version", "invalid_enum", "must be 3.1 or 2.0")
	}
	if s.MTU < minMTU || s.MTU > maxMTU {
		add("/mtu", "out_of_range", "MTU must be 1200-1420")
	}
	switch s.Egress {
	case "direct", "warp":
	default:
		add("/egress", "invalid_enum", "must be direct or warp")
	}
	if !mimicry.Valid(o.Preset) {
		add("/obfuscation/preset", "invalid_enum", "unknown mimicry preset")
	}
	if _, err := parseSubnet4(s.Subnet4); err != nil {
		add("/subnet4", "invalid", err.Error())
	}
	if s.Subnet6 != "" {
		if _, err := parseSubnet6(s.Subnet6); err != nil {
			add("/subnet6", "invalid", err.Error())
		}
	}

	// 2. what a 2.0 server does not know. The stored header protection key of a 2.0 profile is tolerated and
	// never sent: the framework regenerates an empty secret from the defaults (3.1), so a 2.0 profile cannot
	// be stored without one.
	if s.Version == Version20 {
		for _, f := range []struct{ ptr, v string }{
			{"content_padding_addition", o.ContentPaddingAddition}, {"rekey_after_time", o.RekeyAfterTime},
			{"rekey_timeout", o.RekeyTimeout}, {"reject_after_time", o.RejectAfterTime},
			{"keepalive_timeout", o.KeepaliveTimeout}, {"max_handshake_attempts", o.MaxHandshakeAttempts},
		} {
			if f.v != "" {
				add("/obfuscation/"+f.ptr, "not_in_version", "AmneziaWG 2.0 does not have this setting; leave it empty")
			}
		}
		if o.RandomTrailers {
			add("/obfuscation/random_trailers", "not_in_version", "AmneziaWG 2.0 does not have random trailers")
		}
		if o.DisableCookies {
			add("/obfuscation/disable_cookies", "not_in_version", "AmneziaWG 2.0 does not have this setting")
		}
	}

	// 3. junk packets
	if o.Jc < 0 || o.Jc > maxJc {
		add("/obfuscation/jc", "out_of_range", "Jc must be 0-128")
	}
	if o.Jmin < 0 || o.Jmin > maxJunk {
		add("/obfuscation/jmin", "out_of_range", "Jmin must be 0-1280")
	}
	if o.Jmax < 0 || o.Jmax > maxJunk {
		add("/obfuscation/jmax", "out_of_range", "Jmax must be 0-1280")
	} else if o.Jmin <= maxJunk && o.Jmin > o.Jmax {
		add("/obfuscation/jmax", "invalid", "Jmax must not be below Jmin")
	}

	// 4. packet sizes
	hpk := s.hpk() != ""
	sizes := [4]int{148, 92, 64, 32}
	for i, sv := range o.S() {
		ptr := "/obfuscation/s" + strconv.Itoa(i+1)
		limit := maxS12
		if i >= 2 {
			limit = maxS34
		}
		switch {
		case sv < 0 || sv > limit:
			add(ptr, "out_of_range", fmt.Sprintf("S%d must be 0-%d", i+1, limit))
		case hpk && sv < minSWithKey:
			add(ptr, "out_of_range", fmt.Sprintf("S%d must be at least 12 with a header protection key", i+1))
		}
		sizes[i] += sv
	}
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			if sizes[i] == sizes[j] {
				add("/obfuscation/s"+strconv.Itoa(j+1), "duplicate_size", fmt.Sprintf("packet sizes 148+S1, 92+S2, 64+S3 and 32+S4 must all differ (S%d and S%d give %d)", i+1, j+1, sizes[i]))
			}
		}
	}

	// 5. header types: four non-overlapping ranges; "off" is exactly 1,2,3,4 and all four are always sent
	var hr [4]rng
	hOK := true
	for i, hv := range o.H() {
		r, err := parseRange(hv, maxUint32)
		if err != nil {
			add("/obfuscation/h"+strconv.Itoa(i+1), "invalid", "H"+strconv.Itoa(i+1)+": "+err.Error())
			hOK = false
			continue
		}
		hr[i] = r
	}
	if hOK {
		for i := 0; i < 4; i++ {
			for j := i + 1; j < 4; j++ {
				if hr[i].lo <= hr[j].hi && hr[j].lo <= hr[i].hi {
					add("/obfuscation/h"+strconv.Itoa(j+1), "overlap", fmt.Sprintf("H%d and H%d overlap; the four header ranges must be disjoint (1, 2, 3, 4 means off)", i+1, j+1))
				}
			}
		}
	}

	// 6. I1..I5
	total := 0
	for i, iv := range o.I() {
		ptr := "/obfuscation/i" + strconv.Itoa(i+1)
		total += len(iv)
		if _, err := mimicry.Parse(iv); err != nil {
			add(ptr, "invalid", err.Error())
		}
	}
	if total > maxChainTotal {
		add("/obfuscation/i1", "too_long", fmt.Sprintf("I1-I5 together are %d characters, at most %d (awg show hangs on longer lines)", total, maxChainTotal))
	}

	// 7. 3.1 only: key, padding, timers
	if o.HeaderProtectionKey != "" { // checked on 2.0 too: a stored key must at least be a key
		if b, err := base64.StdEncoding.DecodeString(o.HeaderProtectionKey); err != nil || len(b) != hpkBytes {
			add("/obfuscation/header_protection_key", "invalid", "must be 32 bytes in base64")
		}
	}
	ranges := map[string]rng{}
	for _, f := range []struct{ name, v string }{
		{"content_padding_addition", o.ContentPaddingAddition}, {"rekey_after_time", o.RekeyAfterTime},
		{"rekey_timeout", o.RekeyTimeout}, {"reject_after_time", o.RejectAfterTime},
		{"keepalive_timeout", o.KeepaliveTimeout}, {"max_handshake_attempts", o.MaxHandshakeAttempts},
	} {
		if f.v == "" {
			continue
		}
		r, err := parseRange(f.v, maxUint16)
		if err != nil {
			add("/obfuscation/"+f.name, "invalid", err.Error())
			continue
		}
		ranges[f.name] = r
	}
	if a, ok := ranges["rekey_after_time"]; ok {
		if b, ok := ranges["reject_after_time"]; ok && a.hi >= b.lo {
			add("/obfuscation/rekey_after_time", "invalid", "rekey_after_time must end below where reject_after_time starts")
		}
	}
	if o.PersistentKeepalive != "" {
		r, err := parseRange(o.PersistentKeepalive, maxUint16)
		switch {
		case err != nil:
			add("/obfuscation/persistent_keepalive", "invalid", err.Error())
		case s.Version == Version20 && r.lo != r.hi:
			add("/obfuscation/persistent_keepalive", "not_in_version", "AmneziaWG 2.0 takes a single number of seconds")
		}
	}

	// 8. client only: the host name of the packets and the per-device signature. Neither reaches the node.
	if o.Domain != "" {
		if _, err := mimicry.NormalizeDomain(o.Domain); err != nil {
			add("/obfuscation/domain", "invalid", err.Error())
		}
	}
	switch {
	case len(o.SignatureSeed) > maxSeedLen:
		add("/obfuscation/signature_seed", "invalid", fmt.Sprintf("at most %d characters", maxSeedLen))
	case o.PerDeviceSignature && o.SignatureSeed == "":
		add("/obfuscation/signature_seed", "required", "per-device signatures need a seed")
	}
	return errs
}

// Warning is a legal setting that deserves a remark in the editor; Code is stable, the UI owns the text. Params
// carry the numbers the text needs (mtu_headroom: suggested_mtu).
type Warning struct {
	Pointer string
	Code    string
	Params  map[string]string
}

// Warnings lists the remarks for valid settings. Invalid settings give none.
func Warnings(raw json.RawMessage) []Warning {
	s, errs := parse(raw)
	if errs != nil || len(validate(s)) > 0 {
		return nil
	}
	return warnings(s)
}

// plainHeaders reports H1..H4 = 1, 2, 3, 4: the message types of plain WireGuard.
func plainHeaders(o Obfuscation) bool { return o.H() == [4]string{"1", "2", "3", "4"} }

// headerWeakness finds the H values that give the profile away when headers are sent in the clear (no header
// protection key): the pointer of the first offender per kind. Valid settings only.
func headerWeakness(o Obfuscation) (small, lt5 string) {
	for i, hv := range o.H() {
		r, err := parseRange(hv, maxUint32)
		if err != nil {
			continue
		}
		ptr := "/obfuscation/h" + strconv.Itoa(i+1)
		if _, _, isRange := cut(hv); !isRange {
			if r.lo < 5 && lt5 == "" {
				lt5 = ptr
			}
		} else if r.hi-r.lo < 1000 && small == "" {
			small = ptr
		}
	}
	return small, lt5
}

// keepaliveMax is the longest PersistentKeepalive the profile can send, 0 when it sends none.
func keepaliveMax(o Obfuscation) uint64 {
	r, err := parseRange(o.PersistentKeepalive, maxUint16)
	if err != nil {
		return 0
	}
	return r.hi
}

// presetPortMismatch reports whether the preset belongs on other ports than the profile's (mimicry.NaturalPorts).
func presetPortMismatch(s Settings) ([]int, bool) {
	np := mimicry.NaturalPorts(s.Obfuscation.Preset)
	return np, len(np) > 0 && !slices.Contains(np, s.Port)
}

func warnings(s Settings) []Warning {
	o := s.Obfuscation
	var w []Warning
	add := func(ptr, code string, params ...string) {
		x := Warning{Pointer: ptr, Code: code}
		for i := 0; i+1 < len(params); i += 2 {
			if x.Params == nil {
				x.Params = map[string]string{}
			}
			x.Params[params[i]] = params[i+1]
		}
		w = append(w, x)
	}
	if o.Jc > jcWarnAbove {
		add("/obfuscation/jc", "jc_high")
	}
	if o.Jmax >= s.MTU {
		add("/obfuscation/jmax", "jmax_ge_mtu") // fragmentation
	}
	if s.Version == Version31 && o.DisableCookies {
		add("/obfuscation/disable_cookies", "no_flood_protection")
	}
	if s.Version == Version31 && o.RandomTrailers && !(o.S1 == o.S2 && o.S2 == o.S3 && o.S3 == o.S4) {
		add("/obfuscation/random_trailers", "trailers_unequal_s")
	}
	if s.MTU > defaultMTU {
		add("/mtu", "mtu_above_1280") // desktop AmneziaVPN overwrites the MTU of a vpn:// key anyway
	}
	if o.S4 > maxS4(s.MTU) {
		add("/mtu", "mtu_headroom", "suggested_mtu", strconv.Itoa(suggestedMTU(o.S4)), "s4", strconv.Itoa(o.S4))
	}
	if s.hpk() == "" { // with a header protection key the H values are encrypted on the wire
		small, lt5 := headerWeakness(o)
		switch {
		case plainHeaders(o):
			add("/obfuscation/h1", "h_default_v20") // the message types of plain WireGuard: the main fingerprint
		default:
			if lt5 != "" {
				add(lt5, "h_lt5")
			}
			if small != "" {
				add(small, "h_small_range")
			}
		}
	}
	if np, odd := presetPortMismatch(s); odd {
		add("/port", "preset_port", "preset", o.Preset, "ports", joinInts(np))
	}
	if k := keepaliveMax(o); k > natTimeoutSeconds {
		add("/obfuscation/persistent_keepalive", "keepalive_over_nat", "max", strconv.FormatUint(k, 10))
	}
	return w
}

func joinInts(a []int) string {
	s := make([]string, len(a))
	for i, n := range a {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ",")
}

// ---- helpers ----

type rng struct{ lo, hi uint64 }

// parseRange reads "n" or "lo-hi": decimal digits only, lo <= hi, hi <= limit.
func parseRange(s string, limit uint64) (rng, error) {
	if s == "" {
		return rng{}, fmt.Errorf("must be a number or a range lo-hi")
	}
	lo, hi, isRange := cut(s)
	a, ok1 := digits(lo)
	b, ok2 := a, ok1
	if isRange {
		b, ok2 = digits(hi)
	}
	switch {
	case !ok1 || !ok2:
		return rng{}, fmt.Errorf("must be a number or a range lo-hi")
	case a > b:
		return rng{}, fmt.Errorf("range start is above its end")
	case b > limit:
		return rng{}, fmt.Errorf("must not exceed %d", limit)
	}
	return rng{a, b}, nil
}

func cut(s string) (lo, hi string, isRange bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// digits parses up to 10 decimal digits.
func digits(s string) (uint64, bool) {
	if s == "" || len(s) > 10 {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + uint64(s[i]-'0')
	}
	return n, true
}

var ula = netip.MustParsePrefix("fc00::/7")

func parseSubnet4(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("must be an IPv4 network like 10.66.4.0/22")
	}
	if p.Bits() < 16 || p.Bits() > 24 {
		return netip.Prefix{}, fmt.Errorf("prefix length must be 16-24")
	}
	if p != p.Masked() {
		return netip.Prefix{}, fmt.Errorf("host bits must be zero (%s)", p.Masked())
	}
	if !p.Addr().IsPrivate() {
		return netip.Prefix{}, fmt.Errorf("must lie in private address space (10/8, 172.16/12, 192.168/16)")
	}
	return p, nil
}

func parseSubnet6(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is6() || p.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("must be an IPv6 network like fd66:66:0:1::/64")
	}
	if p.Bits() != 64 {
		return netip.Prefix{}, fmt.Errorf("prefix length must be 64")
	}
	if p != p.Masked() {
		return netip.Prefix{}, fmt.Errorf("host bits must be zero (%s)", p.Masked())
	}
	if !ula.Contains(p.Addr()) {
		return netip.Prefix{}, fmt.Errorf("must be a unique local address network (fc00::/7)")
	}
	return p, nil
}
