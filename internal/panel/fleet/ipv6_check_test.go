package fleet

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// CheckIPv6 (ipv6_check.go) against fake agents. The target is tcpCheckTarget's node: a Hysteria2 server with an ACME site on
// 443 (UDP 443, TCP 443 and 80) and a VLESS REALITY server on TCP 8443. Documentation addresses only.

const (
	v6Entry = "2001:db8::5"
	v6Other = "2001:db8:0:1::7"
)

// doctorIPv6 stores the doctor's ipv6 check of a node: the params an agent with the uplink facts sends.
func (x *l3Env) doctorIPv6(nodeID string, params map[string]string) {
	x.t.Helper()
	row := store.DoctorRow{NodeID: nodeID, CheckID: "ipv6", Status: 1, TitleKey: "ipv6", Params: params}
	if err := x.st.PutDoctor(x.ctx, nodeID, []store.DoctorRow{row}, false, time.Now()); err != nil {
		x.t.Fatal(err)
	}
}

func v6Works(addrs string) map[string]string {
	return map[string]string{"ipv6": "yes", "connect": "ok", "iface": "eth0", "addrs": addrs, "src": strings.Split(addrs, "/")[0]}
}

// v6Peer is a node that can send over IPv6: its doctor says outbound IPv6 works.
func (x *l3Env) v6Peer(name string) (*agent, *conn) {
	x.t.Helper()
	p, c := addPortCheckPeer(x, name, "provider-b", "DE", capUDPCheck, capTCPCheck)
	x.doctorIPv6(p.nodeID, v6Works("2001:db8:ffff::"+name[len(name)-1:]+"/64"))
	return p, c
}

func (x *l3Env) checkIPv6(nodeID, addr string) <-chan ipv6Done {
	out := make(chan ipv6Done, 1)
	go func() {
		c, err := x.f.CheckIPv6(x.ctx, nodeID, addr)
		out <- ipv6Done{c, err}
	}()
	return out
}

type ipv6Done struct {
	check store.IPv6Check
	err   error
}

// ipv6Target is tcpCheckTarget with a panel that has no IPv6 of its own, unless a test says otherwise (the real check would look
// at the machine the tests run on).
func ipv6Target(t *testing.T) (*l3Env, *agent, *conn) {
	t.Helper()
	x, target, c := tcpCheckTarget(t)
	x.f.panelIPv6 = func(context.Context) bool { return false }
	return x, target, c
}

func awaitIPv6(t *testing.T, ch <-chan ipv6Done) store.IPv6Check {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("CheckIPv6: %v", r.err)
		}
		return r.check
	case <-time.After(15 * time.Second):
		t.Fatal("CheckIPv6 did not return")
	}
	return store.IPv6Check{}
}

// driveUDP6 answers the UDP half with a node sender over IPv6: the target arms its counter, the sender sends to addr, the target
// counts got of 300 on each port. It returns what the sender was told.
func driveUDP6(targetConn, senderConn *conn, got uint64) *agentv1.UdpSend {
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(senderConn)
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "6", "sent": "300"}})
	stop := waitUDPCount(targetConn, true)
	counts := map[uint16]uint64{}
	for _, p := range arm.Ports {
		counts[uint16(p)] = got
	}
	replyPortStop(targetConn, stop, "6", counts, counts)
	return send
}

func verdictOf(c store.IPv6Check, network string, port uint16) string {
	for _, v := range c.Verdicts {
		if v.Net == network && v.Port == port {
			return v.Verdict
		}
	}
	return "<none>"
}

