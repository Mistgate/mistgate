package awg

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
)

// normalize runs what the access service runs: the client's document over old (nil = a new profile, over the
// defaults), then the plugin's normalisation. It returns the document before and after.
func normalize(t *testing.T, input string, old json.RawMessage, save bool) (merged, out json.RawMessage) {
	t.Helper()
	p := New()
	fresh, err := p.DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ := protocols.FlaggedPointers(p.SettingsSchema(), "x-secret")
	base := old
	if base == nil {
		base = fresh
	}
	if merged, err = protocols.ResolveInput(json.RawMessage(input), base, fresh, secrets); err != nil {
		t.Fatal(err)
	}
	out, err = p.NormalizeSettings(protocols.NormalizeInput{Input: json.RawMessage(input), Old: old, Merged: merged, Save: save})
	if err != nil {
		t.Fatal(err)
	}
	return merged, out
}

func settingsOf(t *testing.T, raw json.RawMessage) Settings {
	t.Helper()
	s, errs := parse(raw)
	if errs != nil {
		t.Fatalf("%v: %s", errs, raw)
	}
	return s
}

// legacy3 is a 3.1 profile as it was stored before the client-only fields existed.
func legacy3(t *testing.T) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"version":"3.1","port":51842,"mtu":1280,"egress":"direct","subnet4":"10.66.4.0/22","subnet6":"","obfuscation":{"preset":"quic","jc":6,"jmin":10,"jmax":50,"s1":24,"s2":24,"s3":24,"s4":24,"h1":"1","h2":"2","h3":"3","h4":"4","i1":"<r 2>","i2":"","i3":"","i4":"","i5":"","header_protection_key":"` + key32(7) + `","random_trailers":true,"disable_cookies":false,"content_padding_addition":"2-10","rekey_after_time":"100-120","rekey_timeout":"3-7","reject_after_time":"150-180","keepalive_timeout":"5-15","max_handshake_attempts":"15-20","persistent_keepalive":"25-35"}}`)
}

// legacy2 is a 2.0 profile with the plain WireGuard headers, a state an owner may have put it in on purpose.
func legacy2(t *testing.T) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"version":"2.0","port":43210,"mtu":1280,"egress":"direct","subnet4":"10.66.8.0/22","subnet6":"","obfuscation":{"preset":"dns","jc":5,"jmin":12,"jmax":60,"s1":40,"s2":60,"s3":25,"s4":18,"h1":"1","h2":"2","h3":"3","h4":"4","i1":"<r 2>","i2":"","i3":"","i4":"","i5":"","header_protection_key":"` + key32(7) + `","random_trailers":false,"disable_cookies":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`)
}

// The schema's own values for a 2.0 form: what a client that renders the form and sends it back would post.
const schemaDefaultsV20 = `{"version":"2.0","obfuscation":{"jc":6,"jmin":10,"jmax":50,"s1":24,"s2":24,"s3":24,"s4":24,"h1":"1","h2":"2","h3":"3","h4":"4","random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`

