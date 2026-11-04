package fleet

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	accesssvc "github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
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

// udpCheckPorts adapts the raw row fixtures used by the list-policy test; runtime code builds plugin specs first.
func udpCheckPorts(requested []uint16, inbounds []store.FleetInboundRow) []uint16 {
	rows := make([]udpCheckInbound, 0, len(inbounds))
	for _, inbound := range inbounds {
		settings := udpPortSettings{}
		_ = json.Unmarshal([]byte(inbound.Settings), &settings)
		settings.Port = udpInboundPort(inbound)
		rows = append(rows, udpCheckInbound{enabled: inbound.Enabled, settings: settings})
	}
	return udpCheckPortList(requested, rows)
}

func udpInboundPort(inbound store.FleetInboundRow) uint16 {
	if inbound.PortOverride != 0 {
		return inbound.PortOverride
	}
	var settings udpPortSettings
	_ = json.Unmarshal([]byte(inbound.Settings), &settings)
	return settings.Port
}

func portCheckTarget(t *testing.T) (*l3Env, *agent, *conn) {
	t.Helper()
	x, target := newL3Env(t)
	reg, err := protocols.NewRegistry(fakeProto{"fakehy", plugin.ClientHapp}, fakeProto{"fakewg", plugin.ClientAmnezia},
		fakeProto{"awg", plugin.ClientAmnezia})
	if err != nil {
		t.Fatal(err)
	}
	x.f.reg = reg
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

func portSpecRows(t *testing.T) (*env, store.NodeRow, []store.FleetInboundRow) {
	t.Helper()
	reg := builtin.Registry()
	x := newEnvWithoutServer(t, reg)
	access, err := accesssvc.New(x.st, x.v, reg, nil, nil, accesssvc.Config{})
	if err != nil {
		t.Fatal(err)
	}
	x.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_de1', 'de1', 'de1.example.com', 'active', 1)`)
	hy2, err := access.CreateProfile(x.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "hysteria2", Name: "hy2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.CreateInbound(x.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
		ProfileId: hy2.Msg.Profile.Id, NodeId: "nod_de1",
	})); err != nil {
		t.Fatal(err)
	}
	settings := `{"reality":{"target":"www.example.org:443"}}`
	vless, err := access.CreateProfile(x.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "vless", Name: "vless", SettingsJson: settings,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.CreateInbound(x.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
		ProfileId: vless.Msg.Profile.Id, NodeId: "nod_de1", PortOverride: 443,
	})); err != nil {
		t.Fatal(err)
	}
	otherVLESS, err := access.CreateProfile(x.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "vless", Name: "vless-alt", SettingsJson: settings,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.CreateInbound(x.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
		ProfileId: otherVLESS.Msg.Profile.Id, NodeId: "nod_de1", PortOverride: 23456,
	})); err != nil {
		t.Fatal(err)
	}
	node, err := x.st.Node(x.ctx, "nod_de1")
	if err != nil {
		t.Fatal(err)
	}
	inbounds, err := x.st.FleetInbounds(x.ctx, node.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	return x, node, inbounds
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
	if send.Host != "203.0.113.10" || !reflect.DeepEqual(send.Ports, []uint32{2053, 443}) || send.Count != udpCheckSendCount || send.Pps != 25 || send.Size != udpCheckSize || len(send.Tag) != 8 {
		t.Fatalf("send command = %+v", send)
	}
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
	stop := waitUDPCount(targetConn, true)
	if !reflect.DeepEqual(stop.Tag, arm.Tag) {
		t.Fatalf("stop tag %x does not match arm tag %x", stop.Tag, arm.Tag)
	}
	sequenceMu.Lock()
	sequence = append(sequence, "stop")
	sequenceMu.Unlock()
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount}, map[uint16]uint64{2053: 4, 443: udpCheckSendCount})

	got := <-result
	if got.err != nil || got.response.ErrorCode != "" || got.response.Sender != "sender" || len(got.response.Ports) != 2 {
		t.Fatalf("CheckPorts response = %+v, %v", got.response, got.err)
	}
	if got.response.Ports[0].Port != 2053 || got.response.Ports[0].Verdict != "ok" || got.response.Ports[0].Sent != udpCheckSendCount || got.response.Ports[0].Got != udpCheckSendCount || got.response.Ports[0].Sender != "sender" {
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
	}{{143, "ok"}, {142, "lossy"}, {105, "lossy"}, {104, "broken"}} {
		if got := udpPortVerdict(udpCheckSendCount, test.got); got != test.verdict {
			t.Errorf("udpPortVerdict(udpCheckSendCount, %d) = %q, want %q", test.got, got, test.verdict)
		}
	}
	if got := udpDelivered(4, 4*28+udpCheckSendCount*udpCheckSize, 28, udpCheckSize, udpCheckSendCount); got != udpCheckSendCount {
		t.Errorf("IPv4 GRO count = %d, want %d", got, udpCheckSendCount)
	}
	if got := udpDelivered(3, 3*48+(udpCheckSendCount+1)*udpCheckSize, 48, udpCheckSize, udpCheckSendCount); got != udpCheckSendCount {
		t.Errorf("IPv6 GRO count clamped to sent = %d, want %d", got, udpCheckSendCount)
	}
	if got := udpDelivered(5, 5*28, 28, udpCheckSize, udpCheckSendCount); got != 0 {
		t.Errorf("header-only count = %d, want 0", got)
	}
}

