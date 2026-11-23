package fleet

import (
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

type noLink struct{ Remote }

func TestReachable(t *testing.T) {
	x, a := newL3Env(t)
	row := store.NodeLiveRow{NodeID: a.nodeID, Connected: true}
	if x.f.reachable(row, a.nodeID) {
		t.Fatal("a connected row without a session here is reachable")
	}
	connectCaps(a, "reach", "doctor/1")
	if !x.f.reachable(row, a.nodeID) {
		t.Fatal("a connected row with a session is not reachable")
	}
	if x.f.reachable(store.NodeLiveRow{NodeID: a.nodeID}, a.nodeID) {
		t.Fatal("a row that is not connected is reachable")
	}
	x.f.cfg.Remote = noLink{}
	if !x.f.reachable(row, "nod_elsewhere") {
		t.Fatal("through the edge link a connected row is not reachable")
	}
	if x.f.reachable(store.NodeLiveRow{}, "nod_elsewhere") {
		t.Fatal("through the edge link a row that is not connected is reachable")
	}
}
