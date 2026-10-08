package fleet

import (
	"context"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type portCheckRPCResult struct {
	response *adminv1.CheckPortsResponse
	err      error
}

type portCheckFleetResult struct {
	checks []store.PortCheck
	sender string
	code   string
}

func portCheckTarget(t *testing.T) (*l3Env, *agent, *conn) {
	t.Helper()
	x, target := newL3Env(t)
	x.exec(`UPDATE node SET address = '203.0.113.10', provider = 'provider-a', country_code = 'TR' WHERE id = ?`, target.nodeID)
	x.f.settleUDP = func(context.Context) error { return nil }
	targetConn, _, _ := connectCaps(target, "ports-target", capUDPCheck)
	return x, target, targetConn
}

func addPortCheckPeer(x *l3Env, name, provider, country string, caps ...string) (*agent, *conn) {
	x.t.Helper()
	peer := x.enroll(name)
	x.exec(`UPDATE node SET provider = ?, country_code = ? WHERE id = ?`, provider, country, peer.nodeID)
	peerConn, _, _ := connectCaps(peer, "ports-"+name, caps...)
	return peer, peerConn
}

func startPortCheckRPC(ctx context.Context, x *l3Env, nodeID string, ports ...uint32) <-chan portCheckRPCResult {
	out := make(chan portCheckRPCResult, 1)
	go func() {
		response, err := (nodeService{x.f}).CheckPorts(ctx, connect.NewRequest(&adminv1.CheckPortsRequest{NodeId: nodeID, Ports: ports}))
		if err != nil {
			out <- portCheckRPCResult{err: err}
			return
		}
		out <- portCheckRPCResult{response: response.Msg}
	}()
	return out
}

func waitUDPCount(c *conn, stop bool) *agentv1.UdpCount {
	c.t.Helper()
	message := c.wait(func(message *agentv1.ConnectResponse) bool {
		return message.GetUdpCount() != nil && message.GetUdpCount().Stop == stop
	})
	return message.GetUdpCount()
}

func waitUDPSend(c *conn) *agentv1.UdpSend {
	c.t.Helper()
	message := c.wait(func(message *agentv1.ConnectResponse) bool { return message.GetUdpSend() != nil })
	return message.GetUdpSend()
}

func replyPortCommand(c *conn, requestID string, result *agentv1.CommandResult) {
	c.t.Helper()
	result.RequestId = requestID
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: result}})
}

func portCountResult(family string, got map[uint16]uint64, packets map[uint16]uint64) *agentv1.CommandResult {
	header := uint64(28)
	if family == "6" {
		header = 48
	}
	params := make(map[string]string, len(got)*2)
	for port, count := range got {
		params["p"+strconv.Itoa(int(port))] = strconv.FormatUint(packets[port], 10)
		params["b"+strconv.Itoa(int(port))] = strconv.FormatUint(count*uint64(udpCheckSize)+packets[port]*header, 10)
	}
	return &agentv1.CommandResult{Ok: true, Params: params}
}

func replyPortStop(c *conn, command *agentv1.UdpCount, family string, got map[uint16]uint64, packets map[uint16]uint64) {
	c.t.Helper()
	replyPortCommand(c, command.RequestId, portCountResult(family, got, packets))
}

func waitPortCheckWaiter(t *testing.T, f *Fleet, nodeID string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		f.portCheckMu.Lock()
		lock := f.portCheckLocks[nodeID]
		waiting := lock != nil && lock.waiters > 0
		f.portCheckMu.Unlock()
		if waiting {
			return
		}
	}
	t.Fatal("second CheckPorts call did not wait on the per-node lock")
}

