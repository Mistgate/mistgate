package update

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func TestRecordProgressRejectsStaleAndRegressingFrames(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now()
	if _, err := e.st.W.ExecContext(context.Background(), `INSERT INTO update_rollout
		(id, status, to_version, to_built, manifest, signature, batch_size, created_at)
		VALUES ('rol_progress', 'running', '0.2.0', 2, X'', X'', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.ExecContext(context.Background(), `INSERT INTO update_step
		(rollout_id, node_id, node_name, stage, state, request_id, sent_at)
		VALUES ('rol_progress', 'nod_a', 'node-a', 0, 'sent', 'req_current', ?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	e.s.beginProgress("nod_a", "req_current", now)

	for _, frame := range []*agentv1.UpdateProgress{
		{RequestId: "req_old", Phase: "downloading", BytesDownloaded: 99, BytesTotal: 100, UpdatedUnix: now.Add(time.Second).Unix()},
		{RequestId: "req_current", Phase: "unknown", BytesDownloaded: 99, BytesTotal: 100, UpdatedUnix: now.Add(time.Second).Unix()},
		{RequestId: "req_current", Phase: "downloading", BytesDownloaded: 101, BytesTotal: 100, UpdatedUnix: now.Add(time.Second).Unix()},
	} {
		if err := e.s.RecordProgress(e.ctx, "nod_a", frame); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.s.progress) != 0 {
		t.Fatalf("invalid progress was recorded: %+v", e.s.progress)
	}
	if err := e.s.RecordProgress(e.ctx, "nod_a", &agentv1.UpdateProgress{
		RequestId: "req_current", Phase: "accepted", UpdatedUnix: now.Add(-2 * time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := e.s.RecordProgress(e.ctx, "nod_a", &agentv1.UpdateProgress{
		RequestId: "req_current", Phase: "downloading", BytesDownloaded: 40, BytesTotal: 100, UpdatedUnix: now.Add(time.Second).Unix(), Source: "panel",
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RecordProgress(e.ctx, "nod_a", &agentv1.UpdateProgress{
		RequestId: "req_current", Phase: "downloading", BytesDownloaded: 20, BytesTotal: 100, UpdatedUnix: now.Add(2 * time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.s.progress["nod_a"].Downloaded; got != 40 {
		t.Fatalf("progress regressed to %d, want 40", got)
	}
	if err := e.s.RecordProgress(e.ctx, "nod_a", &agentv1.UpdateProgress{
		RequestId: "req_current", Phase: "accepted", BytesDownloaded: 40, BytesTotal: 100, UpdatedUnix: now.Add(time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.s.progress["nod_a"].Phase; got != "downloading" {
		t.Fatalf("progress phase regressed to %q, want downloading", got)
	}
	if err := e.s.RecordProgress(e.ctx, "nod_a", &agentv1.UpdateProgress{
		RequestId: "req_current", Phase: "downloading", BytesDownloaded: 40, BytesTotal: 200, UpdatedUnix: now.Add(4 * time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.s.progress["nod_a"].Total; got != 100 {
		t.Fatalf("progress total changed to %d, want 100", got)
	}
	if err := e.s.RecordProgress(e.ctx, "nod_a", &agentv1.UpdateProgress{
		RequestId: "req_current", Phase: "failed", UpdatedUnix: now.Add(5 * time.Second).Unix(), Error: "download_failed", Source: "github",
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.s.progress["nod_a"]; got.Phase != "failed" || got.Downloaded != 40 || got.Total != 100 {
		t.Fatalf("terminal progress = %+v, want failed with preserved counters", got)
	}
	steps, err := e.st.RolloutSteps(e.ctx, "rol_progress")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].ProgressPhase != "failed" || steps[0].ProgressDownloaded != 40 || steps[0].ProgressTotal != 100 || steps[0].ProgressSource != "github" {
		t.Fatalf("persisted progress = %+v", steps)
	}

	e.s.clearProgress("nod_a")
	if _, err := e.st.W.ExecContext(context.Background(), `UPDATE update_step SET state = 'passed' WHERE node_id = 'nod_a'`); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RecordProgress(e.ctx, "nod_a", &agentv1.UpdateProgress{
		RequestId: "req_current", Phase: "completed", BytesDownloaded: 100, BytesTotal: 100, UpdatedUnix: now.Add(3 * time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if len(e.s.progress) != 0 {
		t.Fatalf("progress after step cleanup was recorded: %+v", e.s.progress)
	}
}

func TestGetUpdatesProjectsDownloadSourceForRunningStep(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	nodeID := e.addNode("progress-source", nodeOpts{})
	hold, onWire := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer func() {
		release.Do(func() { close(hold) })
		e.s.wg.Wait()
	}()
	e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
		close(onWire)
		<-hold
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("test command stopped"))
	}
	e.startRollout(nodeID)
	e.s.tick(e.ctx) // deliberately don't wait: the step must still be running below
	<-onWire
	step := e.step(nodeID)
	if step.State != "sent" {
		t.Fatalf("step state = %q, want sent", step.State)
	}
	if err := e.s.RecordProgress(e.ctx, nodeID, &agentv1.UpdateProgress{
		RequestId: step.RequestID, Phase: "downloading", BytesDownloaded: 1, BytesTotal: 10,
		UpdatedUnix: e.clk.Now().Add(time.Second).Unix(), Source: "github",
	}); err != nil {
		t.Fatal(err)
	}
	response := e.get()
	if response.GetRollout() == nil || len(response.GetRollout().GetSteps()) != 1 || response.GetRollout().GetSteps()[0].GetDownloadSource() != "github" {
		t.Fatalf("GetUpdates rollout step = %+v, want download_source=github", response.GetRollout())
	}
	release.Do(func() { close(hold) })
	e.s.wg.Wait()
}
