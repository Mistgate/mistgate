//go:build !js

package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The debug capture API: the procedures pass through to the fleet, and a read cleans every record once more, whatever was stored.

type fakeDebug struct {
	started []fleet.DebugStart
	stopped []string
	err     error
	now     time.Time
}

func (f *fakeDebug) StartDebugCapture(_ context.Context, in fleet.DebugStart) (store.DebugCaptureRow, error) {
	f.started = append(f.started, in)
	if f.err != nil {
		return store.DebugCaptureRow{}, f.err
	}
	return store.DebugCaptureRow{ID: "req_new", NodeID: in.NodeID, NodeName: "de1", Actor: "adm_1", Level: in.Level, Sources: in.Sources,
		DurationS: int(in.DurationS), StartedAt: f.now, EndsAt: f.now.Add(time.Duration(in.DurationS) * time.Second), State: "running"}, nil
}

func (f *fakeDebug) StopDebugCapture(_ context.Context, id string) (store.DebugCaptureRow, error) {
	f.stopped = append(f.stopped, id)
	return store.DebugCaptureRow{ID: id, NodeID: "nod_1", NodeName: "de1", Level: "debug", State: "done", EndReason: "stopped"}, nil
}

func TestDebugCaptureProceduresPassThroughAndWithoutAFleetAreUnimplemented(t *testing.T) {
	be := &fakeDebug{now: time.Now()}
	e := newEnv(t, func(c *Config) { c.Debug = be })
	r, err := e.cl.StartDebugCapture(context.Background(), connect.NewRequest(&opsv1.StartDebugCaptureRequest{NodeId: "nod_1", Level: "info", Sources: []string{"xray"}, DurationS: 120}))
	if err != nil {
		t.Fatal(err)
	}
	if len(be.started) != 1 || be.started[0].NodeID != "nod_1" || be.started[0].DurationS != 120 || be.started[0].Level != "info" {
		t.Errorf("fleet was asked %+v", be.started)
	}
	if c := r.Msg.Capture; c.CaptureId != "req_new" || c.State != "running" || c.NodeName != "de1" || c.EndsUnix-c.StartedUnix != 120 || c.Sources[0] != "xray" {
		t.Errorf("capture = %+v", c)
	}
	s, err := e.cl.StopDebugCapture(context.Background(), connect.NewRequest(&opsv1.StopDebugCaptureRequest{CaptureId: "req_new"}))
	if err != nil || s.Msg.Capture.State != "done" || len(be.stopped) != 1 || be.stopped[0] != "req_new" {
		t.Errorf("stop = %+v, %v, %v", s, err, be.stopped)
	}
	be.err = connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("agent too old"))
	if _, err := e.cl.StartDebugCapture(context.Background(), connect.NewRequest(&opsv1.StartDebugCaptureRequest{NodeId: "nod_1"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a refusal of the fleet is passed on: %v", err)
	}

	bare := newEnv(t)
	if _, err := bare.cl.StartDebugCapture(context.Background(), connect.NewRequest(&opsv1.StartDebugCaptureRequest{NodeId: "nod_1"})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("a panel without a fleet: %v", err)
	}
}

// seedCapture stores a finished capture whose lines carry what the panel must never show: the intake would have cut it, but the read
// does not rely on that.
func seedCapture(t *testing.T, e *env, id string, state string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if _, err := e.st.W.ExecContext(ctx, `INSERT OR IGNORE INTO node (id, name, state, address, created_at) VALUES ('nod_1', 'de1', 'active', '203.0.113.10', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.ExecContext(ctx, `INSERT INTO debug_capture (id, node_id, actor, level, sources, duration_s, started_at, ends_at, state, end_reason, records)
		VALUES (?, 'nod_1', 'adm_1', 'debug', 'hysteria2,xray', 60, ?, ?, ?, 'expired', 5)`, id, now.Add(-10*time.Minute).Unix(), now.Add(-9*time.Minute).Unix(), state); err != nil {
		t.Fatal(err)
	}
	insert := func(seq int, level int, source, msg, attrs string) {
		if _, err := e.st.W.ExecContext(ctx, `INSERT INTO debug_capture_line (capture_id, seq, at_ms, level, source, message, attrs_json) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, seq, now.UnixMilli()+int64(seq), level, source, msg, attrs); err != nil {
			t.Fatal(err)
		}
	}
	insert(1, 0, "hysteria2", "handshake for "+canarySNI+" from "+canaryAddr+":4433", `{"err":"refused by `+canaryAddr+`","code":"hs","user":"`+canaryUser+`"}`)
	insert(2, 1, "xray", "route "+canaryLink+" with "+canaryKey, `{"state":"ok"}`)
	insert(3, 2, "agent", "path /"+canaryPrefix+"/sub opened", `{"err":"dial to host.example.net"}`)
	insert(4, 3, "agent", "boom", `{}`)
	insert(5, 1, "hysteria2", "plain info line", `{}`)
}

