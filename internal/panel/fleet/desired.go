package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// Hello.capabilities strings of the L3 additions (agent.proto "AWG AND WARP"). The panel puts an awg inbound or a
// WarpSpec into the desired state of a stream ONLY when the stream listed the string.
const (
	capAWG  = "awg/1"
	capWarp = "warp/1"
)

// protocolAWG is the plugin id of AmneziaWG. An inbound that has a Tunnel is an L3 inbound whatever the protocol.
const protocolAWG = "awg"

// needsAWG says whether the agent must list "awg/1" to take this inbound.
func needsAWG(s plugin.InboundSpec) bool { return s.Protocol == protocolAWG || !s.Tunnel.IsZero() }

// inboundState is one inbound as the node must hold it.
type inboundState struct {
	spec     plugin.InboundSpec
	specHash string
	creds    []plugin.UserCred // sorted by CredID
}

// nodeState is the complete desired state of a node AS ONE STREAM CAN TAKE IT: what that stream's agent has no
// capability for is not in it (so its hash is the hash that agent computes).
type nodeState struct {
	in   map[string]*inboundState
	warp *plugin.WarpSpec // the node's WARP configuration; nil = none
	hash string           // statehash.StateWarp of the whole
	// withheld are the inbounds the agent cannot take (an awg inbound for an agent without "awg/1"); the node page says why.
	withheld []string
}

