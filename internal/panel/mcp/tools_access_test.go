//go:build !js

package mcp

import (
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mistgate/mistgate/internal/panel/connectiondiag"
)

// Fixtures for the access_report rules and the block classifier: pure inputs, no panel.

type fxb struct{ in *accessInput }

func newFx() *fxb {
	in := &accessInput{Hours: 24, NowUnix: 1700000000, SinceUnix: 1700000000 - 86400, Profiles: map[string]accProfile{}, Totals: map[string]accTotals{},
		Countries: map[string]*accCountry{}, Klick: map[string]*accKlick{}, UDPLossNodes: map[string]bool{}}
	f := &fxb{in: in}
	in.Profiles["prf_vless"] = accProfile{ID: "prf_vless", Name: "Web", Protocol: "vless", Summary: "TCP 8443 · REALITY · Vision"}
	in.Profiles["prf_hy2"] = accProfile{ID: "prf_hy2", Name: "Fast", Protocol: "hysteria2", Summary: "UDP 443 · Salamander"}
	in.Profiles["prf_awg"] = accProfile{ID: "prf_awg", Name: "Home", Protocol: "awg", Summary: "UDP 51820"}
	return f
}

func (f *fxb) node(name, country, status string) *fxb {
	f.in.Nodes = append(f.in.Nodes, accNode{ID: "nod_" + name, Name: name, Country: country, Status: status})
	return f
}

func (f *fxb) inbound(node, profile, proto string, port uint32) string {
	id := "inb_" + node + "_" + proto + "_" + u32(port)
	f.in.Inbounds = append(f.in.Inbounds, accInbound{ID: id, NodeID: "nod_" + node, ProfileID: profile, Protocol: proto, Port: port, Enabled: true, Direct: true, State: "active"})
	return id
}

func (f *fxb) group(id string, profiles ...string) *fxb {
	f.in.Groups = append(f.in.Groups, accGroup{ID: id, Name: id, ProfileIDs: profiles})
	return f
}

func (f *fxb) users(group string, n int, lastSeen int64) *fxb {
	base := 0
	for _, u := range f.in.Users {
		if u.GroupID == group {
			base++
		}
	}
	for i := 0; i < n; i++ {
		f.in.Users = append(f.in.Users, accUser{ID: "usr_" + group + "_" + u32(uint32(base+i)), GroupID: group, AllNodes: true, LastSeen: lastSeen})
	}
	f.in.UsersTotal = len(f.in.Users)
	return f
}

// rows gives every user of the group a device with these counters on the inbound.
func (f *fxb) rows(group, inbound, node string, r accRow) *fxb {
	for _, u := range f.in.Users {
		if u.GroupID == group {
			x := r
			x.UserID, x.DeviceID, x.InboundID, x.NodeID = u.ID, "dev_"+u.ID, inbound, "nod_"+node
			f.in.Rows = append(f.in.Rows, x)
		}
	}
	return f
}

func (f *fxb) cc(node, verdict string, mod func(*accCountry)) *fxb {
	c := &accCountry{NodeID: "nod_" + node, Country: "RU", Verdict: verdict, Confidence: "eyeball", Sources: []string{"globalping", "tspu"}}
	if mod != nil {
		mod(c)
	}
	f.in.Countries[c.NodeID] = c
	return f
}

func vant(network, kind, state string, icmp bool) accVantage {
	return accVantage{Network: network, Kind: kind, State: state, ICMPOK: icmp}
}

func ports(p ...any) []accTSPUPort {
	var out []accTSPUPort
	for i := 0; i < len(p); i += 2 {
		out = append(out, accTSPUPort{Port: uint32(p[i].(int)), Verdict: p[i+1].(string)})
	}
	return out
}

func hy2Good() accTotals {
	return accTotals{Protocol: "hysteria2", Connects: 40, Hellos: 50, Accepted: 45, Handshakes: 44}
}

func classOf(r *AccessReportV, node, class string) *blockClassV {
	for i := range r.BlockDiagnosis {
		if r.BlockDiagnosis[i].Node == node {
			for j := range r.BlockDiagnosis[i].Classes {
				if r.BlockDiagnosis[i].Classes[j].Class == class {
					return &r.BlockDiagnosis[i].Classes[j]
				}
			}
		}
	}
	return nil
}

func classes(r *AccessReportV, node string) []string {
	for _, n := range r.BlockDiagnosis {
		if n.Node == node {
			return classNames(n.Classes)
		}
	}
	return nil
}

func accFinding(r *AccessReportV, code, node string) *accessFindingV {
	for i := range r.Findings {
		if r.Findings[i].Code == code && (node == "" || r.Findings[i].Node == node) {
			return &r.Findings[i]
		}
	}
	return nil
}

func rec(r *AccessReportV, kind string) *accessRecV {
	for i := range r.Recommendations {
		if r.Recommendations[i].Kind == kind {
			return &r.Recommendations[i]
		}
	}
	return nil
}

