package awgcfg

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func hpk() string { return base64.StdEncoding.EncodeToString(make([]byte, 32)) }

// full31 is the "3.1 full" row of the compatibility matrix.
func full31() Settings {
	return Settings{Version: Version31, Obfuscation: Obfuscation{
		Jc: 6, Jmin: 10, Jmax: 50, S1: 24, S2: 24, S3: 24, S4: 24,
		H1: Range{Lo: 1, Hi: 1}, H2: Range{Lo: 2, Hi: 2}, H3: Range{Lo: 3, Hi: 3}, H4: Range{Lo: 4, Hi: 4},
		I1:                  "<b 0xc70000000108><r 8><t><r 40>",
		HeaderProtectionKey: hpk(), RandomTrailers: true,
		ContentPaddingAddition: Range{Lo: 2, Hi: 10},
		RekeyAfterTime:         Range{Lo: 100, Hi: 120}, RekeyTimeout: Range{Lo: 3, Hi: 7}, RejectAfterTime: Range{Lo: 150, Hi: 180},
		KeepaliveTimeout: Range{Lo: 5, Hi: 15}, MaxHandshakeAttempts: Range{Lo: 15, Hi: 20},
		PersistentKeepalive: Range{Lo: 25, Hi: 35},
	}}
}

func off(o *Obfuscation) {
	o.H1, o.H2, o.H3, o.H4 = Range{Lo: 1, Hi: 1}, Range{Lo: 2, Hi: 2}, Range{Lo: 3, Hi: 3}, Range{Lo: 4, Hi: 4}
}

func has(issues []Issue, ptr, code string) bool {
	for _, i := range issues {
		if i.Pointer == ptr && (code == "" || i.Code == code) {
			return true
		}
	}
	return false
}

func TestValidPositive(t *testing.T) {
	v20 := Settings{Version: Version20, Obfuscation: Obfuscation{
		Jc: 5, Jmin: 10, Jmax: 50, S1: 30, S2: 40, S3: 20, S4: 10,
		H1: Range{Lo: 100, Hi: 200}, H2: Range{Lo: 300, Hi: 400}, H3: Range{Lo: 500, Hi: 600}, H4: Range{Lo: 700, Hi: 800},
	}}
	v30 := full31() // 3.0 style: no random trailers, no HPK, distinct S
	v30.Obfuscation.RandomTrailers, v30.Obfuscation.HeaderProtectionKey = false, ""
	v30.Obfuscation.S1, v30.Obfuscation.S2, v30.Obfuscation.S3, v30.Obfuscation.S4 = 31, 47, 23, 17
	v30.Obfuscation.H1, v30.Obfuscation.H2, v30.Obfuscation.H3, v30.Obfuscation.H4 = Range{Lo: 10, Hi: 20}, Range{Lo: 30, Hi: 40}, Range{Lo: 50, Hi: 60}, Range{Lo: 70, Hi: 80}
	min12 := full31()
	min12.Obfuscation.S1, min12.Obfuscation.S2, min12.Obfuscation.S3, min12.Obfuscation.S4 = 12, 12, 12, 12
	amnezia := full31() // Amnezia 5.0.x defaults: S=12, CPA 10-100, DNS-style I1
	amnezia.Obfuscation.S1, amnezia.Obfuscation.S2, amnezia.Obfuscation.S3, amnezia.Obfuscation.S4 = 12, 12, 12, 12
	amnezia.Obfuscation.ContentPaddingAddition = Range{Lo: 10, Hi: 100}
	amnezia.Obfuscation.I1 = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"
	plain := Settings{Version: Version31}
	off(&plain.Obfuscation)
	plain20 := Settings{Version: Version20}
	off(&plain20.Obfuscation)
	extremes := full31() // the boundaries of every limit
	extremes.Obfuscation.Jc, extremes.Obfuscation.Jmin, extremes.Obfuscation.Jmax = 12, 0, 1280
	extremes.Obfuscation.S1, extremes.Obfuscation.S2, extremes.Obfuscation.S3, extremes.Obfuscation.S4 = 150, 150, 64, 64
	extremes.Obfuscation.RandomTrailers = false

	for name, s := range map[string]Settings{"3.1 full": full31(), "3.0 style": v30, "2.0": v20, "S=12 + HPK": min12,
		"amnezia 5.0 defaults": amnezia, "no obfuscation 3.1": plain, "no obfuscation 2.0": plain20, "limits": extremes} {
		if r := Validate(s, Options{MTU: 1280}); !r.OK() {
			t.Errorf("%s: unexpected errors: %v", name, r.Errors)
		}
	}
}

