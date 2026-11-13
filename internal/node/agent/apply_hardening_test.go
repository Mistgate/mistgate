package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

type panicTestEngine struct {
	*fakeEngine
	panicApplyID   string
	panicApplyPort uint16
	panicRemoveID  string
}

func (e *panicTestEngine) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	if spec.ID == e.panicApplyID && (e.panicApplyPort == 0 || spec.Listen.Port == e.panicApplyPort) {
		panic("fake apply failure")
	}
	return e.fakeEngine.Apply(ctx, spec, creds)
}

func (e *panicTestEngine) Remove(ctx context.Context, id string) error {
	if id == e.panicRemoveID {
		panic("fake remove failure")
	}
	return e.fakeEngine.Remove(ctx, id)
}

func inboundResults(results []*pb.InboundResult) map[string]*pb.InboundResult {
	out := make(map[string]*pb.InboundResult, len(results))
	for _, result := range results {
		out[result.InboundId] = result
	}
	return out
}

func TestReconcileContainsEngineApplyPanic(t *testing.T) {
	var logs bytes.Buffer
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) {
		c.Log = slog.New(slog.NewTextHandler(&logs, nil))
	}})
	panics := &panicTestEngine{fakeEngine: h.eng, panicApplyID: "inb_bad"}
	h.a.engines["fake"] = panics

	result := h.a.applyDesired(context.Background(), fullState(1, inb("inb_bad", 0, 0), inb("inb_good", 0, 0)))
	byID := inboundResults(result.Inbounds)
	if result.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL {
		t.Fatalf("status = %s, want PARTIAL", result.Status)
	}
	if bad := byID["inb_bad"]; bad == nil || bad.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED || bad.Error != "engine panic: fake apply failure" {
		t.Fatalf("panicking inbound result = %+v", bad)
	}
	if good := byID["inb_good"]; good == nil || good.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING || good.Error != "" {
		t.Fatalf("other inbound result = %+v", good)
	}
	if !h.eng.has("inb_good") || h.eng.has("inb_bad") {
		t.Fatal("the panic stopped another inbound or recorded the panicking inbound as applied")
	}
	if got := strings.Count(logs.String(), `msg="engine panic"`); got != 1 || !strings.Contains(logs.String(), "panicTestEngine).Apply") {
		t.Fatalf("panic log count/stack = %d\n%s", got, logs.String())
	}
	if strings.Contains(byID["inb_bad"].Error, "goroutine") {
		t.Fatal("stack trace leaked into the inbound error")
	}
}

func TestReconcileContainsEngineRemovePanic(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	panics := &panicTestEngine{fakeEngine: h.eng, panicRemoveID: "inb_removed"}
	h.a.engines["fake"] = panics
	first := h.a.applyDesired(context.Background(), fullState(1, inb("inb_removed", 0, 0), inb("inb_kept", 0, 0)))
	if first.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("initial apply status = %s", first.Status)
	}

	result := h.a.applyDesired(context.Background(), fullState(2, inb("inb_kept", 0, 0)))
	byID := inboundResults(result.Inbounds)
	if result.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL {
		t.Fatalf("status = %s, want PARTIAL", result.Status)
	}
	if removed := byID["inb_removed"]; removed == nil || removed.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED || removed.Error != "engine panic: fake remove failure" {
		t.Fatalf("removed inbound result = %+v", removed)
	}
	if kept := byID["inb_kept"]; kept == nil || kept.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
		t.Fatalf("kept inbound result = %+v", kept)
	}
}

