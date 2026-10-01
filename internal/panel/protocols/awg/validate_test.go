package awg

import (
	"maps"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
)

func errsOf(s Settings) map[string]string {
	out := map[string]string{}
	for _, e := range validate(s) {
		out[e.Pointer] = e.Code
	}
	return out
}

func TestFixturesAreValid(t *testing.T) {
	for name, s := range map[string]Settings{"3.1": fixture31(), "2.0": fixture20()} {
		if e := validate(s); len(e) > 0 {
			t.Errorf("%s fixture: %+v", name, e)
		}
	}
}

// The positive cases of the compatibility matrix: everything that worked there must pass.
func TestValidatorAcceptsTheMatrix(t *testing.T) {
	cases := map[string]func(*Settings){
		"3.1 full": func(s *Settings) {},
		"3.0 style: varying S, header ranges, padding, no key, no trailers": func(s *Settings) {
			o := &s.Obfuscation
			o.HeaderProtectionKey, o.RandomTrailers = "", false
			o.S1, o.S2, o.S3, o.S4 = 40, 60, 25, 18
			o.H1, o.H2, o.H3, o.H4 = "100-200", "300-400", "500-600", "700-800"
		},
		"2.0 style": func(s *Settings) { *s = fixture20() },
		"S=12 with a key": func(s *Settings) {
			s.Obfuscation.S1, s.Obfuscation.S2, s.Obfuscation.S3, s.Obfuscation.S4 = 12, 12, 12, 12
		},
		"Amnezia 5.0.x defaults as a whole": func(s *Settings) {
			o := &s.Obfuscation
			o.Jc, o.Jmin, o.Jmax = 5, 10, 50
			o.S1, o.S2, o.S3, o.S4 = 12, 12, 12, 12
			o.ContentPaddingAddition, o.RandomTrailers, o.DisableCookies = "10-100", true, true
		},
		"no obfuscation at all": func(s *Settings) {
			o := &s.Obfuscation
			o.Jc, o.Jmin, o.Jmax, o.S1, o.S2, o.S3, o.S4 = 0, 0, 0, 0, 0, 0, 0
			o.HeaderProtectionKey, o.RandomTrailers, o.I1 = "", false, ""
		},
		"S at the UI limits": func(s *Settings) {
			s.Obfuscation.S1, s.Obfuscation.S2, s.Obfuscation.S3, s.Obfuscation.S4 = 150, 149, 64, 64
		},
		"a 2.0 profile keeps a stored key that is never sent": func(s *Settings) {
			*s = fixture20()
			s.Obfuscation.HeaderProtectionKey = key32(1)
		},
		"several I packets with every tag": func(s *Settings) {
			s.Obfuscation.I2 = "<b 0xc0ffee><rc 12><rd 4><t><r 100>"
			s.Obfuscation.I5 = "<r 1>"
		},
		"IPv4 only":                 func(s *Settings) { s.Subnet6 = "" },
		"warp exit":                 func(s *Settings) { s.Egress = "warp" },
		"a single keepalive number": func(s *Settings) { s.Obfuscation.PersistentKeepalive = "25" },
		"no keepalive":              func(s *Settings) { s.Obfuscation.PersistentKeepalive = "" },
	}
	for name, mut := range cases {
		s := fixture31()
		mut(&s)
		if e := validate(s); len(e) > 0 {
			t.Errorf("%s: %+v", name, e)
		}
	}
}