// A filtered port is still found when the other seven ports deliver every packet.
func TestRelativeLossMarksAPortWorseThanItsRun(t *testing.T) {
	run := func(gots ...uint32) []string {
		checks := make([]store.PortCheck, len(gots))
		for i, got := range gots {
			checks[i] = store.PortCheck{Sent: udpCheckSendCount, Got: got, Verdict: udpPortVerdict(udpCheckSendCount, uint64(got))}
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
		{[]uint32{150, 150, 150, 150, 150, 150, 150, 143}, []string{"ok", "ok", "ok", "ok", "ok", "ok", "ok", "lossy"}},
		{[]uint32{150, 145, 144}, []string{"ok", "ok", "ok"}}, // the median-to-port difference is inside the noise floor
		{[]uint32{147, 145, 143}, []string{"ok", "ok", "ok"}},
		// 4% of 150 packets is six; the difference floor catches seven lost packets over a clean baseline.
		{[]uint32{150, 150, 150, 150, 150, 150, 150, 144}, []string{"ok", "ok", "ok", "ok", "ok", "ok", "ok", "lossy"}},
		{[]uint32{udpCheckSendCount, 104}, []string{"ok", "broken"}}, // absolute verdicts stay
	} {
		if got := run(test.gots...); !reflect.DeepEqual(got, test.want) {
			t.Errorf("relativeLoss(%v) = %v, want %v", test.gots, got, test.want)
		}
	}
}

func TestUDPSourceFilteredPatternUsesTheOnePercentAndAnchorFloors(t *testing.T) {
	run := func(anchorA, anchorB, otherA, otherB uint32) []store.PortCheck {
		return []store.PortCheck{
			{Port: 443, Sent: udpCheckSendCount, Got: anchorA, Sender: "de1", SenderVerdict: "ok",
				ConfirmSender: "nl1", ConfirmSent: udpCheckSendCount, ConfirmGot: anchorB, ConfirmVerdict: "ok"},
			{Port: 2053, Sent: udpCheckSendCount, Got: otherA, Sender: "de1", SenderVerdict: "broken",
				ConfirmSender: "nl1", ConfirmSent: udpCheckSendCount, ConfirmGot: otherB, ConfirmVerdict: "broken"},
		}
	}
	if !udpSourceFiltered(run(143, 150, 1, 0)) {
		t.Fatal("a paired 95-percent-anchor / at-most-1-percent-other-port run was not classified as source-filtered")
	}
	if udpSourceFiltered(run(142, 150, 1, 0)) || udpSourceFiltered(run(143, 150, 2, 0)) {
		t.Fatal("the source-filter classifier accepted a sub-95-percent anchor or more-than-1-percent other-port count")
	}
}

func TestSourceFilteredNodeReusesOneSenderUntilACleanRun(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	_, firstConn := addPortCheckPeer(x, "first", "provider-a", "TR", capUDPCheck)
	second, secondConn := addPortCheckPeer(x, "second", "provider-b", "DE", capUDPCheck)
	filtered := map[uint16]uint64{2053: 0, 443: udpCheckSendCount}
	clean := map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount}
	finish := func(sender *conn, counts map[uint16]uint64) {
		targetConn.t.Helper()
		arm := waitUDPCount(targetConn, false)
		replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
		send := waitUDPSend(sender)
		if send.Count != udpCheckSendCount || send.Pps != udpCheckPPS || send.Size != udpCheckSize {
			targetConn.t.Fatalf("source-filter check send parameters = %+v", send)
		}
		replyPortCommand(sender, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
		stop := waitUDPCount(targetConn, true)
		replyPortStop(targetConn, stop, "4", counts, counts)
	}
	await := func(result <-chan portCheckRPCResult) *adminv1.CheckPortsResponse {
		targetConn.t.Helper()
		select {
		case got := <-result:
			if got.err != nil {
				targetConn.t.Fatal(got.err)
			}
			return got.response
		case <-time.After(3 * time.Second):
			targetConn.t.Fatal("port check did not finish after its one allowed sender")
			return nil
		}
	}

	initial := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	finish(secondConn, filtered)
	finish(firstConn, filtered)
	paired := await(initial)
	if paired.ErrorCode != "" || paired.GetUdpFilteredUnix() == 0 || len(paired.GetPorts()) != 2 || paired.GetPorts()[0].GetBlame() != "node" {
		t.Fatalf("paired source-filter result = %+v", paired)
	}
	filteredAt, err := x.st.UDPFilteredAt(x.ctx, target.nodeID)
	if err != nil || filteredAt.IsZero() {
		t.Fatalf("stored source-filter time = %v, %v", filteredAt, err)
	}
	filteredSender, err := x.st.UDPFilteredSender(x.ctx, target.nodeID)
	if err != nil || filteredSender != second.nodeID {
		t.Fatalf("stored source-filter sender = %q, %v", filteredSender, err)
	}

	addPortCheckPeer(x, "third", "provider-c", "FR", capUDPCheck)
	if _, err := x.st.W.ExecContext(x.ctx, `UPDATE node SET address = '203.0.113.11' WHERE id = ?`, target.nodeID); err != nil {
		t.Fatal(err)
	}
	if oldAddressChecks, err := x.st.PortChecks(x.ctx, target.nodeID); err != nil || len(oldAddressChecks) != 0 {
		t.Fatalf("old-address port checks = %+v, %v", oldAddressChecks, err)
	}
	recheck := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	finish(secondConn, filtered)
	checked := await(recheck)
	if checked.ErrorCode != "" || checked.GetUdpFilteredUnix() != paired.GetUdpFilteredUnix() ||
		checked.GetPorts()[0].GetConfirmSender() != "" || checked.GetPorts()[0].GetSender() != "second" {
		t.Fatalf("filtered single-sender recheck = %+v", checked)
	}
	if got, err := x.st.UDPFilteredAt(x.ctx, target.nodeID); err != nil || !got.Equal(filteredAt) {
		t.Fatalf("non-clean recheck changed source-filter state: %v, %v", got, err)
	}

	cleanRun := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	finish(secondConn, clean)
	cleared := await(cleanRun)
	if cleared.ErrorCode != "" || cleared.GetUdpFilteredUnix() != 0 {
		t.Fatalf("clean sender run did not clear source-filter state: %+v", cleared)
	}
	if got, err := x.st.UDPFilteredAt(x.ctx, target.nodeID); err != nil || !got.IsZero() {
		t.Fatalf("clean run left source-filter timestamp: %v, %v", got, err)
	}
}

func TestUDPPortSenderDemotionAndConfirmationPreference(t *testing.T) {
	for _, code := range []string{"RU", "BY", "KZ", "KG", "AM", "AZ", "TJ", "UZ", "MD", "TM"} {
		if udpRegion(code) != "cis" {
			t.Errorf("udpRegion(%q) = %q, want cis", code, udpRegion(code))
		}
	}
	if udpRegion("") != "other" || udpRegion("DE") != "other" {
		t.Fatal("empty and non-CIS country codes must be in the other region")
	}
	target := store.NodeRow{ID: "target", Provider: "provider-a", CountryCode: "NL"}
	first := portSender{node: store.NodeRow{ID: "first", Name: "minsk1", Provider: "provider-b", CountryCode: "BY"}}
	candidates := []portSender{
		{node: store.NodeRow{ID: "same-region", Name: "same-region", Provider: "provider-c", CountryCode: "RU"}},
		{node: store.NodeRow{ID: "opposite-same-provider", Name: "de1", Provider: "provider-b", CountryCode: "DE"}},
	}
	if got, ok := udpConfirmationSender(target, first, candidates, nil); !ok || got.node.ID != "opposite-same-provider" {
		t.Fatalf("confirmation sender = %q, %v, want opposite region before provider", got.node.ID, ok)
	}
	providerCandidates := []portSender{
		{node: store.NodeRow{ID: "same-provider", Name: "same-provider", Provider: "provider-b", CountryCode: "RU"}},
		{node: store.NodeRow{ID: "different-provider", Name: "different-provider", Provider: "provider-c", CountryCode: "RU"}},
	}
	if got, ok := udpConfirmationSender(target, first, providerCandidates, nil); !ok || got.node.ID != "different-provider" {
		t.Fatalf("provider comparison = %q, %v, want different provider", got.node.ID, ok)
	}
	countryFirst := portSender{node: store.NodeRow{ID: "country-first", Name: "first", Provider: "provider-b", CountryCode: "RU"}}
	countryCandidates := []portSender{
		{node: store.NodeRow{ID: "same-country", Name: "a-same", Provider: "provider-b", CountryCode: "RU"}},
		{node: store.NodeRow{ID: "different-country", Name: "z-different", Provider: "provider-b", CountryCode: "BY"}},
	}
	if got, ok := udpConfirmationSender(store.NodeRow{CountryCode: "DE"}, countryFirst, countryCandidates, nil); !ok || got.node.ID != "different-country" {
		t.Fatalf("country comparison = %q, %v, want different country", got.node.ID, ok)
	}
	demotedCandidates := []portSender{
		{node: store.NodeRow{ID: "de1", Name: "de1", Provider: "provider-b", CountryCode: "DE"}},
		{node: store.NodeRow{ID: "nl1", Name: "nl1", Provider: "provider-b", CountryCode: "FR"}},
	}
	if got, ok := udpConfirmationSender(target, first, demotedCandidates, map[string]bool{"de1": true}); !ok || got.node.ID != "nl1" {
		t.Fatalf("demoted confirmation sender = %q, %v, want non-demoted opposite-region candidate", got.node.ID, ok)
	}

	ordered := udpPortSenderOrder(target, []store.NodeRow{
		{ID: "same", Name: "same", Provider: "provider-a", CountryCode: "NL"},
		{ID: "preferred-demoted", Name: "preferred", Provider: "provider-b", CountryCode: "DE"},
	}, map[string]bool{"preferred-demoted": true})
	if ordered[0].ID != "same" {
		t.Fatalf("demoted first sender = %+v, want non-demoted sender first", ordered)
	}
}

func TestUDPPortAttributionCases(t *testing.T) {
	makeRun := func(sender string, verdicts ...string) portCheckOutcome {
		checks := make([]store.PortCheck, len(verdicts))
		for i, verdict := range verdicts {
			got := uint32(udpCheckSendCount)
			if verdict == "lossy" {
				got = 105
			} else if verdict == "broken" {
				got = 50
			}
			checks[i] = store.PortCheck{Port: uint16(2053 + i), Sent: udpCheckSendCount, Got: got, Verdict: verdict,
				SenderVerdict: verdict, Sender: sender, CheckedAt: time.Unix(10, 0)}
		}
		return portCheckOutcome{checks: checks, sender: sender}
	}
	for _, tc := range []struct {
		name, first, confirm, wantVerdict, wantBlame string
	}{
		{"both bad prefer lossy", "broken", "lossy", "lossy", "node"},
		{"first sender path", "lossy", "ok", "ok", "path"},
		{"confirmation sender path", "ok", "broken", "ok", "path"},
		{"both clean", "ok", "ok", "ok", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := makeRun("minsk1", tc.first), makeRun("de1", tc.confirm)
			got := combinePortCheckRuns(first, second, "nl1", time.Unix(20, 0))
			if !got.combined || len(got.checks) != 1 || got.checks[0].Verdict != tc.wantVerdict || got.checks[0].Blame != tc.wantBlame {
				t.Fatalf("combined check = %+v", got)
			}
			if got.checks[0].SenderVerdict != tc.first || got.checks[0].ConfirmVerdict != tc.confirm || got.checks[0].ConfirmSender != "de1" {
				t.Fatalf("run fields = %+v", got.checks[0])
			}
			wantLosses := 0
			if tc.wantBlame == "path" {
				wantLosses = 1
			}
			if len(got.pathLoss) != wantLosses {
				t.Fatalf("path loss rows = %+v, want %d", got.pathLoss, wantLosses)
			}
		})
	}
}