func TestCheckPortsOrdersArmSendSettleStopAndFillsGetNode(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	sender, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck)
	var sequence []string
	var sequenceMu sync.Mutex
	x.f.settleUDP = func(context.Context) error {
		sequenceMu.Lock()
		sequence = append(sequence, "settle")
		sequenceMu.Unlock()
		return nil
	}
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)

	arm := waitUDPCount(targetConn, false)
	sequenceMu.Lock()
	sequence = append(sequence, "arm")
	sequenceMu.Unlock()
	if arm.HoldS != 30 || len(arm.Tag) != 8 || !reflect.DeepEqual(arm.Ports, []uint32{2053, 443}) {
		t.Fatalf("arm command = %+v", arm)
	}
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})

	send := waitUDPSend(senderConn)
	sequenceMu.Lock()
	sequence = append(sequence, "send")
	sequenceMu.Unlock()
	if send.Host != "203.0.113.10" || !reflect.DeepEqual(send.Ports, []uint32{2053, 443}) || send.Count != 300 || send.Pps != 50 || send.Size != 1200 || len(send.Tag) != 8 {
		t.Fatalf("send command = %+v", send)
	}
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop := waitUDPCount(targetConn, true)
	if !reflect.DeepEqual(stop.Tag, arm.Tag) {
		t.Fatalf("stop tag %x does not match arm tag %x", stop.Tag, arm.Tag)
	}
	sequenceMu.Lock()
	sequence = append(sequence, "stop")
	sequenceMu.Unlock()
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 300, 443: 300}, map[uint16]uint64{2053: 4, 443: 300})

	got := <-result
	if got.err != nil || got.response.ErrorCode != "" || got.response.Sender != "sender" || len(got.response.Ports) != 2 {
		t.Fatalf("CheckPorts response = %+v, %v", got.response, got.err)
	}
	if got.response.Ports[0].Port != 2053 || got.response.Ports[0].Verdict != "ok" || got.response.Ports[0].Sent != 300 || got.response.Ports[0].Got != 300 || got.response.Ports[0].Sender != "sender" {
		t.Errorf("GRO count did not produce the expected result: %+v", got.response.Ports[0])
	}
	sequenceMu.Lock()
	if !reflect.DeepEqual(sequence, []string{"arm", "send", "settle", "stop"}) {
		t.Errorf("command sequence = %v", sequence)
	}
	sequenceMu.Unlock()
	stored, err := x.st.PortChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 2 || stored[0].Sender != sender.nodeID {
		t.Fatalf("stored rows = %+v, %v", stored, err)
	}
	node, err := (nodeService{x.f}).GetNode(x.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: target.nodeID}))
	if err != nil || len(node.Msg.PortChecks) != 2 || node.Msg.PortChecks[0].Sender != "sender" {
		t.Fatalf("GetNode port checks = %+v, %v", node.Msg.GetPortChecks(), err)
	}
	if x.count(`SELECT count(*) FROM audit WHERE action = 'node.ports_check' AND actor = 'adm_test'`) != 1 {
		t.Error("CheckPorts did not leave one audit row")
	}
}

func TestCheckPortsAsSystemAuditsTheSystemActor(t *testing.T) {
	x, target := newL3Env(t)
	conn, _, _ := connectCaps(target, "ports-old-agent", capDoctor)
	_ = conn
	_, _, code := x.f.CheckPortsAsSystem(x.ctx, target.nodeID, nil)
	if code != "agent_too_old" {
		t.Fatalf("CheckPortsAsSystem error = %q, want agent_too_old", code)
	}
	if got := x.count(`SELECT count(*) FROM audit WHERE action = 'node.ports_check' AND actor = 'system'`); got != 1 {
		t.Fatalf("system port-check audit rows = %d, want one", got)
	}
}

func TestUDPPortVerdictsAndGROMath(t *testing.T) {
	for _, test := range []struct {
		got     uint64
		verdict string
	}{{285, "ok"}, {284, "lossy"}, {210, "lossy"}, {209, "broken"}} {
		if got := udpPortVerdict(300, test.got); got != test.verdict {
			t.Errorf("udpPortVerdict(300, %d) = %q, want %q", test.got, got, test.verdict)
		}
	}
	if got := udpDelivered(4, 4*28+300*1200, 28, 1200, 300); got != 300 {
		t.Errorf("IPv4 GRO count = %d, want 300", got)
	}
	if got := udpDelivered(3, 3*48+301*1200, 48, 1200, 300); got != 300 {
		t.Errorf("IPv6 GRO count clamped to sent = %d, want 300", got)
	}
	if got := udpDelivered(5, 5*28, 28, 1200, 300); got != 0 {
		t.Errorf("header-only count = %d, want 0", got)
	}
}

