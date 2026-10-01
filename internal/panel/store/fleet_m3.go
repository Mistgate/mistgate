package store

import (
	"context"
	"time"
)

// SetInboundAwgHealth keeps the last agent.v1.AwgHealth (protojson) a node reported for one of its inbounds, for the
// inbound view (Inbound.awg, with the report time for "stale" captions). The node id is part of the match: a node can
// only write its own inbounds. An inbound that is gone changes nothing.
func (s *Store) SetInboundAwgHealth(ctx context.Context, nodeID, inboundID, healthJSON string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE inbound SET awg_health_json = ?, awg_health_at = ? WHERE id = ? AND node_id = ?`,
		healthJSON, unix(at), inboundID, nodeID)
	return err
}

// FailWithheldInbounds marks inbounds the panel did not send to a node (its agent lacks a capability) as failed with the
// reason. The reconcile calls it, because an agent that is sent nothing answers nothing and the row would stay pending
// for ever. It writes only what differs, so a repeated call changes no row.
func (s *Store) FailWithheldInbounds(ctx context.Context, nodeID string, ids []string, reason string, now time.Time) error {
	for _, id := range ids {
		if _, err := s.W.ExecContext(ctx, `UPDATE inbound SET state = 'failed', last_error = ?, updated_at = ?
			WHERE id = ? AND node_id = ? AND enabled = 1 AND (state != 'failed' OR last_error != ?)`,
			reason, unix(now), id, nodeID, reason); err != nil {
			return err
		}
	}
	return nil
}
