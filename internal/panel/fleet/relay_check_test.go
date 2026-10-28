package fleet

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	accesssvc "github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// CheckRelay (relay_check.go) against fake agents: the entry E, the exit X (a UDP server on 4443 and a TCP one on 8443) and two
// other nodes, s1 and s2, that can send. The relay forwards E:8443/udp and E:7443/tcp.

const (
	addrEntry = "198.51.100.20"
	addrExit  = "203.0.113.10"
)

type checkEnv struct {
	*relayEnv
	s1, s2         *agent
	ec, xc, c1, c2 *conn // the connected ones
	em             *model
}

func newCheckEnv(t *testing.T, online ...string) *checkEnv {
	t.Helper()
	reg, err := protocols.NewRegistry(fakeProto{"fakehy", plugin.ClientHapp}, fakeProto{"fakewg", plugin.ClientAmnezia},
		tcpProto{fakeProto{"vless", plugin.ClientMihomo}})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, reg)
	x := &checkEnv{relayEnv: &relayEnv{env: e, entry: e.enroll("entry"), exit: e.enroll("exit")}, s1: e.enroll("s1"), s2: e.enroll("s2")}
	now := time.Now().Unix()
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES ('prf_hy', 'fakehy', 'hy', '{"port":4443}', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at) VALUES (?, 'prf_hy', ?, 1, 'active', ?, ?)`, relayInbound, x.exit.nodeID, now, now)
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES ('prf_vless', 'vless', 'vl', '{"port":8443,"network":"tcp","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at) VALUES ('inb_vless', 'prf_vless', ?, 1, 'active', ?, ?)`, x.exit.nodeID, now, now)
	for _, n := range []struct {
		a                         *agent
		address, provider, region string
	}{{x.entry, addrEntry, "p-entry", "RU"}, {x.exit, addrExit, "p-exit", "TR"}, {x.s1, "192.0.2.31", "p-s1", "DE"}, {x.s2, "192.0.2.32", "p-s2", "DE"}} {
		e.exec(`UPDATE node SET state = 'active', address = ?, provider = ?, country_code = ? WHERE id = ?`, n.address, n.provider, n.region, n.a.nodeID)
	}
	e.f.settleUDP = func(context.Context) error { return nil }
	e.run()
	caps := []string{capUDPCheck, capTCPCheck}
	for _, name := range online {
		switch name {
		case "entry":
			x.ec, x.em, _ = connectCaps(x.entry, "entry", append([]string{plugin.CapRelayForward}, caps...)...)
		case "exit":
			x.xc, _, _ = connectCaps(x.exit, "exit", caps...)
		case "s1":
			x.c1, _, _ = connectCaps(x.s1, "s1", caps...)
		case "s2":
			x.c2, _, _ = connectCaps(x.s2, "s2", caps...)
		}
	}
	return x
}

// tcpRelay stores a relay of the entry to the exit's TCP server, on port 7443.
func (x *checkEnv) tcpRelay(on bool) {
	x.t.Helper()
	if err := x.st.CreateRelay(x.ctx, store.Relay{ID: "rly_t", InboundID: "inb_vless", ViaNodeID: x.entry.nodeID, Network: "tcp", Port: 7443}, time.Now()); err != nil {
		x.t.Fatal(err)
	}
	if on {
		x.live("rly_t")
	}
}

// live switches a relay on and has the entry report its forward.
func (x *checkEnv) live(id string) {
	x.t.Helper()
	on := true
	x.setRelay(id, store.RelayPatch{Enabled: &on})
	if err := x.st.RelaysApplied(x.ctx, x.entry.nodeID, []store.RelayApplied{{ID: id, OK: true, ToIP: addrExit}}, time.Now()); err != nil {
		x.t.Fatal(err)
	}
}

type checked struct {
	check store.RelayCheck
	err   error
}

func (x *checkEnv) start(id string, stages ...string) <-chan checked {
	out := make(chan checked, 1)
	go func() {
		c, err := x.f.CheckRelay(x.ctx, id, stages...)
		out <- checked{c, err}
	}()
	return out
}

func result(t *testing.T, ch <-chan checked) store.RelayCheck {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("CheckRelay: %v", r.err)
		}
		return r.check
	case <-time.After(15 * time.Second):
		t.Fatal("CheckRelay did not return")
	}
	return store.RelayCheck{}
}