func TestAccessVlessPortBlockedWhileHY2Works(t *testing.T) {
	f := newFx().node("be", "BE", "online").node("msk1", "RU", "online").group("family", "prf_vless", "prf_hy2").users("family", 4, 1699990000)
	v := f.inbound("be", "prf_vless", "vless", 8443)
	h := f.inbound("be", "prf_hy2", "hysteria2", 443)
	f.in.Totals[h] = hy2Good()
	f.rows("family", h, "be", accRow{Connects: 5, OnlineS: 100})
	f.rows("family", v, "be", accRow{OnlineS: 0})
	f.cc("be", "partial", func(c *accCountry) {
		c.TSPU = ports(443, "open", 8443, "blocked")
		c.Vantages = []accVantage{vant("MTS", "home", "dark", false), vant("Tele2", "mobile", "dark", false), vant("Rostelecom", "home", "reach", true)}
	})
	f.cc("msk1", "ok", func(c *accCountry) { c.Vantages = []accVantage{vant("Rostelecom", "home", "reach", true)} })
	r := buildAccess(f.in)

	c := classOf(r, "be", clsPortBlocked)
	if c == nil || c.Scope != "port:8443/tcp" || c.Confidence != confHigh || c.Detail["open_ports"] != "443" {
		t.Fatalf("port_blocked: %+v", c)
	}
	p := accFinding(r, "port_blocked", "be")
	if p == nil || p.Affected.Users != 4 || p.Affected.Groups[0] != "family" || !slices.Contains(p.Evidence, "tspu:nod_be:8443") {
		t.Fatalf("port_blocked finding: %+v", p)
	}
	pb := accFinding(r, "protocol_blocked_on_node", "be")
	if pb == nil || pb.Detail["blocked"] != "vless/tcp/8443" || pb.Detail["works"] != "hysteria2/udp/443" || !strings.Contains(pb.Detail["operators"], "MTS") {
		t.Fatalf("protocol_blocked_on_node: %+v", pb)
	}
	mv := rec(r, "move_port")
	if mv == nil || mv.Target["to_port"] != "443" || mv.PlanTool != "profile_update_plan" || mv.Target["from_port"] != "8443" {
		t.Fatalf("move_port: %+v", mv)
	}
	if accFinding(r, "group_without_working_way", "") != nil {
		t.Fatal("the group has a working HY2 way")
	}
	for _, cell := range r.Matrix {
		switch cell.Protocol {
		case "vless":
			if cell.AttemptsObservable || cell.SuccessPct != nil || cell.Way != stateFailing {
				t.Fatalf("vless cell: %+v", cell)
			}
		case "hysteria2":
			if cell.DevicesTried != 4 || cell.DevicesOK != 4 || cell.SuccessPct == nil || *cell.SuccessPct != 100 || cell.Verdict != "ok" || len(cell.Groups) != 1 {
				t.Fatalf("hy2 cell: %+v", cell)
			}
		}
	}
}

func TestAccessMovePortSkipsCollisionOnAnotherProfilePlacement(t *testing.T) {
	f := newFx().node("de1", "DE", "online")
	f.inbound("de1", "prf_vless", "vless", 8443)
	f.in.Profiles["prf_other"] = accProfile{ID: "prf_other", Name: "Other", Protocol: "vless", Summary: "TCP 443"}
	f.in.Inbounds = append(f.in.Inbounds, accInbound{ID: "inb_de1_other", NodeID: "nod_de1", ProfileID: "prf_other", Protocol: "vless", Port: 443, Enabled: true, Direct: true, State: "active"})
	f.cc("de1", "partial", func(c *accCountry) { c.TSPU = ports(443, "open", 8443, "blocked") })
	if rec(buildAccess(f.in), "move_port") != nil {
		t.Fatal("move_port recommended a TCP port already used on the profile placement")
	}
}

func TestAccessNodeCapKeepsRequestedNode(t *testing.T) {
	nodes := make([]accNode, 0, accessNodeCap+10)
	for i := 0; i < accessNodeCap+10; i++ {
		nodes = append(nodes, accNode{ID: "nod_" + u32(uint32(i)), Name: "de1", Status: "online"})
	}
	target := nodes[len(nodes)-1].ID
	capped, truncated := capAccessNodes(nodes, target)
	if !truncated || len(capped) != accessNodeCap || capped[len(capped)-1].ID != target {
		t.Fatalf("node cap dropped the requested node: truncated=%v nodes=%+v", truncated, capped)
	}
}

func TestAccessRemoveRecommendationNeedsAlternateForSameUsers(t *testing.T) {
	f := newFx().node("de1", "DE", "online").node("nl1", "NL", "online").
		group("red", "prf_vless").group("blue", "prf_hy2").users("red", 2, 1).users("blue", 2, 1)
	red := f.inbound("nl1", "prf_vless", "vless", 443)
	blue := f.inbound("de1", "prf_hy2", "hysteria2", 443)
	f.in.Totals[blue] = hy2Good()
	f.rows("blue", blue, "de1", accRow{Connects: 5})
	f.cc("nl1", "blocked", func(c *accCountry) {
		c.TSPU = ports(443, "blocked", 8443, "blocked")
		c.ListedBy = []string{"antifilter_ipsum"}
		c.Vantages = []accVantage{vant("MTS", "home", "dark", false), vant("Beeline", "mobile", "dark", false)}
	})
	f.cc("de1", "ok", func(c *accCountry) { c.Vantages = []accVantage{vant("MTS", "home", "reach", true)} })
	r := buildAccess(f.in)
	if rec(r, "remove_from_subscriptions") != nil {
		t.Fatalf("Red's blocked inbound %s has no alternate for Red users: %+v", red, r.Recommendations)
	}
}

