package fleet

import (
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// osStatusUpdates is the VPS panel's update service as far as Fleet sees it: it reads OS status.
type osStatusUpdates struct{ *fakeUpdates }

func (osStatusUpdates) OSUpdatesEnabled() bool { return true }

func osStatusFrame(seq uint64, s *agentv1.OsStatus) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Seq: seq, Message: &agentv1.ConnectRequest_OsStatus{OsStatus: s}}
}

// The panel stores a snapshot only from a session it asked for one: the flag is set at hello time from the agent's
// capability and the panel's own wish, and an unasked frame (an old panel's agent, a hostile one) is dropped.
func TestSessionCoreStoresOsStatusOnlyWithTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps []string
		want bool
	}{
		{"capable agent", []string{capOSStatus}, true},
		{"agent without the capability", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, core, ctx, state, now := coreFixture(t, "osflag")
			e.f.SetUpdates(osStatusUpdates{&fakeUpdates{}})
			h := hello("instance-os", 0, "").GetHello()
			h.Capabilities = tc.caps
			tr := stepHello(t, core, ctx, state, now, h)
			if tr.State.OsStatusEnabled != tc.want {
				t.Fatalf("session flag = %v, want %v", tr.State.OsStatusEnabled, tc.want)
			}
			snapshot := osStatusFixture(now)
			tr2, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: osStatusFrame(1, snapshot)})
			if err != nil || tr2.Close != nil {
				t.Fatalf("OS status frame: %v, close %+v", err, tr2.Close)
			}
			rows, err := e.st.OsStatuses(ctx)
			if err != nil {
				t.Fatal(err)
			}
			row, stored := rows[state.NodeID]
			if stored != tc.want {
				t.Fatalf("snapshot stored = %v, want %v", stored, tc.want)
			}
			if stored {
				got := &agentv1.OsStatus{}
				if err := protojson.Unmarshal([]byte(row.StatusJSON), got); err != nil || !proto.Equal(got, snapshot) {
					t.Fatalf("stored snapshot = %s (%v)", row.StatusJSON, err)
				}
			}
		})
	}
}

// A snapshot with one malformed notable entry is stored without that entry, and the session's frame is not changed.
func TestRecordOsStatusDropsOnlyTheBadEntry(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "osdrop")
	e.f.SetUpdates(osStatusUpdates{&fakeUpdates{}})
	h := hello("instance-osdrop", 0, "").GetHello()
	h.Capabilities = []string{capOSStatus}
	tr := stepHello(t, core, ctx, state, now, h)
	snapshot := osStatusFixture(now)
	snapshot.Notable = []string{"linux-image-generic 5.15.0.125.123 5.15.0.126.125 s", "libc6\n2.35 2.36 s"}
	frame := osStatusFrame(1, snapshot)
	if _, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: frame}); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Notable) != 2 {
		t.Fatalf("the incoming frame was changed: %q", snapshot.Notable)
	}
	rows, err := e.st.OsStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := &agentv1.OsStatus{}
	if err := protojson.Unmarshal([]byte(rows[state.NodeID].StatusJSON), got); err != nil {
		t.Fatal(err)
	}
	if len(got.Notable) != 1 || got.Notable[0] != "linux-image-generic 5.15.0.125.123 5.15.0.126.125 s" {
		t.Fatalf("stored notable = %q", got.Notable)
	}
}

// The edge edition never asks for OS status, whatever its update service says and whatever the agent can do.
func TestOsStatusNotAllowedOnTheEdgeEdition(t *testing.T) {
	e := newCoreEnv(t)
	e.f.SetUpdates(osStatusUpdates{&fakeUpdates{}})
	if !e.f.osStatusAllowed() {
		t.Fatal("a VPS panel with an OS-status update service must allow it")
	}
	e.f.cfg.Remote = &testRemote{}
	if e.f.osStatusAllowed() {
		t.Fatal("the edge edition allows OS status")
	}
	settings := e.f.nodeSettings(store.NodeRow{DialTimeoutS: 30, CountryCode: "DE"}, []string{capOSStatus})
	if settings.GetOsStatus() {
		t.Fatalf("the edge edition asked a capable agent for OS status: %+v", settings)
	}
	e.f.SetUpdates(&fakeUpdates{})
	e.f.cfg.Remote = nil
	if e.f.osStatusAllowed() {
		t.Fatal("an update service that does not read OS status allows it")
	}
}