// The run that found a filtered port on a real node: every port 300 of 300, the filtered one 286 (above 95 %).
func TestRelativeLossMarksAPortWorseThanItsRun(t *testing.T) {
	run := func(gots ...uint32) []string {
		checks := make([]store.PortCheck, len(gots))
		for i, got := range gots {
			checks[i] = store.PortCheck{Sent: 300, Got: got, Verdict: udpPortVerdict(300, uint64(got))}
		}
		relativeLoss(checks)
		verdicts := make([]string, len(checks))
		for i, c := range checks {
			verdicts[i] = c.Verdict
		}
		return verdicts
	}
	for _, test := range []struct {
		gots []uint32
		want []string
	}{
		{[]uint32{300, 300, 300, 286}, []string{"ok", "ok", "ok", "lossy"}},
		{[]uint32{300, 289, 288}, []string{"ok", "ok", "lossy"}}, // 11 below the best is noise, 12 is not
		{[]uint32{294, 290, 286}, []string{"ok", "ok", "ok"}},    // a slightly lossy path: no port stands out
		{[]uint32{300, 200}, []string{"ok", "broken"}},           // absolute verdicts stay
	} {
		if got := run(test.gots...); !reflect.DeepEqual(got, test.want) {
			t.Errorf("relativeLoss(%v) = %v, want %v", test.gots, got, test.want)
		}
	}
}

func TestPortSenderPreferenceByProviderThenCountry(t *testing.T) {
	target := store.NodeRow{Provider: "provider-a", CountryCode: "TR"}
	ordered := udpPortSenderOrder(target, []store.NodeRow{
		{Name: "country", Provider: "provider-a", CountryCode: "DE"},
		{Name: "neither", Provider: "provider-a", CountryCode: "TR"},
		{Name: "provider", Provider: "provider-b", CountryCode: "TR"},
	})
	if got := []string{ordered[0].Name, ordered[1].Name, ordered[2].Name}; !reflect.DeepEqual(got, []string{"provider", "country", "neither"}) {
		t.Fatalf("sender order = %v", got)
	}
}

func TestCheckPortsRetriesAnInconclusiveSenderOnce(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	first, firstConn := addPortCheckPeer(x, "first", "provider-a", "TR", capUDPCheck)
	_, secondConn := addPortCheckPeer(x, "second", "provider-b", "DE", capUDPCheck)
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)

	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(secondConn)
	if send.Host != "203.0.113.10" {
		t.Fatalf("first sender was not preferred by provider: %+v", send)
	}
	replyPortCommand(secondConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 0, 443: 0}, map[uint16]uint64{2053: 0, 443: 0})

	arm = waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send = waitUDPSend(firstConn)
	replyPortCommand(firstConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop = waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 300, 443: 300}, map[uint16]uint64{2053: 300, 443: 300})

	got := <-result
	if got.err != nil || got.response.ErrorCode != "" || got.response.Sender != "first" {
		t.Fatalf("retry response = %+v, %v", got.response, got.err)
	}
	stored, err := x.st.PortChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 2 || stored[0].Sender != first.nodeID {
		t.Fatalf("retry stored rows = %+v, %v", stored, err)
	}
}

