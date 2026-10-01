package update

import (
	"path/filepath"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// rolledBackHello plays the agent coming back on the old build after a RollbackAgent: it reports "manual", whoever
// sent the command.
func (e *env) rolledBackHello(id string) {
	e.t.Helper()
	lu := store.LastUpdateRow{Outcome: "rolled_back", FromVersion: "0.1.0-old", FromBuilt: oldBuilt, ToVersion: "0.2.0-new",
		ToBuilt: newBuilt, Reason: errManual, AtUnix: e.clk.Now().Unix()}
	e.hello(id, "0.1.0-old", oldBuilt, []string{"doctor/1", capUpdate, capGuard}, lu.JSON())
}

func (e *env) lastUpdate(id string) *adminv1.LastUpdate {
	e.t.Helper()
	for _, n := range e.get().Nodes {
		if n.NodeId == id {
			return n.LastUpdate
		}
	}
	e.t.Fatalf("node %s is not listed", id)
	return nil
}

// The gate rolled the node back: the page says so, not "rolled back by you" (the agent's "manual").
func TestGateRollbackIsNotTheOwners(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{inbounds: 1})
	e.hl.set(a, 1, 0, 1)
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.clk.Advance(5*time.Minute + time.Second)
	e.tick()
	e.wantStep(a, store.StepRolledBack, "probe_failed")
	e.clk.Advance(20 * time.Second)
	e.rolledBackHello(a)
	if lu := e.lastUpdate(a); lu == nil || lu.Outcome != "rolled_back" || lu.Reason != "gate_probe_failed" {
		t.Fatalf("last update %+v, want the gate's reason", lu)
	}
}

// The owner's own rollback keeps "manual"; one the panel has no record of says "command".
func TestOwnerRollbackAndUnknownSender(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{built: newBuilt})
	b := e.addNode("b", nodeOpts{})
	if _, err := e.s.rollbackNode(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.rolledBackHello(a)
	if lu := e.lastUpdate(a); lu.Reason != errManual {
		t.Fatalf("owner's rollback: %+v", lu)
	}
	e.rolledBackHello(b)
	if lu := e.lastUpdate(b); lu.Reason != reasonCommand {
		t.Fatalf("no record: %+v", lu)
	}
	// an owner's rollback long before this one is another rollback
	e.clk.Advance(3 * time.Hour)
	e.rolledBackHello(a)
	if lu := e.lastUpdate(a); lu.Reason != reasonCommand {
		t.Fatalf("an old audit row: %+v", lu)
	}
}

func TestRollbackSenderRules(t *testing.T) {
	at := time.Unix(1_791_100_000, 0)
	lu := store.LastUpdateRow{Outcome: "rolled_back", ToBuilt: newBuilt, Reason: errManual, AtUnix: at.Unix()}
	gate := store.GateRollback{ErrorKey: "inbound_failed", ToBuilt: newBuilt, At: at.Add(5 * time.Second)}
	for _, tc := range []struct {
		name string
		r    rollbackSenders
		lu   store.LastUpdateRow
		want string
	}{
		{"agent's own reason", rollbackSenders{}, store.LastUpdateRow{Outcome: "rolled_back", Reason: "crash_loop"}, "crash_loop"},
		{"not a rollback", rollbackSenders{}, store.LastUpdateRow{Outcome: "failed", Reason: errManual}, errManual},
		{"gate", rollbackSenders{gate: map[string]store.GateRollback{"n": gate}}, lu, "gate_inbound_failed"},
		{"gate of another build", rollbackSenders{gate: map[string]store.GateRollback{"n": {ErrorKey: "probe_failed", ToBuilt: 7, At: at}}}, lu, reasonCommand},
		{"owner after the gate", rollbackSenders{gate: map[string]store.GateRollback{"n": gate}, owner: map[string]time.Time{"n": at.Add(time.Minute)}}, lu, errManual},
		{"gate after the owner", rollbackSenders{gate: map[string]store.GateRollback{"n": gate}, owner: map[string]time.Time{"n": at.Add(-time.Minute)}}, lu, "gate_inbound_failed"},
		{"nobody", rollbackSenders{}, lu, reasonCommand},
	} {
		if got := tc.r.reason("n", tc.lu); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A node that has to be updated by hand comes with what its commands need: its address, its architecture and where the
// bundle lies on the panel.
func TestManualNodeCarriesItsCommandFacts(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	old := e.addNode("old", nodeOpts{caps: []string{"doctor/1"}})
	e.exec(`UPDATE node_facts SET arch = 'arm64' WHERE node_id = ?`, old)
	m := e.get()
	want, _ := filepath.Abs(filepath.Join(e.dir, "dist"))
	if m.DistDir != want {
		t.Fatalf("dist dir %q, want %q", m.DistDir, want)
	}
	for _, n := range m.Nodes {
		if n.NodeId == old && (n.State != adminv1.NodeUpdateState_NODE_UPDATE_STATE_UNSUPPORTED || n.Address != "old.example.com" || n.Arch != "arm64") {
			t.Fatalf("manual node %+v", n)
		}
	}
}