func TestApplyCrashMarkerSkipsReplayAndChangedSpecClearsIt(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	if result := h.a.applyDesired(context.Background(), fullState(1, inb("inb_crash", 0, 0), inb("inb_good", 0, 0))); result.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("initial apply status = %s", result.Status)
	}
	crashHash := statehash.Spec(h.a.snapshotModel().inbounds["inb_crash"].spec)
	if err := saveApplyMarkers(h.dir, map[string]string{"inb_crash": crashHash}); err != nil {
		t.Fatal(err)
	}

	// Emulate a fresh process: persisted model remains, while engine and in-memory apply results are new.
	h.eng = newFakeEngine("fake")
	h.a.engines["fake"] = h.eng
	h.a.mu.Lock()
	h.a.model = newModel()
	h.a.mu.Unlock()
	h.a.held = map[string]*held{}
	h.a.applyMarkers = nil
	h.a.applyMarkersLoaded = false
	h.a.crashSkips = nil
	h.a.restore(context.Background())
	if got := h.a.held["inb_crash"].res; got.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED || got.Error != "skipped after a crash while applying it" {
		t.Fatalf("startup replay result = %+v", got)
	}
	if got := h.a.held["inb_good"].res; got.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING || h.eng.applyCount("inb_good") != 1 {
		t.Fatalf("other restored inbound = %+v, apply count %d", got, h.eng.applyCount("inb_good"))
	}
	if _, err := os.Stat(filepath.Join(h.dir, fileApplyMark)); err != nil {
		t.Fatalf("crash marker was lost after skipping replay: %v", err)
	}

	// A later successful apply of the same spec clears the old marker too.
	result := h.a.applyDesired(context.Background(), &pb.DesiredState{Revision: 2, BaseRevision: 1, Inbounds: []*pb.InboundState{inb("inb_crash", 0, 0)}})
	if got := inboundResults(result.Inbounds)["inb_crash"]; got == nil || got.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
		t.Fatalf("same-spec retry = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(h.dir, fileApplyMark)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful apply did not clear crash marker: %v", err)
	}

	// Emulate a new process with a prior marker again so a changed spec exercises the other clearing path.
	if err := saveApplyMarkers(h.dir, map[string]string{"inb_crash": crashHash}); err != nil {
		t.Fatal(err)
	}
	h.a.applyMarkers = nil
	h.a.applyMarkersLoaded = false
	h.a.crashSkips = nil

	panics := &panicTestEngine{fakeEngine: h.eng, panicApplyID: "inb_crash", panicApplyPort: 444}
	h.a.engines["fake"] = panics
	changed := inb("inb_crash", 0, 0)
	changed.Spec.Listen.Port = 444
	result = h.a.applyDesired(context.Background(), fullState(3, changed, inb("inb_good", 0, 0)))
	if bad := inboundResults(result.Inbounds)["inb_crash"]; bad == nil || bad.Error != "engine panic: fake apply failure" {
		t.Fatalf("changed-spec apply result = %+v", bad)
	}
	if _, err := os.Stat(filepath.Join(h.dir, fileApplyMark)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed spec did not clear crash marker: %v", err)
	}

	h.a.engines["fake"] = h.eng
	result = h.a.applyDesired(context.Background(), fullState(4, changed, inb("inb_good", 0, 0)))
	if applied := inboundResults(result.Inbounds)["inb_crash"]; applied == nil || applied.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
		t.Fatalf("changed spec retry = %+v", applied)
	}
}