func TestValidateRules(t *testing.T) {
	type mut func(*Settings)
	cases := []struct {
		name string
		m    mut
		ptr  string
		code string
	}{
		{"version", func(s *Settings) { s.Version = "3.0" }, "/version", "version"},
		{"version empty", func(s *Settings) { s.Version = "" }, "/version", "version"},
		{"2.0 has no hpk", func(s *Settings) {
			s.Version = Version20
			s.Obfuscation.RandomTrailers = false
			s.Obfuscation.ContentPaddingAddition = Range{}
			clearTimers(&s.Obfuscation)
		}, "/obfuscation/header_protection_key", "not_in_version"},
		{"2.0 has no cpa", func(s *Settings) { s.Version = Version20; s.Obfuscation.HeaderProtectionKey = "" }, "/obfuscation/content_padding_addition", "not_in_version"},
		{"2.0 has no random trailers", func(s *Settings) { s.Version = Version20 }, "/obfuscation/random_trailers", "not_in_version"},
		{"2.0 has no rekey", func(s *Settings) { s.Version = Version20 }, "/obfuscation/rekey_after_time", "not_in_version"},
		{"2.0 has no disable cookies", func(s *Settings) { s.Version = Version20; s.Obfuscation.DisableCookies = true }, "/obfuscation/disable_cookies", "not_in_version"},
		{"jc high", func(s *Settings) { s.Obfuscation.Jc = 129 }, "/obfuscation/jc", "range"},
		{"jc negative", func(s *Settings) { s.Obfuscation.Jc = -1 }, "/obfuscation/jc", "range"},
		{"jmin > jmax", func(s *Settings) { s.Obfuscation.Jmin, s.Obfuscation.Jmax = 60, 50 }, "/obfuscation/jmin", "jmin_gt_jmax"},
		{"jmax high", func(s *Settings) { s.Obfuscation.Jmax = 1281 }, "/obfuscation/jmax", "range"},
		{"s1 above ui limit", func(s *Settings) { s.Obfuscation.S1 = 151 }, "/obfuscation/s1", "s_limit"},
		{"s3 above ui limit", func(s *Settings) { s.Obfuscation.S3 = 65 }, "/obfuscation/s3", "s_limit"},
		{"s4 above ui limit", func(s *Settings) { s.Obfuscation.S4 = 65 }, "/obfuscation/s4", "s_limit"},
		{"s not uint16", func(s *Settings) { s.Obfuscation.S2 = 70000 }, "/obfuscation/s2", "range"},
		{"s negative", func(s *Settings) { s.Obfuscation.S2 = -5 }, "/obfuscation/s2", "range"},
		{"hpk needs s >= 12", func(s *Settings) { s.Obfuscation.S2 = 11 }, "/obfuscation/s2", "s_hpk_min"},
		{"packet sizes clash", func(s *Settings) { s.Obfuscation.RandomTrailers = false; s.Obfuscation.S1, s.Obfuscation.S2 = 20, 76 }, "/obfuscation/s2", "s_size_clash"},
		{"s3 s4 clash", func(s *Settings) { s.Obfuscation.RandomTrailers = false; s.Obfuscation.S3, s.Obfuscation.S4 = 20, 52 }, "/obfuscation/s4", "s_size_clash"},
		{"h zero", func(s *Settings) { s.Obfuscation.H2 = Range{} }, "/obfuscation/h2", "h_required"},
		{"h inverted", func(s *Settings) { s.Obfuscation.H3 = Range{Lo: 9, Hi: 5} }, "/obfuscation/h3", "h_order"},
		{"h overlap", func(s *Settings) {
			s.Obfuscation.H1, s.Obfuscation.H2 = Range{Lo: 300, Hi: 400}, Range{Lo: 350, Hi: 450}
		}, "/obfuscation/h2", "h_overlap"},
		{"h equal", func(s *Settings) { s.Obfuscation.H4 = s.Obfuscation.H1 }, "/obfuscation/h4", "h_overlap"},
		{"hpk length", func(s *Settings) {
			s.Obfuscation.HeaderProtectionKey = base64.StdEncoding.EncodeToString(make([]byte, 16))
		}, "/obfuscation/header_protection_key", "hpk"},
		{"hpk not base64", func(s *Settings) { s.Obfuscation.HeaderProtectionKey = "!!!" }, "/obfuscation/header_protection_key", "hpk"},
		{"cpa inverted", func(s *Settings) { s.Obfuscation.ContentPaddingAddition = Range{Lo: 10, Hi: 2} }, "/obfuscation/content_padding_addition", "range_order"},
		{"timer too big", func(s *Settings) { s.Obfuscation.KeepaliveTimeout = Range{Lo: 5, Hi: 70000} }, "/obfuscation/keepalive_timeout", "range"},
		{"rekey after reject", func(s *Settings) { s.Obfuscation.RekeyAfterTime = Range{Lo: 100, Hi: 160} }, "/obfuscation/rekey_after_time", "rekey_after_ge_reject"},
		{"keepalive inverted", func(s *Settings) { s.Obfuscation.PersistentKeepalive = Range{Lo: 35, Hi: 25} }, "/obfuscation/persistent_keepalive", "range_order"},
		{"cps counter tag", func(s *Settings) { s.Obfuscation.I1 = "<b 0x41><c>" }, "/obfuscation/i1", "cps"},
		{"cps empty hex", func(s *Settings) { s.Obfuscation.I2 = "<b 0x>" }, "/obfuscation/i2", "cps"},
		{"cps odd hex", func(s *Settings) { s.Obfuscation.I3 = "<b 0xabc>" }, "/obfuscation/i3", "cps"},
		{"cps r zero", func(s *Settings) { s.Obfuscation.I4 = "<r 0>" }, "/obfuscation/i4", "cps"},
		{"cps r too long", func(s *Settings) { s.Obfuscation.I5 = "<r 1001>" }, "/obfuscation/i5", "cps"},
		{"cps two timestamps", func(s *Settings) { s.Obfuscation.I1 = "<t><t>" }, "/obfuscation/i1", "cps"},
		{"cps unknown tag", func(s *Settings) { s.Obfuscation.I1 = "<x 4>" }, "/obfuscation/i1", "cps"},
		{"cps loose text", func(s *Settings) { s.Obfuscation.I1 = "abc" }, "/obfuscation/i1", "cps"},
		{"cps space between tags", func(s *Settings) { s.Obfuscation.I1 = "<b 0x41> <r 2>" }, "/obfuscation/i1", "cps"},
		{"cps hash", func(s *Settings) { s.Obfuscation.I1 = "<b 0x41>#" }, "/obfuscation/i1", "cps"},
		{"cps one packet too big", func(s *Settings) { s.Obfuscation.I1 = strings.Repeat("<r 1000>", 2) }, "/obfuscation/i1", "cps_size"},
		{"cps total too long", func(s *Settings) {
			long := strings.Repeat("<b 0x41>", 100) // 800 characters, 100 bytes: each packet is fine, the five are not
			s.Obfuscation.I1, s.Obfuscation.I2, s.Obfuscation.I3, s.Obfuscation.I4, s.Obfuscation.I5 = long, long, long, long, long
		}, "/obfuscation/i1", "cps_total"},
	}
	for _, c := range cases {
		s := full31()
		c.m(&s)
		r := Validate(s, Options{MTU: 1280})
		if !has(r.Errors, c.ptr, c.code) {
			t.Errorf("%s: want error %s at %s, got %v", c.name, c.code, c.ptr, r.Errors)
		}
	}
}