func TestCheckPortsConcurrentCallerReadsCompletedRows(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck)
	first := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	second := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	waitPortCheckWaiter(t, x.f, target.nodeID)
	send := waitUDPSend(senderConn)
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 300, 443: 300}, map[uint16]uint64{2053: 300, 443: 300})
	firstResult := <-first
	secondResult := <-second
	if firstResult.err != nil || secondResult.err != nil || firstResult.response.ErrorCode != "" || secondResult.response.ErrorCode != "" {
		t.Fatalf("concurrent results = %+v / %+v", firstResult, secondResult)
	}
	if len(secondResult.response.Ports) != 2 || secondResult.response.Sender != "sender" {
		t.Fatalf("second caller did not read the completed result: %+v", secondResult.response)
	}
	if x.count(`SELECT count(*) FROM audit WHERE action = 'node.ports_check' AND actor = 'adm_test'`) != 2 {
		t.Error("both CheckPorts callers should be audited")
	}
}

func TestCheckPortsPanelFallbackSkipsPeerWithoutCapability(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	addPortCheckPeer(x, "old-peer", "provider-a", "DE", "doctor/1")
	sent := make(chan struct {
		host       string
		ports      []uint16
		count, pps int
		size       int
	}, 1)
	x.f.sendUDP = func(_ context.Context, host string, ports []uint16, _ [8]byte, count, pps, size int) (string, int, error) {
		sent <- struct {
			host       string
			ports      []uint16
			count, pps int
			size       int
		}{host, ports, count, pps, size}
		return "4", 300, nil
	}
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	call := <-sent
	if call.host != "203.0.113.10" || !reflect.DeepEqual(call.ports, []uint16{2053, 443}) || call.count != 300 || call.pps != 50 || call.size != 1200 {
		t.Fatalf("panel sender args = %+v", call)
	}
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 300, 443: 300}, map[uint16]uint64{2053: 300, 443: 300})
	got := <-result
	if got.err != nil || got.response.ErrorCode != "" || got.response.Sender != "panel" {
		t.Fatalf("panel fallback response = %+v, %v", got.response, got.err)
	}
}

func TestCheckPortsReturnsNoSenderWhenPanelFallbackIsDisabled(t *testing.T) {
	x, target, _ := portCheckTarget(t)
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	result := x.f.runPortCheckForEdition(x.ctx, node, []uint16{2053}, false)
	if result.errorCode != "no_sender" {
		t.Fatalf("disabled panel fallback returned %+v", result)
	}
}

func TestCheckPortsTargetOfflineAndAgentTooOld(t *testing.T) {
	x, target := newL3Env(t)
	x.exec(`UPDATE node SET address = '203.0.113.10' WHERE id = ?`, target.nodeID)
	if _, _, code := x.f.CheckPorts(x.ctx, target.nodeID, []uint16{2053}); code != "node_offline" {
		t.Fatalf("offline result = %q", code)
	}
	oldConn, _, _ := connectCaps(target, "ports-old", "doctor/1")
	if _, _, code := x.f.CheckPorts(x.ctx, target.nodeID, []uint16{2053}); code != "agent_too_old" {
		t.Fatalf("old agent result = %q", code)
	}
	_ = oldConn
}

func TestCheckPortsAgentBusyDoesNotSendOrStop(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck)
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Error: "busy"})
	got := <-result
	if got.err != nil || got.response.ErrorCode != "busy" {
		t.Fatalf("busy response = %+v, %v", got.response, got.err)
	}
}

func TestCheckPortsFailedSenderStillDisarms(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck)
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(senderConn)
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Error: "no_route"})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 0, 443: 0}, map[uint16]uint64{2053: 0, 443: 0})
	got := <-result
	if got.err != nil || got.response.ErrorCode != "no_route" {
		t.Fatalf("failed sender response = %+v, %v", got.response, got.err)
	}
	stored, err := x.st.PortChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("failed sender stored rows = %+v, %v", stored, err)
	}
}

