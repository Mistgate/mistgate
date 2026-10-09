package vless

import (
	"context"
	"net"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// An idle UDP link that shares the client's connection with an active sibling (mux/XUDP) ends alone: the idle timeout
// must not close the shared connection.
func TestIdleUDPLinkKeepsSiblingConnection(t *testing.T) {
	f := newRuntimeFixture(t, false)
	f.engine.idleTimeout = 200 * time.Millisecond
	egress := &multiSocketUDPEgress{}
	f.engine.out = func(string) (engine.Egress, error) { return egress, nil }
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	in := f.engine.inbounds[spec.ID]
	inboundClient, inboundPeer := net.Pipe()
	defer inboundPeer.Close()
	shared := &closeCountConn{Conn: inboundClient}
	first, _ := xnet.ParseDestination("udp:203.0.113.10:53")
	second, _ := xnet.ParseDestination("udp:203.0.113.20:5353")
	idle := startManualUDPLink(t, in, cred, shared, first)
	active := startManualUDPLink(t, in, cred, shared, second)
	idle.sendAndExpect(t, []byte("once"))
	for i := 0; i < 10; i++ {
		active.sendAndExpect(t, []byte("keepalive"))
		time.Sleep(100 * time.Millisecond)
	}
	select {
	case <-idle.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the idle link did not time out")
	}
	if got := shared.closes.Load(); got != 0 {
		t.Fatalf("the idle timeout of one UDP link closed the shared connection (%d closes)", got)
	}
	active.sendAndExpect(t, []byte("still here"))
}

// Two engines in one process with the same inbound id keep separate connection trackers: the tracker map is global, so
// the generation must be unique process-wide.
func TestTrackerKeysDistinctAcrossEngines(t *testing.T) {
	a := newRuntimeFixture(t, false)
	b := newRuntimeFixture(t, false)
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	specA := a.spec("node-a", "xhttp")
	if _, err := a.engine.Apply(context.Background(), specA, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	specB := b.spec("node-a", "xhttp")
	for specB.Listen.Port == specA.Listen.Port {
		specB = b.spec("node-a", "xhttp")
	}
	if _, err := b.engine.Apply(context.Background(), specB, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	inA, inB := a.engine.inbounds["node-a"], b.engine.inbounds["node-a"]
	if inA.generation == inB.generation {
		t.Fatalf("both engines numbered their inbound %d", inA.generation)
	}
	echo := startEchoServer(t)
	client := newXrayClient(t, specA.Listen.Port, "xhttp", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	defer conn.Close()
	if _, err := exchange(conn, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if trackerSize(inA) == 0 || trackerSize(inB) != 0 {
		t.Fatalf("a connection to engine A is tracked by A=%d, B=%d", trackerSize(inA), trackerSize(inB))
	}
}

func trackerSize(in *inbound) int {
	if in.connTracker == nil {
		return -1
	}
	in.connTracker.mu.Lock()
	defer in.connTracker.mu.Unlock()
	return len(in.connTracker.conns)
}