func TestCheckIPv6SendsFromAnIPv6NodeToTheAddressAndWritesNoPortCheck(t *testing.T) {
	x, target, targetConn := ipv6Target(t)
	sender, senderConn := x.v6Peer("sender1")
	v4only, v4Conn := addPortCheckPeer(x, "oldv4", "provider-c", "NL", capUDPCheck, capTCPCheck)
	x.doctorIPv6(v4only.nodeID, map[string]string{"ipv6": "no"})
	ch := x.checkIPv6(target.nodeID, v6Entry)

	send := driveUDP6(targetConn, senderConn, 300)
	if send.Host != v6Entry || len(send.Ports) != 1 || send.Ports[0] != 443 {
		t.Fatalf("UDP send = %+v, want %s on 443", send, v6Entry)
	}
	probe := waitTCPProbe(senderConn)
	if probe.Host != v6Entry || len(probe.Targets) != 2 {
		t.Fatalf("TCP probe = %+v", probe)
	}
	// the names are those of the main address; the HTTP-01 mark (port 80) is not part of an IPv6 check
	want := []*agentv1.TcpTarget{{Port: 443, Kind: "tls", Sni: "node-a.example.com"}, {Port: 8443, Kind: "tls", Sni: "www.example.org"}}
	for i, w := range want {
		if g := probe.Targets[i]; g.Port != w.Port || g.Kind != w.Kind || g.Sni != w.Sni || len(g.Token) != 0 {
			t.Errorf("target %d = %+v, want %+v", i, g, w)
		}
	}
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "6", "t443": "ours", "t8443": "ours"}})

	got := awaitIPv6(t, ch)
	if !got.Pass || got.Code != "" || got.By != sender.nodeID || got.Addr != v6Entry || got.At == 0 {
		t.Fatalf("check = %+v", got)
	}
	if verdictOf(got, "tcp", 443) != "ours" || verdictOf(got, "tcp", 8443) != "ours" || verdictOf(got, "udp", 443) != "ok" || len(got.Verdicts) != 3 {
		t.Errorf("verdicts = %+v", got.Verdicts)
	}
	// the main address's results are not touched, and the node that has no IPv6 was never asked
	if rows, err := x.st.PortChecks(x.ctx, target.nodeID); err != nil || len(rows) != 0 {
		t.Errorf("port_check rows: %+v, %v", rows, err)
	}
	if rows, err := x.st.TCPChecks(x.ctx, target.nodeID); err != nil || len(rows) != 0 {
		t.Errorf("tcp_check rows: %+v, %v", rows, err)
	}
	assertNoPortCommands(t, v4Conn)
	var params string
	if err := x.st.R.QueryRowContext(x.ctx, `SELECT params FROM audit WHERE action = 'node.ipv6_check'`).Scan(&params); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	for _, w := range []string{`"pass":"true"`, `"address":"` + v6Entry + `"`, `"by":"` + sender.nodeID + `"`, `tcp/443=ours`, `udp/443=ok`} {
		if !strings.Contains(params, w) {
			t.Errorf("audit %s lacks %s", params, w)
		}
	}
}

func TestCheckIPv6ReportsAPortThatDoesNotAnswerAsTheNodesOwn(t *testing.T) {
	x, target, targetConn := ipv6Target(t)
	_, senderConn := x.v6Peer("sender1")
	ch := x.checkIPv6(target.nodeID, v6Entry)
	driveUDP6(targetConn, senderConn, 300)
	probe := waitTCPProbe(senderConn)
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "6", "t443": "ours", "t8443": "other"}})
	got := awaitIPv6(t, ch)
	if got.Pass || got.Code != "" || verdictOf(got, "tcp", 8443) != "other" {
		t.Fatalf("check = %+v: a port that answers as another service must fail the check, with its verdict", got)
	}
}

func TestCheckIPv6AnInconclusiveUDPRunIsAFindingNotASkip(t *testing.T) {
	x, target, targetConn := ipv6Target(t)
	_, senderConn := x.v6Peer("sender1")
	ch := x.checkIPv6(target.nodeID, v6Entry)
	driveUDP6(targetConn, senderConn, 0) // nothing arrives
	probe := waitTCPProbe(senderConn)
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "6", "t443": "timeout", "t8443": "timeout"}})
	got := awaitIPv6(t, ch)
	if got.Pass || got.Code != "" || verdictOf(got, "udp", 443) != "broken" || verdictOf(got, "tcp", 443) != "timeout" {
		t.Fatalf("check = %+v", got)
	}
}

