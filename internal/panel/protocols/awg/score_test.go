package awg

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// bestScored is a 3.1 profile with nothing to penalise: header protection and trailers, randomised timers and
// padding, a signature with tags on a preset that varies, per-device signatures, a port nobody expects.
func bestScored() Settings {
	s := clean31()
	o := &s.Obfuscation
	o.Preset, o.PerDeviceSignature, o.SignatureSeed = "rtp", true, "0123456789abcdef"
	o.ContentPaddingAddition = "10-60"
	o.RekeyAfterTime, o.RekeyTimeout, o.RejectAfterTime, o.KeepaliveTimeout, o.MaxHandshakeAttempts = "112-150", "5-8", "180-205", "11-25", "16-19"
	return s
}

func v20Scored() Settings { // 2.0, H ranges, a signature with tags, a preset without a port: 65
	s := fixture20()
	s.Obfuscation.Preset = "stun"
	return s
}

func TestScoreTable(t *testing.T) {
	plain := func(o *Obfuscation) { o.H1, o.H2, o.H3, o.H4 = "1", "2", "3", "4" }
	cases := []struct {
		name  string
		base  func() Settings
		mut   func(*Settings)
		value int
		tier  string
		items []string // code:delta, in the order the score lists them
	}{
		{"3.1 at its best", bestScored, func(*Settings) {}, 98, TierHeaderProtection, []string{"per_device_signature:3"}},
		{"3.1 without per-device signatures", bestScored, func(s *Settings) { s.Obfuscation.PerDeviceSignature = false }, 95, TierHeaderProtection, nil},
		{"per-device on a preset whose packets never change earns nothing", bestScored, func(s *Settings) { s.Obfuscation.Preset = "webrtc" }, 95, TierHeaderProtection, nil},
		{"per-device on the custom preset earns nothing", bestScored, func(s *Settings) { s.Obfuscation.Preset = "custom" }, 95, TierHeaderProtection, nil},
		{"random trailers off", bestScored, func(s *Settings) { s.Obfuscation.RandomTrailers = false }, 88, TierHeaderProtection, []string{"no_random_trailers:-10", "per_device_signature:3"}},
		{"S not equal under trailers", bestScored, func(s *Settings) { s.Obfuscation.S1 = 30 }, 93, TierHeaderProtection, []string{"trailers_unequal_s:-5", "per_device_signature:3"}},
		{"no data padding at all", bestScored, func(s *Settings) { s.Obfuscation.ContentPaddingAddition, s.Obfuscation.RandomTrailers = "", false }, 83, TierHeaderProtection, []string{"no_random_trailers:-10", "no_data_padding:-5", "per_device_signature:3"}},
		{"Amnezia's timers", bestScored, func(s *Settings) {
			o := &s.Obfuscation
			o.RekeyAfterTime, o.RekeyTimeout, o.RejectAfterTime, o.KeepaliveTimeout, o.MaxHandshakeAttempts = amneziaTimers[0], amneziaTimers[1], amneziaTimers[2], amneziaTimers[3], amneziaTimers[4]
		}, 93, TierHeaderProtection, []string{"timers_default:-5", "per_device_signature:3"}},
		{"no timers", bestScored, func(s *Settings) {
			o := &s.Obfuscation
			o.RekeyAfterTime, o.RekeyTimeout, o.RejectAfterTime, o.KeepaliveTimeout, o.MaxHandshakeAttempts = "", "", "", "", ""
		}, 93, TierHeaderProtection, []string{"timers_default:-5", "per_device_signature:3"}},
		{"one timer of Amnezia's is not the set", bestScored, func(s *Settings) { s.Obfuscation.RekeyTimeout = "3-7" }, 98, TierHeaderProtection, []string{"per_device_signature:3"}},
		{"no signature packets under a key", bestScored, func(s *Settings) { s.Obfuscation.I1 = "" }, 93, TierHeaderProtection, []string{"no_cps:-5", "per_device_signature:3"}},
		{"a frozen signature", bestScored, func(s *Settings) { s.Obfuscation.I1 = "<b 0xc0ffee>" }, 93, TierHeaderProtection, []string{"cps_frozen:-5", "per_device_signature:3"}},
		{"a tag in I2 makes the chain move", bestScored, func(s *Settings) { s.Obfuscation.I1, s.Obfuscation.I2 = "<b 0xc0ffee>", "<b 0x01><t>" }, 98, TierHeaderProtection, []string{"per_device_signature:3"}},
		{"quic off 443", bestScored, func(s *Settings) { s.Obfuscation.Preset, s.Obfuscation.I1 = "quic", "<b 0xc0ffee>" }, 88, TierHeaderProtection, []string{"cps_frozen:-5", "per_device_signature:3", "preset_port:-5"}},
		{"WireGuard's own port", bestScored, func(s *Settings) { s.Port = 51820 }, 93, TierHeaderProtection, []string{"per_device_signature:3", "port_default:-5"}},
		{"Amnezia's own port", bestScored, func(s *Settings) { s.Port = 55424 }, 93, TierHeaderProtection, []string{"per_device_signature:3", "port_default:-5"}},
		{"keepalive over the NAT timeout", bestScored, func(s *Settings) { s.Obfuscation.PersistentKeepalive = "25-35" }, 93, TierHeaderProtection, []string{"per_device_signature:3", "keepalive_over_nat:-5"}},
		{"no room for S4", bestScored, func(s *Settings) { s.MTU = 1420 }, 93, TierHeaderProtection, []string{"per_device_signature:3", "mtu_headroom:-5"}},
		{"too many junk packets", bestScored, func(s *Settings) { s.Obfuscation.Jc = 20 }, 93, TierHeaderProtection, []string{"jc_high:-5", "per_device_signature:3"}},
		{"junk as big as the MTU", bestScored, func(s *Settings) { s.Obfuscation.Jmax = 1280 }, 93, TierHeaderProtection, []string{"jmax_ge_mtu:-5", "per_device_signature:3"}},
		{"under a key the headers, junk and S1 are not rated", bestScored, func(s *Settings) {
			o := &s.Obfuscation
			plain(o)
			o.Jc, o.Jmin, o.Jmax = 0, 0, 0
		}, 98, TierHeaderProtection, []string{"per_device_signature:3"}},

		{"2.0 with header ranges", v20Scored, func(*Settings) {}, 65, TierRanges, nil},
		{"2.0 on a preset off its port", v20Scored, func(s *Settings) { s.Obfuscation.Preset = "dns" }, 60, TierRanges, []string{"preset_port:-5"}},
		{"2.0 without junk", v20Scored, func(s *Settings) { s.Obfuscation.Jc = 0 }, 55, TierRanges, []string{"jc_zero:-10"}},
		{"2.0 with a narrow junk size", v20Scored, func(s *Settings) { s.Obfuscation.Jmin, s.Obfuscation.Jmax = 12, 30 }, 60, TierRanges, []string{"junk_narrow:-5"}},
		{"2.0 with S1 = 0", v20Scored, func(s *Settings) { s.Obfuscation.S1 = 0 }, 60, TierRanges, []string{"s_zero:-5"}},
		{"2.0 with S1 = S2 = 0", v20Scored, func(s *Settings) { s.Obfuscation.S1, s.Obfuscation.S2 = 0, 0 }, 55, TierRanges, []string{"s_zero:-5", "s_zero:-5"}},
		{"2.0 with a header below 5", v20Scored, func(s *Settings) { s.Obfuscation.H1 = "3" }, 60, TierRanges, []string{"h_lt5:-5"}},
		{"2.0 with a narrow header range", v20Scored, func(s *Settings) { s.Obfuscation.H3 = "500000-500999" }, 60, TierRanges, []string{"h_small_range:-5"}},
		{"2.0 without signature packets", v20Scored, func(s *Settings) { s.Obfuscation.I1 = "" }, 55, TierRanges, []string{"no_cps:-10"}},
		{"2.0 with plain headers and a signature", v20Scored, func(s *Settings) { plain(&s.Obfuscation) }, 30, TierCPS, []string{"h_default_v20:-25"}},
		{"2.0 with plain headers, junk and no signature", v20Scored, func(s *Settings) { plain(&s.Obfuscation); s.Obfuscation.I1 = "" }, 5, TierHeaders, []string{"h_default_v20:-25", "no_cps:-10"}},
		{"2.0 that is plain WireGuard", v20Scored, func(s *Settings) {
			plain(&s.Obfuscation)
			o := &s.Obfuscation
			o.I1, o.Jc, o.S1, o.S2 = "", 0, 0, 0
		}, 0, TierWireGuard, nil},
		{"the floor is 0", v20Scored, func(s *Settings) {
			o := &s.Obfuscation
			plain(o)
			o.Jc, o.S1, o.S2, o.Preset = 20, 0, 0, "dns"
			o.Jmax, o.PersistentKeepalive = 1280, "35"
			s.MTU, s.Port = 1420, 51820
		}, 0, TierCPS, nil},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		s := c.base()
		c.mut(&s)
		got, ok := ScoreSettings(raw(t, s))
		if !ok {
			t.Errorf("%s: no score for %+v (%+v)", c.name, s.Obfuscation, validate(s))
			continue
		}
		var items []string
		sum := got.Base
		for _, it := range got.Items {
			items = append(items, fmt.Sprintf("%s:%d", it.Code, it.Delta))
			sum += it.Delta
			seen[it.Code] = true
			if !strings.HasPrefix(it.Pointer, "/") {
				t.Errorf("%s: item %s has pointer %q", c.name, it.Code, it.Pointer)
			}
		}
		if c.name == "the floor is 0" {
			if sum >= 0 || got.Value != 0 {
				t.Errorf("%s: sum %d, value %d: the case must go below zero", c.name, sum, got.Value)
			}
		} else if got.Value != c.value || got.Tier != c.tier || !slices.Equal(items, c.items) || sum != c.value {
			t.Errorf("%s: value %d tier %s items %v (sum %d), want %d %s %v", c.name, got.Value, got.Tier, items, sum, c.value, c.tier, c.items)
		}
		if got.Value < 0 || got.Value > 100 || got.Base != tierBase[got.Tier] {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	// every code the score can give is under test
	for _, code := range []string{
		"h_default_v20", "h_lt5", "h_small_range", "jc_zero", "junk_narrow", "s_zero", "jc_high", "jmax_ge_mtu", "no_random_trailers",
		"trailers_unequal_s", "no_data_padding", "timers_default", "no_cps", "cps_frozen", "per_device_signature", "preset_port",
		"port_default", "keepalive_over_nat", "mtu_headroom",
	} {
		if !seen[code] {
			t.Errorf("no case gives %s", code)
		}
	}
}

func TestScoreParamsAndInvalid(t *testing.T) {
	s := bestScored()
	s.Obfuscation.Preset, s.Obfuscation.I1, s.MTU, s.Obfuscation.PersistentKeepalive = "quic", "<b 0xc0ffee>", 1420, "25-35"
	sc, ok := ScoreSettings(raw(t, s))
	if !ok {
		t.Fatal("no score")
	}
	params := map[string]map[string]string{}
	for _, it := range sc.Items {
		params[it.Code] = it.Params
	}
	if params["preset_port"]["ports"] != "443" || params["preset_port"]["preset"] != "quic" || params["mtu_headroom"]["suggested_mtu"] != "1396" || params["keepalive_over_nat"]["max"] != "35" {
		t.Errorf("params: %v", params)
	}
	// the same pointer the warning of that code uses
	for _, w := range warnings(s) {
		for _, it := range sc.Items {
			if it.Code == w.Code && it.Pointer != w.Pointer {
				t.Errorf("%s: score points at %s, the warning at %s", w.Code, it.Pointer, w.Pointer)
			}
		}
	}
	// no score for what the validator refuses, or cannot read
	bad := fixture31()
	bad.Port = 0
	if _, ok := ScoreSettings(raw(t, bad)); ok {
		t.Error("an invalid profile was scored")
	}
	if _, ok := ScoreSettings([]byte("{")); ok {
		t.Error("garbage was scored")
	}
}

// A profile that is warned about for a reason the score prices has that price: the two lists agree on the codes
// they share, so the editor never shows a warning that costs nothing or a cost without the warning.
func TestScoreAndWarningsAgree(t *testing.T) {
	shared := []string{"jc_high", "jmax_ge_mtu", "trailers_unequal_s", "mtu_headroom", "h_default_v20", "h_small_range", "h_lt5", "preset_port", "keepalive_over_nat"}
	for _, base := range []func() Settings{bestScored, v20Scored, fixture31, fixture20} {
		for seed := uint64(0); seed < 40; seed++ {
			s := base()
			// a few hand-made settings per seed: pick a mutation from the table
			switch seed % 8 {
			case 1:
				s.Obfuscation.Jc = 30
			case 2:
				s.MTU, s.Obfuscation.Jmax = 1420, 20+s.Obfuscation.Jmin
			case 3:
				s.Obfuscation.PersistentKeepalive = "40"
			case 4:
				s.Obfuscation.Preset = "ntp"
			case 5:
				s.Obfuscation.H1 = "2"
			case 6:
				s.Obfuscation.H2 = "100-200"
			}
			if len(validate(s)) > 0 {
				continue
			}
			sc := score(s)
			var w, c []string
			for _, x := range warnings(s) {
				if slices.Contains(shared, x.Code) {
					w = append(w, x.Code)
				}
			}
			for _, x := range sc.Items {
				if slices.Contains(shared, x.Code) {
					c = append(c, x.Code)
				}
			}
			slices.Sort(w)
			slices.Sort(c)
			// the score does not rate headers under a key, the warning does not either; the score skips the H items
			// of a plain WireGuard profile where the tier already says it
			if tierOf(s) == TierWireGuard {
				continue
			}
			if !slices.Equal(w, c) {
				t.Errorf("seed %d: warnings %v, score items %v for %+v", seed, w, c, s.Obfuscation)
			}
		}
	}
}