func TestDebugCaptureReadCleansEveryRecordAgain(t *testing.T) {
	e := newEnv(t)
	seedCapture(t, e, "req_c1", "done")
	r, err := e.cl.GetDebugCapture(context.Background(), connect.NewRequest(&opsv1.GetDebugCaptureRequest{CaptureId: "req_c1"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Msg.Records) != 5 {
		t.Fatalf("records = %d", len(r.Msg.Records))
	}
	raw := r.Msg.String()
	for _, canary := range []string{canarySNI, "q7x9k2m4", canaryPrefix, canaryAddr, "11111111-2222", canaryKey, canaryUser, "host.example.net"} {
		if strings.Contains(raw, canary) {
			t.Errorf("the read leaked %q:\n%s", canary, raw)
		}
	}
	first := r.Msg.Records[0]
	if first.Level != "debug" || first.Source != "hysteria2" || !strings.Contains(first.Message, "<host>") || !strings.Contains(first.Message, "<ip>") {
		t.Errorf("first record = %+v", first)
	}
	if _, ok := first.Attrs["user"]; ok {
		t.Error("an attribute outside the allow-list was returned")
	}
	if first.Attrs["code"] != "hs" {
		t.Errorf("attrs = %v", first.Attrs)
	}
	if strings.Join(r.Msg.Sources, ",") != "agent,hysteria2,xray" || r.Msg.Capture.State != "done" || r.Msg.Capture.NodeName != "de1" {
		t.Errorf("sources %v capture %+v", r.Msg.Sources, r.Msg.Capture)
	}
}

func TestDebugCaptureReadFiltersAndPages(t *testing.T) {
	e := newEnv(t)
	seedCapture(t, e, "req_c2", "done")
	get := func(req *opsv1.GetDebugCaptureRequest) *opsv1.GetDebugCaptureResponse {
		t.Helper()
		req.CaptureId = "req_c2"
		r, err := e.cl.GetDebugCapture(context.Background(), connect.NewRequest(req))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	seqs := func(r *opsv1.GetDebugCaptureResponse) string {
		var out []string
		for _, rec := range r.Records {
			out = append(out, fmt.Sprint(rec.Seq))
		}
		return strings.Join(out, ",")
	}
	if got := seqs(get(&opsv1.GetDebugCaptureRequest{MinLevel: "info"})); got != "2,3,4,5" {
		t.Errorf("min_level info: %s", got)
	}
	if got := seqs(get(&opsv1.GetDebugCaptureRequest{MinLevel: "error"})); got != "4" {
		t.Errorf("min_level error: %s", got)
	}
	if got := seqs(get(&opsv1.GetDebugCaptureRequest{Source: "hysteria2"})); got != "1,5" {
		t.Errorf("source: %s", got)
	}
	if got := seqs(get(&opsv1.GetDebugCaptureRequest{Query: "PLAIN"})); got != "5" {
		t.Errorf("query: %s", got)
	}
	// a search must not tell whether a secret or an address is in a record: the cleaned text is what is searched
	for _, q := range []string{canaryPrefix, canarySNI, canaryAddr, canaryUser} {
		if got := seqs(get(&opsv1.GetDebugCaptureRequest{Query: q})); got != "" {
			t.Errorf("a search for %q found records %s", q, got)
		}
	}
	if got := seqs(get(&opsv1.GetDebugCaptureRequest{Query: "<secret>"})); got != "3" {
		t.Errorf("a search for the cleaned text: %s", got)
	}
	page := get(&opsv1.GetDebugCaptureRequest{Limit: 2})
	if seqs(page) != "1,2" || page.NextCursor != 2 {
		t.Fatalf("page 1 = %s cursor %d", seqs(page), page.NextCursor)
	}
	page = get(&opsv1.GetDebugCaptureRequest{Limit: 2, AfterSeq: page.NextCursor})
	if seqs(page) != "3,4" || page.NextCursor != 4 {
		t.Fatalf("page 2 = %s cursor %d", seqs(page), page.NextCursor)
	}
	page = get(&opsv1.GetDebugCaptureRequest{Limit: 2, AfterSeq: page.NextCursor})
	if seqs(page) != "5" || page.NextCursor != 0 {
		t.Fatalf("page 3 = %s cursor %d", seqs(page), page.NextCursor)
	}
}

func TestDebugCaptureReadByNodeListAndErrors(t *testing.T) {
	e := newEnv(t)
	seedCapture(t, e, "req_old", "done")
	time.Sleep(1100 * time.Millisecond) // started_at is in seconds
	if _, err := e.st.W.ExecContext(context.Background(), `INSERT INTO debug_capture (id, node_id, actor, level, sources, duration_s, started_at, ends_at, state)
		VALUES ('req_run', 'nod_1', 'adm_1', 'info', '', 60, ?, ?, 'running')`, time.Now().Unix(), time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	r, err := e.cl.GetDebugCapture(context.Background(), connect.NewRequest(&opsv1.GetDebugCaptureRequest{NodeId: "nod_1"}))
	if err != nil || r.Msg.Capture.CaptureId != "req_run" || r.Msg.Capture.State != "running" || len(r.Msg.Records) != 0 {
		t.Fatalf("newest capture of the node = %+v, %v", r, err)
	}
	l, err := e.cl.ListDebugCaptures(context.Background(), connect.NewRequest(&opsv1.ListDebugCapturesRequest{NodeId: "nod_1"}))
	if err != nil || len(l.Msg.Captures) != 2 || l.Msg.Captures[0].CaptureId != "req_run" {
		t.Fatalf("list = %+v, %v", l, err)
	}
	for name, req := range map[string]*opsv1.GetDebugCaptureRequest{
		"neither": {},
		"both":    {CaptureId: "req_old", NodeId: "nod_1"},
		"level":   {CaptureId: "req_old", MinLevel: "loud"},
		"query":   {CaptureId: "req_old", Query: strings.Repeat("q", 101)},
	} {
		if _, err := e.cl.GetDebugCapture(context.Background(), connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.cl.GetDebugCapture(context.Background(), connect.NewRequest(&opsv1.GetDebugCaptureRequest{CaptureId: "req_nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown capture: %v", err)
	}
	// a capture that is still "running" long after its time reads as expired
	if _, err := e.st.W.ExecContext(context.Background(), `UPDATE debug_capture SET started_at = ?, ends_at = ? WHERE id = 'req_run'`,
		time.Now().Add(-3*time.Hour).Unix(), time.Now().Add(-3*time.Hour+time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	r, err = e.cl.GetDebugCapture(context.Background(), connect.NewRequest(&opsv1.GetDebugCaptureRequest{CaptureId: "req_run"}))
	if err != nil || r.Msg.Capture.State != "expired" {
		t.Errorf("late capture = %+v, %v", r.Msg.GetCapture(), err)
	}
}
