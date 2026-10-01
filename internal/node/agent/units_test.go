package agent

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

func statsMsg(traffic int) *pb.ConnectRequest {
	b := &pb.StatsBatch{}
	for i := 0; i < traffic; i++ {
		b.Traffic = append(b.Traffic, &pb.TrafficDelta{CredId: "crd_x", InboundId: "inb_1", BytesUp: 1})
	}
	return &pb.ConnectRequest{Message: &pb.ConnectRequest_Stats{Stats: b}}
}

func eventMsg(code string) *pb.ConnectRequest {
	return &pb.ConnectRequest{Message: &pb.ConnectRequest_Event{Event: &pb.Event{Code: code}}}
}

func seqs(ms []*pb.ConnectRequest) []uint64 {
	var out []uint64
	for _, m := range ms {
		out = append(out, m.Seq)
	}
	return out
}

func TestOutboxSeqAckAfter(t *testing.T) {
	o := newOutbox(100, 1<<20)
	for i := 0; i < 5; i++ {
		if got := o.push(statsMsg(1)); got != uint64(i+1) {
			t.Fatalf("seq %d, want %d", got, i+1)
		}
	}
	next, first := o.hello()
	if next != 6 || first != 1 {
		t.Fatalf("hello next=%d first=%d", next, first)
	}
	if got := seqs(o.after(2)); len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Fatalf("after(2) = %v", got)
	}
	o.ack(3)
	o.ack(2) // a late, smaller ack changes nothing
	if got := seqs(o.after(0)); len(got) != 2 || got[0] != 4 {
		t.Fatalf("after ack = %v", got)
	}
	if _, first := o.hello(); first != 4 {
		t.Fatalf("first unacked %d", first)
	}
	o.ack(99)
	if _, first := o.hello(); first != 0 || len(o.after(0)) != 0 || o.bytes != 0 {
		t.Fatalf("not empty: first=%d bytes=%d", first, o.bytes)
	}
	if o.push(statsMsg(1)) != 6 {
		t.Fatal("seq must keep increasing after the queue drained")
	}
}

func TestOutboxDropsOldestStatsNeverEvents(t *testing.T) {
	o := newOutbox(4, 1<<20)
	o.push(eventMsg("engine_started")) // seq 1, must survive
	for i := 0; i < 6; i++ {
		o.push(statsMsg(1)) // seq 2..7
	}
	got := o.after(0)
	if len(got) != 4 || got[0].Seq != 1 || got[0].GetEvent() == nil {
		t.Fatalf("queue after overflow: %v", seqs(got))
	}
	if want := []uint64{1, 5, 6, 7}; seqs(got)[1] != want[1] || seqs(got)[3] != want[3] {
		t.Fatalf("oldest stats must go first: %v", seqs(got))
	}
	if d := o.takeDropped(); d != 3 {
		t.Fatalf("dropped %d", d)
	}
	if o.takeDropped() != 0 {
		t.Fatal("drop counter not reset")
	}
	// The stats_dropped event itself never triggers more dropping.
	o.push(eventMsg("stats_dropped"))
	if len(o.after(0)) != 5 || o.takeDropped() != 0 {
		t.Fatal("event push dropped something")
	}
}

func TestOutboxByteBudget(t *testing.T) {
	o := newOutbox(1000, 2*proto.Size(statsMsg(200))+10) // room for two batches
	for i := 0; i < 6; i++ {
		o.push(statsMsg(200))
	}
	if n := len(o.after(0)); n > 2 || n == 0 {
		t.Fatalf("byte budget kept %d batches", n)
	}
	if o.takeDropped() == 0 {
		t.Fatal("nothing reported as dropped")
	}
	if o.after(0)[len(o.after(0))-1].Seq != 6 {
		t.Fatal("the newest batch must be kept")
	}
}