func TestBlockDoesNotTreatTestedTSPUPortsAsAddressWide(t *testing.T) {
	f := newFx().node("de1", "DE", "online")
	f.inbound("de1", "prf_vless", "vless", 443)
	f.cc("de1", "partial", func(c *accCountry) { c.TSPU = ports(443, "blocked", 8443, "refused") })
	r := buildAccess(f.in)
	if c := classOf(r, "de1", clsIPBlocked); c != nil {
		t.Fatalf("the tested ports did not show an address-wide block: %+v", c)
	}
}

func TestAccessNodeDarkForOneOperator(t *testing.T) {
	f := newFx().node("nl1", "NL", "online").node("msk1", "RU", "online").group("family", "prf_hy2").users("family", 3, 1699990000)
	h := f.inbound("nl1", "prf_hy2", "hysteria2", 443)
	f.rows("family", h, "nl1", accRow{Connects: 3})
	f.cc("nl1", "partial", func(c *accCountry) {
		c.Vantages = []accVantage{vant("MTS", "home", "dark", false), vant("MTS", "home", "dark", false), vant("Beeline", "home", "reach", true), vant("Rostelecom", "mobile", "reach", true)}
	})
	f.cc("msk1", "ok", func(c *accCountry) { c.Vantages = []accVantage{vant("Rostelecom", "home", "reach", true)} })
	r := buildAccess(f.in)
	c := classOf(r, "nl1", clsOperator)
	if c == nil || !slices.Equal(c.Operators, []string{"MTS"}) || c.Confidence != confHigh {
		t.Fatalf("operator_specific: %+v", c)
	}
	if classOf(r, "nl1", clsIPBlocked) != nil || classOf(r, "nl1", clsWhitelist) != nil {
		t.Fatal("not an ip block and not a whitelist")
	}
	if fd := accFinding(r, "node_blocked_in_country", "nl1"); fd == nil || fd.Severity != "warning" || fd.Detail["dark"] != "MTS" {
		t.Fatalf("finding: %+v", fd)
	}
	rl := rec(r, "add_relay")
	if rl == nil || rl.Target["entry_node"] != "msk1" || rl.PlanTool != "relay_plan" {
		t.Fatalf("add_relay: %+v", rl)
	}
}

func TestAccessGroupWhoseOnlyProfilesFail(t *testing.T) {
	f := newFx().node("be", "BE", "online").node("de1", "DE", "online").
		group("kids", "prf_vless").group("family", "prf_vless", "prf_hy2").users("kids", 2, 1699990000).users("family", 3, 1699990000)
	f.inbound("be", "prf_vless", "vless", 443)
	h := f.inbound("de1", "prf_hy2", "hysteria2", 443)
	f.in.Totals[h] = hy2Good()
	f.rows("family", h, "de1", accRow{Connects: 5})
	f.cc("be", "blocked", func(c *accCountry) {
		c.TSPU = ports(443, "blocked", 8443, "blocked")
		c.ListedBy = []string{"antifilter_ipsum"}
		c.Vantages = []accVantage{vant("MTS", "home", "dark", false), vant("Beeline", "mobile", "dark", false)}
	})
	f.cc("de1", "ok", func(c *accCountry) { c.Vantages = []accVantage{vant("MTS", "home", "reach", true)} })
	r := buildAccess(f.in)
	if c := classOf(r, "be", clsIPBlocked); c == nil || c.Confidence != confHigh || c.Detail["scope"] != "ip" {
		t.Fatalf("ip_blocked: %+v", c)
	}
	g := accFinding(r, "group_without_working_way", "")
	if g == nil || g.Detail["group"] != "kids" || g.Severity != "error" || g.Affected.Users != 2 {
		t.Fatalf("group finding: %+v", g)
	}
	for _, fd := range r.Findings {
		if fd.Code == "group_without_working_way" && fd.Detail["group"] == "family" {
			t.Fatal("family has HY2 on de1")
		}
	}
	if p := rec(r, "prefer_protocol"); p == nil || p.Target["group_id"] != "kids" || p.Target["profile_ids"] != "prf_hy2,prf_vless" || p.PlanTool != "group_update_plan" {
		t.Fatalf("prefer_protocol: %+v", p)
	}
	if rl := rec(r, "add_relay"); rl == nil || rl.Target["entry_node"] != "de1" {
		t.Fatalf("add_relay: %+v", rl)
	}
	if rec(r, "move_port") != nil {
		t.Fatal("changing ports does not help against a banned address")
	}
	if rec(r, "add_cdn_profile") == nil || rec(r, "new_node_or_ip") == nil {
		t.Fatalf("ip_blocked also wants a CDN profile and a new address: %+v", r.Recommendations)
	}
}

