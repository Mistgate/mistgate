package agent

import (
	"context"
	"strings"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/engine"
)

// The engine's fallback signal becomes one warning event with the two facts and no inbound, and nothing else leaves.
func TestAwgBackendFallbackEventIsForwardedOnce(t *testing.T) {
	x := newL3(t, 3, false)
	x.waitConnected()
	x.a.engineEvent(engine.Event{Code: awg.EventBackendFallback, Warning: true, Params: map[string]string{
		"reason": "the amneziawg kernel module is not loaded", "kernel": "7.0.0-38-generic", "secret": "never"}})
	if !x.panel.hasEventEventually(awg.EventBackendFallback) {
		t.Fatalf("events = %v", x.panel.eventCodes())
	}
	n := 0
	for _, c := range x.panel.eventCodes() {
		if c == awg.EventBackendFallback {
			n++
		}
	}
	evs := x.panel.eventsByCode(awg.EventBackendFallback)
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
	ev := evs[0]
	if n != 1 || ev.Severity != pb.Severity_SEVERITY_WARNING || ev.InboundId != "" ||
		ev.Params["kernel"] != "7.0.0-38-generic" || !strings.Contains(ev.Params["reason"], "not loaded") || ev.Params["secret"] != "" {
		t.Fatalf("count %d, event %+v", n, ev)
	}
}

// awg_module_repair on the worker: a module that is there drops the engine and every awg inbound is applied again; a module
// that is not there changes nothing.
func TestAwgModuleRepairAppliesTheInboundsAgain(t *testing.T) {
	x := newL3(t, 3, false)
	x.waitConnected()
	mustApply(t, x.panel, fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1"))))
	before := len(x.ord.all())
	if moved, err := x.a.doctorAwgReprobe(context.Background()); err != nil || moved {
		t.Fatalf("no module: moved=%v err=%v", moved, err)
	}
	if got := len(x.ord.all()); got != before {
		t.Fatalf("a repair without a module touched the engine: %v", x.ord.all()[before:])
	}
	x.awg.reprobe = true
	if moved, err := x.a.doctorAwgReprobe(context.Background()); err != nil || !moved {
		t.Fatalf("module there: moved=%v err=%v", moved, err)
	}
	applied := false
	for _, s := range x.ord.all()[before:] {
		applied = applied || s == "awg.apply:inb_a"
	}
	if !applied {
		t.Fatalf("the inbound was not applied again: %v", x.ord.all()[before:])
	}
}

// The ceiling counts the devices of the enabled awg inbounds.
func TestAwgPeersCountsEnabledAwgDevices(t *testing.T) {
	x := newL3(t, 3, false)
	x.waitConnected()
	mustApply(t, x.panel, fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1"), awgCred("c2"), awgCred("c3"))))
	if n := x.a.AwgPeers(); n != 3 {
		t.Fatalf("AwgPeers = %d", n)
	}
}
