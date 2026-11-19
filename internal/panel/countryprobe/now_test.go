//go:build !js

package countryprobe

import (
	"context"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/fleet"
)

// "Check now" of a node: the probe node tests that one node, stores its row and leaves the newest run's status, the batch rotation
// and the schedule alone.
func TestNowPlanTestsOneNodeAndLeavesTheScheduleAlone(t *testing.T) {
	e := newEnv(t)
	e.enable()
	if !e.tick() {
		t.Fatal("the scheduled run did not happen")
	}
	before := e.lastRun()
	rowBefore := e.rows()["de1"]
	sentBefore := len(e.ff.sent())
	e.advance(30 * time.Minute)

	run, skip := e.svc.NowPlan(context.Background(), "de1")
	if run == nil || skip != "" {
		t.Fatalf("plan = %v %q", run != nil, skip)
	}
	// the run lock is held from the plan until the run ends: a second plan and a scheduled tick find it busy
	if r2, why := e.svc.NowPlan(context.Background(), "nl1"); r2 != nil || why != "busy" {
		t.Fatalf("the second plan = %v %q, want busy", r2 != nil, why)
	}
	state, reason := run(context.Background())
	if state != "done" || reason != "" {
		t.Fatalf("run = %q %q", state, reason)
	}
	sent := e.ff.sent()[sentBefore:]
	if len(sent) != 1 || len(sent[0].req.Targets) != 1 || sent[0].req.Targets[0].Host != "203.0.113.10" {
		t.Fatalf("commands = %+v, want one for de1 alone", sent)
	}
	if !e.rows()["de1"].At.After(rowBefore.At) {
		t.Fatal("the row of the node was not renewed")
	}
	if got := e.lastRun(); got != before {
		t.Fatalf("the newest run changed: %+v, was %+v", got, before)
	}
	if e.changed < 2 {
		t.Fatalf("the alerts were told %d times", e.changed)
	}
	// the lock is let go: the next plan takes it
	if r3, why := e.svc.NowPlan(context.Background(), "nl1"); r3 == nil || why != "" {
		t.Fatalf("after the run the plan = %v %q", r3 != nil, why)
	}
}

func TestNowPlanSaysWhyNot(t *testing.T) {
	ctx := context.Background()
	plan := func(e *env, node string) string {
		t.Helper()
		run, skip := e.svc.NowPlan(ctx, node)
		if run != nil {
			if skip != "" {
				t.Fatalf("both a run and %q", skip)
			}
			run(ctx) // lets the lock go
			return ""
		}
		return skip
	}
	e := newEnv(t)
	if got := plan(e, "de1"); got != "off" {
		t.Fatalf("off source = %q", got)
	}
	e.enable()
	if got := plan(e, "probe1"); got != "probe_node" {
		t.Fatalf("the probe node itself = %q", got)
	}
	if got := plan(e, "de2"); got != "node_offline" {
		t.Fatalf("an offline node = %q", got)
	}
	if got := plan(e, "nod_none"); got != "node_offline" {
		t.Fatalf("an unknown node = %q", got)
	}
	e.setNodes(func(ns []Node) []Node { ns[2].Ports = nil; return ns })
	if got := plan(e, "nl1"); got != "no_ports" {
		t.Fatalf("a node without a TCP port = %q", got)
	}
	e.setNodes(func(ns []Node) []Node { ns[2].Ports = []uint16{443}; ns[2].Address = ns[0].Address; return ns })
	if got := plan(e, "nl1"); got != "no_address" {
		t.Fatalf("a node on the probe node's address = %q", got)
	}
	if got := plan(e, "de1"); got != "" {
		t.Fatalf("a node that can be tested = %q", got)
	}
	e.setNodes(func(ns []Node) []Node { ns[0].Online = false; return ns })
	if got := plan(e, "de1"); got != "no_probe" {
		t.Fatalf("the probe node offline = %q", got)
	}
	e.setNodes(func(ns []Node) []Node { ns[0].Online = true; return ns })
	e.country = "BY"
	if got := plan(e, "de1"); got != "off" {
		t.Fatalf("another country = %q", got)
	}
	edge := newEnv(t, func(e *env) { e.edge = true })
	if got := plan(edge, "de1"); got != "off" {
		t.Fatalf("the edge = %q", got)
	}
}

func TestNowPlanRunStatusIsTheReasonWhenNotOK(t *testing.T) {
	e := newEnv(t)
	e.enable()
	e.ff.script = func(int, call) (fleet.CountryProbeResult, error) {
		return fleet.CountryProbeResult{Code: "control_failed"}, nil
	}
	before := e.lastRun()
	run, skip := e.svc.NowPlan(context.Background(), "de1")
	if run == nil {
		t.Fatal(skip)
	}
	if state, reason := run(context.Background()); state != "failed" || reason != StatusControlFailed {
		t.Fatalf("run = %q %q", state, reason)
	}
	if len(e.rows()) != 0 || e.lastRun() != before {
		t.Fatal("a failed run left rows or a status")
	}
}
