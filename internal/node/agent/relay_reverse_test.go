package agent

import (
	"context"
	"crypto/x509"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestRelayModeSwitchIsFailClosedAndKeepsOtherForwards(t *testing.T) {
	l := newRelayLab(t)
	port := freeRelayTestPort(t)
	serve := &plugin.BridgeServe{Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}}
	if err := l.a.bridgeEntry.SetServe(bridge.ServeConfig{EntryNodeID: "nod_msk1", Path: serve.Path, ExitNodeIDs: serve.ExitNodeIDs, Roots: x509.NewCertPool()}); err != nil {
		t.Fatal(err)
	}
	other := plugin.RelayForward{ID: "rly_b", Network: "tcp", Port: port + 1, ToHost: "203.0.113.8", ToPort: 443}
	forward := plugin.RelayForward{ID: "rly_a", Network: "tcp", Port: port, ToHost: "203.0.113.7", ToPort: 443}
	state := func(a plugin.RelayForward) *model {
		m := newModel()
		m.bridgeServe = serve
		m.relays = []plugin.RelayForward{a, other}
		return m
	}
	l.a.syncRelays(context.Background(), state(forward))
	if got := l.host.last(); len(got) != 2 || got[0].ID != "rly_a" || got[1].ID != "rly_b" {
		t.Fatalf("initial DNAT table = %+v", got)
	}

	callbackCalls := 0
	l.host.duringSet = func(fs []hostctl.Forward) {
		callbackCalls++
		listenerOpen := relayPortOpen(port)
		dnatOpen := hasRelayForward(fs, "rly_a")
		if dnatOpen && !listenerOpen {
			t.Errorf("relay port %d listener closed before its replacement DNAT table was installed", port)
		}
		if !hasRelayForward(fs, "rly_b") {
			t.Errorf("other relay disappeared during host sync: %+v", fs)
		}
	}

	reverse := plugin.RelayForward{ID: "rly_a", Network: "tcp", Port: port, ToHost: "must-not-resolve.invalid", ToPort: 65000, Mode: "reverse", ExitNodeID: "nod_de1"}
	l.a.syncRelays(context.Background(), state(reverse))
	if callbackCalls != 1 || l.lookups(reverse.ToHost) != 0 {
		t.Fatalf("reverse switch did %d host updates and %d unwanted name lookups", callbackCalls, l.lookups(reverse.ToHost))
	}
	if got := l.host.last(); len(got) != 1 || got[0].ID != "rly_b" {
		t.Fatalf("DNAT table in reverse mode = %+v, want only other relay", got)
	}
	if relayPortOpen(port) || len(l.a.relay.pending) != 1 {
		t.Fatal("reverse listener started before INPUT firewall synchronization")
	}
	if !l.a.syncInboundPorts(context.Background(), state(reverse), map[string]bool{}, nil) {
		t.Fatal("INPUT firewall synchronization failed")
	}
	l.a.startReverseRelays(true)
	if !relayPortOpen(port) {
		t.Fatal("reverse listener did not start after INPUT firewall synchronization")
	}
	tcpCalls := l.host.tcpPorts()
	if len(tcpCalls) == 0 || !containsRelayPort(tcpCalls[len(tcpCalls)-1], port) {
		t.Fatalf("reverse listener port %d was not included in host inbound firewall sync: %v", port, tcpCalls)
	}
	if res := l.a.relay.results; len(res) != 2 || res[0].RelayId != "rly_a" || !res[0].Ok || !res[1].Ok {
		t.Fatalf("reverse mode relay results = %+v", res)
	}

	dnat := plugin.RelayForward{ID: "rly_a", Network: "tcp", Port: port, ToHost: "203.0.113.7", ToPort: 443}
	l.a.syncRelays(context.Background(), state(dnat))
	if callbackCalls != 2 || relayPortOpen(port) {
		t.Fatalf("DNAT switch left listener open or skipped host sync: callbacks=%d listener=%v", callbackCalls, relayPortOpen(port))
	}
	l.a.syncInboundPorts(context.Background(), state(dnat), map[string]bool{}, nil)
	lastTCPPorts := l.host.tcpPorts()
	if len(lastTCPPorts) == 0 || containsRelayPort(lastTCPPorts[len(lastTCPPorts)-1], port) {
		t.Fatalf("closed reverse listener port %d remained in host inbound firewall sync: %v", port, lastTCPPorts)
	}
	if got := l.host.last(); !reflect.DeepEqual(got, []hostctl.Forward{
		{ID: "rly_a", Network: "tcp", Port: port, ToIP: netip.MustParseAddr("203.0.113.7"), ToPort: 443},
		{ID: "rly_b", Network: "tcp", Port: other.Port, ToIP: netip.MustParseAddr("203.0.113.8"), ToPort: 443},
	}) {
		t.Fatalf("DNAT table after switching back = %+v", got)
	}
	if res := l.a.relay.results; len(res) != 2 || !res[0].Ok || !res[1].Ok {
		t.Fatalf("DNAT mode relay results = %+v", res)
	}
}

