package update

import (
	"strings"
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestRecordAnswerClipsAndStoresCurrentStepResult(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now()
	ro := store.RolloutRow{ID: "rol_record_answer", Status: store.RolloutRunning, ToVersion: "v2", ToBuilt: 20,
		Manifest: []byte("manifest"), Signature: []byte("signature"), BatchSize: 1, CreatedAt: now}
	if err := e.st.CreateRollout(e.ctx, ro, []store.StepRow{{NodeID: "nod_alice", NodeName: "alice", State: store.StepPending}}); err != nil {
		t.Fatal(err)
	}
	steps, err := e.st.RolloutSteps(e.ctx, ro.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("RolloutSteps = %+v, %v", steps, err)
	}
	step := steps[0]
	step.RequestID, step.RequestAt, step.SentAt, step.DownloadSHA = "req_record", now, now, "sha256_record"
	if ok, err := e.st.MarkStepSent(e.ctx, &step); err != nil || !ok {
		t.Fatalf("MarkStepSent = %v, %v", ok, err)
	}
	result := &agentv1.CommandResult{RequestId: "req_record", Ok: true, Detail: strings.Repeat("x", 250), Error: strings.Repeat("e", 250)}
	if err := e.s.RecordAnswer(e.ctx, "nod_alice", "req_record", result); err != nil {
		t.Fatal(err)
	}
	steps, err = e.st.RolloutSteps(e.ctx, ro.ID)
	if err != nil || len(steps) != 1 || steps[0].Answer == "" {
		t.Fatalf("recorded step = %+v, %v", steps, err)
	}
	var got agentv1.CommandResult
	if err := protojson.Unmarshal([]byte(steps[0].Answer), &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestId != "req_record" || !got.Ok || len(got.Detail) != 200 || len(got.Error) != 200 {
		t.Fatalf("stored result request=%q ok=%v detail bytes=%d error bytes=%d", got.RequestId, got.Ok, len(got.Detail), len(got.Error))
	}
	firstAnswer := steps[0].Answer
	if err := e.s.RecordAnswer(e.ctx, "nod_alice", "req_record", &agentv1.CommandResult{RequestId: "req_record", Error: "later"}); err != nil {
		t.Fatal(err)
	}
	steps, err = e.st.RolloutSteps(e.ctx, ro.ID)
	if err != nil || len(steps) != 1 || steps[0].Answer != firstAnswer {
		t.Fatalf("duplicate answer changed the step: %+v, %v", steps, err)
	}
}