// Every validator rule with a failing case, by pointer and code.
func TestValidatorRejects(t *testing.T) {
	cases := []struct {
		name, ptr, code string
		mut             func(*Settings)
		base            func() Settings
	}{
		{"port 0", "/port", "out_of_range", func(s *Settings) { s.Port = 0 }, nil},
		{"port 65536", "/port", "out_of_range", func(s *Settings) { s.Port = 65536 }, nil},
		{"version", "/version", "invalid_enum", func(s *Settings) { s.Version = "3.0" }, nil},
		{"mtu low", "/mtu", "out_of_range", func(s *Settings) { s.MTU = 1199 }, nil},
		{"mtu high", "/mtu", "out_of_range", func(s *Settings) { s.MTU = 1421 }, nil},
		{"egress", "/egress", "invalid_enum", func(s *Settings) { s.Egress = "tor" }, nil},
		{"preset", "/obfuscation/preset", "invalid_enum", func(s *Settings) { s.Obfuscation.Preset = "tls" }, nil},

		{"2.0 padding", "/obfuscation/content_padding_addition", "not_in_version", func(s *Settings) { s.Obfuscation.ContentPaddingAddition = "2-10" }, fixture20},
		{"2.0 rekey", "/obfuscation/rekey_after_time", "not_in_version", func(s *Settings) { s.Obfuscation.RekeyAfterTime = "100-120" }, fixture20},
		{"2.0 timeout", "/obfuscation/rekey_timeout", "not_in_version", func(s *Settings) { s.Obfuscation.RekeyTimeout = "3-7" }, fixture20},
		{"2.0 reject", "/obfuscation/reject_after_time", "not_in_version", func(s *Settings) { s.Obfuscation.RejectAfterTime = "150-180" }, fixture20},
		{"2.0 keepalive timeout", "/obfuscation/keepalive_timeout", "not_in_version", func(s *Settings) { s.Obfuscation.KeepaliveTimeout = "5-15" }, fixture20},
		{"2.0 attempts", "/obfuscation/max_handshake_attempts", "not_in_version", func(s *Settings) { s.Obfuscation.MaxHandshakeAttempts = "15-20" }, fixture20},
		{"2.0 trailers", "/obfuscation/random_trailers", "not_in_version", func(s *Settings) { s.Obfuscation.RandomTrailers = true }, fixture20},
		{"2.0 cookies", "/obfuscation/disable_cookies", "not_in_version", func(s *Settings) { s.Obfuscation.DisableCookies = true }, fixture20},
		{"2.0 keepalive range", "/obfuscation/persistent_keepalive", "not_in_version", func(s *Settings) { s.Obfuscation.PersistentKeepalive = "25-35" }, fixture20},

		{"jc", "/obfuscation/jc", "out_of_range", func(s *Settings) { s.Obfuscation.Jc = 129 }, nil},
		{"jc negative", "/obfuscation/jc", "out_of_range", func(s *Settings) { s.Obfuscation.Jc = -1 }, nil},
		{"jmin above jmax", "/obfuscation/jmax", "invalid", func(s *Settings) { s.Obfuscation.Jmin, s.Obfuscation.Jmax = 60, 50 }, nil},
		{"jmax", "/obfuscation/jmax", "out_of_range", func(s *Settings) { s.Obfuscation.Jmax = 1281 }, nil},
		{"jmin", "/obfuscation/jmin", "out_of_range", func(s *Settings) { s.Obfuscation.Jmin = 1281 }, nil},

		{"s1 above UI limit", "/obfuscation/s1", "out_of_range", func(s *Settings) { s.Obfuscation.S1 = 151 }, nil},
		{"s3 above UI limit", "/obfuscation/s3", "out_of_range", func(s *Settings) { s.Obfuscation.S3 = 65 }, nil},
		{"s4 negative", "/obfuscation/s4", "out_of_range", func(s *Settings) { s.Obfuscation.S4 = -1 }, nil},
		{"s below 12 with a key", "/obfuscation/s2", "out_of_range", func(s *Settings) { s.Obfuscation.S2 = 11 }, nil},
		{"equal packet sizes", "/obfuscation/s2", "duplicate_size", func(s *Settings) { s.Obfuscation.S1, s.Obfuscation.S2 = 12, 68 }, nil}, // 148+12 == 92+68

		{"h overlap", "/obfuscation/h2", "overlap", func(s *Settings) { s.Obfuscation.H1, s.Obfuscation.H2 = "5-10", "8-20" }, nil},
		{"h equal", "/obfuscation/h4", "overlap", func(s *Settings) { s.Obfuscation.H3, s.Obfuscation.H4 = "7", "7" }, nil},
		{"h zero ranges", "/obfuscation/h2", "overlap", func(s *Settings) { s.Obfuscation.H1, s.Obfuscation.H2 = "0", "0" }, nil},
		{"h reversed", "/obfuscation/h1", "invalid", func(s *Settings) { s.Obfuscation.H1 = "20-10" }, nil},
		{"h empty", "/obfuscation/h3", "invalid", func(s *Settings) { s.Obfuscation.H3 = "" }, nil},
		{"h not a number", "/obfuscation/h1", "invalid", func(s *Settings) { s.Obfuscation.H1 = "a-b" }, nil},
		{"h above uint32", "/obfuscation/h4", "invalid", func(s *Settings) { s.Obfuscation.H4 = "4294967296" }, nil},
		{"h negative", "/obfuscation/h1", "invalid", func(s *Settings) { s.Obfuscation.H1 = "-5" }, nil},

		{"i counter tag", "/obfuscation/i1", "invalid", func(s *Settings) { s.Obfuscation.I1 = "<c><r 10>" }, nil},
		{"i empty hex", "/obfuscation/i2", "invalid", func(s *Settings) { s.Obfuscation.I2 = "<b 0x>" }, nil},
		{"i odd hex", "/obfuscation/i3", "invalid", func(s *Settings) { s.Obfuscation.I3 = "<b 0xabc>" }, nil},
		{"i r too big", "/obfuscation/i4", "invalid", func(s *Settings) { s.Obfuscation.I4 = "<r 1001>" }, nil},
		{"i two times", "/obfuscation/i5", "invalid", func(s *Settings) { s.Obfuscation.I5 = "<t><t>" }, nil},
		{"i comment char", "/obfuscation/i1", "invalid", func(s *Settings) { s.Obfuscation.I1 = "<r 4>#" }, nil},
		{"i packet above 1200", "/obfuscation/i1", "invalid", func(s *Settings) { s.Obfuscation.I1 = "<r 1000><r 201>" }, nil},
		{"i total above 3500", "/obfuscation/i1", "too_long", func(s *Settings) {
			chunk := "<b 0x" + strings.Repeat("ab", 500) + ">" // 1006 chars, 500 bytes
			s.Obfuscation.I1, s.Obfuscation.I2, s.Obfuscation.I3, s.Obfuscation.I4 = chunk, chunk, chunk, chunk
		}, nil},

		{"key length", "/obfuscation/header_protection_key", "invalid", func(s *Settings) { s.Obfuscation.HeaderProtectionKey = "AAAA" }, nil},
		{"key not base64", "/obfuscation/header_protection_key", "invalid", func(s *Settings) { s.Obfuscation.HeaderProtectionKey = strings.Repeat("!", 44) }, nil},
		{"padding reversed", "/obfuscation/content_padding_addition", "invalid", func(s *Settings) { s.Obfuscation.ContentPaddingAddition = "10-2" }, nil},
		{"timer above uint16", "/obfuscation/rekey_timeout", "invalid", func(s *Settings) { s.Obfuscation.RekeyTimeout = "3-65536" }, nil},
		{"rekey not below reject", "/obfuscation/rekey_after_time", "invalid", func(s *Settings) { s.Obfuscation.RekeyAfterTime = "100-160" }, nil},
		{"attempts garbage", "/obfuscation/max_handshake_attempts", "invalid", func(s *Settings) { s.Obfuscation.MaxHandshakeAttempts = "many" }, nil},
		{"keepalive garbage", "/obfuscation/persistent_keepalive", "invalid", func(s *Settings) { s.Obfuscation.PersistentKeepalive = "25-" }, nil},

		{"subnet4 public", "/subnet4", "invalid", func(s *Settings) { s.Subnet4 = "8.8.8.0/24" }, nil},
		{"subnet4 cgnat", "/subnet4", "invalid", func(s *Settings) { s.Subnet4 = "100.64.0.0/22" }, nil},
		{"subnet4 too small", "/subnet4", "invalid", func(s *Settings) { s.Subnet4 = "10.66.4.0/25" }, nil},
		{"subnet4 too big", "/subnet4", "invalid", func(s *Settings) { s.Subnet4 = "10.0.0.0/8" }, nil},
		{"subnet4 host bits", "/subnet4", "invalid", func(s *Settings) { s.Subnet4 = "10.66.4.1/22" }, nil},
		{"subnet4 v6", "/subnet4", "invalid", func(s *Settings) { s.Subnet4 = "fd66::/64" }, nil},
		{"subnet4 empty", "/subnet4", "invalid", func(s *Settings) { s.Subnet4 = "" }, nil},
		{"subnet6 not ula", "/subnet6", "invalid", func(s *Settings) { s.Subnet6 = "2001:db8::/64" }, nil},
		{"subnet6 length", "/subnet6", "invalid", func(s *Settings) { s.Subnet6 = "fd66:66:0:1::/48" }, nil},
		{"subnet6 host bits", "/subnet6", "invalid", func(s *Settings) { s.Subnet6 = "fd66:66:0:1::1/64" }, nil},
		{"subnet6 v4", "/subnet6", "invalid", func(s *Settings) { s.Subnet6 = "10.66.4.0/22" }, nil},
	}
	for _, c := range cases {
		s := fixture31()
		if c.base != nil {
			s = c.base()
		}
		c.mut(&s)
		got := errsOf(s)
		if got[c.ptr] != c.code {
			t.Errorf("%s: want %s %s, got %v", c.name, c.ptr, c.code, got)
		}
	}
}