func containsRelayPort(ports []uint16, want uint16) bool {
	for _, port := range ports {
		if port == want {
			return true
		}
	}
	return false
}

func freeRelayTestPort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	if port == 65535 {
		t.Fatal("test listener port cannot be incremented")
	}
	return port
}

func relayPortOpen(port uint16) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 75*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func hasRelayForward(fs []hostctl.Forward, id string) bool {
	for _, forward := range fs {
		if forward.ID == id {
			return true
		}
	}
	return false
}

func TestReverseRelayModelConversionKeepsModeAndExit(t *testing.T) {
	wire := &pb.RelayForwards{Items: []*pb.RelayForward{{RelayId: "rly_a", Network: "tcp", Port: 8443, ToHost: "ignored.invalid", ToPort: 444, Mode: "reverse", ExitNodeId: "nod_de1"}}}
	got, rejected := relaysFromPB(wire)
	want := []plugin.RelayForward{{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "ignored.invalid", ToPort: 444, Mode: "reverse", ExitNodeID: "nod_de1"}}
	if len(rejected) != 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("reverse relay conversion = %+v, rejected %v", got, rejected)
	}
	roundTrip := relaysToPB(got)
	if roundTrip == nil || roundTrip.Items[0].Mode != "reverse" || roundTrip.Items[0].ExitNodeId != "nod_de1" {
		t.Fatalf("reverse relay protobuf round trip = %+v", roundTrip)
	}
	plain, rejected := relaysFromPB(&pb.RelayForwards{Items: []*pb.RelayForward{{RelayId: "rly_plain", Network: "tcp", Port: 8443, Mode: "dnat"}}})
	if len(rejected) != 0 || len(plain) != 1 || plain[0].Mode != "" {
		t.Fatalf("dnat mode normalization = %+v, rejected %v", plain, rejected)
	}
}

// The stats goroutine reports the bridges while the worker reconciles; the worker-owned bridge state reaches it only
// through the published snapshot (the race detector is the assertion).
func TestBridgeResultsDoNotRaceTheWorker(t *testing.T) {
	l := newRelayLab(t)
	port := freeRelayTestPort(t)
	ds := &pb.DesiredState{Revision: 1,
		BridgeServe: &pb.BridgeServe{Path: "/private-bridge-path", ExitNodeIds: []string{"nod_de1"}},
		Relays:      &pb.RelayForwards{Items: []*pb.RelayForward{{RelayId: "rly_a", Network: "tcp", Port: uint32(port), Mode: "reverse", ExitNodeId: "nod_de1"}}}}
	if res := l.a.applyDesired(context.Background(), ds); res == nil {
		t.Fatal("the desired state was ignored")
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = l.a.bridgeResults()
			}
		}
	}()
	for i := 0; i < 50; i++ {
		l.sweep()
	}
	close(stop)
	<-done
}