func TestNormalizeCreate(t *testing.T) {
	p := New()
	// no document at all, or none that changes the version: DefaultSettings already holds a generated block, and
	// the bytes are left as they are
	for _, in := range []string{``, `{}`, `{"port":4000}`, `{"obfuscation":{"preset":"dns"}}`, `{"version":"3.1","obfuscation":{"jc":9}}`} {
		merged, out := normalize(t, in, nil, true)
		if !bytes.Equal(merged, out) {
			t.Errorf("%q: a create that needs nothing was rewritten", in)
		}
		if e := p.Validate(out); len(e) > 0 {
			t.Errorf("%q: %+v", in, e)
		}
		if s := settingsOf(t, out); !s.Obfuscation.PerDeviceSignature || s.Obfuscation.SignatureSeed == "" || plainHeaders(s.Obfuscation) && s.Version == Version20 {
			t.Errorf("%q: %+v", in, s.Obfuscation)
		}
	}

	// 2.0 without a block: invalid as merged (3.1 keys on a 2.0 profile), a generated 2.0 block after
	merged, out := normalize(t, `{"version":"2.0"}`, nil, true)
	if len(p.Validate(merged)) == 0 {
		t.Fatal("the premise: 3.1 keys under version 2.0 are invalid")
	}
	if e := p.Validate(out); len(e) > 0 {
		t.Fatalf("2.0 without a block: %+v\n%s", e, out)
	}
	s := settingsOf(t, out)
	fresh := settingsOf(t, merged)
	o := s.Obfuscation
	if s.Version != Version20 || plainHeaders(o) || o.RandomTrailers || o.ContentPaddingAddition != "" || o.PersistentKeepalive != "25" || o.I1 == "" {
		t.Errorf("2.0 block: %+v", o)
	}
	if o.HeaderProtectionKey != fresh.Obfuscation.HeaderProtectionKey || o.Preset != fresh.Obfuscation.Preset || !o.PerDeviceSignature || o.SignatureSeed == "" {
		t.Errorf("the key (kept, never sent), preset and per-device choice must survive: %+v", o)
	}

	// 2.0 with a block whose headers are 1, 2, 3, 4: it is there, so it is the owner's (an import of an existing
	// client): nothing is regenerated, only the domain is normalised; the validator warns (h_default_v20)
	merged, out = normalize(t, `{"version":"2.0","obfuscation":{"preset":"sip","domain":"Example.org","per_device_signature":false,"h1":"1","h2":"2","h3":"3","h4":"4","random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`, nil, true)
	s = settingsOf(t, out)
	o = s.Obfuscation
	if e := p.Validate(out); len(e) > 0 || !plainHeaders(o) || o.Preset != "sip" || o.Domain != "example.org" || o.PerDeviceSignature {
		t.Errorf("an explicit 2.0 block with plain headers: %+v %+v", e, o)
	}
	if w := Warnings(out); len(w) == 0 || w[0].Code != "h_default_v20" {
		t.Errorf("warnings: %+v", w)
	}
	_ = merged
	// the same values with a header of the owner's own are an explicit block: untouched
	merged, out = normalize(t, `{"version":"2.0","obfuscation":{"random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25","h1":"100-200000","h2":"300000-400000","h3":"500000-600000","h4":"700000-800000"}}`, nil, true)
	if !bytes.Equal(merged, out) {
		t.Error("an explicit 2.0 block was rewritten")
	}
	// a custom preset with its hand-written chain is an explicit block too: nothing is replaced
	_, out = normalize(t, `{"version":"2.0","obfuscation":{"preset":"custom","i1":"<r 5>","h1":"1","h2":"2","h3":"3","h4":"4","random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`, nil, true)
	if s = settingsOf(t, out); s.Obfuscation.I1 != "<r 5>" || !plainHeaders(s.Obfuscation) || s.Obfuscation.Preset != "custom" {
		t.Errorf("custom: %+v", s.Obfuscation)
	}
	// every create comes out valid whatever the form said
	if _, out := normalize(t, schemaDefaultsV20, nil, true); len(p.Validate(out)) > 0 {
		t.Errorf("schema defaults on 2.0: %+v", p.Validate(out))
	}
}

func TestNormalizeUpdate(t *testing.T) {
	p := New()
	// the version moves with no block in the document: a generated block for the new version
	_, out := normalize(t, `{"version":"2.0"}`, legacy3(t), true)
	s := settingsOf(t, out)
	if e := p.Validate(out); len(e) > 0 || s.Version != Version20 || plainHeaders(s.Obfuscation) || s.Obfuscation.RandomTrailers {
		t.Errorf("3.1 -> 2.0: %+v %+v", e, s.Obfuscation)
	}
	_, out = normalize(t, `{"version":"3.1"}`, legacy2(t), true)
	s = settingsOf(t, out)
	if e := p.Validate(out); len(e) > 0 || s.Version != Version31 || s.Obfuscation.HeaderProtectionKey == key32(7) || !s.Obfuscation.RandomTrailers {
		t.Errorf("2.0 -> 3.1: %+v %+v", e, s.Obfuscation)
	}
	// an owner's per-device choice and signature seed survive the move; an old profile stays off
	if s.Obfuscation.PerDeviceSignature || s.Obfuscation.SignatureSeed == "" {
		t.Errorf("a legacy profile that moves stays off: %+v", s.Obfuscation)
	}
	// moving with an explicit block is the owner's block
	merged, out := normalize(t, `{"version":"2.0","obfuscation":{"random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25","h1":"100-200","h2":"300-400","h3":"500-600","h4":"700-800"}}`, legacy3(t), true)
	if !bytes.Equal(merged, out) {
		t.Error("an explicit block was rewritten on a version move")
	}
	// a 2.0 profile that keeps its version keeps its plain headers: the owner's state, a warning's business
	merged, out = normalize(t, `{"port":4000}`, legacy2(t), true)
	if !bytes.Equal(merged, out) {
		t.Error("a 2.0 profile with plain headers was rewritten by an update that kept the version")
	}
	if w := Warnings(out); len(w) == 0 || w[0].Code != "h_default_v20" {
		t.Errorf("warnings: %+v", w)
	}
	// a version flip INTO 2.0 with a block that has plain headers and S1-S4 of its own (a pasted .conf, an API
	// client): the block is the owner's, S and H reach the server as written
	_, out = normalize(t, `{"version":"2.0","obfuscation":{"s1":40,"s2":60,"s3":25,"s4":18,"h1":"1","h2":"2","h3":"3","h4":"4","random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`, legacy3(t), true)
	if s = settingsOf(t, out); !plainHeaders(s.Obfuscation) || s.Obfuscation.S() != [4]int{40, 60, 25, 18} || len(p.Validate(out)) > 0 {
		t.Errorf("flip into 2.0 with an imported block: %+v", s.Obfuscation)
	}
	// an update that changes nothing the normaliser cares about gives back the very bytes (the caller compares)
	for _, in := range []string{`{}`, `{"mtu":1300}`, `{"egress":"warp"}`, `{"obfuscation":{"jc":9}}`} {
		merged, out := normalize(t, in, legacy3(t), true)
		if !bytes.Equal(merged, out) {
			t.Errorf("%q: an old profile was rewritten", in)
		}
		if bytes.Contains(out, []byte("per_device_signature")) && !bytes.Contains(legacy3(t), []byte("per_device_signature")) && in == `{}` {
			t.Errorf("%q: the new fields appeared in an untouched profile", in)
		}
	}
}

