package access

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/mimicry"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// AwgService: the stateless helpers of the profile editor, and the client networks of AWG profiles.

func (s *Service) ListMimicryPresets(context.Context, *connect.Request[adminv1.ListMimicryPresetsRequest]) (*connect.Response[adminv1.ListMimicryPresetsResponse], error) {
	resp := &adminv1.ListMimicryPresetsResponse{Domains: mimicry.Domains()}
	for _, p := range mimicry.Presets() {
		mp := &adminv1.MimicryPreset{Id: p.ID, Name: p.Label, Versions: []string{awg.Version31, awg.Version20}, UsesDomain: mimicry.UsesDomain(p.ID), VariesPerDevice: awg.PresetVaries(p.ID)}
		for _, port := range mimicry.NaturalPorts(p.ID) {
			mp.NaturalPorts = append(mp.NaturalPorts, uint32(port))
		}
		resp.Presets = append(resp.Presets, mp)
	}
	return connect.NewResponse(resp), nil
}

// warningText is the English line of a validator warning code; the UI localises by code.
var warningText = map[string]string{
	"jc_high":             "a high junk count slows the handshake",
	"jmax_ge_mtu":         "Jmax is not below the MTU: junk packets fragment",
	"no_flood_protection": "cookies are off: nothing slows a handshake flood",
	"trailers_unequal_s":  "random trailers work best with S1-S4 equal",
	"mtu_above_1280":      "AmneziaVPN on a desktop overwrites the MTU of a vpn:// key",
	"mtu_headroom":        "MTU + S4 + 80 does not fit 1500: data packets fragment on a standard path",
	"h_default_v20":       "H1-H4 = 1, 2, 3, 4 are the headers of plain WireGuard: the main fingerprint without header protection",
	"h_small_range":       "an H range narrower than 1000 values is easy to guess",
	"h_lt5":               "an H value below 5 is a message type of plain WireGuard",
	"preset_port":         "this mimicry looks odd on this port",
	"keepalive_over_nat":  "a keepalive above 30 s is slower than the UDP timeout of many NATs",
}

func fieldErrorOf(w awg.Warning) *adminv1.FieldError {
	return &adminv1.FieldError{Pointer: w.Pointer, Code: w.Code, Message: warningText[w.Code], Params: w.Params}
}

// awgAdvice is what PreviewProfile adds for AWG: the warnings and the obfuscation score of valid merged settings.
func awgAdvice(merged json.RawMessage) ([]*adminv1.FieldError, *adminv1.ObfuscationScore) {
	var warns []*adminv1.FieldError
	for _, w := range awg.Warnings(merged) {
		warns = append(warns, fieldErrorOf(w))
	}
	sc, ok := awg.ScoreSettings(merged)
	if !ok {
		return warns, nil
	}
	out := &adminv1.ObfuscationScore{Value: uint32(sc.Value), Base: uint32(sc.Base), Tier: sc.Tier}
	for _, it := range sc.Items {
		out.Items = append(out.Items, &adminv1.ScoreItem{Code: it.Code, Pointer: it.Pointer, Delta: int32(it.Delta), Params: it.Params})
	}
	return warns, out
}

// obfuscationKeys marshals ob as a JSON object, keeping only the keys in keep (all of them when keep is empty) and
// dropping the ones in drop.
func obfuscationKeys(ob awg.Obfuscation, keep, drop []string) (string, error) {
	b, err := json.Marshal(ob)
	if err != nil {
		return "", err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return "", err
	}
	out := map[string]json.RawMessage{}
	for k, v := range all {
		if (len(keep) == 0 || slices.Contains(keep, k)) && !slices.Contains(drop, k) {
			out[k] = v
		}
	}
	b, err = json.Marshal(out)
	return string(b), err
}

