package update

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestEdgeAgentUpdateRPCsAreAvailable(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	e.s.cfg.StepUp = func(context.Context) error { return nil }
	r := rpc{e.s}

	rescan, err := r.RescanBundle(e.ctx, connect.NewRequest(&adminv1.RescanBundleRequest{}))
	if err != nil || rescan.Msg.Bundle.Status != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED {
		t.Fatalf("edge RescanBundle = %+v, %v", rescan, err)
	}

	id := edgeNode(e, "node-a", 0)
	start, err := r.StartRollout(e.ctx, connect.NewRequest(&adminv1.StartRolloutRequest{
		NodeIds: []string{id}, ExpectedVersion: edgeReleaseTag, ExpectedBuilt: newBuilt,
	}))
	if err != nil || start.Msg.Rollout == nil {
		t.Fatalf("edge StartRollout = %+v, %v", start, err)
	}
}

func TestEdgeStillRefusesOnlyPanelUpdateInstall(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	r := rpc{e.s}
	_, err := r.InstallPanelUpdate(e.ctx, connect.NewRequest(&adminv1.InstallPanelUpdateRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "unavailable_in_edition: feature=panel_update") {
		t.Fatalf("edge InstallPanelUpdate = %v, want the panel_update refusal", err)
	}
}

// deadlineProbe records the deadline of the context an update RPC hands to its first dependency.
type deadlineProbe struct {
	mu       sync.Mutex
	deadline time.Time
	seen     bool
}

func (d *deadlineProbe) note(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadline, d.seen = ctx.Deadline()
	d.seen = true
}

func (d *deadlineProbe) get() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	deadline, seen := d.deadline, d.seen
	d.deadline, d.seen = time.Time{}, false
	return deadline, seen
}

type probeFleet struct {
	*fakeFleet
	probe *deadlineProbe
}

func (f probeFleet) Live(ctx context.Context) ([]store.NodeLiveRow, error) {
	f.probe.note(ctx)
	return f.fakeFleet.Live(ctx)
}

type probeUpdater struct{ probe *deadlineProbe }

func (probeUpdater) Status() PanelUpdateStatus { return PanelUpdateStatus{} }
func (u probeUpdater) Check(ctx context.Context) PanelUpdateStatus {
	u.probe.note(ctx)
	return PanelUpdateStatus{}
}
func (probeUpdater) Install(context.Context, string, string) error { return nil }
func (probeUpdater) Run(context.Context)                           {}

// Every update RPC on the edge runs under a 20 s deadline of its own and none on the VPS. Each call is observed at the first
// dependency it reaches: the step-up check, the fleet read or the panel updater. InstallPanelUpdate is refused as an
// unavailable feature before it reaches any of them; TestEdgeStillRefusesOnlyPanelUpdateInstall covers that refusal.
func TestEdgeUpdateRPCsRunUnderTheDeadline(t *testing.T) {
	e := newEnv(t)
	probe := &deadlineProbe{}
	e.s.fl = probeFleet{e.fl, probe}
	e.s.cfg.PanelUpdater = probeUpdater{probe}
	e.s.cfg.StepUp = func(ctx context.Context) error {
		probe.note(ctx)
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	r := rpc{e.s}
	calls := map[string]func(context.Context) error{
		"GetUpdates": func(ctx context.Context) error {
			_, err := r.GetUpdates(ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
			return err
		},
		"CheckPanelUpdate": func(ctx context.Context) error {
			_, err := r.CheckPanelUpdate(ctx, connect.NewRequest(&adminv1.CheckPanelUpdateRequest{}))
			return err
		},
		"StartRollout": func(ctx context.Context) error {
			_, err := r.StartRollout(ctx, connect.NewRequest(&adminv1.StartRolloutRequest{}))
			return err
		},
		"PauseRollout": func(ctx context.Context) error {
			_, err := r.PauseRollout(ctx, connect.NewRequest(&adminv1.PauseRolloutRequest{}))
			return err
		},
		"ResumeRollout": func(ctx context.Context) error {
			_, err := r.ResumeRollout(ctx, connect.NewRequest(&adminv1.ResumeRolloutRequest{}))
			return err
		},
		"CancelRollout": func(ctx context.Context) error {
			_, err := r.CancelRollout(ctx, connect.NewRequest(&adminv1.CancelRolloutRequest{}))
			return err
		},
		"RollbackNode": func(ctx context.Context) error {
			_, err := r.RollbackNode(ctx, connect.NewRequest(&adminv1.RollbackNodeRequest{NodeId: "nod_a"}))
			return err
		},
		"RescanBundle": func(ctx context.Context) error {
			_, err := r.RescanBundle(ctx, connect.NewRequest(&adminv1.RescanBundleRequest{}))
			return err
		},
		"ScheduleNodeUpdate": func(ctx context.Context) error {
			_, err := r.ScheduleNodeUpdate(ctx, connect.NewRequest(&adminv1.ScheduleNodeUpdateRequest{NodeId: "nod_a"}))
			return err
		},
		"CancelNodeUpdateSchedule": func(ctx context.Context) error {
			_, err := r.CancelNodeUpdateSchedule(ctx, connect.NewRequest(&adminv1.CancelNodeUpdateScheduleRequest{NodeId: "nod_a"}))
			return err
		},
		"SetUpdateTimezone": func(ctx context.Context) error {
			_, err := r.SetUpdateTimezone(ctx, connect.NewRequest(&adminv1.SetUpdateTimezoneRequest{}))
			return err
		},
	}
	for _, edge := range []bool{true, false} {
		e.s.cfg.Edge = edge
		for name, call := range calls {
			started := time.Now()
			_ = call(context.Background())
			deadline, seen := probe.get()
			switch {
			case !seen:
				t.Errorf("%s (edge=%t) never reached its probe", name, edge)
			case !edge && !deadline.IsZero():
				t.Errorf("%s on the VPS gained an edge-only deadline", name)
			// The handler sets its deadline a moment after started; on Linux that is 20s plus microseconds, hence the slack.
			case edge && (deadline.IsZero() || deadline.Before(started) || deadline.Sub(started) > edgeAdminRPCTimeout+time.Second):
				t.Errorf("%s on the edge has deadline %v, want within %s of the call", name, deadline, edgeAdminRPCTimeout)
			}
		}
	}
}