func TestAccessAWGHandshakeNoReply(t *testing.T) {
	f := newFx().node("fi1", "FI", "online").node("msk1", "RU", "online").group("family", "prf_awg", "prf_hy2").users("family", 3, 1699990000)
	a := f.inbound("fi1", "prf_awg", "awg", 51820)
	h := f.inbound("fi1", "prf_hy2", "hysteria2", 443)
	f.in.Totals[a] = accTotals{Protocol: "awg", Stalls: 12, KeysStalled: 4, KeysTried: 4, Unsupported: connectiondiag.UnsupportedCounters("awg")} // an agent without links/3
	f.in.Totals[h] = hy2Good()
	f.rows("family", a, "fi1", accRow{Stalls: 4})
	f.rows("family", h, "fi1", accRow{Connects: 4})
	f.in.Nodes[0].UDPChecks = []accUDPCheck{{Port: 51820, Verdict: "ok", Sender: "msk1", CheckedUnix: f.in.NowUnix}}
	f.cc("fi1", "ok", func(c *accCountry) {
		c.TSPU = ports(443, "open")
		c.Vantages = []accVantage{vant("MTS", "home", "reach", true)}
	})
	r := buildAccess(f.in)
	fd := accFinding(r, "awg_handshake_no_reply", "fi1")
	if fd == nil || fd.Detail["stalls"] != "12" || fd.Detail["connects"] != "0" || fd.Detail["keys_stalled"] != "4" {
		t.Fatalf("awg finding: %+v", fd)
	}
	c := classOf(r, "fi1", clsFingerprinted)
	if c == nil || c.Confidence != confLow || c.protocol != "awg" || len(c.Unknown) == 0 || !strings.Contains(c.Unknown[0], "is not known") {
		t.Fatalf("fingerprinted: %+v", c)
	}
	for _, cell := range r.Matrix {
		if cell.Protocol == "awg" && (cell.RejectSplit == nil || cell.RejectSplit.StalledHandshake != 12 || cell.Verdict != "failing") {
			t.Fatalf("awg cell: %+v", cell)
		}
	}
	if rec(r, "change_obfuscation") == nil {
		t.Fatal("change_obfuscation expected")
	}
}

func TestAccessWorkingRelayIsAWay(t *testing.T) {
	build := func(relayOK bool) *AccessReportV {
		f := newFx().node("be", "BE", "online").node("de1", "DE", "online").group("kids", "prf_vless").users("kids", 2, 1699990000)
		v := f.inbound("be", "prf_vless", "vless", 443)
		f.cc("be", "blocked", func(c *accCountry) {
			c.TSPU = ports(443, "blocked")
			c.Vantages = []accVantage{vant("MTS", "home", "dark", false), vant("Beeline", "mobile", "dark", false)}
		})
		f.cc("de1", "ok", func(c *accCountry) { c.Vantages = []accVantage{vant("MTS", "home", "reach", true)} })
		f.in.Relays = []accRelay{{ID: "rel_1", EntryID: "nod_de1", ExitID: "nod_be", InboundID: v, State: "active", Enabled: true, ExitLive: true, AgentSupported: true, Checked: true, CheckOK: relayOK}}
		return buildAccess(f.in)
	}
	r := build(true)
	if accFinding(r, "group_without_working_way", "") != nil {
		t.Fatalf("a working relay is a way: %+v", r.Findings)
	}
	if accFinding(r, "relay_down", "") != nil || rec(r, "add_relay") != nil {
		t.Fatal("no relay_down and no second relay when one works")
	}
	r = build(false)
	if fd := accFinding(r, "relay_down", ""); fd == nil || fd.Detail["why"] != "the last check failed" || !slices.Contains(fd.Evidence, "relay:rel_1") {
		t.Fatalf("relay_down: %+v", fd)
	}
	if accFinding(r, "group_without_working_way", "") == nil {
		t.Fatal("a failed relay is no way")
	}
}

func TestAccessCDNOnlyOption(t *testing.T) {
	f := newFx().node("be", "BE", "online").node("de1", "DE", "online").group("kids", "prf_vless", "prf_hy2").users("kids", 2, 1699990000)
	f.inbound("be", "prf_vless", "vless", 443)
	h := f.inbound("de1", "prf_hy2", "hysteria2", 443)
	f.in.Inbounds[1].CDN = true
	f.in.Totals[h] = hy2Good()
	f.cc("be", "blocked", func(c *accCountry) {
		c.TSPU = ports(443, "blocked")
		c.Vantages = []accVantage{vant("MTS", "home", "dark", false), vant("Beeline", "mobile", "dark", false)}
	})
	r := buildAccess(f.in)
	if accFinding(r, "cdn_only_option", "") == nil {
		t.Fatalf("cdn_only_option: %+v", r.Findings)
	}
}

func TestAccessNeverConnectedGiven(t *testing.T) {
	f := newFx().node("de1", "DE", "online").group("family", "prf_hy2").users("family", 3, 0).users("family", 2, 1699990000)
	h := f.inbound("de1", "prf_hy2", "hysteria2", 443)
	f.in.Totals[h] = hy2Good()
	// only two of the five tried
	f.in.Rows = append(f.in.Rows, accRow{UserID: "usr_family_3", DeviceID: "d", InboundID: h, NodeID: "nod_de1", Connects: 2}, accRow{UserID: "usr_family_4", DeviceID: "d", InboundID: h, NodeID: "nod_de1", Connects: 1})
	r := buildAccess(f.in)
	cell := r.Matrix[0]
	if cell.GivenUsers != 5 || cell.NeverConnected != 3 || cell.UntriedUsers != 3 || cell.DevicesTried != 2 || cell.DevicesOK != 2 {
		t.Fatalf("cell: %+v", cell)
	}
	if fd := accFinding(r, "given_never_connected", "de1"); fd == nil || fd.Affected.Users != 3 {
		t.Fatalf("finding: %+v", fd)
	}
}