func TestParseRejectsUnknownAndWrongTypes(t *testing.T) {
	if e := New().Validate([]byte(`{"version":"3.1","port":1,"bogus":1}`)); len(e) != 1 || e[0].Code != "invalid_json" {
		t.Errorf("unknown key: %+v", e)
	}
	if e := New().Validate([]byte(`{"obfuscation":{"s1":"12"}}`)); len(e) != 1 || e[0].Pointer != "/obfuscation/s1" || e[0].Code != "invalid_type" {
		t.Errorf("wrong type: %+v", e)
	}
	if e := New().Validate([]byte(`[]`)); len(e) != 1 {
		t.Errorf("not an object: %+v", e)
	}
}

func TestWarnings(t *testing.T) {
	codes := func(s Settings) map[string]bool {
		m := map[string]bool{}
		for _, w := range Warnings(raw(t, s)) {
			m[w.Code] = true
		}
		return m
	}
	if w := codes(clean31()); len(w) != 0 {
		t.Errorf("clean fixture has warnings: %v", w)
	}
	s := clean31()
	s.Obfuscation.Jc, s.MTU, s.Obfuscation.Jmax, s.Obfuscation.DisableCookies = 20, 1200, 1250, true
	s.Obfuscation.S1 = 30
	w := codes(s)
	for _, want := range []string{"jc_high", "jmax_ge_mtu", "no_flood_protection", "trailers_unequal_s"} {
		if !w[want] {
			t.Errorf("missing warning %s in %v", want, w)
		}
	}
	s = clean31()
	s.MTU = 1400 // 1400 + 32 + 24 + 48 = 1504
	if w := codes(s); !w["mtu_above_1280"] || !w["mtu_headroom"] || len(w) != 2 {
		t.Errorf("mtu 1400: %v", w)
	}
	bad := fixture31()
	bad.Port = 0
	if Warnings(raw(t, bad)) != nil {
		t.Error("invalid settings must give no warnings")
	}
}