func TestCheckIPv6TakesTheNextSenderWhenTheFirstHasNoRoute(t *testing.T) {
	x, target, targetConn := ipv6Target(t)
	_, c1 := x.v6Peer("sender1")
	s2, c2 := x.v6Peer("sender2")
	ch := x.checkIPv6(target.nodeID, v6Entry)

	// sender1 says it has no route for the UDP part (its doctor was wrong): sender2 takes over
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(c1)
	replyPortCommand(c1, send.RequestId, &agentv1.CommandResult{Ok: false, Error: "no_route"})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "6", map[uint16]uint64{}, map[uint16]uint64{})
	driveUDP6(targetConn, c2, 300)

	// the TCP half has its own sender: sender1 first, no route again, then sender2
	p1 := waitTCPProbe(c1)
	replyPortCommand(c1, p1.RequestId, &agentv1.CommandResult{Ok: false, Error: "no_route"})
	p2 := waitTCPProbe(c2)
	replyPortCommand(c2, p2.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "6", "t443": "ours", "t8443": "ours"}})

	got := awaitIPv6(t, ch)
	if !got.Pass || got.By != s2.nodeID {
		t.Fatalf("check = %+v, want a pass by %s", got, s2.nodeID)
	}
}

func TestCheckIPv6PanelWithoutIPv6IsNeverAPass(t *testing.T) {
	x, target, targetConn := ipv6Target(t)
	var sent atomic.Int32
	x.f.sendUDP = func(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error) {
		sent.Add(1)
		return "6", 300, nil
	}
	x.f.probeTCP = func(context.Context, string, []tcpcheck.Target) (string, map[uint16]string, error) {
		sent.Add(1)
		return "6", map[uint16]string{443: "ours", 8443: "ours"}, nil
	}
	// a node that has IPv4 only is no sender either
	v4, v4Conn := addPortCheckPeer(x, "oldv4", "provider-c", "NL", capUDPCheck, capTCPCheck)
	x.doctorIPv6(v4.nodeID, map[string]string{"ipv6": "yes", "connect": "failed"})

	got := awaitIPv6(t, x.checkIPv6(target.nodeID, v6Entry))
	if got.Pass || got.Code != "no_ipv6_verifier" || got.By != "" || len(got.Verdicts) != 0 {
		t.Fatalf("check = %+v, want no_ipv6_verifier", got)
	}
	if sent.Load() != 0 {
		t.Error("the panel sent although it has no IPv6")
	}
	assertNoPortCommands(t, targetConn, v4Conn)
}

func TestCheckIPv6PanelWithIPv6SendsLast(t *testing.T) {
	x, target, targetConn := ipv6Target(t)
	var udpHost, tcpHost string
	x.f.panelIPv6 = func(context.Context) bool { return true }
	x.f.sendUDP = func(_ context.Context, host string, _ []uint16, _ [8]byte, _, _, _ int) (string, int, error) {
		udpHost = host
		return "6", 300, nil
	}
	x.f.probeTCP = func(_ context.Context, host string, _ []tcpcheck.Target) (string, map[uint16]string, error) {
		tcpHost = host
		return "6", map[uint16]string{443: "ours", 8443: "ours"}, nil
	}
	ch := x.checkIPv6(target.nodeID, v6Entry)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "6", map[uint16]uint64{443: 300}, map[uint16]uint64{443: 300})
	got := awaitIPv6(t, ch)
	if !got.Pass || got.By != "panel" || udpHost != v6Entry || tcpHost != v6Entry {
		t.Fatalf("check = %+v, udp host %q, tcp host %q", got, udpHost, tcpHost)
	}
}