func TestAccessFilters(t *testing.T) {
	f := newFx().node("be", "BE", "online").node("de1", "DE", "online").group("family", "prf_vless", "prf_hy2").users("family", 2, 1699990000)
	f.inbound("be", "prf_vless", "vless", 443)
	f.inbound("de1", "prf_hy2", "hysteria2", 443)
	f.in.Node = "nod_be"
	if r := buildAccess(f.in); len(r.Matrix) != 1 || r.Matrix[0].Node != "be" || r.Filters["node"] != "nod_be" {
		t.Fatalf("node filter: %+v", r.Matrix)
	}
	f.in.Node, f.in.Protocol = "", "hysteria2"
	if r := buildAccess(f.in); len(r.Matrix) != 1 || r.Matrix[0].Protocol != "hysteria2" {
		t.Fatalf("protocol filter: %+v", r.Matrix)
	}
}

func TestAccessUDPLossyPath(t *testing.T) {
	f := newFx().node("nl1", "NL", "online").node("de1", "DE", "online").group("family", "prf_hy2").users("family", 3, 1699990000)
	h := f.inbound("nl1", "prf_hy2", "hysteria2", 443)
	f.in.Totals[h] = hy2Good()
	f.rows("family", h, "nl1", accRow{Connects: 3, LossPermille: 120, PktsSent: 5000})
	f.in.Loss = []accLoss{{FromID: "nod_de1", FromName: "de1", ToID: "nod_nl1", ToName: "nl1", LossPct: 22}}
	r := buildAccess(f.in)
	fd := accFinding(r, "udp_lossy_path", "nl1")
	if fd == nil || !slices.Contains(fd.Evidence, "loss:nod_de1:nod_nl1") || !slices.Contains(fd.Evidence, "conn:"+h) {
		t.Fatalf("udp_lossy_path: %+v", fd)
	}
	if x := rec(r, "investigate"); x == nil || x.NextTool != "path_probe" {
		t.Fatal("investigate with path_probe")
	}
}

// ---------------------------------------------------------------------------------------------------------------------
// the block classes

func TestBlockIPBlockedTCPOnly(t *testing.T) {
	f := newFx().node("be", "BE", "online").group("g", "prf_vless").users("g", 1, 1)
	f.inbound("be", "prf_vless", "vless", 443)
	f.cc("be", "tcp_blocked", func(c *accCountry) {
		c.TSPU = ports(443, "blocked", 8443, "blocked")
		c.Vantages = []accVantage{vant("MTS", "home", "dark", true), vant("Beeline", "mobile", "dark", true)}
	})
	c := classOf(buildAccess(f.in), "be", clsIPBlocked)
	if c == nil || c.Detail["scope"] != "tcp_only" || c.Confidence == confLow || !strings.Contains(strings.Join(c.Unknown, " "), "UDP") {
		t.Fatalf("%+v", c)
	}
}

func TestBlockWhitelistVersusIPBlocked(t *testing.T) {
	mob := []accVantage{vant("MTS", "mobile", "dark", false), vant("Tele2", "mobile", "dark", false)}
	// mobile dark, fixed lines reach: whitelist
	f := newFx().node("be", "BE", "online")
	f.cc("be", "partial", func(c *accCountry) { c.Vantages = append(slices.Clone(mob), vant("Rostelecom", "home", "reach", true)) })
	r := buildAccess(f.in)
	c := classOf(r, "be", clsWhitelist)
	if c == nil || c.Confidence != confMedium || !slices.Equal(c.Operators, []string{"MTS", "Tele2"}) || classOf(r, "be", clsIPBlocked) != nil {
		t.Fatalf("whitelist: %v", classes(r, "be"))
	}
	if len(c.Next) == 0 || c.Next[0].Kind != "add_relay" || !strings.Contains(c.Explain, "will not help") || len(c.Unknown) == 0 {
		t.Fatalf("next: %+v", c)
	}
	// the same with a whitelist vantage state: high
	f = newFx().node("be", "BE", "online")
	f.cc("be", "partial", func(c *accCountry) {
		c.Vantages = []accVantage{vant("MTS", "mobile", "whitelist", false), vant("Rostelecom", "home", "reach", true)}
	})
	if c := classOf(buildAccess(f.in), "be", clsWhitelist); c == nil || c.Confidence != confHigh {
		t.Fatalf("whitelist state: %+v", c)
	}
	// fixed lines dark too: it is the address, not a whitelist
	f = newFx().node("be", "BE", "online")
	f.cc("be", "blocked", func(c *accCountry) { c.Vantages = append(slices.Clone(mob), vant("Rostelecom", "home", "dark", false)) })
	r = buildAccess(f.in)
	if classOf(r, "be", clsWhitelist) != nil || classOf(r, "be", clsIPBlocked) == nil {
		t.Fatalf("ip: %v", classes(r, "be"))
	}
	// one mobile operator only: not enough for a whitelist, it is operator specific
	f = newFx().node("be", "BE", "online")
	f.cc("be", "partial", func(c *accCountry) {
		c.Vantages = []accVantage{vant("MTS", "mobile", "dark", false), vant("Rostelecom", "home", "reach", true)}
	})
	r = buildAccess(f.in)
	if classOf(r, "be", clsWhitelist) != nil || classOf(r, "be", clsOperator) == nil {
		t.Fatalf("one operator: %v", classes(r, "be"))
	}
	// a whitelist signal from bsbord or cheburcheck lifts a single mobile operator to a high-confidence whitelist
	f = newFx().node("be", "BE", "online")
	f.cc("be", "partial", func(c *accCountry) {
		c.WhitelistSource = true
		c.Vantages = []accVantage{vant("MTS", "mobile", "dark", false), vant("Rostelecom", "home", "reach", true)}
	})
	if c := classOf(buildAccess(f.in), "be", clsWhitelist); c == nil || c.Confidence != confHigh {
		t.Fatalf("source whitelist: %+v", c)
	}
}