func clearTimers(o *Obfuscation) {
	o.RekeyAfterTime, o.RekeyTimeout, o.RejectAfterTime, o.KeepaliveTimeout, o.MaxHandshakeAttempts = Range{}, Range{}, Range{}, Range{}, Range{}
}

func TestWarnings(t *testing.T) {
	s := full31()
	s.Obfuscation.DisableCookies = true
	s.Obfuscation.S2 = 30 // unequal S with random trailers
	s.Obfuscation.Jc = 20
	s.Obfuscation.Jmax = 1200
	for mtu, wants := range map[int][]string{
		1376: {"disable_cookies", "rt_unequal_s", "jc_high", "mtu_high"},
		1200: {"jmax_ge_mtu"},
	} {
		r := Validate(s, Options{MTU: mtu})
		if !r.OK() {
			t.Fatalf("mtu %d: errors: %v", mtu, r.Errors)
		}
		for _, w := range wants {
			found := false
			for _, i := range r.Warnings {
				found = found || i.Code == w
			}
			if !found {
				t.Errorf("mtu %d: missing warning %s in %v", mtu, w, r.Warnings)
			}
		}
	}
}

func TestRangeJSON(t *testing.T) {
	var o Obfuscation
	if err := json.Unmarshal([]byte(`{"h1":"1","h2":2,"h3":"100-200","h4":"x","content_padding_addition":""}`), &o); err != nil {
		t.Fatal(err)
	}
	if o.H1 != (Range{Lo: 1, Hi: 1}) || o.H2 != (Range{Lo: 2, Hi: 2}) || o.H3 != (Range{Lo: 100, Hi: 200}) || !o.ContentPaddingAddition.IsZero() {
		t.Fatalf("parsed %+v", o)
	}
	s := Settings{Version: Version31, Obfuscation: o}
	s.Obfuscation.S1, s.Obfuscation.S2, s.Obfuscation.S3, s.Obfuscation.S4 = 12, 13, 14, 15
	r := Validate(s, Options{})
	if !has(r.Errors, "/obfuscation/h4", "h_format") {
		t.Errorf("a malformed range must be reported at its field, got %v", r.Errors)
	}
	for _, bad := range []string{"4294967296", "1-", "-5", "a-b", "1-2-3"} {
		if _, err := ParseRange(bad); err == nil {
			t.Errorf("ParseRange(%q) should fail", bad)
		}
	}
}