func TestCheckIPv6GlobalpingIsTheLastResortAndSaysSo(t *testing.T) {
	x, target, targetConn := ipv6Target(t)
	var gotAddr string
	var gotPort uint16
	answered, connected := 2, 1
	var failure error
	x.f.cfg.GlobalpingTCP6 = func(_ context.Context, addr string, port uint16) (int, int, error) {
		gotAddr, gotPort = addr, port
		return answered, connected, failure
	}
	got := awaitIPv6(t, x.checkIPv6(target.nodeID, v6Entry))
	if !got.Pass || got.By != store.IPv6ByGlobalping || gotAddr != v6Entry || gotPort != 443 ||
		len(got.Verdicts) != 1 || verdictOf(got, "tcp", 443) != "reachable" {
		t.Fatalf("check = %+v (asked %s:%d)", got, gotAddr, gotPort)
	}
	// probes that answered but none connected: the address does not answer
	connected = 0
	got = awaitIPv6(t, x.checkIPv6(target.nodeID, v6Entry))
	if got.Pass || got.By != store.IPv6ByGlobalping || verdictOf(got, "tcp", 443) != "timeout" {
		t.Fatalf("no probe connected: %+v", got)
	}
	// Globalping off or failing: nothing verified the address
	failure = errors.New("switched off")
	got = awaitIPv6(t, x.checkIPv6(target.nodeID, v6Entry))
	if got.Pass || got.Code != "no_ipv6_verifier" {
		t.Fatalf("Globalping failed: %+v", got)
	}
	assertNoPortCommands(t, targetConn) // the target was never asked to count: nothing was sent to it
}

func TestCheckIPv6HoldsTheNodesPortCheckGate(t *testing.T) {
	x, target, _ := ipv6Target(t)
	lock, _, _, err := x.f.acquirePortCheck(x.ctx, target.nodeID) // a port check or a relay check is running
	if err != nil {
		t.Fatal(err)
	}
	ch := x.checkIPv6(target.nodeID, v6Entry)
	waitPortCheckWaiter(t, x.f, target.nodeID)
	select {
	case r := <-ch:
		t.Fatalf("CheckIPv6 ran while the node's gate was held: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	<-lock.gate
	if got := awaitIPv6(t, ch); got.Code != "no_ipv6_verifier" {
		t.Fatalf("check = %+v", got)
	}
}

func TestCheckIPv6RefusesWhatIsNotAGlobalIPv6Address(t *testing.T) {
	x, target, _ := tcpCheckTarget(t)
	for _, a := range []string{"", "203.0.113.5", "::ffff:203.0.113.5", "fe80::1", "fd00::1", "::1", "2001:db8::5/64", "example.com"} {
		if _, err := x.f.CheckIPv6(x.ctx, target.nodeID, a); !errors.Is(err, ErrIPv6Address) {
			t.Errorf("CheckIPv6(%q) = %v, want ErrIPv6Address", a, err)
		}
	}
	if _, err := x.f.CheckIPv6(x.ctx, "nod_missing", v6Entry); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing node: %v", err)
	}
}

// hy2Node is a node with one Hysteria2 server (UDP 443, an ACME site on TCP 443) whose agent has not connected yet.
func hy2Node(t *testing.T) (*l3Env, *agent) {
	t.Helper()
	x, target := newL3Env(t)
	reg, err := protocols.NewRegistry(fakeProto{"awg", plugin.ClientAmnezia}, tcpProto{fakeProto{"hysteria2", plugin.ClientHapp}})
	if err != nil {
		t.Fatal(err)
	}
	x.f.reg = reg
	x.f.panelIPv6 = func(context.Context) bool { return false }
	x.exec(`UPDATE node SET address = '203.0.113.10' WHERE id = ?`, target.nodeID)
	x.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES
		('prf_hy2', 'hysteria2', 'alice-hy2', '{"port":443,"network":"udp","tls":"acme","name":"node-a.example.com"}', 1, 1, 1)`)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, enabled, created_at, updated_at) VALUES ('inb_hy2', 'prf_hy2', ?, 1, 1, 1, 1)`, target.nodeID)
	return x, target
}

func TestCheckIPv6NodeStatesGiveCodesNotErrors(t *testing.T) {
	x, target := hy2Node(t) // the agent is not connected
	if got, err := x.f.CheckIPv6(x.ctx, target.nodeID, v6Entry); err != nil || got.Code != "node_offline" || got.Pass {
		t.Fatalf("offline node: %+v, %v", got, err)
	}
	x2, target2, _ := ipv6Target(t)
	x2.exec(`UPDATE inbound SET enabled = 0 WHERE node_id = ?`, target2.nodeID)
	if got, err := x2.f.CheckIPv6(x2.ctx, target2.nodeID, v6Entry); err != nil || got.Code != "nothing_to_check" {
		t.Fatalf("no enabled server: %+v, %v", got, err)
	}
	x3, target3 := hy2Node(t)
	connectCaps(target3, "old-target") // lists no capabilities
	if got, err := x3.f.CheckIPv6(x3.ctx, target3.nodeID, v6Entry); err != nil || got.Code != "agent_too_old" {
		t.Fatalf("agent without udpcheck/1: %+v, %v", got, err)
	}
}