// udpRun answers one UDP run: the exit arms its counter, the sender sends, the exit reports got of the 300 packets on countPort.
func udpRun(exit, sender *conn, countPort uint16, got uint64) (*agentv1.UdpCount, *agentv1.UdpSend) {
	arm := waitUDPCount(exit, false)
	replyPortCommand(exit, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(sender)
	replyPortCommand(sender, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop := waitUDPCount(exit, true)
	replyPortStop(exit, stop, "4", map[uint16]uint64{countPort: got}, map[uint16]uint64{countPort: got})
	return arm, send
}

// udpRunByPanel is udpRun with the panel as the sender (Fleet.sendUDP is the fake).
func udpRunByPanel(exit *conn, countPort uint16, got uint64) {
	arm := waitUDPCount(exit, false)
	replyPortCommand(exit, arm.RequestId, &agentv1.CommandResult{Ok: true})
	stop := waitUDPCount(exit, true)
	replyPortStop(exit, stop, "4", map[uint16]uint64{countPort: got}, map[uint16]uint64{countPort: got})
}

func tcpRun(c *conn, verdict string, port uint16) *agentv1.TcpProbe {
	p := waitTCPProbe(c)
	replyPortCommand(c, p.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t" + strconv.Itoa(int(port)): verdict}})
	return p
}

// idle fails if any of the connections is sent a port-check command within d.
func idle(t *testing.T, d time.Duration, conns ...*conn) {
	t.Helper()
	deadline := time.After(d)
	for {
		for _, c := range conns {
			if c == nil {
				continue
			}
			select {
			case m := <-c.in:
				if m.GetHttpMark() != nil || m.GetTcpProbe() != nil || m.GetUdpCount() != nil || m.GetUdpSend() != nil {
					t.Fatalf("a port check command was sent: %+v", m)
				}
			default:
			}
		}
		select {
		case <-deadline:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCheckRelayUDPLegThenPathCountedAtTheExit(t *testing.T) {
	x := newCheckEnv(t, "entry", "exit", "s1", "s2")
	x.relay("rly_u", 8443, false)
	x.live("rly_u")
	res := x.start("rly_u")

	// The leg: the entry sends to the exit's address and server port; the exit counts that port.
	arm, send := udpRun(x.xc, x.ec, 4443, 300)
	if len(arm.Ports) != 1 || arm.Ports[0] != 4443 || arm.HoldS != 30 || send.Host != addrExit || len(send.Ports) != 1 || send.Ports[0] != 4443 || send.Count != 300 {
		t.Fatalf("leg: arm %+v send %+v", arm, send)
	}
	// The path: s1 sends to the entry's address and forwarded port; the exit counts its own port.
	arm2, send2 := udpRun(x.xc, x.c1, 4443, 300)
	if len(arm2.Ports) != 1 || arm2.Ports[0] != 4443 || send2.Host != addrEntry || len(send2.Ports) != 1 || send2.Ports[0] != 8443 || string(send2.Tag) != string(arm2.Tag) {
		t.Fatalf("path: arm %+v send %+v", arm2, send2)
	}
	c := result(t, res)
	if !c.OK || !c.Verified() || c.Stage != "" || c.Network != "udp" || c.Port != 8443 || c.To != addrExit+":4443" {
		t.Fatalf("check = %+v", c)
	}
	if c.Leg.Status != "ok" || c.Leg.Sender != x.entry.nodeID || c.Leg.Sent != 300 || c.Leg.Got != 300 || c.Leg.Verdict != "ok" {
		t.Errorf("leg = %+v", c.Leg)
	}
	if c.Path.Status != "ok" || c.Path.Sender != x.s1.nodeID || c.Path.Got != 300 {
		t.Errorf("path = %+v", c.Path)
	}
	// Never the entry or the exit as the path's sender: the entry sent once (the leg), the exit sent nothing, s2 was not needed.
	idle(t, 150*time.Millisecond, x.ec, x.xc, x.c2)
	// What was found is the relay's.
	l, err := x.st.RelayLink(x.ctx, "rly_u")
	if err != nil {
		t.Fatal(err)
	}
	if stored, ok := l.Check(); !ok || stored != c || !l.Proven() {
		t.Errorf("stored = %+v ok=%v proven=%v", stored, ok, l.Proven())
	}
}

func TestCheckRelayLegFailureStopsBeforeThePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  uint64
		code string
	}{{"nothing arrives", 0, "broken"}, {"two thirds arrive", 200, "broken"}, {"a fifth is lost", 240, "lossy"}} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCheckEnv(t, "entry", "exit", "s1")
			x.relay("rly_u", 8443, false)
			x.live("rly_u")
			res := x.start("rly_u")
			udpRun(x.xc, x.ec, 4443, tc.got)
			c := result(t, res)
			if c.OK || c.Stage != "leg" || c.Leg.Status != "fail" || c.Leg.Code != tc.code || c.Leg.Got != uint32(tc.got) || c.Path.Status != "skipped" || c.Path.Code != "leg_failed" {
				t.Fatalf("check = %+v", c)
			}
			idle(t, 100*time.Millisecond, x.c1) // no third party was asked
		})
	}
}

func TestCheckRelayLegNeedsTheEntryOnlineWithTheCapability(t *testing.T) {
	x := newCheckEnv(t, "exit", "s1") // the entry is not connected
	x.relay("rly_u", 8443, false)
	c, err := x.f.CheckRelay(x.ctx, "rly_u")
	if err != nil || c.Leg.Status != "fail" || c.Leg.Code != "node_offline" || c.Stage != "leg" {
		t.Fatalf("offline entry: %+v %v", c, err)
	}
	idle(t, 50*time.Millisecond, x.xc, x.c1) // nobody else stood in for the entry: the leg is the entry's

	x = newCheckEnv(t, "entry", "s1")
	x.relay("rly_u", 8443, false)
	if c, err = x.f.CheckRelay(x.ctx, "rly_u"); err != nil || c.Leg.Code != "exit_offline" {
		t.Fatalf("offline exit (UDP is counted there): %+v %v", c, err)
	}

	// An entry whose agent has no UDP check cannot send.
	x = newCheckEnv(t, "exit", "s1")
	x.ec, _, _ = connectCaps(x.entry, "entry-old", plugin.CapRelayForward)
	x.relay("rly_u", 8443, false)
	if c, err = x.f.CheckRelay(x.ctx, "rly_u"); err != nil || c.Leg.Code != "agent_too_old" {
		t.Fatalf("old entry: %+v %v", c, err)
	}
}

func TestCheckRelayPathTriesAnotherSenderAndNeverTheEntryOrTheExit(t *testing.T) {
	x := newCheckEnv(t, "entry", "exit", "s1", "s2")
	x.relay("rly_u", 8443, false)
	x.live("rly_u")
	var panelCalls int
	var mu sync.Mutex
	x.f.sendUDP = func(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error) {
		mu.Lock()
		panelCalls++
		mu.Unlock()
		return "4", 300, nil
	}

	// s1 cannot get through, s2 can: the path passes, from s2.
	res := x.start("rly_u", store.RelayStagePath)
	_, send1 := udpRun(x.xc, x.c1, 4443, 0)
	_, send2 := udpRun(x.xc, x.c2, 4443, 300)
	c := result(t, res)
	if send1.Host != addrEntry || send2.Host != addrEntry || c.Path.Status != "ok" || c.Path.Sender != x.s2.nodeID {
		t.Fatalf("second sender: %+v / %+v / %+v", send1, send2, c.Path)
	}
	// The leg was not asked for: it keeps what it had (nothing), so the check is not ok as a whole.
	if c.Leg.Status != "" || c.OK {
		t.Errorf("a path-only check of a relay never checked on its leg: %+v", c)
	}

	// Both fail: the first finding stands, and the panel is not asked (nodes could, and found it broken).
	res = x.start("rly_u", store.RelayStagePath)
	udpRun(x.xc, x.c1, 4443, 100)
	udpRun(x.xc, x.c2, 4443, 250)
	c = result(t, res)
	if c.Path.Status != "fail" || c.Path.Code != "broken" || c.Path.Sender != x.s1.nodeID || c.Path.Got != 100 || c.Stage != "path" {
		t.Fatalf("both fail: %+v", c.Path)
	}
	idle(t, 100*time.Millisecond, x.ec, x.xc)
	mu.Lock()
	defer mu.Unlock()
	if panelCalls != 0 {
		t.Errorf("the panel sent %d times while nodes could", panelCalls)
	}
}

func TestCheckRelayPathFallsBackToThePanelOnlyWhenNoNodeCan(t *testing.T) {
	var host string
	var ports []uint16
	var mu sync.Mutex
	sends := 0
	panel := func(_ context.Context, h string, p []uint16, _ [8]byte, _, _, _ int) (string, int, error) {
		mu.Lock()
		defer mu.Unlock()
		host, ports, sends = h, p, sends+1
		return "4", 300, nil
	}

	t.Run("only the entry and the exit are online", func(t *testing.T) {
		x := newCheckEnv(t, "entry", "exit")
		x.relay("rly_u", 8443, false)
		x.live("rly_u")
		x.f.sendUDP = panel
		res := x.start("rly_u")
		udpRun(x.xc, x.ec, 4443, 300) // the leg, by the entry
		udpRunByPanel(x.xc, 4443, 300)
		c := result(t, res)
		mu.Lock()
		defer mu.Unlock()
		if c.Path.Status != "ok" || c.Path.Sender != "panel" || sends != 1 || host != addrEntry || len(ports) != 1 || ports[0] != 8443 {
			t.Fatalf("path = %+v, panel sent %d times to %s %v", c.Path, sends, host, ports)
		}
		idle(t, 100*time.Millisecond, x.ec, x.xc) // neither was asked to send the path
	})

	t.Run("a node that cannot run the probe is replaced by the panel", func(t *testing.T) {
		mu.Lock()
		sends = 0
		mu.Unlock()
		x := newCheckEnv(t, "entry", "exit", "s1")
		x.relay("rly_u", 8443, false)
		x.live("rly_u")
		x.f.sendUDP = panel
		res := x.start("rly_u", store.RelayStagePath)
		arm := waitUDPCount(x.xc, false)
		replyPortCommand(x.xc, arm.RequestId, &agentv1.CommandResult{Ok: true})
		send := waitUDPSend(x.c1)
		replyPortCommand(x.c1, send.RequestId, &agentv1.CommandResult{Error: "busy"})
		stop := waitUDPCount(x.xc, true)
		replyPortStop(x.xc, stop, "4", map[uint16]uint64{}, map[uint16]uint64{})
		udpRunByPanel(x.xc, 4443, 300)
		c := result(t, res)
		mu.Lock()
		defer mu.Unlock()
		if c.Path.Status != "ok" || c.Path.Sender != "panel" || sends != 1 {
			t.Fatalf("path = %+v, panel sent %d times", c.Path, sends)
		}
	})

	t.Run("a panel that has no route fails the path", func(t *testing.T) {
		x := newCheckEnv(t, "entry", "exit")
		x.relay("rly_u", 8443, false)
		x.live("rly_u")
		x.f.sendUDP = func(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error) {
			return "", 0, errors.New("no socket")
		}
		res := x.start("rly_u", store.RelayStagePath)
		arm := waitUDPCount(x.xc, false)
		replyPortCommand(x.xc, arm.RequestId, &agentv1.CommandResult{Ok: true})
		stop := waitUDPCount(x.xc, true)
		replyPortStop(x.xc, stop, "4", map[uint16]uint64{}, map[uint16]uint64{})
		c := result(t, res)
		if c.Path.Status != "fail" || c.Path.Code != "failed" || c.Path.Sender != "panel" || c.Stage != "path" {
			t.Fatalf("path = %+v", c.Path)
		}
	})

	t.Run("the panel on the entry's own host proves nothing", func(t *testing.T) {
		x := newCheckEnv(t, "entry", "exit")
		x.exec(`UPDATE node SET address = '127.0.0.1' WHERE id = ?`, x.entry.nodeID)
		x.relay("rly_u", 8443, false)
		x.live("rly_u")
		x.f.sendUDP = func(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error) {
			t.Error("the panel sent from the entry's own host")
			return "4", 300, nil
		}
		c, err := x.f.CheckRelay(x.ctx, "rly_u", store.RelayStagePath)
		if err != nil || c.Path.Status != "fail" || c.Path.Code != "same_host" {
			t.Fatalf("path = %+v %v", c.Path, err)
		}
	})
}

func TestCheckRelayPathOfARelayThatIsNotForwardingYet(t *testing.T) {
	x := newCheckEnv(t, "entry", "exit", "s1")
	x.relay("rly_u", 8443, false)
	// off: there is no forward to reach
	res := x.start("rly_u")
	udpRun(x.xc, x.ec, 4443, 300)
	c := result(t, res)
	if !c.OK || c.Verified() || c.Leg.Status != "ok" || c.Path.Status != "skipped" || c.Path.Code != "off" {
		t.Fatalf("off: %+v", c)
	}
	// on, but the entry has not reported
	on := true
	x.setRelay("rly_u", store.RelayPatch{Enabled: &on})
	res = x.start("rly_u", store.RelayStagePath)
	if c = result(t, res); c.Path.Status != "skipped" || c.Path.Code != "pending" {
		t.Fatalf("pending: %+v", c.Path)
	}
	// on, and the entry reported that it could not install it
	if err := x.st.RelaysApplied(x.ctx, x.entry.nodeID, []store.RelayApplied{{ID: "rly_u", Error: "forward_drop"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if c = result(t, x.start("rly_u", store.RelayStagePath)); c.Path.Status != "fail" || c.Path.Code != "apply_failed" || c.Stage != "path" {
		t.Fatalf("failed: %+v", c.Path)
	}
	idle(t, 100*time.Millisecond, x.c1)
}

func TestCheckRelayPathOnlyKeepsTheLegOfTheSameTarget(t *testing.T) {
	x := newCheckEnv(t, "entry", "exit", "s1")
	x.relay("rly_u", 8443, false)
	res := x.start("rly_u", store.RelayStageLeg)
	udpRun(x.xc, x.ec, 4443, 300)
	leg := result(t, res)
	if leg.Path.Status != "" || !leg.OK {
		t.Fatalf("leg only: %+v", leg)
	}
	x.live("rly_u")
	res = x.start("rly_u", store.RelayStagePath)
	udpRun(x.xc, x.c1, 4443, 300)
	c := result(t, res)
	if !c.Verified() || c.Leg != leg.Leg {
		t.Fatalf("path after leg: %+v (leg was %+v)", c, leg.Leg)
	}
	// The exit moves: what the leg proved is about another target.
	x.exec(`UPDATE node SET address = '203.0.113.99' WHERE id = ?`, x.exit.nodeID)
	res = x.start("rly_u", store.RelayStagePath)
	udpRun(x.xc, x.c1, 4443, 300)
	if c = result(t, res); c.Leg.Status != "" || c.Verified() || c.To != "203.0.113.99:4443" {
		t.Fatalf("after the exit moved: %+v", c)
	}
}

func TestCheckRelayTCPProvesTheServerAtBothStages(t *testing.T) {
	x := newCheckEnv(t, "entry", "exit", "s1", "s2")
	x.exec(`UPDATE inbound SET cert_pin_sha256 = ? WHERE id = 'inb_vless'`, strings.Repeat("ab", 32))
	x.tcpRelay(true)
	res := x.start("rly_t")
	leg := tcpRun(x.ec, "ours", 8443)
	if leg.Host != addrExit || len(leg.Targets) != 1 || leg.Targets[0].Port != 8443 || leg.Targets[0].Kind != "tls" || leg.Targets[0].Sni != "www.example.org" || leg.Targets[0].PinSha256 != strings.Repeat("ab", 32) {
		t.Fatalf("leg probe = %+v", leg)
	}
	path := tcpRun(x.c1, "ours", 7443)
	if path.Host != addrEntry || len(path.Targets) != 1 || path.Targets[0].Port != 7443 || path.Targets[0].Sni != "www.example.org" || path.Targets[0].PinSha256 != strings.Repeat("ab", 32) {
		t.Fatalf("path probe = %+v", path)
	}
	c := result(t, res)
	if !c.Verified() || c.Leg.Verdict != "ours" || c.Leg.Sender != x.entry.nodeID || c.Path.Sender != x.s1.nodeID || c.Network != "tcp" || c.Port != 7443 || c.To != addrExit+":8443" {
		t.Fatalf("check = %+v", c)
	}
	idle(t, 100*time.Millisecond, x.xc, x.c2) // TCP arms nothing on the exit

	// Something else answers the forwarded port; another sender sees the same.
	res = x.start("rly_t")
	tcpRun(x.ec, "ours", 8443)
	tcpRun(x.c1, "other", 7443)
	tcpRun(x.c2, "timeout", 7443)
	c = result(t, res)
	if c.Stage != "path" || c.Path.Code != "other" || c.Path.Sender != x.s1.nodeID || c.Path.Verdict != "other" {
		t.Fatalf("a wrong server on the entry: %+v", c.Path)
	}

	// The leg: the exit's port answers with something that is not ours.
	res = x.start("rly_t")
	tcpRun(x.ec, "refused", 8443)
	if c = result(t, res); c.Stage != "leg" || c.Leg.Code != "refused" || c.Path.Code != "leg_failed" {
		t.Fatalf("refused leg: %+v", c)
	}
	idle(t, 100*time.Millisecond, x.c1)
}

func TestCheckRelayTCPPanelFallback(t *testing.T) {
	x := newCheckEnv(t, "entry", "exit")
	x.tcpRelay(true)
	var hosts []string
	x.f.probeTCP = func(_ context.Context, host string, targets []tcpcheck.Target) (string, map[uint16]string, error) {
		hosts = append(hosts, host)
		out := map[uint16]string{}
		for _, t := range targets {
			out[t.Port] = "ours"
		}
		return "4", out, nil
	}
	res := x.start("rly_t")
	tcpRun(x.ec, "ours", 8443)
	c := result(t, res)
	if !c.Verified() || c.Path.Sender != "panel" || len(hosts) != 1 || hosts[0] != addrEntry {
		t.Fatalf("check = %+v, panel probed %v", c, hosts)
	}
}

// The exit has one UDP counter table: a relay check and a port check take turns, whichever comes first.
func TestCheckRelayAndPortChecksShareTheExitsGate(t *testing.T) {
	t.Run("the relay check holds it", func(t *testing.T) {
		x := newCheckEnv(t, "entry", "exit")
		x.relay("rly_u", 8443, false)
		res := x.start("rly_u")
		arm := waitUDPCount(x.xc, false) // the leg has armed the exit
		port := make(chan portCheckFleetResult, 1)
		go func() {
			rows, sender, code := x.f.CheckPorts(x.ctx, x.exit.nodeID, nil)
			port <- portCheckFleetResult{rows, sender, code}
		}()
		waitPortCheckWaiter(t, x.f, x.exit.nodeID)
		idle(t, 200*time.Millisecond, x.xc) // the port check did not arm a second table
		replyPortCommand(x.xc, arm.RequestId, &agentv1.CommandResult{Ok: true})
		send := waitUDPSend(x.ec)
		replyPortCommand(x.ec, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
		stop := waitUDPCount(x.xc, true)
		replyPortStop(x.xc, stop, "4", map[uint16]uint64{4443: 300}, map[uint16]uint64{4443: 300})
		if c := result(t, res); c.Leg.Status != "ok" {
			t.Fatalf("leg = %+v", c.Leg)
		}
		// now the port check has its turn, with its own commands (not the relay check's result)
		driveUDPPart(x.xc, x.ec)
		if got := <-port; got.code != "" || got.sender != x.entry.nodeID || len(got.checks) == 0 {
			t.Fatalf("port check = %+v", got)
		}
	})

	t.Run("the port check holds it", func(t *testing.T) {
		x := newCheckEnv(t, "entry", "exit")
		x.relay("rly_u", 8443, false)
		port := make(chan portCheckFleetResult, 1)
		go func() {
			rows, sender, code := x.f.CheckPorts(x.ctx, x.exit.nodeID, nil)
			port <- portCheckFleetResult{rows, sender, code}
		}()
		arm := waitUDPCount(x.xc, false)
		res := x.start("rly_u")
		waitPortCheckWaiter(t, x.f, x.exit.nodeID)
		idle(t, 200*time.Millisecond, x.xc)
		replyPortCommand(x.xc, arm.RequestId, &agentv1.CommandResult{Ok: true})
		send := waitUDPSend(x.ec)
		replyPortCommand(x.ec, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
		stop := waitUDPCount(x.xc, true)
		got, packets := map[uint16]uint64{}, map[uint16]uint64{}
		for _, p := range arm.Ports {
			got[uint16(p)], packets[uint16(p)] = 300, 300
		}
		replyPortStop(x.xc, stop, "4", got, packets)
		if p := <-port; p.code != "" {
			t.Fatalf("port check = %+v", p)
		}
		udpRun(x.xc, x.ec, 4443, 300) // the relay's leg
		if c := result(t, res); c.Leg.Status != "ok" {
			t.Fatalf("leg = %+v", c.Leg)
		}
	})
}

// The whole of it, with the access module's rules: no switch without a fresh leg; a path that fails switches the relay off and
// takes the forward off the entry; one that passes keeps it, and the relay is proven.
func TestSwitchingARelayOnEndToEnd(t *testing.T) {
	x := newCheckEnv(t, "entry", "exit", "s1")
	acc, err := accesssvc.New(x.st, x.v, x.f.reg, x.f, nil, accesssvc.Config{CheckRelay: x.f.CheckRelay})
	if err != nil {
		t.Fatal(err)
	}
	acc.SetRelayWaitForTest(10*time.Second, 10*time.Millisecond)
	x.relay("rly_u", 8443, false)
	on := true
	enable := func() error {
		_, err := acc.UpdateRelay(x.ctx, connect.NewRequest(&adminv1.UpdateRelayRequest{Id: "rly_u", Enabled: &on}))
		return err
	}
	stateOf := func() store.RelayLink {
		l, err := x.st.RelayLink(x.ctx, "rly_u")
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	// No check yet: refused, and the entry hears nothing.
	if err := enable(); err == nil || err.Error() != "failed_precondition: relay_unchecked" {
		t.Fatalf("without a check: %v", err)
	}
	x.ec.quiet(200 * time.Millisecond)

	// CheckRelay of a relay that is off: the leg only.
	type rpc struct {
		resp *connect.Response[adminv1.CheckRelayResponse]
		err  error
	}
	checkRPC := make(chan rpc, 1)
	go func() {
		r, err := acc.CheckRelay(x.ctx, connect.NewRequest(&adminv1.CheckRelayRequest{Id: "rly_u"}))
		checkRPC <- rpc{r, err}
	}()
	udpRun(x.xc, x.ec, 4443, 300)
	got := <-checkRPC
	if got.err != nil || !got.resp.Msg.Relay.Check.Ok || got.resp.Msg.Relay.Check.Verified || got.resp.Msg.Relay.Check.Path.Code != "off" || got.resp.Msg.Relay.Enabled {
		t.Fatalf("check of a relay that is off: %+v %v", got.resp, got.err)
	}

	// Switch on: the entry installs the forward; the path is broken: the relay goes off again, and so does the forward.
	switchOn := func(pathGot uint64) error {
		done := make(chan error, 1)
		go func() { done <- enable() }()
		ds := x.ec.next(x.em)
		if ds.Relays == nil || len(ds.Relays.Items) != 1 || ds.Relays.Items[0].Port != 8443 || ds.Relays.Items[0].ToHost != addrExit {
			t.Errorf("the forward the entry is told to install: %v", ds.Relays)
		}
		r := applied(ds, x.em.hash())
		r.GetApplyResult().Relays = []*agentv1.RelayResult{{RelayId: "rly_u", Ok: true, ToIp: addrExit}}
		x.ec.send(0, r)
		udpRun(x.xc, x.c1, 4443, pathGot)
		return <-done
	}
	err = switchOn(0)
	if err == nil || err.Error() != "failed_precondition: relay_check_failed: code=broken&stage=path" {
		t.Fatalf("a broken path: %v", err)
	}
	if l := stateOf(); l.Enabled || l.Proven() {
		t.Fatalf("after a failed path: enabled=%v proven=%v", l.Enabled, l.Proven())
	}
	if ds := x.ec.next(x.em); ds.Relays == nil || len(ds.Relays.Items) != 0 || len(x.em.relays) != 0 {
		t.Fatalf("the entry still forwards: %v", ds.Relays)
	}

	// The leg stays good for the next try; the path now passes.
	if err = switchOn(300); err != nil {
		t.Fatalf("a working path: %v", err)
	}
	if l := stateOf(); !l.Enabled || !l.Proven() || l.State != "active" {
		t.Fatalf("after a passing path: enabled=%v proven=%v state=%s", l.Enabled, l.Proven(), l.State)
	}
	x.ec.quiet(200 * time.Millisecond)
	if len(x.em.relays) != 1 {
		t.Errorf("the entry lost the forward: %v", x.em.relays)
	}
}
