package awg

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/mimicry"
)

var _ protocols.SettingsNormalizer = (*Protocol)(nil)

// NormalizeSettings makes sure the schema defaults never become a live config. The form
// shows the schema's fixed values (H1-H4 = 1..4, S = 24); the values worth storing are the generated ones, which
// differ for every profile. So, when the result is stored:
//   - a profile created, or moved to another version, WITHOUT an obfuscation block gets a generated one for its
//     version (a create without a block and without a version is DefaultSettings already);
//   - a block that is there is the owner's choice, whatever its values: an import of an existing client's S1-S4 and
//     H1-H4 must reach the server as written, even when the headers are the plain WireGuard 1, 2, 3, 4. The
//     validator only warns (h_default_v20).
//
// The owner's own choices survive the new block: preset, domain, per-device signatures and seed, and the chain of a
// custom preset. Always, stored or previewed: a seed is made when per-device signatures are on without one, and a
// domain is brought to its normal form. Anything it cannot fix (an invalid domain, a bad MTU) is left for the
// validator to report.
func (*Protocol) NormalizeSettings(in protocols.NormalizeInput) (json.RawMessage, error) {
	s, errs := parse(in.Merged)
	if errs != nil {
		return in.Merged, nil
	}
	o := &s.Obfuscation
	changed := false

	if in.Save && (s.Version == Version31 || s.Version == Version20) {
		created := in.Old == nil
		oldVersion := ""
		if old, errs := parse(in.Old); errs == nil {
			oldVersion = old.Version
		}
		var top map[string]json.RawMessage
		_ = json.Unmarshal(in.Input, &top)
		_, hasBlock := top["obfuscation"]
		moved := created || oldVersion != s.Version
		noBlock := !hasBlock && (!created || s.Version == Version20) // a create at 3.1 holds DefaultSettings' block
		if moved && noBlock {
			preset := o.Preset
			if !mimicry.Valid(preset) {
				preset = defaultPreset
			}
			if fresh, err := GenerateObfuscation(s.Version, preset, s.MTU, o.Domain); err == nil {
				keep := *o
				if preset == mimicry.Custom { // a hand-written chain is not ours to replace
					fresh.I1, fresh.I2, fresh.I3, fresh.I4, fresh.I5 = keep.I1, keep.I2, keep.I3, keep.I4, keep.I5
				}
				fresh.PerDeviceSignature = keep.PerDeviceSignature
				if keep.SignatureSeed != "" {
					fresh.SignatureSeed = keep.SignatureSeed
				}
				if s.Version == Version20 { // a 2.0 profile keeps the stored key, which is never sent anywhere
					fresh.HeaderProtectionKey = keep.HeaderProtectionKey
				}
				*o, changed = fresh, true
			}
		}
	}

	if o.PerDeviceSignature && o.SignatureSeed == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		o.SignatureSeed, changed = hex.EncodeToString(b[:]), true
	}
	if o.Domain != "" {
		if n, err := mimicry.NormalizeDomain(o.Domain); err == nil && n != o.Domain {
			o.Domain, changed = n, true
		}
	}
	if !changed {
		return in.Merged, nil
	}
	return canonical(s)
}

// canonical encodes v the way the framework encodes a merged document: an object with sorted keys, so the bytes
// compare with the stored ones.
func canonical(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}