// Every warning of the new set, with the case that raises it, the one that does not, and its pointer and params.
func TestWarningMatrix(t *testing.T) {
	type tc struct {
		name, code, ptr string
		params          map[string]string
		base            func() Settings
		mut             func(*Settings)
		quiet           func(*Settings) // the same setting, fixed: the code must be gone
	}
	noKey := func(s *Settings) { s.Obfuscation.HeaderProtectionKey, s.Obfuscation.RandomTrailers = "", false }
	cases := []tc{
		{"2.0 with the headers of plain WireGuard", "h_default_v20", "/obfuscation/h1", nil, fixture20,
			func(s *Settings) {
				s.Obfuscation.H1, s.Obfuscation.H2, s.Obfuscation.H3, s.Obfuscation.H4 = "1", "2", "3", "4"
			},
			func(s *Settings) { s.Obfuscation.H1 = "100000-200000" }},
		{"3.1 without a key, plain headers", "h_default_v20", "/obfuscation/h1", nil, clean31, noKey,
			func(s *Settings) { s.Obfuscation.H1 = "100-9999" }},
		{"a hand-typed H below 5", "h_lt5", "/obfuscation/h2", nil, fixture20,
			func(s *Settings) { s.Obfuscation.H2 = "3" }, func(s *Settings) { s.Obfuscation.H2 = "30000" }},
		{"a hand-typed narrow H range", "h_small_range", "/obfuscation/h3", nil, fixture20,
			func(s *Settings) { s.Obfuscation.H3 = "500000-500999" }, func(s *Settings) { s.Obfuscation.H3 = "500000-501000" }},
		{"quic off 443", "preset_port", "/port", map[string]string{"preset": "quic", "ports": "443"}, clean31,
			func(s *Settings) { s.Obfuscation.Preset = "quic" }, func(s *Settings) { s.Port = 443 }},
		{"dns off 53", "preset_port", "/port", map[string]string{"preset": "dns", "ports": "53"}, clean31,
			func(s *Settings) { s.Obfuscation.Preset = "dns" }, func(s *Settings) { s.Obfuscation.Preset = "stun" }},
		{"ntp off 123", "preset_port", "/port", map[string]string{"preset": "ntp", "ports": "123"}, clean31,
			func(s *Settings) { s.Obfuscation.Preset = "ntp" }, func(s *Settings) { s.Port = 123 }},
		{"sip off 5060", "preset_port", "/port", map[string]string{"preset": "sip", "ports": "5060"}, clean31,
			func(s *Settings) { s.Obfuscation.Preset = "sip" }, func(s *Settings) { s.Port = 5060 }},
		{"ssdp off 1900", "preset_port", "/port", map[string]string{"preset": "ssdp", "ports": "1900"}, clean31,
			func(s *Settings) { s.Obfuscation.Preset = "ssdp" }, func(s *Settings) { s.Port = 1900 }},
		{"mtu with no room for S4", "mtu_headroom", "/mtu", map[string]string{"suggested_mtu": "1396", "s4": "24"}, clean31,
			func(s *Settings) { s.MTU = 1420 }, func(s *Settings) { s.MTU = 1396 }},
		{"keepalive over the NAT timeout", "keepalive_over_nat", "/obfuscation/persistent_keepalive", map[string]string{"max": "35"}, clean31,
			func(s *Settings) { s.Obfuscation.PersistentKeepalive = "25-35" }, func(s *Settings) { s.Obfuscation.PersistentKeepalive = "25-30" }},
		{"a single keepalive over it", "keepalive_over_nat", "/obfuscation/persistent_keepalive", map[string]string{"max": "31"}, fixture20,
			func(s *Settings) { s.Obfuscation.PersistentKeepalive = "31" }, func(s *Settings) { s.Obfuscation.PersistentKeepalive = "30" }},
	}
	find := func(s Settings, code string) []Warning {
		var out []Warning
		for _, w := range Warnings(raw(t, s)) {
			if w.Code == code {
				out = append(out, w)
			}
		}
		return out
	}
	for _, c := range cases {
		s := c.base()
		s.Obfuscation.Preset = "webrtc" // no natural port: only the case under test may speak
		s.Port = 51842
		c.mut(&s)
		got := find(s, c.code)
		if len(got) != 1 || got[0].Pointer != c.ptr || !maps.Equal(got[0].Params, c.params) {
			t.Errorf("%s: %+v, want %s on %s %v", c.name, got, c.code, c.ptr, c.params)
		}
		c.quiet(&s)
		if got := find(s, c.code); len(got) != 0 {
			t.Errorf("%s: still warns after the fix: %+v", c.name, got)
		}
	}

	// quiet cases: a header protection key hides the H values; presets without a natural port ask nothing; 2.0
	// with ranges and a clean 3.1 say nothing
	s := fixture31()
	s.Obfuscation.H1, s.Obfuscation.H2, s.Obfuscation.H3, s.Obfuscation.H4 = "1", "2", "3", "4"
	if w := find(s, "h_default_v20"); len(w) != 0 {
		t.Errorf("a 3.1 profile with a key warns about its headers: %+v", w)
	}
	for _, p := range []string{"stun", "webrtc", "rtp", "dtls", "custom"} {
		s := fixture31()
		s.Obfuscation.Preset = p
		if w := find(s, "preset_port"); len(w) != 0 {
			t.Errorf("%s: %+v", p, w)
		}
	}
	if w := Warnings(raw(t, clean31())); len(w) != 0 {
		t.Errorf("clean 3.1 fixture: %+v", w)
	}
	s20 := fixture20()
	s20.Obfuscation.Preset = "stun"
	if w := Warnings(raw(t, s20)); len(w) != 0 {
		t.Errorf("clean 2.0 fixture: %+v", w)
	}
	// several hand-typed H values: one remark per kind, on the first field of that kind
	s = fixture20()
	s.Obfuscation.Preset = "stun"
	s.Obfuscation.H1, s.Obfuscation.H2, s.Obfuscation.H3, s.Obfuscation.H4 = "2", "3", "500-600", "700-800"
	w := Warnings(raw(t, s))
	if len(w) != 2 || w[0].Pointer != "/obfuscation/h1" || w[0].Code != "h_lt5" || w[1].Pointer != "/obfuscation/h3" || w[1].Code != "h_small_range" {
		t.Errorf("hand-typed headers: %+v", w)
	}
}