// A path that lets some ports through whole and drops the rest outright is a filter (the case seen live: a hoster that
// admits only UDP 443 from data-centre networks); one that loses a share of every port is a lossy line.
func TestUDPPathLossNamesAFilter(t *testing.T) {
	run := func(sender string, got map[uint16]uint32) portCheckOutcome {
		var checks []store.PortCheck
		for _, port := range []uint16{443, 2053, 14491} {
			verdict := "ok"
			if got[port]*100 < udpCheckSendCount*95 {
				verdict = "broken"
			}
			checks = append(checks, store.PortCheck{Port: port, Sent: udpCheckSendCount, Got: got[port], Verdict: verdict, SenderVerdict: verdict,
				Sender: sender, CheckedAt: time.Unix(10, 0)})
		}
		return portCheckOutcome{checks: checks, sender: sender}
	}
	whole := map[uint16]uint32{443: udpCheckSendCount, 2053: udpCheckSendCount, 14491: udpCheckSendCount}
	for _, tc := range []struct {
		name     string
		first    map[uint16]uint32
		wantOpen string
	}{
		{"only 443 arrives", map[uint16]uint32{443: udpCheckSendCount, 2053: 0, 14491: 0}, "443"},
		{"a share lost on every port", map[uint16]uint32{443: 103, 2053: 105, 14491: 102}, ""},
		{"some got through on the lost ports", map[uint16]uint32{443: udpCheckSendCount, 2053: 40, 14491: 0}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := combinePortCheckRuns(run("de1", tc.first), run("msk1", whole), "fi1", time.Unix(20, 0))
			if len(got.pathLoss) != 1 || got.pathLoss[0].SenderID != "de1" || got.pathLoss[0].OpenPorts != tc.wantOpen {
				t.Fatalf("path loss = %+v, want open ports %q", got.pathLoss, tc.wantOpen)
			}
		})
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
	second, secondConn := addPortCheckPeer(x, "second", "provider-b", "DE", capUDPCheck)
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)

	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(secondConn)
	if send.Host != "203.0.113.10" {
		t.Fatalf("first sender was not preferred by provider: %+v", send)
	}
	replyPortCommand(secondConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 0, 443: 0}, map[uint16]uint64{2053: 0, 443: 0})

	arm = waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send = waitUDPSend(firstConn)
	replyPortCommand(firstConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
	stop = waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount}, map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount})

	got := <-result
	if got.err != nil || got.response.ErrorCode != "" || got.response.Sender != "second" {
		t.Fatalf("retry response = %+v, %v", got.response, got.err)
	}
	stored, err := x.st.PortChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 2 || stored[0].Sender != second.nodeID || stored[0].SenderVerdict != "broken" ||
		stored[0].ConfirmSender != first.nodeID || stored[0].Verdict != "ok" || stored[0].Blame != "path" {
		t.Fatalf("retry stored rows = %+v, %v", stored, err)
	}
	if got.response.Ports[0].GetConfirmSender() != "first" || got.response.Ports[0].GetConfirmGot() != udpCheckSendCount ||
		got.response.Ports[0].GetConfirmVerdict() != "ok" {
		t.Fatalf("confirmation API fields = %+v", got.response.Ports[0])
	}
	if count := x.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'path_lossy' AND severity = 2`, second.nodeID); count != 1 {
		t.Fatalf("path_lossy events = %d, want one", count)
	}
	if count := x.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'port_lossy'`, target.nodeID); count != 0 {
		t.Fatalf("path-blamed port_lossy events = %d, want none", count)
	}
	pathRows, err := x.st.UDPPathLosses(x.ctx, time.Time{})
	if err != nil || len(pathRows) != 1 || pathRows[0].SenderID != second.nodeID || pathRows[0].TargetID != target.nodeID ||
		pathRows[0].OtherSenderID != first.nodeID || pathRows[0].Ports != "443, 2053" || pathRows[0].Got != 0 || pathRows[0].OtherGot != udpCheckSendCount {
		t.Fatalf("path-loss persistence = %+v, %v", pathRows, err)
	}
	var raw string
	if err := x.st.R.QueryRowContext(x.ctx, `SELECT params_json FROM event WHERE node_id = ? AND code = 'path_lossy'`, second.nodeID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatal(err)
	}
	if params["target"] != "nodea" || params["target_id"] != target.nodeID || params["ports"] != "443, 2053" ||
		params["sent"] != "150" || params["got"] != "0" || params["other"] != "first" || params["other_got"] != "150" ||
		params["region"] != "other" || params["other_region"] != "other" {
		t.Fatalf("path-loss event params = %+v", params)
	}
}

