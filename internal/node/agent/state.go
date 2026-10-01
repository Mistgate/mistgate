package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// model is the desired state as the agent holds it: per inbound the spec and the COMPLETE credential
// set. Deltas from the panel are merged into it here; engines always receive the whole set.
type model struct {
	revision uint64
	inbounds map[string]*inbound
	settings *pb.NodeSettings
	// warp is the node-level WARP configuration; nil = this node has no WARP, Enabled=false = paused. It is replaced
	// as a whole and never mutated once stored (agent.proto "AWG AND WARP").
	warp *plugin.WarpSpec
}

type inbound struct {
	spec  plugin.InboundSpec
	creds map[string]plugin.UserCred
}

var (
	errBaseMismatch = errors.New("base revision does not match the applied revision")
	errRejected     = errors.New("rejected")
)

func rejectf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errRejected, fmt.Sprintf(format, a...))
}

func newModel() *model { return &model{inbounds: map[string]*inbound{}} }

func (m *model) clone() *model {
	c := &model{revision: m.revision, settings: m.settings, warp: m.warp, inbounds: make(map[string]*inbound, len(m.inbounds))}
	for id, in := range m.inbounds {
		ci := &inbound{spec: in.spec, creds: make(map[string]plugin.UserCred, len(in.creds))}
		for k, v := range in.creds {
			ci.creds[k] = v
		}
		c.inbounds[id] = ci
	}
	return c
}

// merge applies a DesiredState on top of cur and returns the new model; cur is never modified, so a
// rejected message changes nothing. known says whether a protocol has an engine on this node.
func merge(cur *model, ds *pb.DesiredState, known func(protocol string) bool) (*model, error) {
	var next *model
	switch {
	case ds.BaseRevision == 0: // full snapshot: anything not listed is removed
		next = newModel()
	case ds.BaseRevision != cur.revision:
		return nil, errBaseMismatch
	default:
		next = cur.clone()
	}
	next.revision = ds.Revision
	if ds.Settings != nil {
		next.settings = ds.Settings
	} else if next.settings == nil {
		next.settings = cur.settings
	}
	full := ds.BaseRevision == 0
	// WARP: presence replaces the whole configuration (a delta too), absence in a delta keeps it, absence in a full
	// state means this node has no WARP (next started empty above).
	if ds.Warp != nil {
		w, err := warpFromPB(ds.Warp)
		if err != nil {
			return nil, err
		}
		next.warp = w
	} else if full {
		next.warp = nil
	}
	for _, id := range ds.RemovedInboundIds {
		delete(next.inbounds, id)
	}
	seen := map[string]bool{}
	for _, is := range ds.Inbounds {
		if is == nil || is.InboundId == "" {
			return nil, rejectf("inbound without id")
		}
		if seen[is.InboundId] {
			return nil, rejectf("inbound %s listed twice", is.InboundId)
		}
		seen[is.InboundId] = true
		in := next.inbounds[is.InboundId]
		if is.Spec != nil {
			spec, err := specFromPB(is.InboundId, is.Spec)
			if err != nil {
				return nil, err
			}
			if !known(spec.Protocol) {
				return nil, rejectf("inbound %s: no engine for protocol %q", is.InboundId, spec.Protocol)
			}
			if in == nil {
				in = &inbound{creds: map[string]plugin.UserCred{}}
				next.inbounds[is.InboundId] = in
			}
			in.spec = spec
		} else if in == nil {
			return nil, rejectf("inbound %s: new inbound without spec", is.InboundId)
		}
		if is.CredsReplace || full {
			in.creds = make(map[string]plugin.UserCred, len(is.Creds))
		}
		for _, id := range is.RemovedCredIds {
			delete(in.creds, id)
		}
		for _, c := range is.Creds {
			uc, err := credFromPB(c)
			if err != nil {
				return nil, rejectf("inbound %s: %v", is.InboundId, err)
			}
			in.creds[uc.CredID] = uc
		}
	}
	return next, nil
}

func specFromPB(id string, s *pb.InboundSpec) (plugin.InboundSpec, error) {
	if s.InboundId != "" && s.InboundId != id {
		return plugin.InboundSpec{}, rejectf("inbound %s: spec carries id %s", id, s.InboundId)
	}
	if s.Protocol == "" {
		return plugin.InboundSpec{}, rejectf("inbound %s: empty protocol", id)
	}
	l := s.Listen
	if l == nil {
		return plugin.InboundSpec{}, rejectf("inbound %s: no listen", id)
	}
	if l.Network != "udp" && l.Network != "tcp" {
		return plugin.InboundSpec{}, rejectf("inbound %s: network %q", id, l.Network)
	}
	if l.Port == 0 || l.Port > 65535 || l.HopFrom > 65535 || l.HopTo > 65535 {
		return plugin.InboundSpec{}, rejectf("inbound %s: port out of range", id)
	}
	if (l.HopFrom != 0 || l.HopTo != 0) && (l.HopFrom == 0 || l.HopTo < l.HopFrom) {
		return plugin.InboundSpec{}, rejectf("inbound %s: bad hop range %d-%d", id, l.HopFrom, l.HopTo)
	}
	sp := plugin.InboundSpec{
		ID: id, Protocol: s.Protocol, ProfileID: s.ProfileId, Version: s.SpecVersion, Enabled: s.Enabled,
		Listen: plugin.Listen{Network: l.Network, Port: uint16(l.Port), HopFrom: uint16(l.HopFrom), HopTo: uint16(l.HopTo)},
		Egress: s.Egress, Settings: json.RawMessage(s.SettingsJson),
	}
	if s.Tls != nil {
		if s.Tls.Mode < 0 || s.Tls.Mode > pb.TlsMode_TLS_MODE_SELF_SIGNED {
			return plugin.InboundSpec{}, rejectf("inbound %s: tls mode %d", id, s.Tls.Mode)
		}
		sp.TLS = plugin.TLS{Mode: plugin.TLSMode(s.Tls.Mode), ServerName: s.Tls.ServerName}
	}
	if s.Tunnel != nil {
		t, err := tunnelFromPB(s.Tunnel)
		if err != nil {
			return plugin.InboundSpec{}, rejectf("inbound %s: %v", id, err)
		}
		sp.Tunnel = t
	}
	return sp, nil
}

