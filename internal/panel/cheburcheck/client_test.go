package cheburcheck

import (
	"strings"
	"testing"
)

func TestReadSSEFrames(t *testing.T) {
	body := ": keepalive\n" +
		"event:started\n" +
		"data:{\"online_probes\":2}\n\n" +
		"event: result\n" +
		"data: first\n" +
		"data:second\n\n" +
		"event:done\n" +
		"data:{\"status\":\"done\"}\n\n" +
		"event: result\n" +
		"data: ignored\n\n"
	var got []sseEvent
	err := readSSE(strings.NewReader(body), maxBody, func(ev sseEvent) bool {
		got = append(got, ev)
		return ev.Name != "done"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != (sseEvent{Name: "started", Data: `{"online_probes":2}`}) ||
		got[1] != (sseEvent{Name: "result", Data: "first\nsecond"}) || got[2] != (sseEvent{Name: "done", Data: `{"status":"done"}`}) {
		t.Fatalf("SSE frames = %#v", got)
	}
}

func TestProbeResultMapsIPVerdictsAndCleansNames(t *testing.T) {
	got, ok := (probeResult{ID: "17", Region: "Example\x01 Region", Provider: "Example ISP", ASN: "AS64496 Example 123",
		Verdicts: []string{"uncertain", "ok"}, DPIHop: 3, TargetHop: 5}).vantage()
	if !ok || got.Key != "probe:17" || got.Region != "Example Region" || got.Network != "Example ISP" || got.ASN != "64496" ||
		got.State != "reach" || got.DPIHop != 3 || got.TargetHop != 5 {
		t.Fatalf("reach vantage = %+v, %v", got, ok)
	}
	got, ok = (probeResult{ID: "18", Verdicts: []string{"tspu_block"}}).vantage()
	if !ok || got.State != "dark" {
		t.Fatalf("dark vantage = %+v, %v", got, ok)
	}
	got, ok = (probeResult{ID: "19", Verdicts: []string{"future_verdict"}}).vantage()
	if !ok || got.State != "uncertain" {
		t.Fatalf("unknown verdict vantage = %+v, %v", got, ok)
	}
}
