package awg

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols/awg/mimicry"
)

// Property: whatever the generator returns is valid, for every version, preset, MTU and seed.
func TestGeneratedObfuscationIsAlwaysValid(t *testing.T) {
	for _, version := range []string{Version31, Version20} {
		for _, p := range mimicry.Presets() {
			for _, mtu := range []int{1200, 1280, 1408, 1420} {
				for seed := uint64(0); seed < 60; seed++ {
					o, err := GenerateObfuscationSeeded(version, p.ID, mtu, seed, "")
					if err != nil {
						t.Fatalf("%s %s mtu %d seed %d: %v", version, p.ID, mtu, seed, err)
					}
					s := fixture31()
					s.Version, s.MTU, s.Obfuscation = version, mtu, o
					if e := validate(s); len(e) > 0 {
						t.Fatalf("%s %s mtu %d seed %d: %+v\n%+v", version, p.ID, mtu, seed, e, o)
					}
					if o.Jmax >= mtu {
						t.Fatalf("jmax %d not below mtu %d", o.Jmax, mtu)
					}
					if o.Preset != p.ID || (p.ID == mimicry.Custom) != (o.I1 == "") {
						t.Fatalf("preset %q, i1 %q", o.Preset, o.I1)
					}
					if len(o.SignatureSeed) != 32 || o.PerDeviceSignature {
						t.Fatalf("seed %q, per device %v: the generator makes a seed and leaves the switch to its caller", o.SignatureSeed, o.PerDeviceSignature)
					}
					if version == Version31 && (o.HeaderProtectionKey == "" || !o.RandomTrailers || o.S1 != o.S2 || o.S2 != o.S3 || o.S3 != o.S4) {
						t.Fatalf("3.1 shape: %+v", o)
					}
					if version == Version20 && (o.HeaderProtectionKey != "" || o.RandomTrailers || o.ContentPaddingAddition != "" || o.PersistentKeepalive != "25" || plainHeaders(o)) {
						t.Fatalf("2.0 carries 3.x keys or plain headers: %+v", o)
					}
					for _, w := range warnings(s) {
						switch w.Code {
						case "h_default_v20", "h_small_range", "h_lt5", "keepalive_over_nat", "jc_high", "jmax_ge_mtu", "trailers_unequal_s":
							t.Fatalf("%s %s mtu %d seed %d: the generator's own output warns %s", version, p.ID, mtu, seed, w.Code)
						case "mtu_headroom": // only where the 3.1 key's minimum S4 of 12 cannot fit
							if version != Version31 || mtu <= pathMTU-wireOverhead-12 {
								t.Fatalf("%s mtu %d seed %d: mtu_headroom", version, mtu, seed)
							}
						}
					}
				}
			}
		}
	}
}