func TestCheckPortsDemotesSenderFromPersistedPathLoss(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	demoted, demotedConn := addPortCheckPeer(x, "de1", "provider-b", "DE", capUDPCheck)
	other, otherConn := addPortCheckPeer(x, "fr1", "provider-a", "TR", capUDPCheck)
	if err := x.st.UpdateUDPPathLosses(x.ctx, target.nodeID, []string{demoted.nodeID, other.nodeID}, []store.UDPPathLoss{{
		SenderID: demoted.nodeID, TargetID: target.nodeID, Ports: "2053", Sent: udpCheckSendCount, Got: 100,
		OtherSenderID: other.nodeID, OtherGot: udpCheckSendCount, CheckedAt: x.f.now().Add(-time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	_ = demotedConn
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(otherConn)
	replyPortCommand(otherConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount}, map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount})
	got := <-result
	if got.err != nil || got.response.ErrorCode != "" || got.response.Sender != "fr1" {
		t.Fatalf("persistently demoted sender was selected: %+v, %v", got.response, got.err)
	}
}

func TestCheckPortsBothSendersInconclusiveDoesNotStore(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	first, firstConn := addPortCheckPeer(x, "first", "provider-a", "TR", capUDPCheck)
	_, secondConn := addPortCheckPeer(x, "second", "provider-b", "DE", capUDPCheck)
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	for _, senderConn := range []*conn{secondConn, firstConn} {
		arm := waitUDPCount(targetConn, false)
		replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
		send := waitUDPSend(senderConn)
		replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
		stop := waitUDPCount(targetConn, true)
		replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 0, 443: 0}, map[uint16]uint64{2053: 0, 443: 0})
	}
	got := <-result
	if got.err != nil || got.response.ErrorCode != "inconclusive" || len(got.response.Ports) != 2 {
		t.Fatalf("both inconclusive response = %+v, %v", got.response, got.err)
	}
	if rows, err := x.st.PortChecks(x.ctx, target.nodeID); err != nil || len(rows) != 0 {
		t.Fatalf("both inconclusive rows = %+v, %v", rows, err)
	}
	if rows, err := x.st.UDPPathLosses(x.ctx, time.Time{}); err != nil || len(rows) != 0 {
		t.Fatalf("both inconclusive path rows = %+v, %v", rows, err)
	}
	_ = first
}