func TestBlockTCPThrottledVersusBroken(t *testing.T) {
	build := func(status string) *AccessReportV {
		f := newFx().node("de1", "DE", status).group("g1", "prf_vless", "prf_hy2").group("g2", "prf_vless", "prf_hy2").
			users("g1", 2, 1).users("g2", 1, 1)
		f.inbound("de1", "prf_vless", "vless", 443)
		h := f.inbound("de1", "prf_hy2", "hysteria2", 8443)
		f.in.Totals[h] = hy2Good()
		f.in.Klick["nod_de1"] = &accKlick{accKlickTally: accKlickTally{Reports: 6, OK: 2, Freeze: 4}, Kinds: []accKlickKind{
			{Kind: "vless", accKlickTally: accKlickTally{Reports: 5, OK: 1, Freeze: 4}, DLChecked: 5, DLStalled: 4},
			{Kind: "hysteria2", accKlickTally: accKlickTally{Reports: 3, OK: 3}}}}
		f.cc("de1", "ok", func(c *accCountry) {
			c.TSPU = ports(443, "open")
			c.Vantages = []accVantage{vant("MTS", "home", "reach", true)}
		})
		return buildAccess(f.in)
	}
	r := build("online")
	c := classOf(r, "de1", clsTCPThrottled)
	if c == nil || c.Confidence != confHigh || c.protocol != "vless" || c.Detail["udp_works"] != "yes" {
		t.Fatalf("throttled: %+v", c)
	}
	var prefer []accessRecV
	for _, recommendation := range r.Recommendations {
		if recommendation.Kind == "prefer_protocol" {
			prefer = append(prefer, recommendation)
		}
	}
	if len(prefer) != 2 || rec(r, "move_port") != nil {
		t.Fatalf("throttled wants UDP, never a port: %+v", r.Recommendations)
	}
	for _, recommendation := range prefer {
		if recommendation.Target["group_id"] == "" || recommendation.Target["profile_ids"] != "prf_hy2" || recommendation.Target["prefer"] != "" || recommendation.PlanTool != "group_update_plan" {
			t.Fatalf("group update target does not match its plan: %+v", recommendation)
		}
	}
	// the node is down: ours, never the censor
	r = build("down")
	if cl := classes(r, "de1"); !slices.Equal(cl, []string{clsBroken}) {
		t.Fatalf("down node: %v", cl)
	}
	if rec(r, "fix_ours") == nil {
		t.Fatal("fix_ours")
	}
	// a failed server: ours for that server
	f := newFx().node("de1", "DE", "online")
	f.inbound("de1", "prf_vless", "vless", 443)
	f.in.Inbounds[0].State, f.in.Inbounds[0].LastErrorCode = "failed", "bind"
	f.cc("de1", "ok", nil)
	if c := classOf(buildAccess(f.in), "de1", clsBroken); c == nil || c.Scope != "inbound:"+f.in.Inbounds[0].ID {
		t.Fatalf("failed inbound: %+v", c)
	}
	// a closed port seen from outside the country is ours too
	f = newFx().node("de1", "DE", "online")
	f.in.Nodes[0].TCPChecks = []accTCPCheck{{Port: 8443, Verdict: "refused", Sender: "panel", CheckedUnix: f.in.NowUnix}}
	f.cc("de1", "ok", func(c *accCountry) { c.TSPU = ports(443, "open", 8443, "blocked") })
	r = buildAccess(f.in)
	if classOf(r, "de1", clsBroken) == nil || classOf(r, "de1", clsPortBlocked) != nil {
		t.Fatalf("refused port: %v", classes(r, "de1"))
	}
}

func TestBlockUDPBlocked(t *testing.T) {
	f := newFx().node("fi1", "FI", "online").node("msk1", "RU", "online").group("g", "prf_vless", "prf_hy2", "prf_awg").users("g", 2, 1)
	f.inbound("fi1", "prf_vless", "vless", 443)
	h := f.inbound("fi1", "prf_hy2", "hysteria2", 443)
	a := f.inbound("fi1", "prf_awg", "awg", 51820)
	f.in.Totals[h] = accTotals{Protocol: "hysteria2", Hellos: 60, Accepted: 0, Handshakes: 0, HellosForeign: 5}
	f.in.Totals[a] = accTotals{Protocol: "awg", Stalls: 9}
	f.in.Loss = []accLoss{{FromID: "nod_msk1", FromName: "msk1", ToID: "nod_fi1", ToName: "fi1", LossPct: 100}}
	f.cc("fi1", "ok", func(c *accCountry) {
		c.TSPU = ports(443, "open")
		c.Vantages = []accVantage{vant("MTS", "home", "reach", true)}
	})
	r := buildAccess(f.in)
	c := classOf(r, "fi1", clsUDPBlocked)
	if c == nil || c.Confidence != confHigh || classOf(r, "fi1", clsFingerprinted) != nil {
		t.Fatalf("udp_blocked: %v", classes(r, "fi1"))
	}
	if rec(r, "prefer_tcp_protocol") == nil {
		t.Fatalf("recs: %+v", r.Recommendations)
	}
	// counters only, no in-country check: low confidence and the missing check is named
	f.in.Loss = nil
	c = classOf(buildAccess(f.in), "fi1", clsUDPBlocked)
	if c == nil || c.Confidence != confLow || !strings.Contains(strings.Join(c.Unknown, " "), "path_probe udp") {
		t.Fatalf("counters only: %+v", c)
	}
	// no TCP evidence: UDP failing does not prove UDP is cut (the node may be broken)
	cc := f.in.Countries["nod_fi1"]
	cc.Verdict, cc.Vantages, cc.TSPU = "unknown", nil, nil
	if classOf(buildAccess(f.in), "fi1", clsUDPBlocked) != nil {
		t.Fatal("no claim without a working TCP path")
	}
}