// The new client-only fields: what the validator accepts and refuses.
func TestValidateSignatureFields(t *testing.T) {
	cases := []struct {
		name, ptr, code string // code "" = valid
		mut             func(*Obfuscation)
	}{
		{"domain", "", "", func(o *Obfuscation) { o.Domain = "example.com" }},
		{"domain in capitals is allowed, it is normalised on save", "", "", func(o *Obfuscation) { o.Domain = "Example.COM." }},
		{"domain empty is the pool", "", "", func(o *Obfuscation) { o.Domain = "" }},
		{"domain without a dot", "/obfuscation/domain", "invalid", func(o *Obfuscation) { o.Domain = "localhost" }},
		{"domain with a space", "/obfuscation/domain", "invalid", func(o *Obfuscation) { o.Domain = "ex ample.com" }},
		{"domain unicode", "/obfuscation/domain", "invalid", func(o *Obfuscation) { o.Domain = "пример.рф" }},
		{"domain too long", "/obfuscation/domain", "invalid", func(o *Obfuscation) { o.Domain = strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + ".com" }},
		{"per device with a seed", "", "", func(o *Obfuscation) { o.PerDeviceSignature, o.SignatureSeed = true, "abc123" }},
		{"per device without a seed", "/obfuscation/signature_seed", "required", func(o *Obfuscation) { o.PerDeviceSignature, o.SignatureSeed = true, "" }},
		{"a seed alone is harmless", "", "", func(o *Obfuscation) { o.SignatureSeed = "abc123" }},
		{"seed too long", "/obfuscation/signature_seed", "invalid", func(o *Obfuscation) { o.SignatureSeed = strings.Repeat("a", 65) }},
		{"per device with the custom preset is allowed and does nothing", "", "", func(o *Obfuscation) { o.PerDeviceSignature, o.SignatureSeed, o.Preset = true, "s", "custom" }},
	}
	for _, c := range cases {
		for name, base := range map[string]func() Settings{"3.1": fixture31, "2.0": fixture20} {
			s := base()
			c.mut(&s.Obfuscation)
			got := errsOf(s)
			if c.code == "" && len(got) != 0 || c.code != "" && (got[c.ptr] != c.code || len(got) != 1) {
				t.Errorf("%s (%s): want %s %s, got %v", c.name, name, c.ptr, c.code, got)
			}
		}
	}
}

