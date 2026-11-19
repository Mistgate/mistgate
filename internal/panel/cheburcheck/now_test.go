//go:build !js

package cheburcheck

import (
	"context"
	"net/http"
	"testing"
	"time"
)

const okStream = "event: started\ndata: {\"online_probes\":3}\n\n" +
	"event: result\ndata: {\"probe_id\":17,\"region\":\"Example Region\",\"provider\":\"Example ISP\",\"asn\":\"AS64496\",\"verdicts\":[\"ok\"]}\n\n" +
	"event: result\ndata: {\"probe_id\":18,\"region\":\"Sample Region\",\"provider\":\"Sample Network\",\"asn\":64497,\"verdicts\":[\"ok\"]}\n\n" +
	"event: done\ndata: {\"status\":\"done\",\"online_probes\":2}\n\n"

// "Check now" of a node checks it on the source out of turn, whatever the interval says, and stores the result like a scheduled one.
func TestNowPlanChecksTheNodeBeforeItsIntervalEnds(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{body: okStream}, {body: okStream}}
	e.enablePublic()
	id := e.addNode("de1", "203.0.113.10")
	e.addNode("nl1", "203.0.113.20")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.advance(31 * time.Second) // the site's spacing
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if len(before) != 2 {
		t.Fatalf("scheduled rows = %d", len(before))
	}
	e.advance(31 * time.Second) // well within the 180-minute interval
	run, skip := e.svc.NowPlan(context.Background(), id, SourcePublic)
	if run == nil || skip != "" {
		t.Fatalf("plan = %v %q", run != nil, skip)
	}
	e.fake.probeResponses = []fakeReply{{body: okStream}}
	state, reason := run(context.Background())
	if state != "done" || reason != "" {
		t.Fatalf("run = %q %q", state, reason)
	}
	after, _ := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	var at, was int64
	for _, r := range after {
		if r.NodeID == id {
			at = r.At.Unix()
		}
	}
	for _, r := range before {
		if r.NodeID == id {
			was = r.At.Unix()
		}
	}
	if at <= was {
		t.Fatalf("the node's row was not renewed: %d, was %d", at, was)
	}
}

func TestNowPlanSaysWhyNot(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, 0)
	id := e.addNode("de1", "203.0.113.10")
	if _, skip := e.svc.NowPlan(ctx, id, SourcePublic); skip != "off" {
		t.Fatalf("off source = %q", skip)
	}
	e.enablePublic()
	if _, skip := e.svc.NowPlan(ctx, id, SourceOwn); skip != "off" {
		t.Fatalf("the token lane without a token = %q", skip)
	}
	if _, skip := e.svc.NowPlan(ctx, "nod_none", SourcePublic); skip != "node_offline" {
		t.Fatalf("an unknown node = %q", skip)
	}
	private := e.addNode("pv1", "10.0.0.5")
	if _, skip := e.svc.NowPlan(ctx, private, SourcePublic); skip != "no_address" {
		t.Fatalf("a private address = %q", skip)
	}
	if _, skip := e.svc.NowPlan(ctx, id, "other"); skip != "off" {
		t.Fatalf("an unknown source = %q", skip)
	}
	// a hold after a rate limit is kept: no call goes to the site
	e.fake.probeResponses = []fakeReply{{status: http.StatusTooManyRequests}}
	if err := e.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	calls := len(e.fake.snapshot())
	if run, skip := e.svc.NowPlan(ctx, id, SourcePublic); run != nil || skip != "held" {
		t.Fatalf("during the hold = %v %q", run != nil, skip)
	}
	if len(e.fake.snapshot()) != calls {
		t.Fatal("a call was made during the hold")
	}
	e.setCountry("BY")
	e.advance(time.Hour)
	if _, skip := e.svc.NowPlan(ctx, id, SourcePublic); skip != "off" {
		t.Fatalf("another country = %q", skip)
	}
}

func TestNowPlanReportsAFailedCheck(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{body: "event: started\ndata: {\"online_probes\":0}\n\nevent: done\ndata: {\"status\":\"done\",\"online_probes\":0}\n\n"}}
	e.enablePublic()
	id := e.addNode("de1", "203.0.113.10")
	run, skip := e.svc.NowPlan(context.Background(), id, SourcePublic)
	if run == nil {
		t.Fatal(skip)
	}
	if state, reason := run(context.Background()); state != "failed" || reason != "no_probe" {
		t.Fatalf("a check that found no probe = %q %q", state, reason)
	}
}