func TestCheckPortsFailedConfirmationKeepsFirstRunUnconfirmed(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	_, firstConn := addPortCheckPeer(x, "first", "provider-a", "TR", capUDPCheck)
	second, secondConn := addPortCheckPeer(x, "second", "provider-b", "DE", capUDPCheck)
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)

	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(secondConn)
	replyPortCommand(secondConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 105, 443: udpCheckSendCount}, map[uint16]uint64{2053: 105, 443: udpCheckSendCount})

	arm = waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send = waitUDPSend(firstConn)
	replyPortCommand(firstConn, send.RequestId, &agentv1.CommandResult{Error: "busy"})
	stop = waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 0, 443: 0}, map[uint16]uint64{2053: 0, 443: 0})

	got := <-result
	if got.err != nil || got.response.ErrorCode != "" || len(got.response.Ports) != 2 {
		t.Fatalf("failed confirmation response = %+v, %v", got.response, got.err)
	}
	port := got.response.Ports[0]
	if port.GetPort() != 2053 || port.GetVerdict() != "lossy" || port.GetSenderVerdict() != "lossy" ||
		port.GetConfirmSender() != "" || port.GetBlame() != "" {
		t.Fatalf("failed confirmation was not kept as an unconfirmed first run: %+v", port)
	}
	rows, err := x.st.PortChecks(x.ctx, target.nodeID)
	if err != nil || len(rows) != 2 || rows[0].Sender != second.nodeID || rows[0].ConfirmSender != "" {
		t.Fatalf("stored first run = %+v, %v", rows, err)
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
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount}, map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount})
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
		return "4", udpCheckSendCount, nil
	}
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	call := <-sent
	if call.host != "203.0.113.10" || !reflect.DeepEqual(call.ports, []uint16{2053, 443}) || call.count != udpCheckSendCount || call.pps != 25 || call.size != udpCheckSize {
		t.Fatalf("panel sender args = %+v", call)
	}
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount}, map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount})
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
		return "4", udpCheckSendCount, nil
	}
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount}, map[uint16]uint64{2053: udpCheckSendCount, 443: udpCheckSendCount})
	got := <-result
	if got.err != nil || got.response.ErrorCode != "same_host" || got.response.Sender != "panel" || len(got.response.Ports) != 2 || got.response.Ports[0].Got != udpCheckSendCount {
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
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
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
		replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
		stop := waitUDPCount(targetConn, true)
		replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 105, 443: udpCheckSendCount}, map[uint16]uint64{2053: 105, 443: udpCheckSendCount})
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