// ---- the RPC ----

func asOwner(ctx context.Context) context.Context {
	return auth.WithAdmin(ctx, store.Admin{ID: "adm_owner", Role: store.RoleOwner})
}

func TestCheckIpv6RPCIsOwnerOnlyAndNeedsTheNodesOwnAddress(t *testing.T) {
	x, target, targetConn := ipv6Target(t)
	_, senderConn := x.v6Peer("sender1")
	svc := nodeService{x.f}
	call := func(ctx context.Context, addr string) (*adminv1.CheckIpv6Response, error) {
		r, err := svc.CheckIpv6(ctx, connect.NewRequest(&adminv1.CheckIpv6Request{NodeId: target.nodeID, Address: addr}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	for _, role := range []string{store.RoleHelper, store.RoleReadonly} {
		if _, err := call(auth.WithAdmin(x.ctx, store.Admin{ID: "adm_x", Role: role}), v6Entry); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: %v, want PERMISSION_DENIED", role, err)
		}
	}
	if _, err := call(x.ctx, v6Entry); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("no admin: %v, want PERMISSION_DENIED", err)
	}

	// the doctor has not reported yet, then it says there is no IPv6, then that outbound IPv6 fails, then it lists an address
	for _, tc := range []struct {
		doctor map[string]string
		want   string
	}{
		{nil, "not_checked"},
		{map[string]string{"ipv6": "no"}, "no_ipv6"},
		{map[string]string{"ipv6": "yes", "connect": "failed"}, "ipv6_broken"},
		{map[string]string{"ipv6": "yes", "connect": "ok"}, "agent_old"},
	} {
		if tc.doctor != nil {
			x.doctorIPv6(target.nodeID, tc.doctor)
		}
		if _, err := call(asOwner(x.ctx), v6Entry); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("doctor %v: %v, want FAILED_PRECONDITION %s", tc.doctor, err, tc.want)
		}
	}
	x.doctorIPv6(target.nodeID, v6Works(v6Entry+"/64"))
	if _, err := call(asOwner(x.ctx), v6Other); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("an address the node does not hold: %v, want INVALID_ARGUMENT", err)
	}
	if _, err := call(asOwner(x.ctx), "203.0.113.9"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("an IPv4 address: %v, want INVALID_ARGUMENT", err)
	}

	// no address given: the one the node uses for outbound IPv6
	done := make(chan *adminv1.CheckIpv6Response, 1)
	go func() {
		r, err := call(asOwner(x.ctx), "")
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	send := driveUDP6(targetConn, senderConn, 300)
	if send.Host != v6Entry {
		t.Errorf("the default address was %s, want %s", send.Host, v6Entry)
	}
	probe := waitTCPProbe(senderConn)
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "6", "t443": "ours", "t8443": "ours"}})
	r := <-done
	if r == nil || !r.Passed || r.ErrorCode != "" || r.Sender != "sender1" || r.Address != v6Entry || len(r.Verdicts) != 3 || r.CheckedUnix == 0 {
		t.Fatalf("response = %+v", r)
	}
}

func TestCheckIpv6MainAddressThatIsIPv6HasNothingToAdd(t *testing.T) {
	x, target, _ := tcpCheckTarget(t)
	x.exec(`UPDATE node SET address = '2001:db8::1' WHERE id = ?`, target.nodeID)
	x.doctorIPv6(target.nodeID, v6Works(v6Entry+"/64"))
	_, err := nodeService{x.f}.CheckIpv6(asOwner(x.ctx), connect.NewRequest(&adminv1.CheckIpv6Request{NodeId: target.nodeID}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "main_is_ipv6") {
		t.Fatalf("err = %v, want main_is_ipv6", err)
	}
}
