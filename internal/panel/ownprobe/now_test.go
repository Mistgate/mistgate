//go:build !js

package ownprobe

import (
	"context"
	"testing"
	"time"
)

// "Check now" of a node makes the next job of every probe that is heard from due, and says so; a silent probe is not asked.
func TestNowPlanMakesTheJobsOfTheProbesHeardFromDue(t *testing.T) {
	f := probeFixture(t)
	f.enable(t)
	ctx := context.Background()
	if _, skip := f.NowPlan(ctx, "nod_de1"); skip != "no_probe" {
		t.Fatalf("with no probe = %q", skip)
	}
	heard, _ := f.probe(t, "prb_home", "home")
	silent, _ := f.probe(t, "prb_gone", "gone")
	// a job that is not due for an hour, for the probe that is heard from; the other has been silent for two hours
	for _, id := range []string{heard.ID, silent.ID} {
		if err := f.st.SetOwnProbeJob(ctx, id, testJobID, encodeJob(f.targets), f.now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := seen(f, heard.ID, f.now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seen(f, silent.ID, f.now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	run, skip := f.NowPlan(ctx, "nod_de1")
	if run == nil || skip != "" {
		t.Fatalf("plan = %v %q", run != nil, skip)
	}
	if state, reason := run(ctx); state != "queued" || reason != "" {
		t.Fatalf("run = %q %q", state, reason)
	}
	row, err := f.st.OwnProbe(ctx, heard.ID)
	if err != nil || row.JobID != "" || row.JobDue.After(f.now) {
		t.Fatalf("the probe's job = %+v, %v: not due now", row, err)
	}
	other, _ := f.st.OwnProbe(ctx, silent.ID)
	if !other.JobDue.Equal(f.now.Add(time.Hour)) || other.JobID != testJobID {
		t.Fatalf("the silent probe was asked: %+v", other)
	}
	// every probe silent: it says so
	if err := seen(f, heard.ID, f.now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if run, skip := f.NowPlan(ctx, "nod_de1"); run != nil || skip != "probe_silent" {
		t.Fatalf("all silent = %v %q", run != nil, skip)
	}
}

func seen(f *fixture, id string, at time.Time) error {
	_, err := f.st.W.ExecContext(context.Background(), `UPDATE own_probe SET last_seen = ? WHERE id = ?`, at.Unix(), id)
	return err
}

func TestNowPlanOffWhenTheSourceIsOff(t *testing.T) {
	f := probeFixture(t)
	f.probe(t, "prb_home", "home")
	if _, skip := f.NowPlan(context.Background(), "nod_de1"); skip != "off" {
		t.Fatalf("off = %q", skip)
	}
}