func TestPortLossyEventIncludesConfirmationRun(t *testing.T) {
	x, node, inbounds := portSpecRows(t)
	x.exec(`INSERT INTO node (id, name, address, created_at) VALUES ('nod_bob', 'bob', '203.0.113.42', 1)`)
	x.f.portLossyEvents(x.ctx, node, inbounds, []store.PortCheck{{NodeID: node.ID, Port: 443, Verdict: "lossy",
		Sent: udpCheckSendCount, Got: 105, Sender: "panel", ConfirmSender: "nod_bob", ConfirmSent: udpCheckSendCount, ConfirmGot: udpCheckSendCount,
		ConfirmVerdict: "ok", CheckedAt: time.Now()}}, map[uint16]store.PortCheck{})
	var raw string
	if err := x.st.R.QueryRowContext(x.ctx, `SELECT params_json FROM event WHERE node_id = ? AND code = 'port_lossy'`, node.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatal(err)
	}
	if params["confirm_sender"] != "bob" || params["confirm_got"] != "150" || params["confirm_sent"] != "150" {
		t.Fatalf("confirmed port_lossy event params = %+v", params)
	}
}

// A hoster's filter drops the test datagrams but the panel's own protocol check through the inbound passes: no
// "loses 100 %" event for a port whose clients connect (the alert is suppressed by the same rule).
func TestPortLossyEventSkipsAPortWhoseProtocolWorks(t *testing.T) {
	x, node, inbounds := portSpecRows(t)
	checked := time.Now()
	hy2 := ""
	for _, inbound := range inbounds {
		if inbound.ProfileName == "hy2" { // the Hysteria2 inbound on UDP 443
			hy2 = inbound.ID
		}
	}
	if err := x.st.InsertSample(x.ctx, store.CheckSample{InboundID: hy2, At: checked.Add(-5 * time.Minute),
		Status: int(adminv1.CheckStatus_CHECK_STATUS_OK)}); err != nil {
		t.Fatal(err)
	}
	x.f.portLossyEvents(x.ctx, node, inbounds, []store.PortCheck{{NodeID: node.ID, Port: 443, Verdict: "broken",
		Sent: udpCheckSendCount, Got: 0, Sender: "panel", CheckedAt: checked}}, map[uint16]store.PortCheck{})
	if events := x.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'port_lossy'`, node.ID); events != 0 {
		t.Fatalf("port_lossy events = %d for a port whose protocol check passed", events)
	}
}

func TestUDPCheckPortListUsesEnabledPortsAndAnchorAndKeepsRequestedCandidates(t *testing.T) {
	inbounds := []store.FleetInboundRow{
		{Enabled: true, Settings: `{"port":2053}`},
		{Enabled: true, PortOverride: 4443, Settings: `{"port":8443}`},
		{Enabled: false, Settings: `{"port":2096}`},
		{Settings: `{"port":3000,"hop":{"from":4000,"to":4500}}`},
	}
	got := udpCheckPorts(nil, inbounds)
	want := []uint16{2053, 4443, 443}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default ports = %v, want %v", got, want)
	}
	if got := udpCheckPorts([]uint16{8443}, inbounds); !reflect.DeepEqual(got, []uint16{8443, 443}) {
		t.Fatalf("explicit ports = %v", got)
	}
	requested := []uint16{8443, 4443, 2053, 2083, 2087, 2096}
	if got := udpCheckPorts(requested, inbounds); !reflect.DeepEqual(got, append(requested, 443)) {
		t.Fatalf("requested twin candidates = %v", got)
	}
}

func TestUDPPortChecksUseBuiltNetworkAndPort(t *testing.T) {
	x, node, inbounds := portSpecRows(t)
	ports := x.f.udpCheckPorts(node, nil, inbounds)
	if slices.Contains(ports, 23456) {
		t.Fatalf("UDP check ports include VLESS TCP override 23456: %v", ports)
	}
	if got := x.f.udpCheckPorts(node, []uint16{443}, inbounds); !reflect.DeepEqual(got, []uint16{443}) {
		t.Fatalf("UDP check ports for Hysteria2 UDP 443 and VLESS TCP 443 = %v, want [443] once", got)
	}

	var hy2ID string
	var vlessIDs []string
	for _, inbound := range inbounds {
		switch inbound.Protocol {
		case "hysteria2":
			hy2ID = inbound.ID
		case "vless":
			vlessIDs = append(vlessIDs, inbound.ID)
		}
	}
	if hy2ID == "" || len(vlessIDs) != 2 {
		t.Fatalf("test inbounds do not contain Hysteria2 and both VLESS listeners: %+v", inbounds)
	}
	x.f.portLossyEvents(x.ctx, node, inbounds, []store.PortCheck{{
		NodeID: node.ID, Port: 443, Verdict: "lossy", CheckedAt: time.Now(),
	}}, map[uint16]store.PortCheck{})
	if got := x.count(`SELECT count(*) FROM event WHERE node_id = ? AND inbound_id = ? AND code = 'port_lossy'`, node.ID, hy2ID); got != 1 {
		t.Fatalf("Hysteria2 port_lossy events = %d, want 1", got)
	}
	for _, id := range vlessIDs {
		if got := x.count(`SELECT count(*) FROM event WHERE node_id = ? AND inbound_id = ? AND code = 'port_lossy'`, node.ID, id); got != 0 {
			t.Fatalf("VLESS inbound %s port_lossy events = %d, want 0", id, got)
		}
	}
}
