package update

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// An agent writes "manual" into its last_update for every RollbackAgent it gets: it cannot tell the owner's button
// from a rollout's gate. The panel can, from its own records, and GetUpdates names the sender, so a rollback the
// gate made never reads "rolled back by you" (ops-11).

// rollbackWindow is how far apart the agent's record of a rollback and the panel's record of sending it may be (the
// agent answers late, its clock is a little off) and still be the same rollback.
const rollbackWindow = time.Hour

// Reasons the panel puts in place of the agent's "manual".
const (
	reasonGatePrefix = "gate_"   // + the gate's step error code: the rollout rolled the node back
	reasonCommand    = "command" // neither the gate nor the owner on record (an old record, a pruned rollout)
)

// rollbackSenders is what the panel knows about the RollbackAgent commands it sent.
type rollbackSenders struct {
	gate  map[string]store.GateRollback // node id -> the newest rollback by a rollout's gate
	owner map[string]time.Time          // node id -> the newest RollbackNode (audit update_rollback_node)
}

func (s *Service) rollbackSenders(ctx context.Context) (rollbackSenders, error) {
	gate, err := s.st.GateRollbacks(ctx)
	if err != nil {
		return rollbackSenders{}, err
	}
	owner, err := s.st.LastAuditByNode(ctx, "update_rollback_node")
	if err != nil {
		return rollbackSenders{}, err
	}
	return rollbackSenders{gate: gate, owner: owner}, nil
}

// reason is the reason the page shows for a node's last update: the agent's own code, unless it is the "manual" of
// a RollbackAgent, which is replaced by who sent it.
func (r rollbackSenders) reason(nodeID string, lu store.LastUpdateRow) string {
	if lu.Outcome != "rolled_back" || lu.Reason != errManual {
		return lu.Reason
	}
	near := func(t time.Time) bool {
		return !t.IsZero() && (lu.AtUnix == 0 || t.Sub(time.Unix(lu.AtUnix, 0)).Abs() <= rollbackWindow)
	}
	g, byGate := r.gate[nodeID]
	byGate = byGate && near(g.At) && (lu.ToBuilt == 0 || g.ToBuilt == lu.ToBuilt)
	o, byOwner := r.owner[nodeID]
	byOwner = byOwner && near(o)
	switch {
	case byGate && (!byOwner || !g.At.Before(o)):
		return reasonGatePrefix + g.ErrorKey
	case byOwner:
		return errManual
	}
	return reasonCommand
}
