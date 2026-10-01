package awg

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// The obfuscation score: one number 0-100 for "how far is this profile from the fingerprints a censor already
// knows", with every point explained. The idea is awg-manager's awgConfScore.ts (MIT); the tiers, weights and
// codes below are ours. It rates the configuration, not a censor: a high score means no known tell is left in the
// settings, not that the traffic cannot be blocked.
//
// The base is what the handshake and the headers show on the wire (the tier). Every Item after it is a penalty,
// the one bonus is per-device signatures. Items carry a stable Code (the UI owns the text, awg.score.<code>), the
// pointer of the field to fix and their Delta. Only valid settings are scored.

// Tiers, from the strongest.
const (
	TierHeaderProtection = "header_protection" // 3.1 with a header protection key: the headers are encrypted
	TierRanges           = "ranges"            // no key, H ranges: AmneziaWG 2.0
	TierCPS              = "cps"               // no key, H singles, a signature packet: AmneziaWG 1.5
	TierHeaders          = "headers"           // no key, junk and padding only: AmneziaWG 1.0
	TierWireGuard        = "wireguard"         // nothing hidden
)

var tierBase = map[string]int{TierHeaderProtection: 95, TierRanges: 65, TierCPS: 55, TierHeaders: 40, TierWireGuard: 0}

// ScoreItem is one reason of the score. Delta is negative for a penalty.
type ScoreItem struct {
	Code    string
	Pointer string
	Delta   int
	Params  map[string]string
}

// Score is the result. Value = clamp(Base + the Items' Deltas, 0, 100).
type Score struct {
	Value int
	Base  int
	Tier  string
	Items []ScoreItem
}

// ScoreSettings scores a settings document; false when it does not parse or is not valid (nothing to rate).
func ScoreSettings(raw json.RawMessage) (Score, bool) {
	s, errs := parse(raw)
	if errs != nil || len(validate(s)) > 0 {
		return Score{}, false
	}
	return score(s), true
}

func hasChain(o Obfuscation) bool {
	for _, v := range o.I() {
		if v != "" {
			return true
		}
	}
	return false
}

func hasHRange(o Obfuscation) bool {
	for _, v := range o.H() {
		if _, _, isRange := cut(v); isRange {
			return true
		}
	}
	return false
}

func tierOf(s Settings) string {
	o := s.Obfuscation
	switch {
	case s.hpk() != "":
		return TierHeaderProtection
	case hasHRange(o):
		return TierRanges
	case hasChain(o):
		return TierCPS
	case o.Jc > 0 || o.S1 > 0 || o.S2 > 0 || !plainHeaders(o):
		return TierHeaders
	}
	return TierWireGuard
}

// frozenChain: the profile sends a signature that is the same bytes at every handshake (no <r>, <rc>, <rd> or <t>
// anywhere), which is a statistic of its own. The QUIC presets are like that: an Initial is
// encrypted, a tag inside it would break it.
func frozenChain(o Obfuscation) bool {
	seen := false
	for _, v := range o.I() {
		if v == "" {
			continue
		}
		seen = true
		for _, tag := range []string{"<r ", "<rc ", "<rd ", "<t>"} {
			if strings.Contains(v, tag) {
				return false
			}
		}
	}
	return seen
}

// perDeviceInEffect: every device gets its own chain (deviceSignature would return one). Only for a preset whose chain
// varies: STUN, WebRTC, DTLS and custom give the same bytes whatever the seed, so the profile's own I1-I5 (a hand edit
// included) stay, as the editor says ("the switch changes nothing").
func perDeviceInEffect(o Obfuscation) bool {
	return o.PerDeviceSignature && o.SignatureSeed != "" && PresetVaries(o.Preset)
}

func score(s Settings) Score {
	o := s.Obfuscation
	hpk := s.hpk() != ""
	tier := tierOf(s)
	r := Score{Base: tierBase[tier], Tier: tier}
	add := func(code, ptr string, delta int, params ...string) {
		it := ScoreItem{Code: code, Pointer: ptr, Delta: delta}
		for i := 0; i+1 < len(params); i += 2 {
			if it.Params == nil {
				it.Params = map[string]string{}
			}
			it.Params[params[i]] = params[i+1]
		}
		r.Items = append(r.Items, it)
	}
	// headers: only readable on the wire without the key
	if !hpk && tier != TierWireGuard {
		small, lt5 := headerWeakness(o)
		switch {
		case plainHeaders(o):
			add("h_default_v20", "/obfuscation/h1", -25)
		default:
			if lt5 != "" {
				add("h_lt5", lt5, -5)
			}
			if small != "" {
				add("h_small_range", small, -5)
			}
		}
	}

	// junk and padding: the size of the handshake, hidden by the key's encryption
	if !hpk && tier != TierWireGuard {
		if o.Jc == 0 {
			add("jc_zero", "/obfuscation/jc", -10)
		} else if o.Jmax-o.Jmin < 30 {
			add("junk_narrow", "/obfuscation/jmax", -5)
		}
		for i, v := range [2]int{o.S1, o.S2} {
			if v == 0 {
				add("s_zero", "/obfuscation/s"+strconv.Itoa(i+1), -5)
			}
		}
	}
	if o.Jc > jcWarnAbove {
		add("jc_high", "/obfuscation/jc", -5)
	}
	if o.Jmax >= s.MTU {
		add("jmax_ge_mtu", "/obfuscation/jmax", -5)
	}

	// 3.1: what the data packets and the timers show
	if s.Version == Version31 {
		if hpk && !o.RandomTrailers {
			add("no_random_trailers", "/obfuscation/random_trailers", -10)
		}
		if o.RandomTrailers && !(o.S1 == o.S2 && o.S2 == o.S3 && o.S3 == o.S4) {
			add("trailers_unequal_s", "/obfuscation/random_trailers", -5)
		}
		if o.ContentPaddingAddition == "" && !o.RandomTrailers {
			add("no_data_padding", "/obfuscation/content_padding_addition", -5)
		}
		timers := [5]string{o.RekeyAfterTime, o.RekeyTimeout, o.RejectAfterTime, o.KeepaliveTimeout, o.MaxHandshakeAttempts}
		if timers == amneziaTimers || timers == [5]string{} {
			add("timers_default", "/obfuscation/rekey_after_time", -5)
		}
	}

	// the first packets
	switch {
	case !hasChain(o) && tier != TierWireGuard:
		d := -10
		if hpk {
			d = -5
		}
		add("no_cps", "/obfuscation/i1", d)
	case frozenChain(o):
		add("cps_frozen", "/obfuscation/i1", -5)
	}
	if perDeviceInEffect(o) && PresetVaries(o.Preset) {
		add("per_device_signature", "/obfuscation/per_device_signature", 3)
	}

	// the server's face
	if np, odd := presetPortMismatch(s); odd {
		add("preset_port", "/port", -5, "preset", o.Preset, "ports", joinInts(np))
	}
	if slices.Contains(knownPorts, s.Port) {
		add("port_default", "/port", -5, "port", strconv.Itoa(s.Port))
	}
	if k := keepaliveMax(o); k > natTimeoutSeconds {
		add("keepalive_over_nat", "/obfuscation/persistent_keepalive", -5, "max", strconv.FormatUint(k, 10))
	}
	if o.S4 > maxS4(s.MTU) {
		add("mtu_headroom", "/mtu", -5, "suggested_mtu", strconv.Itoa(suggestedMTU(o.S4)))
	}

	sum := r.Base
	for _, it := range r.Items {
		sum += it.Delta
	}
	r.Value = min(max(sum, 0), 100)
	return r
}
