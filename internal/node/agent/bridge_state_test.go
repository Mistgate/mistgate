package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

func bridgeTestModel() *model {
	m := newModel()
	m.revision = 7
	m.relays = []plugin.RelayForward{{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "ignored.example", ToPort: 443, Mode: "reverse", ExitNodeID: "nod_de1"}}
	m.bridgeServe = &plugin.BridgeServe{Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}}
	m.bridgeDials = []plugin.BridgeDial{{EntryNodeID: "nod_msk1", Host: "192.0.2.10", Port: 443, SNI: "msk1.example", PinSHA256: strings.Repeat("ab", 32),
		Path: "/private-bridge-path", Pool: 2, Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: 8443}}}}
	return m
}

func TestBridgeDesiredStateHashMergeAndSnapshot(t *testing.T) {
	known := func(string) bool { return true }
	m := bridgeTestModel()
	if got, want := m.hash(), statehash.StateBridge(nil, nil, m.relays, m.bridgeServe, m.bridgeDials); got != want {
		t.Fatalf("model hash = %s, want StateBridge %s", got, want)
	}
	if got, want := newModel().hash(), statehash.StateBridge(nil, nil, nil, nil, nil); got != want {
		t.Fatalf("empty model hash = %s, want existing no-bridge hash %s", got, want)
	}
	if m.hash() == newModel().hash() {
		t.Fatal("bridge desired state did not change the model hash")
	}

	wire := m.snapshot()
	loaded, err := merge(newModel(), wire, known)
	if err != nil || !reflect.DeepEqual(loaded.bridgeServe, m.bridgeServe) || !reflect.DeepEqual(loaded.bridgeDials, m.bridgeDials) || loaded.hash() != m.hash() {
		t.Fatalf("snapshot round trip: err=%v serve=%+v dials=%+v hash=%s", err, loaded.bridgeServe, loaded.bridgeDials, loaded.hash())
	}

	kept, err := merge(m, &pb.DesiredState{Revision: 8, BaseRevision: 7}, known)
	if err != nil || !reflect.DeepEqual(kept.bridgeServe, m.bridgeServe) || !reflect.DeepEqual(kept.bridgeDials, m.bridgeDials) {
		t.Fatalf("delta without bridge fields did not keep them: err=%v model=%+v", err, kept)
	}
	cleared, err := merge(m, &pb.DesiredState{Revision: 8, BaseRevision: 7, Relays: &pb.RelayForwards{},
		BridgeServe: &pb.BridgeServe{}, BridgeDials: &pb.BridgeDials{}}, known)
	if err != nil || cleared.bridgeServe != nil || len(cleared.bridgeDials) != 0 || len(cleared.relays) != 0 {
		t.Fatalf("empty wrappers did not clear bridge state: err=%v model=%+v", err, cleared)
	}
	full, err := merge(m, &pb.DesiredState{Revision: 9}, known)
	if err != nil || full.bridgeServe != nil || len(full.bridgeDials) != 0 || len(full.relays) != 0 || full.hash() != newModel().hash() {
		t.Fatalf("full state without bridge fields did not clear them: err=%v model=%+v", err, full)
	}
}

func TestBridgeDesiredStateRestoresAcrossAgentRestart(t *testing.T) {
	l := newRelayLab(t)
	want := bridgeTestModel()
	want.relays = nil
	if err := saveState(l.dir, want); err != nil {
		t.Fatal(err)
	}
	restarted := l.newAgent()
	defer restarted.closeEngines()
	restarted.restore(context.Background())
	got := restarted.snapshotModel()
	if !reflect.DeepEqual(got.bridgeServe, want.bridgeServe) || !reflect.DeepEqual(got.bridgeDials, want.bridgeDials) ||
		!reflect.DeepEqual(got.relays, want.relays) || got.hash() != want.hash() {
		t.Fatalf("restored desired state: serve=%+v dials=%+v relays=%+v hash=%s", got.bridgeServe, got.bridgeDials, got.relays, got.hash())
	}
	if states := restarted.bridgeDialer.States(); len(states) != 1 || states[0].Error != "target_not_local" {
		t.Fatalf("restored bridge dial state = %+v, want fail-closed target validation", states)
	}
}

func TestBridgeDialEntryLimit(t *testing.T) {
	wire := &pb.BridgeDials{Items: make([]*pb.BridgeDial, maxBridgeDialEntries+1)}
	got, rejected := bridgeDialsFromPB(wire)
	if len(got) != 0 || len(rejected) != 1 || !strings.Contains(rejected[0].Error(), "too many entries") {
		t.Fatalf("over-limit bridge dials: got %d items, rejected %v", len(got), rejected)
	}
}

func TestBridgeStateReportsThePinOfTheEntrysOwnSite(t *testing.T) {
	l := newRelayLab(t)
	m := bridgeTestModel()
	m.bridgeServe = &plugin.BridgeServe{Port: 8443, Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1", "nod_de2"}}
	m.bridgeDials = nil
	l.a.setModel(m)
	pin := strings.Repeat("cd", 32)
	l.a.bridgeInfo.Store(&bridgeInfo{site: &bridgeSiteServer{pin: pin, port: 8443}})
	rows := l.a.bridgeResults()
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want one per exit", rows)
	}
	for _, row := range rows {
		if row.SiteCertPinSha256 != pin {
			t.Fatalf("row of %s carries pin %q, want the site's %q", row.PeerNodeId, row.SiteCertPinSha256, pin)
		}
	}

	// An exit-side row, and an entry whose bridge arrives on an inbound's site (port 0), carry none.
	m = bridgeTestModel()
	m.bridgeServe = &plugin.BridgeServe{Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}}
	l.a.setModel(m)
	l.a.bridgeInfo.Store(&bridgeInfo{})
	for _, row := range l.a.bridgeResults() {
		if row.SiteCertPinSha256 != "" {
			t.Fatalf("row of %s carries a site pin %q without an own site", row.PeerNodeId, row.SiteCertPinSha256)
		}
	}
}