func TestBlockIgnoresStalePortChecks(t *testing.T) {
	f := newFx().node("de1", "DE", "online").node("msk1", "RU", "online").group("g", "prf_vless", "prf_hy2").users("g", 2, 1)
	f.inbound("de1", "prf_vless", "vless", 8443)
	h := f.inbound("de1", "prf_hy2", "hysteria2", 443)
	f.in.Totals[h] = accTotals{Protocol: "hysteria2", Hellos: 60, Accepted: 0, Handshakes: 0, HellosForeign: 5}
	f.in.Nodes[0].UDPChecks = []accUDPCheck{{Port: 443, Verdict: "broken", Sender: "msk1", CheckedUnix: f.in.NowUnix - 25*60*60}}
	f.in.Nodes[0].TCPChecks = []accTCPCheck{{Port: 8443, Verdict: "refused", Sender: "panel", CheckedUnix: f.in.NowUnix - 25*60*60}}
	f.cc("de1", "ok", func(c *accCountry) {
		c.TSPU = ports(443, "open")
		c.Vantages = []accVantage{vant("MTS", "home", "reach", true)}
	})
	r := buildAccess(f.in)
	if c := classOf(r, "de1", clsUDPBlocked); c == nil || c.Confidence != confLow || !strings.Contains(strings.Join(c.Unknown, " "), "path_probe udp") {
		t.Fatalf("stale local UDP check affected classification: %+v", c)
	}
	if c := classOf(r, "de1", clsBroken); c != nil {
		t.Fatalf("stale foreign TCP refusal affected classification: %+v", c)
	}
}

func TestBlockRegionalAndUnknown(t *testing.T) {
	f := newFx().node("be", "BE", "online").node("de1", "DE", "online")
	f.cc("be", "partial", func(c *accCountry) {
		c.Vantages = []accVantage{{Network: "MTS", Region: "Crimea", Kind: "home", State: "dark"}, {Network: "MTS", Region: "Moscow", Kind: "home", State: "reach", ICMPOK: true},
			{Network: "Beeline", Region: "Moscow", Kind: "home", State: "reach", ICMPOK: true}}
	})
	r := buildAccess(f.in)
	if c := classOf(r, "be", clsRegional); c == nil || !slices.Equal(c.Regions, []string{"Crimea"}) {
		t.Fatalf("regional: %v", classes(r, "be"))
	}
	c := classOf(r, "de1", clsUnknown)
	if c == nil || !strings.Contains(c.Unknown[0], "country check is not configured") || c.Confidence != confLow {
		t.Fatalf("unknown: %+v", c)
	}
	if fd := accFinding(r, "unknown", "de1"); fd == nil || fd.Severity != "info" {
		t.Fatal("unknown is info")
	}
}

func TestBlockListedOnlyIsLowConfidence(t *testing.T) {
	f := newFx().node("be", "BE", "online")
	f.cc("be", "listed", func(c *accCountry) {
		c.ListedBy = []string{"antifilter_ipsum"}
		c.Sources = []string{"lists"}
		c.Confidence = "listed"
	})
	r := buildAccess(f.in)
	c := classOf(r, "be", clsIPBlocked)
	if c == nil || c.Confidence != confLow || len(c.Unknown) == 0 {
		t.Fatalf("listed: %+v", c)
	}
	if rec(r, "investigate") == nil || rec(r, "add_relay") != nil {
		t.Fatalf("a list alone proves nothing: %+v", r.Recommendations)
	}
}

func TestBlockPortBlockedFromGlobalping(t *testing.T) {
	f := newFx().node("be", "BE", "online")
	f.cc("be", "partial", func(c *accCountry) {
		c.ProbePorts = []accProbePort{{Port: 443, OK: true, Probes: 4, Fail: 0}, {Port: 8443, OK: false, Probes: 4, Fail: 4}}
		c.TSPU = nil
	})
	c := classOf(buildAccess(f.in), "be", clsPortBlocked)
	if c == nil || c.Confidence != confMedium || c.Detail["open_ports"] != "443" {
		t.Fatalf("%+v", c)
	}
}