func TestCheckPortsCanceledSendStillDisarms(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck)
	ctx, cancel := context.WithCancel(x.ctx)
	defer cancel()
	result := make(chan portCheckFleetResult, 1)
	go func() {
		checks, sender, code := x.f.CheckPorts(ctx, target.nodeID, []uint16{2053})
		result <- portCheckFleetResult{checks: checks, sender: sender, code: code}
	}()
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	_ = waitUDPSend(senderConn)
	cancel()
	stop := waitUDPCount(targetConn, true)
	if !reflect.DeepEqual(stop.Tag, arm.Tag) {
		t.Fatalf("cancel cleanup used a different tag: arm=%x stop=%x", arm.Tag, stop.Tag)
	}
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 0, 443: 0}, map[uint16]uint64{2053: 0, 443: 0})
	got := <-result
	if got.code != "failed" || len(got.checks) != 0 {
		t.Fatalf("canceled check = %+v", got)
	}
}

func TestCheckPortsSameHostReturnsCountsWithoutStoring(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	x.exec(`UPDATE node SET address = '127.0.0.1' WHERE id = ?`, target.nodeID)
	x.f.sendUDP = func(_ context.Context, _ string, _ []uint16, _ [8]byte, _, _, _ int) (string, int, error) {
		return "4", 300, nil
	}
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 300, 443: 300}, map[uint16]uint64{2053: 300, 443: 300})
	got := <-result
	if got.err != nil || got.response.ErrorCode != "same_host" || got.response.Sender != "panel" || len(got.response.Ports) != 2 || got.response.Ports[0].Got != 300 {
		t.Fatalf("same-host response = %+v, %v", got.response, got.err)
	}
	stored, err := x.st.PortChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("same-host rows were stored: %+v, %v", stored, err)
	}
}

func TestCheckPortsInconclusiveDoesNotStore(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck)
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(senderConn)
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 0, 443: 0}, map[uint16]uint64{2053: 0, 443: 0})
	got := <-result
	if got.err != nil || got.response.ErrorCode != "inconclusive" || len(got.response.Ports) != 2 {
		t.Fatalf("inconclusive response = %+v, %v", got.response, got.err)
	}
	stored, err := x.st.PortChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("inconclusive rows were stored: %+v, %v", stored, err)
	}
}

func TestPortLossyEventFiresOnlyOnFirstBadRun(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck)
	x.exec(`UPDATE profile SET settings_json = '{"port":2053}' WHERE id = 'prf_awg'`)
	for range 2 {
		result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
		arm := waitUDPCount(targetConn, false)
		replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
		send := waitUDPSend(senderConn)
		replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
		stop := waitUDPCount(targetConn, true)
		replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 210, 443: 300}, map[uint16]uint64{2053: 210, 443: 300})
		got := <-result
		if got.err != nil || got.response.ErrorCode != "" || got.response.Ports[0].Verdict != "lossy" {
			t.Fatalf("lossy run response = %+v, %v", got.response, got.err)
		}
	}
	if events := x.count(`SELECT count(*) FROM event WHERE node_id = ? AND inbound_id = 'inb_awg' AND code = 'port_lossy'`, target.nodeID); events != 1 {
		t.Fatalf("port_lossy events = %d, want one", events)
	}
	if events := x.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'port_lossy' AND severity = 2`, target.nodeID); events != 1 {
		t.Fatalf("severity-2 port_lossy events = %d, want one", events)
	}
}

func TestUDPCheckPortListUsesEnabledPortsPreferredFreePortsAndAnchor(t *testing.T) {
	inbounds := []store.FleetInboundRow{
		{Enabled: true, Settings: `{"port":2053}`},
		{Enabled: true, PortOverride: 4443, Settings: `{"port":8443}`},
		{Enabled: false, Settings: `{"port":2096}`},
		{Settings: `{"port":3000,"hop":{"from":4000,"to":4500}}`},
	}
	got := udpCheckPorts(nil, inbounds)
	want := []uint16{2053, 4443, 8443, 2083, 2087, 443}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default ports = %v, want %v", got, want)
	}
	if got := udpCheckPorts([]uint16{8443}, inbounds); !reflect.DeepEqual(got, []uint16{8443, 443}) {
		t.Fatalf("explicit ports = %v", got)
	}
}