func TestNormalizeSeedAndDomain(t *testing.T) {
	p := New()
	for _, save := range []bool{false, true} { // a preview and a save both fill the seed
		merged, out := normalize(t, `{"obfuscation":{"per_device_signature":true,"signature_seed":"","domain":"Example.COM."}}`, legacy3(t), save)
		s := settingsOf(t, out)
		if len(s.Obfuscation.SignatureSeed) != 32 || !s.Obfuscation.PerDeviceSignature || s.Obfuscation.Domain != "example.com" {
			t.Errorf("save=%v: %+v", save, s.Obfuscation)
		}
		if e := p.Validate(out); len(e) > 0 {
			t.Errorf("save=%v: %+v", save, e)
		}
		if len(p.Validate(merged)) == 0 {
			t.Errorf("save=%v: the premise: per-device without a seed is invalid before normalisation", save)
		}
		// the rest of the profile is as it was
		was, now := settingsOf(t, legacy3(t)), s
		now.Obfuscation.PerDeviceSignature, now.Obfuscation.SignatureSeed, now.Obfuscation.Domain = false, "", ""
		if was != now {
			t.Errorf("save=%v: more than the seed and the domain changed:\n%+v\n%+v", save, was, now)
		}
	}
	// two profiles never share a seed
	_, a := normalize(t, `{"obfuscation":{"per_device_signature":true,"signature_seed":""}}`, legacy3(t), true)
	_, b := normalize(t, `{"obfuscation":{"per_device_signature":true,"signature_seed":""}}`, legacy3(t), true)
	if settingsOf(t, a).Obfuscation.SignatureSeed == settingsOf(t, b).Obfuscation.SignatureSeed {
		t.Error("two seeds are equal")
	}
	// a given seed is kept
	_, out := normalize(t, `{"obfuscation":{"per_device_signature":true,"signature_seed":"mine"}}`, legacy3(t), true)
	if settingsOf(t, out).Obfuscation.SignatureSeed != "mine" {
		t.Error("the seed was replaced")
	}
	// a bad domain is the validator's to report, not the normaliser's to fix or to fail on
	_, out = normalize(t, `{"obfuscation":{"domain":"not a domain"}}`, legacy3(t), true)
	if e := p.Validate(out); len(e) != 1 || e[0].Pointer != "/obfuscation/domain" {
		t.Errorf("bad domain: %+v", e)
	}
	// a preview of a new 2.0 profile shows the form as it is (nothing is stored, nothing is replaced)
	merged, out := normalize(t, `{"version":"2.0"}`, nil, false)
	if !bytes.Equal(merged, out) {
		t.Error("a preview rewrote the form")
	}
	// garbage goes through untouched for the validator
	for _, in := range []string{`{"version":"9"}`, `{"mtu":10}`} {
		merged, out := normalize(t, in, nil, true)
		if !bytes.Equal(merged, out) || len(p.Validate(out)) == 0 {
			t.Errorf("%q: %s", in, out)
		}
	}
	if out, err := p.NormalizeSettings(protocols.NormalizeInput{Merged: json.RawMessage(`[]`), Save: true}); err != nil || string(out) != `[]` {
		t.Errorf("not an object: %s %v", out, err)
	}
}

// A complete block imported from an existing 2.0 client is an explicit choice, even when its four message headers
// happen to be the WireGuard defaults: S1-S4 and H1-H4 reach the server as written, on a create and on a move from 3.1.
func TestNormalizeKeepsAnImportedV20Block(t *testing.T) {
	imported := fixture20()
	imported.Obfuscation.Preset = "custom"
	imported.Obfuscation.H1, imported.Obfuscation.H2, imported.Obfuscation.H3, imported.Obfuscation.H4 = "1", "2", "3", "4"
	input, err := json.Marshal(imported)
	if err != nil {
		t.Fatal(err)
	}
	for name, old := range map[string]json.RawMessage{"create": nil, "update from 3.1": legacy3(t)} {
		_, out := normalize(t, string(input), old, true)
		got := settingsOf(t, out)
		if got.Obfuscation.H() != imported.Obfuscation.H() || got.Obfuscation.S() != imported.Obfuscation.S() {
			t.Errorf("%s: S %v -> %v, H %v -> %v", name, imported.Obfuscation.S(), got.Obfuscation.S(), imported.Obfuscation.H(), got.Obfuscation.H())
		}
	}
}
