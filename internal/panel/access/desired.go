package access

import (
	"context"
	"encoding/json"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// NodeInbound is one inbound of a node's desired state: the spec and the complete credential set.
type NodeInbound struct {
	Spec  plugin.InboundSpec
	Creds []plugin.UserCred
}

// Desired computes what a node should run: its enabled inbounds and, for each, the credentials allowed by
// the effective-access rule written at the top of user.proto:
//
//	user ACTIVE AND profile in the user's group AND (all nodes OR node selected)
//	AND one of the user's enabled apps consumes the protocol AND the inbound is enabled.
//
// The fleet module turns the result into the agent's DesiredState. An inbound whose settings cannot be
// built (corrupt profile) is left out rather than blocking every other inbound of the node, and is marked
// failed with the reason in last_error (buildFailed), so the node page shows why it is not running.
func (s *Service) Desired(ctx context.Context, nodeID string) ([]NodeInbound, error) {
	a := s.st.Access()
	full, err := a.InboundsFull(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	var out []NodeInbound
	at := map[string]int{}
	for _, f := range full {
		if !f.Inbound.Enabled {
			continue
		}
		merged, err := s.mergedSettings(f.Profile)
		if err != nil {
			s.log.Error("access: cannot open profile secrets", "profile", f.Profile.ID, "err", err)
			s.buildFailed(ctx, f, err)
			continue
		}
		spec, err := s.buildSpec(f, merged)
		if err != nil {
			s.log.Error("access: cannot build inbound", "inbound", f.Inbound.ID, "err", err)
			s.buildFailed(ctx, f, err)
			continue
		}
		at[f.Inbound.ID] = len(out)
		out = append(out, NodeInbound{Spec: spec, Creds: []plugin.UserCred{}})
	}
	if len(out) == 0 {
		return out, nil
	}
	rows, err := a.DesiredCreds(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		i, ok := at[r.InboundID]
		if !ok || !s.allowed(r.Protocol, r.AppHapp, r.AppAmnezia) {
			continue
		}
		out[i].Creds = append(out[i].Creds, plugin.UserCred{
			CredID: r.CredID, UserID: r.UserID, DeviceID: r.DeviceID, Data: json.RawMessage(r.DataJSON),
			RateLimitBps: r.SpeedLimitBps, ValidUntil: r.ExpiresAt,
		})
	}
	return out, nil
}

// buildFailed shows a build error on the inbound row (state failed, last_error). Best effort: a failed
// write is logged and must not stop the desired state from being computed.
func (s *Service) buildFailed(ctx context.Context, f store.AccessInboundFull, err error) {
	msg := "cannot build inbound: " + err.Error()
	if f.Inbound.State == "failed" && f.Inbound.LastError == store.Clip(msg, 512) {
		return // already recorded: no write
	}
	if werr := s.st.Access().SetInboundBuildError(ctx, f.Inbound.ID, msg, s.now()); werr != nil {
		s.log.Warn("access: cannot record inbound build error", "inbound", f.Inbound.ID, "err", werr)
	}
}