func credFromPB(c *pb.Credential) (plugin.UserCred, error) {
	if c == nil || c.CredId == "" {
		return plugin.UserCred{}, errors.New("credential without id")
	}
	uc := plugin.UserCred{
		CredID: c.CredId, UserID: c.UserId, DeviceID: c.DeviceId,
		Data: json.RawMessage(c.DataJson), RateLimitBps: c.RateLimitBps,
	}
	if c.ValidUntilUnix > 0 {
		uc.ValidUntil = time.Unix(c.ValidUntilUnix, 0)
	}
	return uc, nil
}

func specToPB(s plugin.InboundSpec) *pb.InboundSpec {
	ps := &pb.InboundSpec{
		InboundId: s.ID, Protocol: s.Protocol, ProfileId: s.ProfileID, SpecVersion: s.Version, Enabled: s.Enabled,
		Listen:       &pb.Listen{Network: s.Listen.Network, Port: uint32(s.Listen.Port), HopFrom: uint32(s.Listen.HopFrom), HopTo: uint32(s.Listen.HopTo)},
		Tls:          &pb.Tls{Mode: pb.TlsMode(s.TLS.Mode), ServerName: s.TLS.ServerName},
		Egress:       s.Egress,
		SettingsJson: string(s.Settings),
	}
	if !s.Tunnel.IsZero() {
		ps.Tunnel = tunnelToPB(s.Tunnel)
	}
	return ps
}

func credToPB(c plugin.UserCred) *pb.Credential {
	pc := &pb.Credential{CredId: c.CredID, UserId: c.UserID, DeviceId: c.DeviceID, DataJson: string(c.Data), RateLimitBps: c.RateLimitBps}
	if !c.ValidUntil.IsZero() {
		pc.ValidUntilUnix = c.ValidUntil.Unix()
	}
	return pc
}

// sortedCreds returns the credentials in a stable order (by id).
func (in *inbound) sortedCreds() []plugin.UserCred {
	out := make([]plugin.UserCred, 0, len(in.creds))
	for _, c := range in.creds {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CredID < out[j].CredID })
	return out
}

// active returns the credentials that are not expired at now (the agent drops a term itself when it ends).
func (in *inbound) active(now time.Time) []plugin.UserCred {
	all := in.sortedCreds()
	out := all[:0]
	for _, c := range all {
		if c.ValidUntil.IsZero() || c.ValidUntil.After(now) {
			out = append(out, c)
		}
	}
	return out
}

func (m *model) ids() []string {
	ids := make([]string, 0, len(m.inbounds))
	for id := range m.inbounds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// snapshot turns the model into a full DesiredState (the persisted form).
func (m *model) snapshot() *pb.DesiredState {
	ds := &pb.DesiredState{Revision: m.revision, Settings: m.settings, StateHash: m.hash(), Warp: warpToPB(m.warp)}
	for _, id := range m.ids() {
		in := m.inbounds[id]
		is := &pb.InboundState{InboundId: id, Spec: specToPB(in.spec), CredsReplace: true}
		for _, c := range in.sortedCreds() {
			is.Creds = append(is.Creds, credToPB(c))
		}
		ds.Inbounds = append(ds.Inbounds, is)
	}
	return ds
}

func saveState(dir string, m *model) error {
	b, err := protojson.Marshal(m.snapshot())
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, fileState), b, 0o600)
}

// loadState reads the persisted state; a missing or corrupt file yields an empty model (the panel will
// resend everything) and, for corruption, an error to log.
func loadState(dir string, known func(string) bool) (*model, error) {
	b, err := os.ReadFile(filepath.Join(dir, fileState))
	if errors.Is(err, os.ErrNotExist) {
		return newModel(), nil
	} else if err != nil {
		return newModel(), err
	}
	var ds pb.DesiredState
	if err := protojson.Unmarshal(b, &ds); err != nil {
		return newModel(), fmt.Errorf("%s: %w", fileState, err)
	}
	ds.BaseRevision = 0
	// A protocol whose engine is gone is skipped, not fatal: the other inbounds must come back.
	kept := ds.Inbounds[:0]
	for _, is := range ds.Inbounds {
		if is.Spec != nil && known(is.Spec.Protocol) {
			kept = append(kept, is)
		}
	}
	ds.Inbounds = kept
	m, err := merge(newModel(), &ds, known)
	if err != nil {
		return newModel(), fmt.Errorf("%s: %w", fileState, err)
	}
	return m, nil
}
