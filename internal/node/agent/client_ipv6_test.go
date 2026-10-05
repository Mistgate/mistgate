package agent

import (
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// "IPv6 for clients" off (NodeSettings.client_ipv6_disabled): the tunnel firewall gets RejectV6, and it goes away again
// when the switch is back on. The agent says it can do it in Hello.
func TestClientIPv6OffReachesTheTunnelFirewallAndBack(t *testing.T) {
	x := newL3(t, 3, true)
	hello := <-x.panel.hellos
	if !contains(hello.Capabilities, "client-ipv6/1") {
		t.Fatalf("the agent does not list client-ipv6/1: %v", hello.Capabilities)
	}
	x.waitConnected()

	ds := fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
	ds.Settings = &pb.NodeSettings{}
	mustApply(t, x.panel, ds)
	if ts := x.tun.lastSet(); len(ts) != 1 || ts[0].RejectV6 {
		t.Fatalf("default settings: %+v", ts)
	}
	if x.a.ClientIPv6Disabled() {
		t.Fatal("ClientIPv6Disabled() is true by default")
	}

	n := x.tun.setCount()
	mustApply(t, x.panel, &pb.DesiredState{Revision: 2, BaseRevision: 1, Settings: &pb.NodeSettings{ClientIpv6Disabled: true}})
	eventually(t, func() bool { return x.tun.setCount() > n }, "the tunnel table is replaced")
	if ts := x.tun.lastSet(); len(ts) != 1 || !ts[0].RejectV6 || ts[0].Subnet6.IsValid() == false {
		t.Fatalf("switch off: %+v", ts)
	}
	if !x.a.ClientIPv6Disabled() {
		t.Fatal("ClientIPv6Disabled() is false after the switch went off")
	}

	n = x.tun.setCount()
	mustApply(t, x.panel, &pb.DesiredState{Revision: 3, BaseRevision: 2, Settings: &pb.NodeSettings{}})
	eventually(t, func() bool { return x.tun.setCount() > n }, "the tunnel table is replaced again")
	if ts := x.tun.lastSet(); len(ts) != 1 || ts[0].RejectV6 {
		t.Fatalf("switch back on: %+v", ts)
	}
}

// The switch was changed while the node was offline: the panel sends nothing on reconnect when the inbound hashes match
// (the settings are not in the hash), so the HelloAck is the only news. The tunnel firewall follows at once, not at
// the next sweep.
func TestClientIPv6SwitchChangedWhileOfflineReachesTheFirewallAtOnce(t *testing.T) {
	x := newL3Tuned(t, 3, false, func(a *Agent) { a.sweepEvery = time.Hour })
	x.waitConnected()
	ds := fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
	ds.Settings = &pb.NodeSettings{}
	mustApply(t, x.panel, ds)
	if ts := x.tun.lastSet(); len(ts) != 1 || ts[0].RejectV6 {
		t.Fatalf("default settings: %+v", ts)
	}

	n, conns := x.tun.setCount(), x.panel.connCount()
	x.panel.settings.Store(&pb.NodeSettings{ClientIpv6Disabled: true}) // changed while the node is away
	x.panel.dropConn()
	eventually(t, func() bool { return x.panel.connCount() > conns }, "the agent reconnects")
	eventually(t, func() bool { return x.tun.setCount() > n && len(x.tun.lastSet()) == 1 && x.tun.lastSet()[0].RejectV6 }, "the tunnel table follows the new HelloAck settings")
}