func (s *nodeState) ids() []string {
	ids := make([]string, 0, len(s.in))
	for id := range s.in {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// buildState computes the desired state of a node for an agent with these capabilities. A retired node has none. The
// inbounds and their credentials come from Config.Desired (the access module owns the effective-access rule); the WarpSpec
// from the WARP module. An awg inbound goes only to an agent that lists "awg/1" and the WarpSpec only to one that lists
// "warp/1": an old agent would reject the unknown protocol, and the whole state with it.
func (f *Fleet) buildState(ctx context.Context, n store.NodeRow, caps []string) (*nodeState, error) {
	st := &nodeState{in: map[string]*inboundState{}}
	if n.State != "retired" {
		list, err := f.cfg.Desired(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		for _, x := range list {
			if needsAWG(x.Spec) && !slices.Contains(caps, capAWG) {
				st.withheld = append(st.withheld, x.Spec.ID)
				continue
			}
			creds := append([]plugin.UserCred(nil), x.Creds...)
			sort.Slice(creds, func(i, j int) bool { return creds[i].CredID < creds[j].CredID })
			st.in[x.Spec.ID] = &inboundState{spec: x.Spec, specHash: statehash.Spec(x.Spec), creds: creds}
		}
		sort.Strings(st.withheld)
		if w := f.warpModule(); w != nil && slices.Contains(caps, capWarp) {
			if st.warp, err = w.Spec(ctx, n.ID); err != nil {
				return nil, err
			}
		}
	}
	st.hash = statehash.StateWarp(st.list(), st.warp)
	return st, nil
}

// prepareDesiredState reads the node and builds its desired state without touching session state.
func (f *Fleet) prepareDesiredState(ctx context.Context, nodeID string, caps []string) (*preparedDesiredState, error) {
	node, err := f.st.Node(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	prepared := &preparedDesiredState{node: node}
	if node.State != "retired" {
		prepared.desired, err = f.buildState(ctx, node, caps)
		if err != nil {
			return nil, err
		}
	}
	return prepared, nil
}

func (s *nodeState) list() []statehash.Inbound {
	out := make([]statehash.Inbound, 0, len(s.in))
	for _, id := range s.ids() {
		out = append(out, statehash.Inbound{Spec: s.in[id].spec, Creds: s.in[id].creds})
	}
	return out
}

// buildSpec turns an inbound row into the node-side spec through the protocol plugin. The framework owns
// the identity fields: the plugin does not know the inbound id.
func (f *Fleet) buildSpec(n store.NodeRow, row store.FleetInboundRow) (plugin.InboundSpec, error) {
	p, ok := f.reg.Get(row.Protocol)
	if !ok {
		return plugin.InboundSpec{}, fmt.Errorf("protocol %q is not available on this panel", row.Protocol)
	}
	settings, err := f.mergedSettings(row)
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	var state []byte
	if len(row.PluginStateEnc) > 0 { // the key material a plugin made for this inbound (AWG: the server key pair)
		if state, err = f.v.Open(row.PluginStateEnc, row.ID); err != nil {
			return plugin.InboundSpec{}, fmt.Errorf("inbound %s key material: %w", row.ID, err)
		}
	}
	spec, err := p.BuildInbound(protocols.InboundInput{
		Profile:               protocols.ProfileView{ID: row.ProfileID, Version: row.ProfileVersion, Settings: settings},
		Node:                  protocols.NodeView{ID: n.ID, Name: n.Name, Address: n.Address, CountryCode: n.CountryCode},
		PortOverride:          row.PortOverride,
		TLSServerNameOverride: row.TLSNameOverride,
		SpecVersion:           row.SpecVersion,
		Enabled:               true,
		InboundID:             row.ID,
		PluginState:           state,
		PluginPublic:          json.RawMessage(row.PluginPublic),
	})
	if err != nil {
		return plugin.InboundSpec{}, err
	}
	spec.ID, spec.Protocol, spec.ProfileID, spec.Version, spec.Enabled = row.ID, row.Protocol, row.ProfileID, row.SpecVersion, true
	return spec, nil
}

// mergedSettings puts the vault-held "x-secret" values ({json-pointer: string}) back into the profile
// settings. The output has sorted keys, so the spec hash stays stable across rebuilds.
func (f *Fleet) mergedSettings(row store.FleetInboundRow) (json.RawMessage, error) {
	if len(row.SecretsEnc) == 0 {
		return json.RawMessage(row.Settings), nil
	}
	raw, err := f.v.Open(row.SecretsEnc, row.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("profile %s secrets: %w", row.ProfileID, err)
	}
	secrets := map[string]string{}
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return nil, fmt.Errorf("profile %s secrets: %w", row.ProfileID, err)
	}
	return protocols.MergeSecrets(json.RawMessage(row.Settings), secrets)
}

// --- wire conversion ---

func specProto(s plugin.InboundSpec) *agentv1.InboundSpec {
	out := &agentv1.InboundSpec{
		InboundId: s.ID, Protocol: s.Protocol, ProfileId: s.ProfileID, SpecVersion: s.Version, Enabled: s.Enabled,
		Listen: &agentv1.Listen{Network: s.Listen.Network, Port: uint32(s.Listen.Port),
			HopFrom: uint32(s.Listen.HopFrom), HopTo: uint32(s.Listen.HopTo)},
		Tls:          &agentv1.Tls{Mode: agentv1.TlsMode(s.TLS.Mode), ServerName: s.TLS.ServerName},
		Egress:       s.Egress,
		SettingsJson: string(s.Settings),
	}
	if !s.Tunnel.IsZero() {
		out.Tunnel = &agentv1.Tunnel{Mtu: uint32(s.Tunnel.MTU)}
		if s.Tunnel.AddrV4.IsValid() {
			out.Tunnel.AddrV4 = s.Tunnel.AddrV4.String()
		}
		if s.Tunnel.AddrV6.IsValid() {
			out.Tunnel.AddrV6 = s.Tunnel.AddrV6.String()
		}
	}
	return out
}

// warpProto is the wire form of the WARP configuration (it carries the private key: mutual TLS only, never logged).
func warpProto(w *plugin.WarpSpec) *agentv1.WarpSpec {
	if w == nil {
		return nil
	}
	out := &agentv1.WarpSpec{
		Enabled: w.Enabled, PrivateKey: w.PrivateKey, PeerPublicKey: w.PeerPublicKey, EndpointV4: w.EndpointV4, EndpointV6: w.EndpointV6,
		AddressV4: w.AddressV4, AddressV6: w.AddressV6, Mtu: uint32(w.MTU), Reserved: append([]byte(nil), w.Reserved...), Backend: w.Backend,
	}
	for _, p := range w.Ports {
		out.Ports = append(out.Ports, uint32(p))
	}
	return out
}

func sameWarp(a, b *plugin.WarpSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(*a, *b)
}

func credProto(c plugin.UserCred) *agentv1.Credential {
	var vu int64
	if !c.ValidUntil.IsZero() {
		vu = c.ValidUntil.Unix()
	}
	return &agentv1.Credential{CredId: c.CredID, UserId: c.UserID, DeviceId: c.DeviceID, DataJson: string(c.Data),
		RateLimitBps: c.RateLimitBps, ValidUntilUnix: vu}
}

func credsProto(cs []plugin.UserCred) []*agentv1.Credential {
	out := make([]*agentv1.Credential, len(cs))
	for i, c := range cs {
		out[i] = credProto(c)
	}
	return out
}

// fullInbounds lists every inbound with spec and the complete credential set.
func fullInbounds(s *nodeState) []*agentv1.InboundState {
	out := make([]*agentv1.InboundState, 0, len(s.in))
	for _, id := range s.ids() {
		is := s.in[id]
		out = append(out, &agentv1.InboundState{InboundId: id, Spec: specProto(is.spec), CredsReplace: true, Creds: credsProto(is.creds)})
	}
	return out
}

func sameCred(a, b plugin.UserCred) bool {
	return a.CredID == b.CredID && a.UserID == b.UserID && a.DeviceID == b.DeviceID &&
		bytes.Equal(a.Data, b.Data) && a.RateLimitBps == b.RateLimitBps && a.ValidUntil.Equal(b.ValidUntil)
}

// diffState returns the delta that turns old into next: new or re-specced inbounds are sent whole, inbounds
// with an unchanged spec only as credential upserts/removals (the agent applies those without a restart).
func diffState(old, next *nodeState) (changed []*agentv1.InboundState, removed []string) {
	for _, id := range next.ids() {
		n := next.in[id]
		o, ok := old.in[id]
		if !ok || o.specHash != n.specHash {
			changed = append(changed, &agentv1.InboundState{InboundId: id, Spec: specProto(n.spec), CredsReplace: true, Creds: credsProto(n.creds)})
			continue
		}
		oldByID := make(map[string]plugin.UserCred, len(o.creds))
		for _, c := range o.creds {
			oldByID[c.CredID] = c
		}
		var up []*agentv1.Credential
		seen := make(map[string]bool, len(n.creds))
		for _, c := range n.creds {
			seen[c.CredID] = true
			if oc, ok := oldByID[c.CredID]; !ok || !sameCred(oc, c) {
				up = append(up, credProto(c))
			}
		}
		var rm []string
		for _, c := range o.creds {
			if !seen[c.CredID] {
				rm = append(rm, c.CredID)
			}
		}
		if len(up) > 0 || len(rm) > 0 {
			changed = append(changed, &agentv1.InboundState{InboundId: id, Creds: up, RemovedCredIds: rm})
		}
	}
	for _, id := range old.ids() {
		if _, ok := next.in[id]; !ok {
			removed = append(removed, id)
		}
	}
	return changed, removed
}