func TestBackoffStep(t *testing.T) {
	lo, hi := time.Second, time.Minute
	delay := lo
	var nominal []time.Duration
	for i := 0; i < 9; i++ {
		wait, next := backoffStep(delay, 0, lo, hi)
		if wait < time.Duration(0.8*float64(delay)) || wait > time.Duration(1.2*float64(delay)) {
			t.Fatalf("wait %v outside +-20%% of %v", wait, delay)
		}
		nominal = append(nominal, delay)
		delay = next
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i, w := range want {
		if nominal[i] != w*time.Second {
			t.Fatalf("nominal delays %v", nominal)
		}
	}
	// A stream that lived a minute resets the ladder.
	wait, next := backoffStep(60*time.Second, 61*time.Second, lo, hi)
	if wait > 1200*time.Millisecond || next != 2*time.Second {
		t.Fatalf("no reset: wait=%v next=%v", wait, next)
	}
	// A stream that died quickly does not.
	if _, next := backoffStep(8*time.Second, 59*time.Second, lo, hi); next != 16*time.Second {
		t.Fatalf("reset too early: %v", next)
	}
}

func known(p string) bool { return p == "fake" }

func TestMergeRules(t *testing.T) {
	base, err := merge(newModel(), fullState(5, inb("inb_1", 0, 0, cred("crd_a")), inb("inb_2", 0, 0, cred("crd_b"))), known)
	if err != nil {
		t.Fatal(err)
	}
	if base.revision != 5 || len(base.inbounds) != 2 {
		t.Fatal("full state not merged")
	}

	// A delta never mutates the model it is applied on, so a failure later cannot leave half a state.
	delta := &pb.DesiredState{Revision: 6, BaseRevision: 5, RemovedInboundIds: []string{"inb_2"}, Inbounds: []*pb.InboundState{
		{InboundId: "inb_1", Creds: []*pb.Credential{cred("crd_c")}},
	}}
	next, err := merge(base, delta, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.inbounds) != 2 || len(base.inbounds["inb_1"].creds) != 1 {
		t.Fatal("merge mutated its input")
	}
	if len(next.inbounds) != 1 || len(next.inbounds["inb_1"].creds) != 2 || next.revision != 6 {
		t.Fatalf("delta result: %+v", next)
	}

	// creds_replace replaces; a full state also replaces the credential set of inbounds it lists.
	repl := &pb.DesiredState{Revision: 7, BaseRevision: 6, Inbounds: []*pb.InboundState{
		{InboundId: "inb_1", CredsReplace: true, Creds: []*pb.Credential{cred("crd_z")}},
	}}
	if n, _ := merge(next, repl, known); len(n.inbounds["inb_1"].creds) != 1 {
		t.Fatal("creds_replace did not replace")
	}

	if _, err := merge(base, &pb.DesiredState{Revision: 9, BaseRevision: 7}, known); !errors.Is(err, errBaseMismatch) {
		t.Fatalf("want base mismatch, got %v", err)
	}

	for name, ds := range map[string]*pb.DesiredState{
		"new inbound without spec": {Revision: 6, BaseRevision: 5, Inbounds: []*pb.InboundState{{InboundId: "inb_9"}}},
		"empty id":                 {Revision: 6, BaseRevision: 5, Inbounds: []*pb.InboundState{{}}},
		"duplicate":                {Revision: 6, BaseRevision: 5, Inbounds: []*pb.InboundState{{InboundId: "inb_1"}, {InboundId: "inb_1"}}},
		"credential without id":    {Revision: 6, BaseRevision: 5, Inbounds: []*pb.InboundState{{InboundId: "inb_1", Creds: []*pb.Credential{{}}}}},
		"unknown protocol":         {Revision: 6, BaseRevision: 5, Inbounds: []*pb.InboundState{func() *pb.InboundState { i := inb("inb_7", 0, 0); i.Spec.Protocol = "x"; return i }()}},
	} {
		if _, err := merge(base, ds, known); !errors.Is(err, errRejected) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSpecValidation(t *testing.T) {
	mut := func(f func(*pb.InboundSpec)) error {
		s := inb("inb_1", 0, 0).Spec
		f(s)
		_, err := specFromPB("inb_1", s)
		return err
	}
	for name, f := range map[string]func(*pb.InboundSpec){
		"port 0":          func(s *pb.InboundSpec) { s.Listen.Port = 0 },
		"port too big":    func(s *pb.InboundSpec) { s.Listen.Port = 70000 },
		"hop too big":     func(s *pb.InboundSpec) { s.Listen.HopFrom, s.Listen.HopTo = 20000, 70000 },
		"hop inverted":    func(s *pb.InboundSpec) { s.Listen.HopFrom, s.Listen.HopTo = 30000, 20000 },
		"hop half":        func(s *pb.InboundSpec) { s.Listen.HopFrom = 20000 },
		"network":         func(s *pb.InboundSpec) { s.Listen.Network = "sctp" },
		"no listen":       func(s *pb.InboundSpec) { s.Listen = nil },
		"no protocol":     func(s *pb.InboundSpec) { s.Protocol = "" },
		"foreign id":      func(s *pb.InboundSpec) { s.InboundId = "inb_other" },
		"tls mode":        func(s *pb.InboundSpec) { s.Tls.Mode = 99 },
		"negative tlsmod": func(s *pb.InboundSpec) { s.Tls.Mode = -1 },
	} {
		if err := mut(f); !errors.Is(err, errRejected) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if err := mut(func(*pb.InboundSpec) {}); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}
}

func TestStateSurvivesRoundTripThroughDisk(t *testing.T) {
	m, err := merge(newModel(), fullState(3, inb("inb_1", 20000, 30000, cred("crd_a"), cred("crd_b"))), known)
	if err != nil {
		t.Fatal(err)
	}
	m.inbounds["inb_1"].creds["crd_a"] = plugin.UserCred{CredID: "crd_a", UserID: "u", DeviceID: "d", Data: []byte(`{"k":1}`), RateLimitBps: 5, ValidUntil: time.Unix(2000000000, 0)}
	m.settings = &pb.NodeSettings{StatsIntervalS: 7}
	dir := t.TempDir()
	if err := saveState(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := loadState(dir, known)
	if err != nil {
		t.Fatal(err)
	}
	if got.hash() != m.hash() || got.revision != 3 || got.settings.StatsIntervalS != 7 {
		t.Fatalf("round trip changed the state: %s vs %s", got.hash(), m.hash())
	}
	// A protocol whose engine disappeared is skipped, not fatal.
	if g, err := loadState(dir, func(string) bool { return false }); err != nil || len(g.inbounds) != 0 {
		t.Fatalf("unknown protocol on restore: %v %v", g, err)
	}
	// A corrupt file yields an empty model and an error to log.
	corrupt(t, dir)
	if g, err := loadState(dir, known); err == nil || g.revision != 0 {
		t.Fatalf("corrupt state: %v %v", g, err)
	}
}

func corrupt(t *testing.T, dir string) {
	t.Helper()
	if err := writeFileAtomic(dir+"/"+fileState, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLogRingTailAndHandler(t *testing.T) {
	ring := newLogRing()
	inner := slog.New(slog.NewTextHandler(&strings.Builder{}, &slog.HandlerOptions{Level: slog.LevelError}))
	log := slog.New(&ringHandler{ring: ring, inner: inner.Handler()})
	log.Info("hello", "k", "v")
	log.With("source", "hysteria2").Warn("engine slow", slog.Group("g", slog.Int("n", 3)))
	log.Debug("below the ring threshold")
	log.Error("boom")

	all, _ := ring.tail(10, func(logRecord) bool { return true }, false)
	if len(all) != 3 { // info, warn, error; the debug record is not kept
		t.Fatalf("%d records", len(all))
	}
	if all[0].msg != "hello" || all[0].source != "agent" || all[0].attrs["k"] != "v" || all[0].level != pb.Severity_SEVERITY_INFO {
		t.Errorf("first: %+v", all[0])
	}
	if all[1].source != "hysteria2" || all[1].attrs["g.n"] != "3" || all[1].level != pb.Severity_SEVERITY_WARNING {
		t.Errorf("second: %+v", all[1])
	}
	if _, has := all[1].attrs["source"]; has {
		t.Error("source leaked into attrs")
	}

	req := &pb.LogRequest{Sources: []string{"hysteria2"}}
	if got, _ := ring.tail(10, logFilter(req), false); len(got) != 1 {
		t.Errorf("source filter: %d", len(got))
	}
	req = &pb.LogRequest{MinLevel: pb.Severity_SEVERITY_WARNING}
	if got, _ := ring.tail(10, logFilter(req), false); len(got) != 2 {
		t.Errorf("level filter: %d", len(got))
	}
	if got, _ := ring.tail(1, logFilter(&pb.LogRequest{}), false); len(got) != 1 || got[0].msg != "boom" {
		t.Errorf("tail(1) must be the newest record: %+v", got)
	}
}

func TestLogRingWrapsAndNeverBlocksOnSlowFollower(t *testing.T) {
	ring := newLogRing()
	_, f := ring.tail(0, func(logRecord) bool { return true }, true)
	for i := 0; i < ringSize+followBuf+50; i++ {
		ring.add(logRecord{msg: "m", source: "agent"})
	}
	if got, _ := ring.tail(ringSize+100, func(logRecord) bool { return true }, false); len(got) != ringSize {
		t.Fatalf("ring holds %d", len(got))
	}
	if d := ring.takeDropped(f); d != ringSize+50 { // everything beyond the follower's buffer
		t.Fatalf("slow follower told %d drops", d)
	}
	ring.unfollow(f)
}

func TestLogRequestOverTheStream(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.a.log.Info("marker one")
	h.a.log.Warn("marker two", "inbound", "inb_1")

	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_LogRequest{LogRequest: &pb.LogRequest{RequestId: "log_1", TailLines: 1000}}})
	var lines []*pb.LogLine
	for {
		c := nextLog(t, h)
		if c.RequestId != "log_1" {
			t.Fatalf("chunk for %q", c.RequestId)
		}
		lines = append(lines, c.Lines...)
		if c.Eof {
			break
		}
	}
	var seen1, seen2 bool
	for _, l := range lines {
		seen1 = seen1 || l.Message == "marker one"
		if l.Message == "marker two" {
			seen2 = true
			if l.Level != pb.Severity_SEVERITY_WARNING || l.Attrs["inbound"] != "inb_1" || l.Source != "agent" || l.TimeUnixMs == 0 {
				t.Errorf("line = %v", l)
			}
		}
	}
	if !seen1 || !seen2 {
		t.Fatalf("records missing from the tail: %v", lines)
	}

	// Follow: new records stream until LogCancel, which ends the stream with eof.
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_LogRequest{LogRequest: &pb.LogRequest{RequestId: "log_2", Follow: true, MinLevel: pb.Severity_SEVERITY_WARNING}}})
	time.Sleep(100 * time.Millisecond)
	h.a.log.Info("filtered out")
	h.a.log.Warn("followed line")
	deadline := time.After(5 * time.Second)
	got := false
	for !got {
		select {
		case c := <-h.panel.logs:
			for _, l := range c.Lines {
				if l.Message == "filtered out" {
					t.Fatal("min_level ignored")
				}
				got = got || l.Message == "followed line"
			}
		case <-deadline:
			t.Fatal("followed line never arrived")
		}
	}
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_LogCancel{LogCancel: &pb.LogCancel{RequestId: "log_2"}}})
	for {
		c := nextLog(t, h)
		if c.Eof {
			break
		}
	}
	eventually(t, func() bool {
		s := h.a.cur.Load()
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.logs) == 0
	}, "log stream forgotten after cancel")
}

func nextLog(t *testing.T, h *harness) *pb.LogChunk {
	t.Helper()
	select {
	case c := <-h.panel.logs:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no LogChunk")
		return nil
	}
}

func TestDNSFollowsNodeSettings(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	ds := fullState(1)
	ds.Settings = &pb.NodeSettings{DnsResolvers: []string{"77.88.8.8", "77.88.8.1:53"}, StatsIntervalS: 5}
	h.panel.push(ds)
	h.panel.nextApply()
	if got := h.a.DNS(); len(got) != 2 || got[0] != "77.88.8.8" {
		t.Fatalf("dns = %v", got)
	}
	// The returned slice is a copy: engines cannot edit the agent's settings through it.
	h.a.DNS()[0] = "evil"
	if h.a.DNS()[0] != "77.88.8.8" {
		t.Fatal("DNS() exposes internal state")
	}
}
