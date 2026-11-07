package update

import (
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func TestRecordProgressRejectsStaleAndRegressingFrames(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now()
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
		RequestId: "req_current", Phase: "downloading", BytesDownloaded: 40, BytesTotal: 100, UpdatedUnix: now.Add(time.Second).Unix(),
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
		RequestId: "req_current", Phase: "failed", UpdatedUnix: now.Add(5 * time.Second).Unix(), Error: "download_failed",
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.s.progress["nod_a"]; got.Phase != "failed" || got.Downloaded != 40 || got.Total != 100 {
		t.Fatalf("terminal progress = %+v, want failed with preserved counters", got)
	}

	e.s.clearProgress("nod_a")
	if err := e.s.RecordProgress(e.ctx, "nod_a", &agentv1.UpdateProgress{
		RequestId: "req_current", Phase: "completed", BytesDownloaded: 100, BytesTotal: 100, UpdatedUnix: now.Add(3 * time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if len(e.s.progress) != 0 {
		t.Fatalf("progress after step cleanup was recorded: %+v", e.s.progress)
	}
}