func (s *Service) GenerateObfuscation(_ context.Context, req *connect.Request[adminv1.GenerateObfuscationRequest]) (*connect.Response[adminv1.GenerateObfuscationResponse], error) {
	m := req.Msg
	if m.Version != awg.Version31 && m.Version != awg.Version20 {
		return nil, invalid("version must be %q or %q", awg.Version31, awg.Version20)
	}
	if !mimicry.Valid(m.Preset) {
		return nil, invalid("unknown preset %q", m.Preset)
	}
	if m.Domain != "" {
		if _, err := mimicry.NormalizeDomain(m.Domain); err != nil {
			return nil, invalid("domain: %v", err)
		}
	}
	if m.Mode == adminv1.GenerateMode_GENERATE_MODE_SIGNATURE {
		if m.Preset == mimicry.Custom {
			return nil, invalid("the custom preset has no generator: write I1-I5 by hand")
		}
		ob, err := awg.GenerateSignature(m.Preset, m.Domain)
		if err != nil {
			return nil, s.internal("generate signature", err)
		}
		obJSON, err := obfuscationKeys(ob, []string{"preset", "domain", "i1", "i2", "i3", "i4", "i5"}, nil)
		if err != nil {
			return nil, s.internal("marshal obfuscation", err)
		}
		return connect.NewResponse(&adminv1.GenerateObfuscationResponse{ObfuscationJson: obJSON}), nil
	}
	mtu := int(m.Mtu)
	if mtu == 0 {
		mtu = 1280
	}
	if mtu < 1200 || mtu > 1420 {
		return nil, invalid("mtu must be 1200-1420")
	}
	ob, err := awg.GenerateObfuscation(m.Version, m.Preset, mtu, m.Domain)
	if err != nil {
		return nil, s.internal("generate obfuscation", err)
	}
	// per_device_signature is the owner's choice, not the generator's: the form keeps what it has.
	obJSON, err := obfuscationKeys(ob, nil, []string{"per_device_signature"})
	if err != nil {
		return nil, s.internal("marshal obfuscation", err)
	}
	resp := &adminv1.GenerateObfuscationResponse{ObfuscationJson: obJSON}
	// The notes come from the validator's Warnings, which judge a whole settings document: lay the result over the
	// defaults. The port is the defaults' random one, so what depends on it (preset_port) says nothing here.
	def, err := awgDefaultSettings()
	if err == nil {
		def.Version, def.MTU, def.Obfuscation = m.Version, mtu, ob
		if raw, err := json.Marshal(def); err == nil {
			for _, w := range awg.Warnings(raw) {
				if w.Code != "preset_port" {
					resp.Warnings = append(resp.Warnings, fieldErrorOf(w))
				}
			}
		}
	}
	return connect.NewResponse(resp), nil
}

func awgDefaultSettings() (awg.Settings, error) {
	raw, err := awg.New().DefaultSettings()
	if err != nil {
		return awg.Settings{}, err
	}
	var st awg.Settings
	return st, json.Unmarshal(raw, &st)
}

// ---- client networks ----

// awgNetworks are the two client networks of an AWG profile's settings ("" = none).
type awgNetworks struct {
	Subnet4 string `json:"subnet4"`
	Subnet6 string `json:"subnet6"`
}

func networksOf(settings []byte) (n awgNetworks) {
	_ = json.Unmarshal(settings, &n)
	return n
}