// A profile stored before the new fields parses, and stays what it was.
func TestOldProfilesParse(t *testing.T) {
	old := `{"version":"3.1","port":51842,"mtu":1280,"egress":"direct","subnet4":"10.66.4.0/22","subnet6":"","obfuscation":{"preset":"quic","jc":6,"jmin":10,"jmax":50,"s1":24,"s2":24,"s3":24,"s4":24,"h1":"1","h2":"2","h3":"3","h4":"4","i1":"","i2":"","i3":"","i4":"","i5":"","header_protection_key":"` + key32(7) + `","random_trailers":true,"disable_cookies":false,"content_padding_addition":"2-10","rekey_after_time":"100-120","rekey_timeout":"3-7","reject_after_time":"150-180","keepalive_timeout":"5-15","max_handshake_attempts":"15-20","persistent_keepalive":"25-35"}}`
	if e := New().Validate([]byte(old)); len(e) != 0 {
		t.Fatalf("old profile: %+v", e)
	}
	s, errs := parse([]byte(old))
	if errs != nil || s.Obfuscation.PerDeviceSignature || s.Obfuscation.SignatureSeed != "" || s.Obfuscation.Domain != "" {
		t.Errorf("an old profile has per-device signatures off: %+v %v", s.Obfuscation, errs)
	}
	if _, ok := deviceSignature(s.Obfuscation, "dev_1"); ok {
		t.Error("an old profile got a per-device signature")
	}
}

func FuzzValidate(f *testing.F) {
	f.Add(`{"version":"3.1","port":51842,"obfuscation":{"i1":"<r 2>"}}`)
	f.Add(`{"obfuscation":{"h1":"1-","i1":"<b 0x"}}`)
	f.Fuzz(func(t *testing.T, s string) {
		p := New()
		_ = p.Validate([]byte(s))
		_ = Warnings([]byte(s))
		_, _ = ScoreSettings([]byte(s))
		_ = p.Summary([]byte(s))
		for _, save := range []bool{false, true} {
			if out, err := p.NormalizeSettings(protocols.NormalizeInput{Input: []byte(s), Old: []byte(s), Merged: []byte(s), Save: save}); err == nil {
				_ = p.Validate(out)
			}
		}
	})
}