func TestMergeKeepsValidInboundsWhenItemsFailParsing(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	badSpec := inb("inb_bad_spec", 0, 0, cred("crd_bad_spec"))
	badSpec.Spec.Listen.Network = "quic"
	badCredential := inb("inb_bad_credential", 0, 0, cred("crd_kept"))
	badCredential.Creds = append(badCredential.Creds, nil)
	ds := fullState(1, inb("inb_good", 0, 0, cred("crd_good")), badSpec, badCredential)
	ds.StateHash = "panel-state-with-invalid-items"

	result := h.a.applyDesired(context.Background(), ds)
	byID := inboundResults(result.Inbounds)
	if result.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL || result.StateHash == ds.StateHash {
		t.Fatalf("status/hash = %s/%q", result.Status, result.StateHash)
	}
	if good := byID["inb_good"]; good == nil || good.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
		t.Fatalf("valid inbound result = %+v", good)
	}
	if failed := byID["inb_bad_spec"]; failed == nil || failed.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED || !strings.Contains(failed.Error, `network "quic"`) {
		t.Errorf("inb_bad_spec result = %+v", failed)
	}
	// one bad credential does not take the inbound down: it runs with the rest and says what it skipped
	if kept := byID["inb_bad_credential"]; kept == nil || kept.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING || !strings.Contains(kept.Error, "credential without id") {
		t.Errorf("inb_bad_credential result = %+v", kept)
	}
	if ids := h.eng.credIDs("inb_bad_credential"); len(ids) != 1 || ids[0] != "crd_kept" {
		t.Errorf("inb_bad_credential runs with %v, want crd_kept only", ids)
	}
	if !h.eng.has("inb_good") || h.eng.applyCount("inb_bad_spec") != 0 {
		t.Fatal("an invalid inbound reached the engine or the valid inbound did not run")
	}
	duplicate := h.a.applyDesired(context.Background(), &pb.DesiredState{Revision: 1, BaseRevision: 1})
	if duplicate.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL {
		t.Fatalf("duplicate apply status = %s, want PARTIAL", duplicate.Status)
	}
}

func TestBadRelayAndWarpItemsAreSkippedWithEventsAndPartialStatus(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ds := fullState(1, inb("inb_good", 0, 0))
	ds.StateHash = "panel-state-with-invalid-items"
	ds.Warp = &pb.WarpSpec{Mtu: 65536}
	ds.Relays = &pb.RelayForwards{Items: []*pb.RelayForward{{RelayId: "rly_bad", Port: 65536}}}

	result := h.a.applyDesired(context.Background(), ds)
	if result.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL || result.StateHash == ds.StateHash {
		t.Fatalf("status/hash = %s/%q", result.Status, result.StateHash)
	}
	model := h.a.snapshotModel()
	if model.warp != nil || len(model.relays) != 0 || !model.partial {
		t.Fatalf("invalid entries remained in accepted state: warp=%+v relays=%+v partial=%t", model.warp, model.relays, model.partial)
	}
	skipped := map[string]string{}
	for _, message := range h.a.out.after(0) {
		if event := message.GetEvent(); event != nil {
			if event.Code == "desired_state_item_skipped" {
				skipped[event.Params["kind"]] = event.Params["error"]
			}
		}
	}
	if len(skipped) != 2 || skipped["warp"] == "" || skipped["relay"] == "" {
		t.Fatalf("skipped-item events = %v", skipped)
	}
}

func TestMergeSkipsBadWarpAndKeepsLastGoodDeltaValue(t *testing.T) {
	base := newModel()
	base.revision = 1
	var err error
	base.warp, err = warpFromPB(warpSpecPB(true))
	if err != nil {
		t.Fatal(err)
	}

	next, err := merge(base, &pb.DesiredState{Revision: 2, BaseRevision: 1, Warp: &pb.WarpSpec{Mtu: 70000}}, func(string) bool { return true })
	var partial *partialStateError
	if !errors.As(err, &partial) || next == nil || next.warp != base.warp || !next.partial {
		t.Fatalf("bad WARP delta: next=%+v err=%v; want partial while retaining the last good WARP", next, err)
	}
}

func TestCredentialWarningsRemainRunningAndPartial(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	h.eng.warnings["inb_warn"] = []string{"credential c: malformed data", "credentials a and c share one key"}
	result := h.a.applyDesired(context.Background(), fullState(1, inb("inb_warn", 0, 0)))
	warning := inboundResults(result.Inbounds)["inb_warn"]
	if result.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL || warning == nil || warning.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING || warning.Error != "2 credentials skipped: credential c: malformed data; credentials a and c share one key" {
		t.Fatalf("result = %+v, inbound warning = %+v", result, warning)
	}
	duplicate := h.a.applyDesired(context.Background(), &pb.DesiredState{Revision: 1, BaseRevision: 1})
	if duplicate.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL || inboundResults(duplicate.Inbounds)["inb_warn"].Error != warning.Error {
		t.Fatalf("duplicate warning result = %+v", duplicate)
	}
}
