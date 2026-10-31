package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

type storeTCP = store.TCPCheck

func hy2Spec(id string, tls plugin.TLSMode, name, settings string) tcpSpec {
	if settings == "" {
		settings = `{}`
	}
	return tcpSpec{Profile: "hy2-" + id, Spec: plugin.InboundSpec{ID: id, Protocol: "hysteria2", Listen: plugin.Listen{Network: "udp", Port: 443},
		TLS: plugin.TLS{Mode: tls, ServerName: name}, Settings: json.RawMessage(settings)}}
}

func vlessSpec(id string, port uint16, settings string, tls plugin.TLS) tcpSpec {
	return tcpSpec{Profile: "vless-" + id, Spec: plugin.InboundSpec{ID: id, Protocol: "vless", Listen: plugin.Listen{Network: "tcp", Port: port},
		TLS: tls, Settings: json.RawMessage(settings)}}
}

const realityOther = `{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`

func TestTCPTargets(t *testing.T) {
	pin := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	for name, tc := range map[string]struct {
		specs []tcpSpec
		pins  map[string]string
		want  []tcpTarget
	}{
		"no inbounds": {},
		"no ACME gives no port 80": {
			specs: []tcpSpec{hy2Spec("a", plugin.TLSSelfSigned, "203.0.113.10", ""), vlessSpec("b", 8443, realityOther, plugin.TLS{})},
			want: []tcpTarget{
				{Port: 443, Role: "site", Profile: "hy2-a", Kind: "tls", SNI: "203.0.113.10"},
				{Port: 8443, Role: "vless", Profile: "vless-b", Kind: "tls", SNI: "www.example.org"},
			},
		},
		"an ACME site on 443 answers TLS-ALPN-01, 80 comes first": {
			specs: []tcpSpec{vlessSpec("b", 8443, realityOther, plugin.TLS{}), hy2Spec("a", plugin.TLSAcmeDomain, "node-a.example.com", "")},
			want: []tcpTarget{
				{Port: 80, Role: "acme", Kind: "http_mark"},
				{Port: 443, Role: "site", ALPN: true, Profile: "hy2-a", Kind: "tls", SNI: "node-a.example.com"},
				{Port: 8443, Role: "vless", Profile: "vless-b", Kind: "tls", SNI: "www.example.org"},
			},
		},
		"a site on another port has no ALPN": {
			specs: []tcpSpec{hy2Spec("a", plugin.TLSAcmeDomain, "node-a.example.com", `{"masquerade":{"tcp_port":8443}}`)},
			want: []tcpTarget{
				{Port: 80, Role: "acme", Kind: "http_mark"},
				{Port: 8443, Role: "site", Profile: "hy2-a", Kind: "tls", SNI: "node-a.example.com"},
			},
		},
		"a site that is switched off is not probed": {
			specs: []tcpSpec{hy2Spec("a", plugin.TLSSelfSigned, "203.0.113.10", `{"masquerade":{"tcp_port":0}}`)},
		},
		"a site claimed by VLESS on its port is not probed, VLESS REALITY with another site is": {
			specs: []tcpSpec{hy2Spec("a", plugin.TLSAcmeDomain, "node-a.example.com", ""), vlessSpec("b", 443, realityOther, plugin.TLS{})},
			want: []tcpTarget{
				{Port: 80, Role: "acme", Kind: "http_mark"},
				{Port: 443, Role: "vless", Profile: "vless-b", Kind: "tls", SNI: "www.example.org"},
			},
		},
		"REALITY self and a TLS front answer TLS-ALPN-01 on 443": {
			specs: []tcpSpec{
				vlessSpec("b", 443, `{"security":"reality","reality":{"target":"self","server_names":["node-a.example.com"]}}`, plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "node-a.example.com"}),
				vlessSpec("c", 8443, `{"security":"tls"}`, plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "node-a.example.com"}),
			},
			want: []tcpTarget{
				{Port: 80, Role: "acme", Kind: "http_mark"},
				{Port: 443, Role: "vless", ALPN: true, Profile: "vless-b", Kind: "tls", SNI: "node-a.example.com"},
				{Port: 8443, Role: "vless", Profile: "vless-c", Kind: "tls", SNI: "node-a.example.com"},
			},
		},
		"TLS fronts use their server name and only ACME on 443 gets ALPN": {
			specs: []tcpSpec{
				vlessSpec("a", 443, `{"security":"tls"}`, plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "origin.example.com"}),
				vlessSpec("b", 8443, `{"security":"tls"}`, plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "origin.example.com"}),
				vlessSpec("c", 9443, `{"security":"tls"}`, plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "origin.example.com"}),
			},
			want: []tcpTarget{
				{Port: 80, Role: "acme", Kind: "http_mark"},
				{Port: 443, Role: "vless", ALPN: true, Profile: "vless-a", Kind: "tls", SNI: "origin.example.com"},
				{Port: 8443, Role: "vless", Profile: "vless-b", Kind: "tls", SNI: "origin.example.com"},
				{Port: 9443, Role: "vless", Profile: "vless-c", Kind: "tls", SNI: "origin.example.com"},
			},
		},
		"the reported pin goes with its inbound": {
			specs: []tcpSpec{hy2Spec("a", plugin.TLSSelfSigned, "203.0.113.10", "")},
			pins:  map[string]string{"a": pin, "other": pin},
			want:  []tcpTarget{{Port: 443, Role: "site", Profile: "hy2-a", Kind: "tls", SNI: "203.0.113.10", Pin: pin}},
		},
		"a REALITY target without a name to send is skipped": {
			specs: []tcpSpec{vlessSpec("b", 8443, `{"reality":{"target":"203.0.113.9:443"}}`, plugin.TLS{})},
		},
		"the name of the target host is the fallback": {
			specs: []tcpSpec{vlessSpec("b", 8443, `{"reality":{"target":"www.example.org:443"}}`, plugin.TLS{})},
			want:  []tcpTarget{{Port: 8443, Role: "vless", Profile: "vless-b", Kind: "tls", SNI: "www.example.org"}},
		},
		"a UDP inbound of another protocol is not a TCP port": {
			specs: []tcpSpec{{Profile: "awg", Spec: plugin.InboundSpec{ID: "w", Protocol: "awg", Listen: plugin.Listen{Network: "udp", Port: 51820}}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := tcpTargets(tc.specs, tc.pins)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tcpTargets() =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

func TestTCPTargetsAreCappedAtEightWith80First(t *testing.T) {
	specs := []tcpSpec{hy2Spec("a", plugin.TLSAcmeDomain, "node-a.example.com", "")}
	for i := 0; i < 12; i++ {
		specs = append(specs, vlessSpec(fmt.Sprintf("v%02d", i), uint16(9000-i), realityOther, plugin.TLS{}))
	}
	got := tcpTargets(specs, nil)
	if len(got) != tcpcheck.MaxTargets || got[0].Port != 80 {
		t.Fatalf("got %d targets, first %+v", len(got), got[0])
	}
	for i := 2; i < len(got); i++ {
		if got[i].Port <= got[i-1].Port {
			t.Fatalf("targets after 80 are not sorted by port: %+v", got)
		}
	}
	if got[1].Port != 443 {
		t.Fatalf("the lowest port is dropped before higher ones: %+v", got)
	}
}

// tcpProto builds a TCP or UDP inbound with the TLS mode, name and network its profile settings ask for, so that the
// run can be driven without the real protocol plugins.
type tcpProto struct {
	fakeProto
}

func (p tcpProto) BuildInbound(in protocols.InboundInput) (plugin.InboundSpec, error) {
	var s struct {
		Port    int    `json:"port"`
		Network string `json:"network"`
		TLS     string `json:"tls"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(in.Profile.Settings, &s); err != nil {
		return plugin.InboundSpec{}, err
	}
	if in.PortOverride != 0 {
		s.Port = int(in.PortOverride)
	}
	spec := plugin.InboundSpec{Listen: plugin.Listen{Network: s.Network, Port: uint16(s.Port)}, Egress: "direct", Settings: in.Profile.Settings}
	switch s.TLS {
	case "acme":
		spec.TLS = plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: s.Name}
	case "acme_ip":
		spec.TLS = plugin.TLS{Mode: plugin.TLSAcmeIP, ServerName: s.Name}
	case "self":
		spec.TLS = plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: s.Name}
	}
	return spec, nil
}

// tcpCheckTarget is portCheckTarget with a hysteria2 (ACME site on 443) and a VLESS (REALITY with another site on 8443)
// inbound on the target.
func tcpCheckTarget(t *testing.T) (*l3Env, *agent, *conn) {
	t.Helper()
	x, target := newL3Env(t)
	reg, err := protocols.NewRegistry(fakeProto{"awg", plugin.ClientAmnezia},
		tcpProto{fakeProto{"hysteria2", plugin.ClientHapp}}, tcpProto{fakeProto{"vless", plugin.ClientMihomo}})
	if err != nil {
		t.Fatal(err)
	}
	x.f.reg = reg
	x.exec(`UPDATE node SET address = '203.0.113.10', provider = 'provider-a', country_code = 'TR' WHERE id = ?`, target.nodeID)
	x.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES
		('prf_hy2', 'hysteria2', 'alice-hy2', '{"port":443,"network":"udp","tls":"acme","name":"node-a.example.com"}', 1, 1, 1),
		('prf_vless', 'vless', 'alice-vless', '{"port":8443,"network":"tcp","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}', 1, 1, 1)`)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, enabled, created_at, updated_at) VALUES
		('inb_hy2', 'prf_hy2', ?, 1, 1, 1, 1), ('inb_vless', 'prf_vless', ?, 1, 1, 1, 1)`, target.nodeID, target.nodeID)
	x.f.settleUDP = func(context.Context) error { return nil }
	targetConn, _, _ := connectCaps(target, "tcp-target", capUDPCheck, capTCPCheck)
	return x, target, targetConn
}

func waitHTTPMark(c *conn) *agentv1.HttpMark {
	c.t.Helper()
	return c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHttpMark() != nil }).GetHttpMark()
}

func waitTCPProbe(c *conn) *agentv1.TcpProbe {
	c.t.Helper()
	return c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetTcpProbe() != nil }).GetTcpProbe()
}

// driveUDPPart answers the UDP half of a run with a node sender (arm, send, stop), all ports clean.
func driveUDPPart(targetConn, senderConn *conn) {
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(senderConn)
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop := waitUDPCount(targetConn, true)
	got, packets := map[uint16]uint64{}, map[uint16]uint64{}
	for _, port := range arm.Ports {
		got[uint16(port)], packets[uint16(port)] = 300, 300
	}
	replyPortStop(targetConn, stop, "4", got, packets)
}

func TestCheckPortsProbesTCPAfterUDPAndStoresTheRows(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	sender, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	result := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)

	driveUDPPart(targetConn, senderConn)
	mark := waitHTTPMark(targetConn)
	if len(mark.Token) != tcpcheck.MarkSize || len(mark.Answer) != tcpcheck.MarkSize || mark.HoldS != 30 {
		t.Fatalf("mark = %+v", mark)
	}
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
	probe := waitTCPProbe(senderConn)
	if probe.Host != "203.0.113.10" || len(probe.Targets) != 3 {
		t.Fatalf("probe = %+v", probe)
	}
	want := []*agentv1.TcpTarget{
		{Port: 80, Kind: "http_mark", Token: mark.Token, Answer: mark.Answer},
		{Port: 443, Kind: "tls", Sni: "node-a.example.com"},
		{Port: 8443, Kind: "tls", Sni: "www.example.org"},
	}
	for i, w := range want {
		g := probe.Targets[i]
		if g.Port != w.Port || g.Kind != w.Kind || g.Sni != w.Sni || string(g.Token) != string(w.Token) || string(g.Answer) != string(w.Answer) {
			t.Fatalf("target %d = %+v, want %+v", i, g, w)
		}
	}
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{
		"family": "4", "t80": "ours", "t443": "ours", "t8443": "timeout",
	}})

	got := <-result
	if got.err != nil || got.response.ErrorCode != "" || len(got.response.Tcp) != 3 {
		t.Fatalf("CheckPorts response = %+v, %v", got.response, got.err)
	}
	rows := got.response.Tcp
	if rows[0].Port != 80 || rows[0].Role != "acme" || rows[0].Verdict != "ours" || rows[0].Sender != "sender" || rows[0].Alpn {
		t.Errorf("port 80 row = %+v", rows[0])
	}
	if rows[1].Port != 443 || rows[1].Role != "site" || !rows[1].Alpn || rows[1].Profile != "alice-hy2" || rows[1].Verdict != "ours" {
		t.Errorf("port 443 row = %+v", rows[1])
	}
	if rows[2].Port != 8443 || rows[2].Role != "vless" || rows[2].Alpn || rows[2].Profile != "alice-vless" || rows[2].Verdict != "timeout" || rows[2].CheckedUnix == 0 {
		t.Errorf("port 8443 row = %+v", rows[2])
	}
	stored, err := x.st.TCPChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 3 || stored[0].Sender != sender.nodeID || stored[1].Verdict != "ours" {
		t.Fatalf("stored rows = %+v, %v", stored, err)
	}
	node, err := (nodeService{x.f}).GetNode(x.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: target.nodeID}))
	if err != nil || len(node.Msg.TcpChecks) != 3 || node.Msg.TcpChecks[2].Sender != "sender" || node.Msg.TcpChecks[2].Verdict != "timeout" {
		t.Fatalf("GetNode TCP checks = %+v, %v", node.Msg.GetTcpChecks(), err)
	}
	if got := x.count(`SELECT count(*) FROM audit WHERE action = 'node.ports_check' AND params LIKE '%"tcp_ports":"80,443,8443"%'`); got != 1 {
		t.Errorf("audit rows with the TCP ports = %d, want 1", got)
	}
}

func TestCheckPortsSaveTimeRunLeavesTheTCPHalfOut(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	done := make(chan struct{})
	go func() {
		defer close(done)
		x.f.CheckPorts(x.ctx, target.nodeID, []uint16{2053})
	}()
	driveUDPPart(targetConn, senderConn)
	// no TCP timeout is waited for: the call returns as soon as the UDP part is done
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CheckPorts did not return after the UDP part")
	}
	if rows, err := x.st.TCPChecks(x.ctx, target.nodeID); err != nil || len(rows) != 0 {
		t.Fatalf("CheckPorts stored TCP rows: %+v, %v", rows, err)
	}
	assertNoPortCommands(t, targetConn, senderConn)
}

// assertNoPortCommands drains what is queued for the connections and fails on a port check command (UDP or TCP).
func assertNoPortCommands(t *testing.T, conns ...*conn) {
	t.Helper()
	for _, c := range conns {
		for {
			select {
			case m := <-c.in:
				if m.GetHttpMark() != nil || m.GetTcpProbe() != nil || m.GetUdpCount() != nil || m.GetUdpSend() != nil {
					t.Fatalf("a port check command was sent: %+v", m)
				}
				continue
			default:
			}
			break
		}
	}
}

func TestCheckPortsAsSystemUsesThePanelAsTCPSenderOfLastResort(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	x.f.sendUDP = func(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error) {
		return "4", 300, nil
	}
	var gotHost string
	var gotTargets []tcpcheck.Target
	x.f.probeTCP = func(_ context.Context, host string, targets []tcpcheck.Target) (string, map[uint16]string, error) {
		gotHost, gotTargets = host, targets
		return "4", map[uint16]string{80: "other", 443: "ours", 8443: "refused"}, nil
	}
	done := make(chan portCheckFleetResult, 1)
	go func() {
		checks, sender, code := x.f.CheckPortsAsSystem(x.ctx, target.nodeID, []uint16{2053})
		done <- portCheckFleetResult{checks, sender, code}
	}()
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 300, 443: 300}, map[uint16]uint64{2053: 300, 443: 300})
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
	if r := <-done; r.code != "" || r.sender != "panel" {
		t.Fatalf("result = %+v", r)
	}
	if gotHost != "203.0.113.10" || len(gotTargets) != 3 || gotTargets[0].Kind != tcpcheck.KindHTTPMark || string(gotTargets[0].Token) != string(mark.Token) {
		t.Fatalf("panel probe: %q %+v", gotHost, gotTargets)
	}
	stored, err := x.st.TCPChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 3 || stored[0].Verdict != "other" || stored[0].Sender != "panel" || stored[2].Verdict != "refused" {
		t.Fatalf("stored rows = %+v, %v", stored, err)
	}
}

func TestTCPCheckWithoutAnySenderDoesNotRun(t *testing.T) {
	x, target, _ := tcpCheckTarget(t)
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	// the edge edition: the panel never sends, and no other node can
	if rows, ran := x.f.runTCPCheck(x.ctx, node, false); ran || len(rows) != 0 {
		t.Fatalf("no sender: rows %+v ran %v", rows, ran)
	}
	// a node without tcpcheck/1 is no sender
	addPortCheckPeer(x, "old-peer", "provider-b", "DE", capUDPCheck)
	if rows, ran := x.f.runTCPCheck(x.ctx, node, false); ran || len(rows) != 0 {
		t.Fatalf("old peer: rows %+v ran %v", rows, ran)
	}
}

func TestTCPCheckTargetWithoutTheCapabilityStillGetsTLSRows(t *testing.T) {
	x, target := newL3Env(t)
	reg, err := protocols.NewRegistry(fakeProto{"awg", plugin.ClientAmnezia},
		tcpProto{fakeProto{"hysteria2", plugin.ClientHapp}})
	if err != nil {
		t.Fatal(err)
	}
	x.f.reg = reg
	x.exec(`UPDATE node SET address = '203.0.113.10' WHERE id = ?`, target.nodeID)
	x.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES
		('prf_hy2', 'hysteria2', 'alice-hy2', '{"port":443,"network":"udp","tls":"acme","name":"node-a.example.com"}', 1, 1, 1)`)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, enabled, created_at, updated_at) VALUES ('inb_hy2', 'prf_hy2', ?, 1, 1, 1, 1)`, target.nodeID)
	connectCaps(target, "old-target", capUDPCheck)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		rows []storeTCP
		ran  bool
	}
	done := make(chan outcome, 1)
	go func() {
		rows, ran := x.f.runTCPCheck(x.ctx, node, false)
		done <- outcome{rows, ran}
	}()
	probe := waitTCPProbe(senderConn)
	if len(probe.Targets) != 1 || probe.Targets[0].Port != 443 {
		t.Fatalf("probe = %+v, want the TLS port only", probe)
	}
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t443": "ours"}})
	got := <-done
	if !got.ran || len(got.rows) != 2 || got.rows[0].Port != 80 || got.rows[0].Verdict != "" || got.rows[0].Reason != "agent_too_old" ||
		got.rows[1].Port != 443 || got.rows[1].Verdict != "ours" {
		t.Fatalf("rows = %+v ran %v", got.rows, got.ran)
	}
}

func TestTCPCheckPort80StatesFromTheTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  *agentv1.CommandResult
		want    bool // port 80 has a row
		verdict string
		reason  string
	}{
		{"another program holds port 80", &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "busy"}}, true, "busy", ""},
		{"no ACME name on the agent", &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "off"}}, false, "", ""},
		{"another mark is armed", &agentv1.CommandResult{Error: "busy"}, true, "", "busy"},
		{"an unknown answer", &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "maybe"}}, true, "", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, target, targetConn := tcpCheckTarget(t)
			_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
			node, err := x.st.Node(x.ctx, target.nodeID)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan []storeTCP, 1)
			go func() {
				rows, _ := x.f.runTCPCheck(x.ctx, node, false)
				done <- rows
			}()
			mark := waitHTTPMark(targetConn)
			replyPortCommand(targetConn, mark.RequestId, tc.result)
			probe := waitTCPProbe(senderConn)
			for _, target := range probe.Targets {
				if target.Port == 80 {
					t.Fatalf("port 80 was probed: %+v", probe.Targets)
				}
			}
			replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t443": "ours", "t8443": "ours"}})
			rows := <-done
			var row80 *storeTCP
			for i := range rows {
				if rows[i].Port == 80 {
					row80 = &rows[i]
				}
			}
			if (row80 != nil) != tc.want {
				t.Fatalf("rows = %+v", rows)
			}
			if row80 != nil && (row80.Verdict != tc.verdict || row80.Reason != tc.reason || row80.Sender != "") {
				t.Fatalf("port 80 row = %+v", *row80)
			}
		})
	}
}

func TestTCPCheckTriesTheNextSenderWhenTheFirstCannotProbe(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	_, firstConn := addPortCheckPeer(x, "sender-a", "provider-b", "DE", capUDPCheck, capTCPCheck)
	_, secondConn := addPortCheckPeer(x, "sender-b", "provider-c", "FR", capUDPCheck, capTCPCheck)
	if err := x.st.PutTCPChecks(x.ctx, target.nodeID, []storeTCP{{Port: 443, Role: "site", Verdict: "ours", Sender: "panel", CheckedAt: x.f.now()}}); err != nil {
		t.Fatal(err)
	}
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []storeTCP, 1)
	go func() {
		rows, _ := x.f.runTCPCheck(x.ctx, node, false)
		done <- rows
	}()
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
	// the senders are ordered by name when provider and country tell nothing: sender-a is asked first
	probe := waitTCPProbe(firstConn)
	replyPortCommand(firstConn, probe.RequestId, &agentv1.CommandResult{Error: "busy"})
	probe = waitTCPProbe(secondConn)
	replyPortCommand(secondConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{
		"family": "4", "t80": "ours", "t443": "ours", "t8443": "ours"}})
	rows := <-done
	if len(rows) != 3 || rows[0].Verdict != "ours" {
		t.Fatalf("rows = %+v", rows)
	}
	stored, err := x.st.TCPChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 3 {
		t.Fatalf("the second sender's rows were not stored: %+v, %v", stored, err)
	}
}

func TestTCPCheckEveryProbeFailingKeepsTheLastStoredResult(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	old := storeTCP{Port: 443, Role: "site", Verdict: "ours", Sender: "panel", CheckedAt: x.f.now()}
	if err := x.st.PutTCPChecks(x.ctx, target.nodeID, []storeTCP{old}); err != nil {
		t.Fatal(err)
	}
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []storeTCP, 1)
	go func() {
		rows, _ := x.f.runTCPCheck(x.ctx, node, false)
		done <- rows
	}()
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
	probe := waitTCPProbe(senderConn)
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Error: "no_route"})
	rows := <-done
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	for _, row := range rows {
		if row.Verdict != "" || row.Reason != "no_route" {
			t.Fatalf("row = %+v, want no verdict and reason no_route", row)
		}
	}
	stored, err := x.st.TCPChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 1 || stored[0].Verdict != "ours" {
		t.Fatalf("stored rows = %+v, %v", stored, err)
	}
}

func TestTCPProbeVerdictsOutsideTheKnownSetAreFailures(t *testing.T) {
	for _, v := range []string{"ours", "other", "refused", "timeout"} {
		if !validTCPVerdict(v) {
			t.Errorf("%q is a verdict", v)
		}
	}
	for _, v := range []string{"", "busy", "OURS", strconv.Itoa(1)} {
		if validTCPVerdict(v) {
			t.Errorf("%q is not a verdict of a probe", v)
		}
	}
}

func TestTCPCheckPanelOnTheTargetsHostAnswersButStoresNothing(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	x.exec(`UPDATE node SET address = '127.0.0.1' WHERE id = ?`, target.nodeID)
	x.f.probeTCP = func(context.Context, string, []tcpcheck.Target) (string, map[uint16]string, error) {
		return "4", map[uint16]string{80: "ours", 443: "ours", 8443: "ours"}, nil
	}
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []storeTCP, 1)
	go func() {
		rows, _ := x.f.runTCPCheck(x.ctx, node, true)
		done <- rows
	}()
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
	rows := <-done
	if len(rows) != 3 || rows[1].Verdict != "ours" || rows[1].Sender != "panel" {
		t.Fatalf("rows = %+v", rows)
	}
	if stored, err := x.st.TCPChecks(x.ctx, target.nodeID); err != nil || len(stored) != 0 {
		t.Fatalf("a hairpin result was stored: %+v, %v", stored, err)
	}
}

func TestTCPCheckDoesNotReuseTheMarkAfterAnAmbiguousFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		firstError string
		marked     bool // port 80 goes to the second sender
	}{
		{"a lost sender may have made its GET", "failed: link lost", false},
		{"a sender that never left", "no_route", true},
		{"a busy sender", "busy", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, target, targetConn := tcpCheckTarget(t)
			_, firstConn := addPortCheckPeer(x, "sender-a", "provider-b", "DE", capUDPCheck, capTCPCheck)
			second, secondConn := addPortCheckPeer(x, "sender-b", "provider-c", "FR", capUDPCheck, capTCPCheck)
			node, err := x.st.Node(x.ctx, target.nodeID)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan []storeTCP, 1)
			go func() {
				rows, _ := x.f.runTCPCheck(x.ctx, node, false)
				done <- rows
			}()
			mark := waitHTTPMark(targetConn)
			replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
			probe := waitTCPProbe(firstConn)
			if len(probe.Targets) != 3 || probe.Targets[0].Port != 80 {
				t.Fatalf("first probe = %+v", probe.Targets)
			}
			replyPortCommand(firstConn, probe.RequestId, &agentv1.CommandResult{Error: tc.firstError})
			probe = waitTCPProbe(secondConn)
			if has80 := probe.Targets[0].Port == 80; has80 != tc.marked {
				t.Fatalf("second probe = %+v, port 80 expected: %v", probe.Targets, tc.marked)
			}
			params := map[string]string{"family": "4", "t443": "ours", "t8443": "ours"}
			if tc.marked {
				params["t80"] = "ours"
			}
			replyPortCommand(secondConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: params})
			rows := <-done
			if len(rows) != 3 || rows[0].Port != 80 {
				t.Fatalf("rows = %+v", rows)
			}
			if tc.marked {
				if rows[0].Verdict != "ours" || rows[0].Sender != second.nodeID {
					t.Fatalf("port 80 row = %+v, want ours from the second sender", rows[0])
				}
			} else if rows[0].Verdict != "" || rows[0].Reason != "failed" {
				t.Fatalf("port 80 row = %+v, want no verdict and reason failed (never a false 'other')", rows[0])
			}
			if rows[1].Verdict != "ours" || rows[1].Sender != second.nodeID {
				t.Fatalf("port 443 row = %+v", rows[1])
			}
			if stored, err := x.st.TCPChecks(x.ctx, target.nodeID); err != nil || len(stored) != 3 {
				t.Fatalf("stored rows = %+v, %v", stored, err)
			}
		})
	}
}

func TestTCPCheckClearsRowsWhenNoTCPPortIsLeft(t *testing.T) {
	x, target, _ := tcpCheckTarget(t)
	if err := x.st.PutTCPChecks(x.ctx, target.nodeID, []storeTCP{{Port: 443, Role: "site", Verdict: "ours", Sender: "panel", CheckedAt: x.f.now()}}); err != nil {
		t.Fatal(err)
	}
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	// every inbound is switched off: no sender and no live agent are needed to see that the rows describe nothing
	x.exec(`UPDATE inbound SET enabled = 0 WHERE node_id = ?`, target.nodeID)
	if rows, ran := x.f.runTCPCheck(x.ctx, node, false); ran || len(rows) != 0 {
		t.Fatalf("rows %+v ran %v", rows, ran)
	}
	if stored, err := x.st.TCPChecks(x.ctx, target.nodeID); err != nil || len(stored) != 0 {
		t.Fatalf("stale rows stay: %+v, %v", stored, err)
	}
}

// onlyPort80Target leaves the target with a single TCP target, port 80: the Hysteria2 site is switched off, VLESS is
// disabled, and an old site row is stored.
func onlyPort80Target(t *testing.T) (*l3Env, store.NodeRow, *conn) {
	t.Helper()
	x, target, targetConn := tcpCheckTarget(t)
	addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	x.exec(`UPDATE profile SET settings_json = '{"port":443,"network":"udp","tls":"acme","name":"node-a.example.com","masquerade":{"tcp_port":0}}' WHERE id = 'prf_hy2'`)
	x.exec(`UPDATE inbound SET enabled = 0 WHERE id = 'inb_vless'`)
	if err := x.st.PutTCPChecks(x.ctx, target.nodeID, []storeTCP{{Port: 443, Role: "site", Verdict: "ours", Sender: "panel", CheckedAt: x.f.now()}}); err != nil {
		t.Fatal(err)
	}
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return x, node, targetConn
}

func TestTCPCheckPort80FallingAwayIsFinal(t *testing.T) {
	x, node, targetConn := onlyPort80Target(t)
	done := make(chan []storeTCP, 1)
	go func() {
		rows, _ := x.f.runTCPCheck(x.ctx, node, false)
		done <- rows
	}()
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "off"}})
	if rows := <-done; len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
	if stored, err := x.st.TCPChecks(x.ctx, node.ID); err != nil || len(stored) != 0 {
		t.Fatalf("the old site row stays: %+v, %v", stored, err)
	}
}

func TestTCPCheckBusyPort80IsTheWholeResultWhenNothingElseIsProbed(t *testing.T) {
	x, node, targetConn := onlyPort80Target(t)
	done := make(chan []storeTCP, 1)
	go func() {
		rows, _ := x.f.runTCPCheck(x.ctx, node, false)
		done <- rows
	}()
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "busy"}})
	<-done
	stored, err := x.st.TCPChecks(x.ctx, node.ID)
	if err != nil || len(stored) != 1 || stored[0].Port != 80 || stored[0].Verdict != "busy" {
		t.Fatalf("stored rows = %+v, %v", stored, err)
	}
}

func TestTCPCheckBusyPort80DoesNotReplaceGoodTLSRows(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	good := []storeTCP{
		{Port: 443, Role: "site", Verdict: "ours", Sender: "panel", CheckedAt: x.f.now()},
		{Port: 8443, Role: "vless", Verdict: "ours", Sender: "panel", CheckedAt: x.f.now()},
	}
	if err := x.st.PutTCPChecks(x.ctx, target.nodeID, good); err != nil {
		t.Fatal(err)
	}
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []storeTCP, 1)
	go func() {
		rows, _ := x.f.runTCPCheck(x.ctx, node, false)
		done <- rows
	}()
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "busy"}})
	probe := waitTCPProbe(senderConn)
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Error: "no_route"}) // the TLS ports were not probed
	rows := <-done
	if len(rows) != 3 || rows[0].Verdict != "busy" || rows[1].Verdict != "" || rows[1].Reason != "no_route" {
		t.Fatalf("rows = %+v", rows)
	}
	stored, err := x.st.TCPChecks(x.ctx, target.nodeID)
	if err != nil || len(stored) != 2 || stored[0].Port != 443 || stored[0].Verdict != "ours" || stored[1].Verdict != "ours" {
		t.Fatalf("a busy port 80 replaced the stored TLS rows: %+v, %v", stored, err)
	}
}

func TestCheckPortsAdminCallWaitingBehindASaveTimeRunRunsItsOwnTCPHalf(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	_, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	saved := make(chan struct{})
	go func() {
		defer close(saved)
		x.f.CheckPorts(x.ctx, target.nodeID, []uint16{2053}) // the save-time run: UDP only
	}()
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	admin := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	waitPortCheckWaiter(t, x.f, target.nodeID)
	send := waitUDPSend(senderConn)
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 300, 443: 300}, map[uint16]uint64{2053: 300, 443: 300})
	<-saved

	// the waiter may not take the UDP-only result: it runs UDP and TCP of its own
	driveUDPPart(targetConn, senderConn)
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
	probe := waitTCPProbe(senderConn)
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t80": "ours", "t443": "ours", "t8443": "ours"}})
	got := <-admin
	if got.err != nil || got.response.ErrorCode != "" || len(got.response.Tcp) != 3 || got.response.Tcp[0].Verdict != "ours" {
		t.Fatalf("admin response = %+v, %v", got.response, got.err)
	}
}

func TestCheckPortsSaveTimeCallWaitingBehindAFullRunTakesItsResult(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	sender, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	full := startPortCheckRPC(x.ctx, x, target.nodeID, 2053)
	arm := waitUDPCount(targetConn, false)
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	saved := make(chan portCheckFleetResult, 1)
	go func() {
		checks, sender, code := x.f.CheckPorts(x.ctx, target.nodeID, []uint16{2053})
		saved <- portCheckFleetResult{checks, sender, code}
	}()
	waitPortCheckWaiter(t, x.f, target.nodeID)
	send := waitUDPSend(senderConn)
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "300"}})
	stop := waitUDPCount(targetConn, true)
	replyPortStop(targetConn, stop, "4", map[uint16]uint64{2053: 300, 443: 300}, map[uint16]uint64{2053: 300, 443: 300})
	mark := waitHTTPMark(targetConn)
	replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
	probe := waitTCPProbe(senderConn)
	replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t80": "ours", "t443": "ours", "t8443": "ours"}})
	if got := <-full; got.err != nil || len(got.response.Tcp) != 3 {
		t.Fatalf("full run = %+v, %v", got.response, got.err)
	}
	select {
	case got := <-saved:
		if got.code != "" || got.sender != sender.nodeID || len(got.checks) != 2 {
			t.Fatalf("save-time result = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the save-time call did not take the finished run's result")
	}
	assertNoPortCommands(t, targetConn, senderConn)
	if calls := x.count(`SELECT count(*) FROM audit WHERE action = 'node.ports_check'`); calls != 2 {
		t.Errorf("audited calls = %d, want both", calls)
	}
}

func TestGetNodeNamesTheSendersOfUDPAndTCPRows(t *testing.T) {
	x, target, _ := tcpCheckTarget(t)
	peer, _ := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capTCPCheck)
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.st.PutPortChecks(x.ctx, []store.PortCheck{{NodeID: node.ID, Address: node.Address, Port: 443, Sent: 300, Got: 300,
		Verdict: "ok", Sender: peer.nodeID, CheckedAt: x.f.now()}}); err != nil {
		t.Fatal(err)
	}
	if err := x.st.PutTCPChecks(x.ctx, node.ID, []storeTCP{
		{Port: 443, Role: "site", Verdict: "ours", Sender: peer.nodeID, CheckedAt: x.f.now()},
		{Port: 8443, Role: "vless", Verdict: "ours", Sender: "nod_gone", CheckedAt: x.f.now()},
		{Port: 9443, Role: "vless", Verdict: "ours", Sender: "panel", CheckedAt: x.f.now()},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := (nodeService{x.f}).GetNode(x.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: node.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.PortChecks) != 1 || got.Msg.PortChecks[0].Sender != "sender" {
		t.Errorf("UDP rows = %+v", got.Msg.PortChecks)
	}
	tcp := got.Msg.TcpChecks
	if len(tcp) != 3 || tcp[0].Sender != "sender" || tcp[1].Sender != "unknown" || tcp[2].Sender != "panel" {
		t.Errorf("TCP rows = %+v", tcp)
	}
}