func TestAccessRecommendationsAreDeterministicAndKnown(t *testing.T) {
	build := func() *AccessReportV {
		f := newFx().node("be", "BE", "online").node("de1", "DE", "online").node("msk1", "RU", "online").group("kids", "prf_vless").users("kids", 3, 1)
		f.inbound("be", "prf_vless", "vless", 8443)
		f.cc("be", "blocked", func(c *accCountry) {
			c.TSPU = ports(443, "blocked", 8443, "blocked")
			c.Vantages = []accVantage{vant("MTS", "home", "dark", false), vant("Beeline", "mobile", "dark", false)}
		})
		f.cc("de1", "ok", nil)
		f.cc("msk1", "ok", nil)
		return buildAccess(f.in)
	}
	a, b := build(), build()
	ja, _ := marshal(a)
	jb, _ := marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("the report is not deterministic")
	}
	admin := ToolNames(ProfileAdmin)
	if len(a.Recommendations) == 0 {
		t.Fatal("no recommendations")
	}
	for i, r := range a.Recommendations {
		if r.Rank != i+1 || len(r.Why) == 0 || r.Effect == "" {
			t.Fatalf("rec %d: %+v", i, r)
		}
		for _, tool := range []string{r.PlanTool, r.NextTool} {
			if tool != "" && !slices.Contains(admin, tool) {
				t.Errorf("%s names a tool that does not exist: %s", r.Kind, tool)
			}
		}
	}
	for _, n := range a.BlockDiagnosis {
		for _, c := range n.Classes {
			for _, s := range c.Next {
				if s.PlanTool != "" && !slices.Contains(admin, s.PlanTool) {
					t.Errorf("%s: next step names a missing tool %s", c.Class, s.PlanTool)
				}
			}
		}
	}
}

func TestAccessSizeCap(t *testing.T) {
	f := newFx()
	for i := 0; i < 40; i++ {
		name := "n" + u32(uint32(i))
		f.node(name, "DE", "online")
		f.inbound(name, "prf_vless", "vless", 8443)
		f.inbound(name, "prf_hy2", "hysteria2", 443)
		f.cc(name, "partial", func(c *accCountry) {
			c.TSPU = ports(443, "open", 8443, "blocked")
			for j := 0; j < 8; j++ {
				c.Vantages = append(c.Vantages, vant("Operator"+u32(uint32(j)), "home", map[bool]string{true: "dark", false: "reach"}[j%2 == 0], false))
			}
		})
	}
	for g := 0; g < 12; g++ {
		f.group("grp"+u32(uint32(g)), "prf_vless", "prf_hy2").users("grp"+u32(uint32(g)), 5, 1)
	}
	r := buildAccess(f.in)
	full, _ := marshal(r)
	if len(full) <= maxResultBytes {
		t.Fatalf("the fixture is too small to test the cap: %d", len(full))
	}
	res, _, err := result(r)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*sdk.TextContent).Text
	if len(text) > maxResultBytes || !r.Truncated || !strings.Contains(text, `"truncated":true`) {
		t.Fatalf("size %d, truncated %v", len(text), r.Truncated)
	}
}

func TestAccessSizeCapShrinksClassesWithinOneNode(t *testing.T) {
	f := newFx().node("de1", "DE", "online")
	for i := 0; i < 400; i++ {
		f.inbound("de1", "prf_vless", "vless", uint32(i+1))
		f.in.Inbounds[len(f.in.Inbounds)-1].State = "failed"
	}
	r := buildAccess(f.in)
	full, _ := marshal(r)
	if len(full) <= maxResultBytes {
		t.Fatalf("the one-node fixture is too small to test nested shrinking: %d", len(full))
	}
	res, _, err := result(r)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*sdk.TextContent).Text
	if len(text) > maxResultBytes || !r.Truncated {
		t.Fatalf("size %d, truncated %v", len(text), r.Truncated)
	}
}

// Through the real MCP layer over the fake admin API: the tool answers, and nothing secret or addressed reaches the agent.
func TestAccessToolsAreReadonlyAndLeakNothing(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	names := toolNames(t, s)
	for _, n := range []string{"access_report", "block_diagnosis"} {
		if !slices.Contains(names, n) {
			t.Fatalf("readonly misses %s", n)
		}
	}
	out := mustOK(t, s, "access_report", map[string]any{"hours": 48})
	noCanary(t, "access_report", out)
	r := decode[AccessReportV](t, out)
	if r.Hours != 48 || len(r.Limits) == 0 || r.Matrix == nil || r.Findings == nil {
		t.Fatalf("report: %.400s", out)
	}
	if got := decode[AccessReportV](t, mustOK(t, s, "access_report", map[string]any{"node": "de1", "protocol": "hy2", "country": "ru"})); got.Filters["country"] != "RU" {
		t.Fatalf("filters: %+v", got.Filters)
	}
	b := mustOK(t, s, "block_diagnosis", map[string]any{"country": "RU"})
	noCanary(t, "block_diagnosis", b)
	if d := decode[BlockDiagnosisV](t, b); len(d.Nodes) == 0 {
		t.Fatalf("block_diagnosis: %.400s", b)
	}
	if out := mustFail(t, s, "access_report", map[string]any{"hours": 999}); !strings.Contains(out, "1 to 168") {
		t.Fatalf("hours: %s", out)
	}
	if out := mustFail(t, s, "access_report", map[string]any{"group": "nope"}); !strings.Contains(out, "not found") {
		t.Fatalf("group: %s", out)
	}
	if strings.Contains(out, "usr_") {
		t.Fatal("successful access report exposes user IDs")
	}
}
