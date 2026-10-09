//go:build js && wasm

package main

import (
	"context"
	"runtime"
	"sort"
	"syscall/js"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/proto"
)

// TestMeasureStepCPU records, per step kind, the time of 50 runs (design/cloudflare-edition/AGENT-LINK.md §7.7). The
// fake D1 is SQLite in the same process, so the time is split: "Go side" is the wall time without what the fake spent
// inside SQLite (real D1 runs off the isolate and costs no isolate CPU while awaited). It always runs, it is cheap;
// the numbers are for `go test -v`, the test fails only when a step kind stops working.
func TestMeasureStepCPU(t *testing.T) {
	const runs = 50
	type sample struct{ wall, db float64 }
	samples := map[string][]sample{}
	order := []string{}
	d1 := js.Global().Get("__d1")
	requestFrame, err := proto.Marshal(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{RequestId: "req_measure"}}})
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < runs; run++ {
		_, _, _, nodeID := newEdgeLinkFixture(t)
		now := time.Now().UTC().Truncate(time.Millisecond)
		until := now.Add(20 * time.Second).UnixMilli()
		tick := 0
		stateText, stateIsNull := "", true
		step := func(label, kind string, add func(js.Value)) js.Value {
			t.Helper()
			tick++
			event := makeStepEvent(kind, now.Add(time.Duration(tick)*time.Second).UnixMilli())
			if add != nil {
				add(event)
			}
			d1.Call("__beginQueryCount", label)
			start := time.Now()
			out, err := callStep(nodeID, stateText, stateIsNull, until, event)
			wall := time.Since(start)
			counts := d1.Call("__endQueryCount")
			if err != nil {
				t.Fatalf("%s step: %v", label, err)
			}
			if _, ok := samples[label]; !ok {
				order = append(order, label)
			}
			samples[label] = append(samples[label], sample{wall: float64(wall) / 1e6, db: counts.Get("dbMillis").Float()})
			stateText, stateIsNull = out.Get("state").String(), false
			return out
		}
		step("open", "open", func(e js.Value) {
			e.Set("generation", 1)
			e.Set("certSerial", "0a1b")
			e.Set("certNotAfterUnix", now.Add(90*24*time.Hour).Unix())
		})
		step("desired (before Hello)", "desired", nil)
		step("alarm (before Hello)", "alarm", nil)
		step("request", "request", func(e js.Value) {
			e.Set("requestId", "req_measure")
			e.Set("deadlineAt", now.Add(10*time.Second).UnixMilli())
			e.Set("frame", bytesToJS(requestFrame))
		})
		hello, err := proto.Marshal(&agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{ApiVersion: fleet.APIVersion, InstanceId: "instance-measure"}}})
		if err != nil {
			t.Fatal(err)
		}
		step("Hello frame", "frame", func(e js.Value) { e.Set("frame", bytesToJS(hello)) })
		stats, err := proto.Marshal(&agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
			IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Add(5 * time.Second).Unix()}}})
		if err != nil {
			t.Fatal(err)
		}
		step("stats frame", "frame", func(e js.Value) { e.Set("frame", bytesToJS(stats)) })
		step("desired (after Hello)", "desired", nil)
		step("alarm (after Hello)", "alarm", nil)
		step("closed", "closed", func(e js.Value) { e.Set("owned", false) })
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("%d runs of each step kind on the fake D1 (ms; Go side = wall minus time inside the fake SQLite)", runs)
	t.Logf("%-24s %9s %9s %12s %12s", "step", "wall p50", "wall p95", "Go side p50", "Go side p95")
	for _, label := range order {
		var wall, goSide []float64
		for _, s := range samples[label] {
			wall = append(wall, s.wall)
			goSide = append(goSide, max(s.wall-s.db, 0))
		}
		t.Logf("%-24s %9.2f %9.2f %12.2f %12.2f", label, pct(wall, 50), pct(wall, 95), pct(goSide, 50), pct(goSide, 95))
	}
	t.Logf("Go memory from the OS after the runs: %.0f MB", float64(mem.Sys)/(1<<20))
}

func pct(values []float64, p int) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[min(len(sorted)-1, (len(sorted)*p+99)/100-1)]
}

// TestLoadEdgeInstanceReadsSettingsInOneCall: a cold isolate loads the stored instance on every start, so it must cost one
// D1 round trip, not nine (§7.7), and a database from before link_prefix existed still gets one generated.
func TestLoadEdgeInstanceReadsSettingsInOneCall(t *testing.T) {
	ctx := context.Background()
	d1 := js.Global().Get("__d1")
	d1.Call("__reset")
	st, err := store.OpenD1(ctx, d1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, pending, err := newEdgeInstance(initOptions{publicURL: "https://de1.example.com", adminPath: "/test-admin/", subPath: "/test-sub/"})
	if err != nil {
		t.Fatal(err)
	}
	delete(pending, "link_prefix") // an instance stored before the link existed
	if err := st.SetSettings(ctx, pending); err != nil {
		t.Fatal(err)
	}
	d1.Call("__beginQueryCount", "loadEdgeInstance")
	in, fill, err := loadEdgeInstance(ctx, st, initOptions{})
	calls := d1.Call("__endQueryCount").Get("sequentialQueries").Int()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("loadEdgeInstance used %d D1 calls, want 1", calls)
	}
	if in.PublicURL != "https://de1.example.com" || in.AdminPrefix != "/test-admin/" || in.SubPrefix != "/test-sub/" || in.AgentSNI != pending["agent_sni"] ||
		in.RPID != "de1.example.com" || len(in.RPOrigins) != 1 {
		t.Fatalf("stored instance = %+v", in)
	}
	if len(fill) != 1 || fill["link_prefix"] == "" || in.LinkPrefix != fill["link_prefix"] {
		t.Fatalf("missing link prefix: fill=%v instance=%+v", fill, in)
	}
}
