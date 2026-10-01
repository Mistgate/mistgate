package awgcfg

import (
	"fmt"
	"strings"
)

// Issue is one finding of Validate. Pointer is a JSON pointer into the profile ("/obfuscation/s1").
type Issue struct {
	Pointer string
	Code    string // stable snake_case id
	Message string // short English fact
}

func (i Issue) Error() string { return i.Pointer + ": " + i.Message }

// Result is what Validate found. Errors make the profile unusable; Warnings do not.
type Result struct {
	Errors   []Issue
	Warnings []Issue
}

// OK reports whether there is no error.
func (r Result) OK() bool { return len(r.Errors) == 0 }

// Err joins the errors into one error (nil when OK).
func (r Result) Err() error {
	if r.OK() {
		return nil
	}
	parts := make([]string, len(r.Errors))
	for i, e := range r.Errors {
		parts[i] = e.Error()
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// Options are the facts outside Settings that some rules need. Zero values skip the rule.
type Options struct {
	MTU int // tunnel MTU: enables the jmax >= mtu and mtu > 1280 warnings
}

// Limits. They are stricter than both implementations on purpose:
// the module answers only EINVAL, amneziawg-go accepts what the docs forbid, and old clients reject a whole config.
const (
	maxJc          = 128
	warnJc         = 12
	maxJunk        = 1280
	maxS12         = 150
	maxS34         = 64
	minSWithHPK    = 12
	maxU16         = 65535
	hpkLen         = 32
	maxIChars      = 3500 // total of i1..i5; `awg show` hangs above ~3500
	maxIPacketSize = 1200
)

// Validate applies the rules that concern the settings themselves (port, subnets and the
// backend's capabilities are checked by whoever owns those facts). The same code runs in the panel plugin's
// Validate and again in the agent's Apply.
func Validate(s Settings, opt Options) Result {
	var r Result
	bad := func(ptr, code, msg string, a ...any) {
		r.Errors = append(r.Errors, Issue{ptr, code, fmt.Sprintf(msg, a...)})
	}
	warn := func(ptr, code, msg string, a ...any) {
		r.Warnings = append(r.Warnings, Issue{ptr, code, fmt.Sprintf(msg, a...)})
	}
	o := s.Obfuscation
	const obf = "/obfuscation/"

	// 2. version decides which keys exist at all.
	is31 := false
	switch s.Version {
	case Version31:
		is31 = true
	case Version20:
	default:
		bad("/version", "version", "must be %q or %q", Version20, Version31)
	}
	if s.Version == Version20 || s.Version == "" {
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"header_protection_key", o.HeaderProtectionKey != ""},
			{"content_padding_addition", !o.ContentPaddingAddition.IsZero()},
			{"rekey_after_time", !o.RekeyAfterTime.IsZero()},
			{"rekey_timeout", !o.RekeyTimeout.IsZero()},
			{"reject_after_time", !o.RejectAfterTime.IsZero()},
			{"keepalive_timeout", !o.KeepaliveTimeout.IsZero()},
			{"max_handshake_attempts", !o.MaxHandshakeAttempts.IsZero()},
			{"random_trailers", o.RandomTrailers},
			{"disable_cookies", o.DisableCookies},
		} {
			if f.set {
				bad(obf+f.name, "not_in_version", "%s does not exist in AWG 2.0", f.name)
			}
		}
	}

	// 3. junk.
	if o.Jc < 0 || o.Jc > maxJc {
		bad(obf+"jc", "range", "must be 0..%d", maxJc)
	} else if o.Jc > warnJc {
		warn(obf+"jc", "jc_high", "more than %d junk packets per handshake is a visible pattern and costs bandwidth", warnJc)
	}
	if o.Jmin < 0 || o.Jmin > maxJunk {
		bad(obf+"jmin", "range", "must be 0..%d", maxJunk)
	}
	if o.Jmax < 0 || o.Jmax > maxJunk {
		bad(obf+"jmax", "range", "must be 0..%d", maxJunk)
	}
	if o.Jmin > o.Jmax {
		bad(obf+"jmin", "jmin_gt_jmax", "jmin %d is greater than jmax %d (both implementations accept it silently)", o.Jmin, o.Jmax)
	}
	if opt.MTU > 0 && o.Jmax >= opt.MTU {
		warn(obf+"jmax", "jmax_ge_mtu", "jmax %d >= mtu %d: junk packets get fragmented", o.Jmax, opt.MTU)
	}
	if opt.MTU > 1280 {
		warn("/mtu", "mtu_high", "mtu %d > 1280: AmneziaVPN desktop overwrites the MTU of vpn:// keys anyway", opt.MTU)
	}

	// 4. S1..S4.
	hpk, hpkOK := o.HPK()
	if !hpkOK {
		bad(obf+"header_protection_key", "hpk", "must be base64 of exactly %d bytes", hpkLen)
	}
	sv := o.S()
	limits := [4]int{maxS12, maxS12, maxS34, maxS34}
	sOK := true
	for i, v := range sv {
		ptr := fmt.Sprintf("%ss%d", obf, i+1)
		switch {
		case v < 0 || v > maxU16:
			bad(ptr, "range", "must be 0..%d", maxU16)
			sOK = false
		case v > limits[i]:
			bad(ptr, "s_limit", "must be at most %d", limits[i])
			sOK = false
		case hpk != nil && v < minSWithHPK:
			bad(ptr, "s_hpk_min", "must be at least %d when header_protection_key is set", minSWithHPK)
			sOK = false
		}
	}
	if sOK {
		sizes := [4]int{148 + sv[0], 92 + sv[1], 64 + sv[2], 32 + sv[3]}
		for i := 0; i < 4; i++ {
			for j := i + 1; j < 4; j++ {
				if sizes[i] == sizes[j] {
					bad(fmt.Sprintf("%ss%d", obf, j+1), "s_size_clash", "packet size %d of s%d equals that of s%d: the receiver cannot tell the packet types apart", sizes[j], j+1, i+1)
				}
			}
		}
	}
	if o.RandomTrailers && (sv[0] != sv[1] || sv[1] != sv[2] || sv[2] != sv[3]) {
		warn(obf+"random_trailers", "rt_unequal_s", "with random_trailers the docs advise s1 = s2 = s3 = s4")
	}

	// 5. H1..H4: all four are always sent; 1,2,3,4 means "off".
	hv := o.H()
	hOK := true
	for i, h := range hv {
		ptr := fmt.Sprintf("%sh%d", obf, i+1)
		switch {
		case h.bad:
			bad(ptr, "h_format", "must be a number or lo-hi within uint32")
			hOK = false
		case h.IsZero():
			bad(ptr, "h_required", "required: send all four, 1,2,3,4 means off (zero ranges overlap each other)")
			hOK = false
		case h.Lo > h.Hi:
			bad(ptr, "h_order", "lo %d is greater than hi %d", h.Lo, h.Hi)
			hOK = false
		}
	}
	if hOK {
		for i := 0; i < 4; i++ {
			for j := i + 1; j < 4; j++ {
				if hv[i].Lo <= hv[j].Hi && hv[j].Lo <= hv[i].Hi {
					bad(fmt.Sprintf("%sh%d", obf, j+1), "h_overlap", "h%d overlaps h%d (headers must not overlap)", j+1, i+1)
				}
			}
		}
	}

	// 6. I1..I5 (client side, checked wherever they appear).
	total := 0
	for i, spec := range o.I() {
		if spec == "" {
			continue
		}
		ptr := fmt.Sprintf("%si%d", obf, i+1)
		total += len(spec)
		size, err := CheckCPS(spec)
		if err != nil {
			bad(ptr, "cps", "%v", err)
		} else if size > maxIPacketSize {
			bad(ptr, "cps_size", "packet is %d bytes, at most %d (larger than the MTU gets fragmented)", size, maxIPacketSize)
		}
	}
	if total > maxIChars {
		bad(obf+"i1", "cps_total", "i1..i5 total %d characters, at most %d (`awg show` hangs above that)", total, maxIChars)
	}

	// 7. timers and the rest of the ranges.
	names := [6]string{"content_padding_addition", "rekey_after_time", "rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts"}
	timers := o.Timers()
	for i, t := range timers {
		checkU16Range(obf+names[i], t, bad)
	}
	checkU16Range(obf+"persistent_keepalive", o.PersistentKeepalive, bad)
	if ra, rj := o.RekeyAfterTime, o.RejectAfterTime; !ra.IsZero() && !rj.IsZero() && !ra.bad && !rj.bad && ra.Hi >= rj.Lo {
		bad(obf+"rekey_after_time", "rekey_after_ge_reject", "rekey_after_time upper bound %d must be below reject_after_time lower bound %d", ra.Hi, rj.Lo)
	}

	// 8. warnings.
	if is31 && o.DisableCookies {
		warn(obf+"disable_cookies", "disable_cookies", "no protection from handshake floods with spoofed addresses")
	}
	return r
}

func checkU16Range(ptr string, v Range, bad func(ptr, code, msg string, a ...any)) {
	switch {
	case v.bad:
		bad(ptr, "range_format", "must be a number or lo-hi")
	case v.Lo > v.Hi:
		bad(ptr, "range_order", "lo %d is greater than hi %d", v.Lo, v.Hi)
	case v.Hi > maxU16:
		bad(ptr, "range", "must be at most %d", maxU16)
	}
}
