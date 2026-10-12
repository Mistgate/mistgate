package update

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The edge has no node bundle, rollout or panel self-update yet: the RPCs that start one are refused with the coded
// reason before the step-up is asked, and on the VPS they go on to their usual checks.
func TestEdgeRefusesUpdateRPCs(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("node-a", nodeOpts{})
	stepUps := 0
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	e.stepUp = func(context.Context) error { stepUps++; return denied }
	r := rpc{e.s}
	calls := map[string]struct {
		feature string
		call    func() error
	}{
		"InstallPanelUpdate": {"panel_update", func() error {
			_, err := r.InstallPanelUpdate(e.ctx, connect.NewRequest(&adminv1.InstallPanelUpdateRequest{}))
			return err
		}},
		"StartRollout": {"agent_updates", func() error {
			_, err := r.StartRollout(e.ctx, connect.NewRequest(&adminv1.StartRolloutRequest{}))
			return err
		}},
		"RollbackNode": {"agent_updates", func() error {
			_, err := r.RollbackNode(e.ctx, connect.NewRequest(&adminv1.RollbackNodeRequest{NodeId: a}))
			return err
		}},
		"RescanBundle": {"agent_updates", func() error {
			_, err := r.RescanBundle(e.ctx, connect.NewRequest(&adminv1.RescanBundleRequest{}))
			return err
		}},
		"ScheduleNodeUpdate": {"agent_updates", func() error {
			_, err := r.ScheduleNodeUpdate(e.ctx, connect.NewRequest(&adminv1.ScheduleNodeUpdateRequest{NodeId: a}))
			return err
		}},
	}

	e.s.cfg.Edge = true
	for name, c := range calls {
		err := c.call()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "unavailable_in_edition: feature="+c.feature) {
			t.Errorf("edge %s = %v, want the coded refusal for %s", name, err, c.feature)
		}
	}
	if stepUps != 0 {
		t.Errorf("a refused call asked for the step-up %d times", stepUps)
	}
	if _, err := r.GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{})); err != nil {
		t.Errorf("edge GetUpdates = %v, reads must keep working", err)
	}

	e.s.cfg.Edge = false
	for name, c := range calls {
		if err := c.call(); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("VPS %s = %v, want it to reach the step-up", name, err)
		}
	}
	e.stepUp = func(context.Context) error { return nil }
	bundle := e.s.current().manifest
	if _, err := r.StartRollout(e.ctx, connect.NewRequest(&adminv1.StartRolloutRequest{NodeIds: []string{a}, ExpectedVersion: bundle.Version, ExpectedBuilt: bundle.Built})); err != nil {
		t.Errorf("VPS StartRollout = %v", err)
	}
}