func TestNodeJSONDropsClientFieldsAndIsStable(t *testing.T) {
	s := full31()
	s.Obfuscation.Preset = "quic"
	s.PrivateKey = hpk()
	a, err := s.NodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"i1", "preset", "persistent_keepalive"} {
		if strings.Contains(string(a), `"`+leak+`"`) {
			t.Errorf("node JSON carries client-only field %s: %s", leak, a)
		}
	}
	b, _ := s.NodeJSON()
	if string(a) != string(b) {
		t.Error("NodeJSON is not deterministic")
	}
	back, err := ParseSettings(a)
	if err != nil || back.Obfuscation.H3 != s.Obfuscation.H3 || back.Version != Version31 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	if strings.Index(string(a), `"jc"`) > strings.Index(string(a), `"s1"`) || strings.Index(string(a), `"s1"`) > strings.Index(string(a), `"h1"`) {
		t.Errorf("field order changed (the bytes are hashed): %s", a)
	}
}

func FuzzCheckCPS(f *testing.F) {
	for _, s := range []string{"", "<b 0x41>", "<r 8><t><r 40>", "<c>", "<<>>", "<b 0x", "<r -1>", "<t x>", "<rc 1000>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		size, err := CheckCPS(s) // must never panic; a pass implies a sane size
		if err == nil && (size < 0 || (s != "" && size == 0)) {
			t.Fatalf("CheckCPS(%q) = %d, nil", s, size)
		}
	})
}