func (n awgNetworks) prefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{n.Subnet4, n.Subnet6} {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// overlaps names the other profile whose networks overlap any of the prefixes.
func overlaps(prefixes []netip.Prefix, others []store.AccessProfile) (string, bool) {
	for _, o := range others {
		for _, q := range networksOf([]byte(o.SettingsJSON)).prefixes() {
			for _, p := range prefixes {
				if p.Overlaps(q) {
					return o.Name, true
				}
			}
		}
	}
	return "", false
}

// awgOtherProfiles lists the AWG profiles other than exceptID.
func (s *Service) awgOtherProfiles(ctx context.Context, exceptID string) ([]store.AccessProfile, error) {
	ps, err := s.st.Access().Profiles(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ps, func(p store.AccessProfile) bool { return p.Protocol != awg.ID || p.ID == exceptID }), nil
}

// checkAWGNetworks enforces the two rules about the client networks of an AWG profile:
//   - they never overlap the networks of another AWG profile (the addresses are the identity of a peer);
//   - once the profile is deployed on a node or has handed out an address they cannot change, because every
//     issued config holds an address from them.
//
// On creation (old == nil) a network the request did not name is chosen: the first profile slot
// (awg.SubnetsFor) that no other profile touches. It returns the settings to store. Problems come back as
// FieldError rows on /subnet4 and /subnet6 so the editor shows them at the field.
func (s *Service) checkAWGNetworks(ctx context.Context, profileID string, merged json.RawMessage, input string, old json.RawMessage) (json.RawMessage, error) {
	others, err := s.awgOtherProfiles(ctx, profileID)
	if err != nil {
		return nil, s.internal("profiles", err)
	}
	nets := networksOf(merged)
	if old == nil {
		var named map[string]json.RawMessage
		_ = json.Unmarshal([]byte(input), &named)
		_, has4 := named["subnet4"]
		_, has6 := named["subnet6"]
		if !has4 || !has6 {
			slot := -1
			for n := 1; n < 64 && slot < 0; n++ { // slot 0 is not used: the examples and DefaultSettings start at 10.66.4.0/22
				v4, v6, err := awg.SubnetsFor(n)
				if err != nil {
					return nil, s.internal("network slot", err)
				}
				var try []netip.Prefix
				if !has4 {
					try = append(try, netip.MustParsePrefix(v4))
				}
				if !has6 {
					try = append(try, netip.MustParsePrefix(v6))
				}
				if _, clash := overlaps(try, others); !clash {
					slot = n
				}
			}
			if slot < 0 {
				return nil, failed("no free client network left (10.66.0.0/16 holds 63 AmneziaWG profiles)")
			}
			v4, v6, _ := awg.SubnetsFor(slot)
			doc := map[string]json.RawMessage{}
			if err := json.Unmarshal(merged, &doc); err != nil {
				return nil, s.internal("settings", err)
			}
			if !has4 {
				doc["subnet4"], _ = json.Marshal(v4)
				nets.Subnet4 = v4
			}
			if !has6 {
				doc["subnet6"], _ = json.Marshal(v6)
				nets.Subnet6 = v6
			}
			if merged, err = json.Marshal(doc); err != nil {
				return nil, s.internal("settings", err)
			}
		}
	} else if was := networksOf(old); was != nets {
		in, err := s.st.Access().InboundsOfProfile(ctx, profileID)
		if err != nil {
			return nil, s.internal("profile inbounds", err)
		}
		peers, err := s.st.Access().ProfileHasPeers(ctx, profileID)
		if err != nil {
			return nil, s.internal("profile peers", err)
		}
		if len(in) > 0 || peers {
			var errs []protocols.FieldError
			if was.Subnet4 != nets.Subnet4 {
				errs = append(errs, protocols.FieldError{Pointer: "/subnet4", Code: "immutable", Message: "the client network cannot change once the profile is on a node or has devices"})
			}
			if was.Subnet6 != nets.Subnet6 {
				errs = append(errs, protocols.FieldError{Pointer: "/subnet6", Code: "immutable", Message: "the client network cannot change once the profile is on a node or has devices"})
			}
			return nil, fieldErrors(errs)
		}
	}
	var errs []protocols.FieldError
	for ptr, val := range map[string]string{"/subnet4": nets.Subnet4, "/subnet6": nets.Subnet6} {
		p, err := netip.ParsePrefix(val)
		if err != nil {
			continue // the plugin's validator already judged the value
		}
		if name, clash := overlaps([]netip.Prefix{p}, others); clash {
			errs = append(errs, protocols.FieldError{Pointer: ptr, Code: "overlap", Message: fmt.Sprintf("overlaps the network of profile %q", name)})
		}
	}
	if len(errs) > 0 {
		slices.SortFunc(errs, func(a, b protocols.FieldError) int { return strings.Compare(a.Pointer, b.Pointer) })
		return nil, fieldErrors(errs)
	}
	return merged, nil
}