// The 3.1 timers are ranges around the WireGuard constants, drawn per profile (awg2.sh does the same), and obey the
// engine's rules: rekey ends before reject starts, the retry timeout stays below 10 s, the attempts at 20, the
// keepalive under the 30 s of a NAT.
func TestGenerate31Randomisation(t *testing.T) {
	type field struct {
		name     string
		get      func(Obfuscation) string
		loA, loB uint64 // bounds of the lower end
		hiA, hiB uint64 // bounds of the upper end
	}
	fields := []field{
		{"content_padding_addition", func(o Obfuscation) string { return o.ContentPaddingAddition }, 8, 24, 48, 96},
		{"rekey_after_time", func(o Obfuscation) string { return o.RekeyAfterTime }, 110, 125, 140, 160},
		{"rekey_timeout", func(o Obfuscation) string { return o.RekeyTimeout }, 5, 6, 7, 9},
		{"reject_after_time", func(o Obfuscation) string { return o.RejectAfterTime }, 175, 190, 200, 215},
		{"keepalive_timeout", func(o Obfuscation) string { return o.KeepaliveTimeout }, 9, 14, 20, 30},
		{"max_handshake_attempts", func(o Obfuscation) string { return o.MaxHandshakeAttempts }, 14, 17, 17, 20},
		{"persistent_keepalive", func(o Obfuscation) string { return o.PersistentKeepalive }, 22, 25, 27, 30},
	}
	seen := map[string]map[string]bool{}
	allAmnezia := 0
	for seed := uint64(0); seed < 500; seed++ {
		o, err := GenerateObfuscationSeeded(Version31, "dns", 1280, seed, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fields {
			r, err := parseRange(f.get(o), maxUint16)
			if err != nil || r.lo < f.loA || r.lo > f.loB || r.hi < f.hiA || r.hi > f.hiB || r.lo >= r.hi {
				t.Fatalf("seed %d %s = %q (%v), want lo %d-%d hi %d-%d", seed, f.name, f.get(o), err, f.loA, f.loB, f.hiA, f.hiB)
			}
			if seen[f.name] == nil {
				seen[f.name] = map[string]bool{}
			}
			seen[f.name][f.get(o)] = true
		}
		rekey, _ := parseRange(o.RekeyAfterTime, maxUint16)
		reject, _ := parseRange(o.RejectAfterTime, maxUint16)
		if rekey.hi >= reject.lo {
			t.Fatalf("seed %d: rekey %s not below reject %s", seed, o.RekeyAfterTime, o.RejectAfterTime)
		}
		if [5]string{o.RekeyAfterTime, o.RekeyTimeout, o.RejectAfterTime, o.KeepaliveTimeout, o.MaxHandshakeAttempts} == amneziaTimers {
			allAmnezia++
		}
	}
	for _, f := range fields {
		want := 8
		if f.name == "rekey_timeout" {
			want = 4 // 5-7, 5-8, 6-8, 6-9: all there is
		}
		if len(seen[f.name]) < want {
			t.Errorf("%s: only %d distinct values in 500 profiles", f.name, len(seen[f.name]))
		}
	}
	if allAmnezia != 0 {
		t.Errorf("%d profiles carry exactly Amnezia's timers", allAmnezia)
	}
}

// MTU + 32 + S4 + 8 + 40 must fit 1500: the generator keeps S4 within it for every MTU, as far as the key's minimum
// S4 of 12 allows, and the validator names the MTU that fits when it cannot.
func TestGenerateKeepsTheDataPacketWithinThePath(t *testing.T) {
	for mtu := minMTU; mtu <= maxMTU; mtu++ {
		for _, version := range []string{Version31, Version20} {
			for seed := uint64(0); seed < 6; seed++ {
				o, err := GenerateObfuscationSeeded(version, "stun", mtu, seed, "")
				if err != nil {
					t.Fatal(err)
				}
				s := fixture31()
				s.Version, s.MTU, s.Obfuscation = version, mtu, o
				fits := mtu+32+o.S4+8+40 <= pathMTU
				var head *Warning
				for _, w := range warnings(s) {
					if w.Code == "mtu_headroom" {
						head = &w
					}
				}
				if fits == (head != nil) {
					t.Fatalf("%s mtu %d S4 %d: fits %v but the warning is %+v", version, mtu, o.S4, fits, head)
				}
				if !fits {
					if version == Version20 || o.S4 != 12 {
						t.Fatalf("%s mtu %d: S4 %d does not fit and is not the unavoidable minimum", version, mtu, o.S4)
					}
					want := suggestedMTU(o.S4)
					if head.Params["suggested_mtu"] != strconv.Itoa(want) || want+32+o.S4+8+40 != pathMTU {
						t.Fatalf("mtu %d: suggestion %v, want %d", mtu, head.Params, want)
					}
					s.MTU = want // taking the advice clears it
					for _, w := range warnings(s) {
						if w.Code == "mtu_headroom" {
							t.Fatalf("the suggested MTU %d still warns", want)
						}
					}
				}
			}
		}
	}
	if maxS4(1280) != 140 || maxS4(1420) != 0 || maxS4(1300) != 120 || suggestedMTU(24) != 1396 || suggestedMTU(0) != 1420 || suggestedMTU(300) != minMTU {
		t.Errorf("maxS4/suggestedMTU: %d %d %d %d %d %d", maxS4(1280), maxS4(1420), maxS4(1300), suggestedMTU(24), suggestedMTU(0), suggestedMTU(300))
	}
}

func TestGenerateRandomnessAndSeeds(t *testing.T) {
	a, _ := GenerateObfuscationSeeded(Version31, "quic", 1280, 1, "")
	b, _ := GenerateObfuscationSeeded(Version31, "quic", 1280, 1, "")
	c, _ := GenerateObfuscationSeeded(Version31, "quic", 1280, 2, "")
	if a != b {
		t.Error("the same seed gives different output")
	}
	if a == c || a.HeaderProtectionKey == c.HeaderProtectionKey || a.SignatureSeed == c.SignatureSeed {
		t.Error("different seeds give the same output")
	}
	// Production path: two calls never agree on the key or the signature seed, and the key is 32 bytes.
	x, _ := GenerateObfuscation(Version31, "quic", 1280, "")
	y, _ := GenerateObfuscation(Version31, "quic", 1280, "")
	if x.HeaderProtectionKey == y.HeaderProtectionKey || x.SignatureSeed == y.SignatureSeed || x.I1 == y.I1 && x.S1 == y.S1 && x.Jc == y.Jc && x.Jmin == y.Jmin {
		t.Error("production output repeats")
	}
	if b, err := base64.StdEncoding.DecodeString(x.HeaderProtectionKey); err != nil || len(b) != 32 {
		t.Errorf("key: %v %d", err, len(b))
	}
	for _, bad := range []struct {
		v, p string
		m    int
		d    string
	}{{"1.0", "quic", 1280, ""}, {"3.1", "tls", 1280, ""}, {"3.1", "quic", 1000, ""}, {"3.1", "quic", 1500, ""}, {"3.1", "custom", 1280, "no_dot"}, {"3.1", "stun", 1280, "ex ample.com"}} {
		if _, err := GenerateObfuscation(bad.v, bad.p, bad.m, bad.d); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// The domain reaches the packets of the presets that carry one, comes back in its normal form, and a bad one is an
// error for every preset.
func TestGenerateDomain(t *testing.T) {
	o, err := GenerateObfuscationSeeded(Version31, mimicry.SIP, 1280, 3, "Example.COM.")
	if err != nil {
		t.Fatal(err)
	}
	if o.Domain != "example.com" || !strings.Contains(o.I1, hex.EncodeToString([]byte("example.com"))) {
		t.Errorf("domain %q, I1 %s", o.Domain, o.I1)
	}
	if o, _ = GenerateObfuscationSeeded(Version31, mimicry.NTP, 1280, 3, "example.com"); o.Domain != "example.com" {
		t.Errorf("the domain of a preset without a name on the wire is kept for the form: %q", o.Domain)
	}
	if o, _ = GenerateObfuscationSeeded(Version31, mimicry.SIP, 1280, 3, ""); o.Domain != "" {
		t.Errorf("an empty domain stays empty (the pool is drawn per packet): %q", o.Domain)
	}
}

func TestGenerateSignature(t *testing.T) {
	o, err := GenerateSignature(mimicry.DNS, "Example.org")
	if err != nil {
		t.Fatal(err)
	}
	if o.Preset != mimicry.DNS || o.Domain != "example.org" || o.I1 == "" || o.I2 == "" || o.I3 == "" || o.I4 != "" {
		t.Errorf("signature: %+v", o)
	}
	// Nothing but the preset, the domain and the packets: the rest is what the peers agree on.
	o.Preset, o.Domain, o.I1, o.I2, o.I3, o.I4, o.I5 = "", "", "", "", "", "", ""
	if o != (Obfuscation{}) {
		t.Errorf("a signature carries more than the packets: %+v", o)
	}
	a, _ := GenerateSignature(mimicry.DNS, "")
	b, _ := GenerateSignature(mimicry.DNS, "")
	if a == b {
		t.Error("two signatures are identical")
	}
	for name, c := range map[string][2]string{"custom": {"custom", ""}, "unknown": {"tls", ""}, "bad domain": {"dns", "a_b.com"}} {
		if _, err := GenerateSignature(c[0], c[1]); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Per-device signatures: the same device and seed always give the same chain, another device or seed another one.
func TestDeviceSignature(t *testing.T) {
	base := fixture31().Obfuscation
	base.PerDeviceSignature, base.SignatureSeed = true, "00112233445566778899aabbccddeeff"
	for _, preset := range []string{mimicry.QUIC, mimicry.DNS, mimicry.SIP} {
		o := base
		o.Preset = preset
		a, ok := deviceSignature(o, "dev_AAAA")
		if !ok || a[0] == "" {
			t.Fatalf("%s: no signature (%v)", preset, ok)
		}
		for range 3 {
			if again, _ := deviceSignature(o, "dev_AAAA"); again != a {
				t.Fatalf("%s: the chain of one device changes between renders", preset)
			}
		}
		if b, _ := deviceSignature(o, "dev_BBBB"); b == a {
			t.Errorf("%s: two devices share a chain", preset)
		}
		o.SignatureSeed = "ffeeddccbbaa99887766554433221100"
		if c, _ := deviceSignature(o, "dev_AAAA"); c == a {
			t.Errorf("%s: another seed gives the same chain", preset)
		}
		for i, v := range a {
			if _, err := mimicry.Parse(v); err != nil {
				t.Fatalf("%s I%d: %v", preset, i+1, err)
			}
		}
	}
	// "ab"+"c" is not "a"+"bc"
	o := base
	o.Preset, o.SignatureSeed = mimicry.SIP, "seed-ab"
	x, _ := deviceSignature(o, "c")
	o.SignatureSeed = "seed-a"
	if y, _ := deviceSignature(o, "bc"); x == y {
		t.Error("seed and device id run together")
	}
	// the domain of the profile goes into every device's chain
	o = base
	o.Preset, o.Domain = mimicry.SIP, "example.com"
	if c, _ := deviceSignature(o, "dev_X"); !strings.Contains(c[0], hex.EncodeToString([]byte("example.com"))) {
		t.Errorf("domain missing from %s", c[0])
	}
	// off, custom, no seed, no device: the profile's own packets
	for name, mut := range map[string]func(*Obfuscation){
		"off":     func(o *Obfuscation) { o.PerDeviceSignature = false },
		"custom":  func(o *Obfuscation) { o.Preset = mimicry.Custom },
		"no seed": func(o *Obfuscation) { o.SignatureSeed = "" },
	} {
		o := base
		mut(&o)
		if _, ok := deviceSignature(o, "dev_AAAA"); ok {
			t.Errorf("%s: a per-device signature was made", name)
		}
	}
	if _, ok := deviceSignature(base, ""); ok {
		t.Error("a per-device signature without a device")
	}
}

func TestDefaultSettings(t *testing.T) {
	p := New()
	seen, seeds := map[string]bool{}, map[string]bool{}
	for i := 0; i < 50; i++ {
		raw, err := p.DefaultSettings()
		if err != nil {
			t.Fatal(err)
		}
		if e := p.Validate(raw); len(e) > 0 {
			t.Fatalf("defaults are invalid: %+v\n%s", e, raw)
		}
		var s Settings
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if s.Version != Version31 || s.Port < 10000 || s.Port > 60000 || slices.Contains(knownPorts, s.Port) || s.MTU != 1280 || s.Egress != "direct" {
			t.Fatalf("defaults: %s", raw)
		}
		if s.Subnet4 != "10.66.4.0/22" || s.Subnet6 != "fd66:66:0:1::/64" || s.Obfuscation.Preset != defaultPreset || s.Obfuscation.I1 == "" {
			t.Fatalf("defaults: %s", raw)
		}
		if !s.Obfuscation.PerDeviceSignature || s.Obfuscation.SignatureSeed == "" || s.Obfuscation.Domain != "" {
			t.Fatalf("a new profile has per-device signatures on, a seed and the pool for a domain: %s", raw)
		}
		// DNS is on a random port: that is the one remark a new profile starts with, and the score stays high
		if w := warnings(s); len(w) != 1 || w[0].Code != "preset_port" || w[0].Params["ports"] != "53" {
			t.Fatalf("the defaults warn: %+v", w)
		}
		if sc := score(s); sc.Value < 85 {
			t.Fatalf("the defaults score %d: %+v", sc.Value, sc.Items)
		}
		seen[s.Obfuscation.HeaderProtectionKey] = true
		seeds[s.Obfuscation.SignatureSeed] = true
	}
	if len(seen) != 50 || len(seeds) != 50 {
		t.Errorf("%d distinct header protection keys and %d seeds in 50 profiles", len(seen), len(seeds))
	}
}

// PresetVaries is a list kept by hand: the generator decides.
func TestPresetVariesMatchesTheGenerator(t *testing.T) {
	for _, p := range mimicry.Presets() {
		if p.ID == mimicry.Custom {
			if PresetVaries(p.ID) {
				t.Error("custom has no generator")
			}
			continue
		}
		first, err := mimicry.GenerateChain(p.ID, mimicry.Options{}, pcgFrom([]byte("0123456789abcdef")))
		if err != nil {
			t.Fatal(err)
		}
		differs := false
		for seed := byte(1); seed < 40 && !differs; seed++ {
			c, err := mimicry.GenerateChain(p.ID, mimicry.Options{}, pcgFrom([]byte{seed, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
			if err != nil {
				t.Fatal(err)
			}
			differs = c != first
		}
		if differs != PresetVaries(p.ID) {
			t.Errorf("%s: the chain differs between seeds = %v, PresetVaries = %v", p.ID, differs, PresetVaries(p.ID))
		}
	}
}

func TestSubnetsFor(t *testing.T) {
	v4, v6, err := SubnetsFor(1)
	if err != nil || v4 != "10.66.4.0/22" || v6 != "fd66:66:0:1::/64" {
		t.Errorf("slot 1: %s %s %v", v4, v6, err)
	}
	v4, v6, _ = SubnetsFor(63)
	if v4 != "10.66.252.0/22" || v6 != "fd66:66:0:3f::/64" {
		t.Errorf("slot 63: %s %s", v4, v6)
	}
	seen := map[string]bool{}
	for n := 0; n < 64; n++ {
		a, b, err := SubnetsFor(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseSubnet4(a); err != nil {
			t.Errorf("slot %d: %v", n, err)
		}
		if _, err := parseSubnet6(b); err != nil {
			t.Errorf("slot %d: %v", n, err)
		}
		if seen[a] || seen[b] {
			t.Errorf("slot %d repeats", n)
		}
		seen[a], seen[b] = true, true
	}
	if _, _, err := SubnetsFor(64); err == nil {
		t.Error("slot 64 accepted")
	}
	if _, _, err := SubnetsFor(-1); err == nil {
		t.Error("slot -1 accepted")
	}
}

func TestRandomPort(t *testing.T) {
	for i := 0; i < 2000; i++ {
		p, err := RandomPort()
		if err != nil || p < 10000 || p > 60000 || slices.Contains(knownPorts, p) {
			t.Fatalf("port %d, %v", p, err)
		}
	}
}
