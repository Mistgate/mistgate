package subs_test

import (
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// ipv6_egress: on every path, true only where the exit's IPv6 is known to work and is given to clients.

// setIPv6 stores what the doctor's ipv6 check says of a node ("" = it has not reported).
func (s *syncEnv) setIPv6(node, state string) {
	s.t.Helper()
	if _, err := s.st.W.Exec(`DELETE FROM doctor_result WHERE node_id = ? AND check_id = 'ipv6'`, node); err != nil {
		s.t.Fatal(err)
	}
	if state == "" {
		return
	}
	params := map[string]string{
		"ok":     `{"ipv6":"yes","connect":"ok","iface":"eth0","src":"2001:db8::5","addrs":"2001:db8::5/64"}`,
		"old":    `{"ipv6":"yes","connect":"ok"}`, // an agent that names no address: IPv6 works all the same
		"broken": `{"ipv6":"yes","connect":"failed"}`,
		"none":   `{"ipv6":"no"}`,
		"junk":   `{"ipv6":"maybe"}`,
	}[state]
	if _, err := s.st.W.Exec(`INSERT INTO doctor_result (node_id, check_id, status, params_json, received_unix) VALUES (?, 'ipv6', 1, ?, 1)`, node, params); err != nil {
		s.t.Fatal(err)
	}
}

func (s *syncEnv) setClientIPv6(node string, on bool) {
	s.t.Helper()
	if _, err := s.st.W.Exec(`UPDATE node SET client_ipv6 = ? WHERE id = ?`, on, node); err != nil {
		s.t.Fatal(err)
	}
}

// egressOf lists "<protocol>/<via>=<ipv6_egress>" of the paths, in the order of the answer.
func egressOf(b syncBody) string {
	var got []string
	for _, srv := range b.Servers {
		for _, p := range srv.Paths {
			got = append(got, p.Protocol+"/"+p.Via+"="+strconv.FormatBool(p.IPv6Egress))
		}
	}
	return strings.Join(got, " ")
}

// The AmneziaWG path of an exit without IPv6 has no address_v6 and keeps both ranges in allowed_ips.
func TestSyncIPv6EgressRule(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	want := func(name string, b syncBody, v6 bool) {
		t.Helper()
		exp := "hysteria2/direct=" + strconv.FormatBool(v6) + " awg/direct=" + strconv.FormatBool(v6)
		if got := egressOf(b); got != exp {
			t.Errorf("%s: %q, want %q", name, got, exp)
		}
		if !strings.Contains(b.raw, `"ipv6_egress"`) {
			t.Errorf("%s: no ipv6_egress in the answer", name)
		}
		aw := b.paths("awg")[0].Settings
		if _, has := aw["address_v6"]; has != v6 {
			t.Errorf("%s: AmneziaWG address_v6 present = %v, want %v", name, has, v6)
		}
		if allowed, _ := aw["allowed_ips"].([]any); len(allowed) != 2 {
			t.Errorf("%s: allowed_ips = %v, want both ranges", name, allowed)
		}
	}

	// Not reported yet: unknown is no, so a client is not told about IPv6 it may not have.
	unknown := e.mustSync(a.Key)
	want("not reported", unknown, false)

	e.setIPv6("nod_1", "ok")
	working := e.mustSync(a.Key)
	want("working", working, true)
	if working.etg == unknown.etg {
		t.Error("the ETag did not follow the node's IPv6 getting known")
	}

	e.setIPv6("nod_1", "old")
	want("working, agent without addresses", e.mustSync(a.Key), true)

	for _, state := range []string{"broken", "none", "junk"} { // ipv6_broken, no_ipv6, and a report that says nothing
		e.setIPv6("nod_1", state)
		got := e.mustSync(a.Key)
		want(state, got, false)
		if got.etg != unknown.etg {
			t.Errorf("%s: an answer that says what the unreported one says has another ETag", state)
		}
	}

	// The node's own switch.
	e.setIPv6("nod_1", "ok")
	e.setClientIPv6("nod_1", false)
	off := e.mustSync(a.Key)
	want("IPv6 for clients off", off, false)
	if off.etg != unknown.etg {
		t.Error("a node with the switch off and a node unknown differ")
	}
	e.setClientIPv6("nod_1", true)
	back := e.mustSync(a.Key)
	want("back on", back, true)
	assertNoInternals(t, "ipv6_egress", back.raw)
}

// A relay does not decide: the client leaves by the exit.
func TestSyncIPv6EgressOfARelayIsTheExits(t *testing.T) {
	e := newSyncEnv(t, nil)
	e.addRelay(e.inboundOf(e.profile), "FI", "udp")
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	hyPaths := func() string {
		var got []string
		for _, p := range e.mustSync(a.Key).paths("hysteria2") {
			got = append(got, p.Via+"="+strconv.FormatBool(p.IPv6Egress))
		}
		return strings.Join(got, " ")
	}
	e.setIPv6("nod_1", "broken") // the exit has none, the relay's node does
	e.setIPv6("nod_2", "ok")
	if got := hyPaths(); got != "direct=false relay=false" {
		t.Errorf("exit without IPv6, relay with: %s", got)
	}
	e.setIPv6("nod_1", "ok") // the exit has it, the relay's node has none
	e.setIPv6("nod_2", "none")
	e.setClientIPv6("nod_2", false)
	if got := hyPaths(); got != "direct=true relay=true" {
		t.Errorf("exit with IPv6, relay without: %s", got)
	}
}

// Through WARP the node's uplink is not what the client leaves by: the WARP account's IPv6 address is, and the node's "IPv6 for
// clients" switch does not apply.
func TestSyncIPv6EgressThroughWARP(t *testing.T) {
	e := newSyncEnv(t, nil)
	warp := must(e.svc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "hy2 WARP", SettingsJson: `{"port":8443,"egress":"warp"}`}))).Msg.Profile
	must(e.svc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: warp.Id, NodeId: "nod_1"})))
	g := must(e.svc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "warp", ProfileIds: []string{warp.Id}}))).Msg.Group.Id
	_, tok := e.personIn("alice", g, false, true)
	a := e.enrolDevice(tok, hwidA)
	account := func(v6 string, enabled int) {
		t.Helper()
		if _, err := e.st.W.Exec(`DELETE FROM warp_account`); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.W.Exec(`INSERT INTO warp_account (node_id, source, secret_enc, peer_public_key, endpoint_v4, address_v4, address_v6, enabled, created_at, updated_at)
			VALUES ('nod_1', 'imported', X'01', 'peer', '203.0.113.1', '172.16.0.2/32', ?, ?, 1, 1)`, v6, enabled); err != nil {
			t.Fatal(err)
		}
	}
	e.setIPv6("nod_1", "ok") // uplink and switch say yes, but there is no WARP account: not known to work
	if got := egressOf(e.mustSync(a.Key)); got != "hysteria2/warp=false" {
		t.Errorf("no WARP account: %q", got)
	}
	account("", 1)
	if got := egressOf(e.mustSync(a.Key)); got != "hysteria2/warp=false" {
		t.Errorf("WARP without IPv6: %q", got)
	}
	e.setIPv6("nod_1", "broken") // the uplink and the switch do not matter
	e.setClientIPv6("nod_1", false)
	account("2606:4700:110:8a::2/128", 1)
	if got := egressOf(e.mustSync(a.Key)); got != "hysteria2/warp=true" {
		t.Errorf("WARP with IPv6: %q", got)
	}
	account("2606:4700:110:8a::2/128", 0)
	if got := egressOf(e.mustSync(a.Key)); got != "hysteria2/warp=false" {
		t.Errorf("a disabled WARP account: %q", got)
	}
}
