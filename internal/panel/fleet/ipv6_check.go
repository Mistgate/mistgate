package fleet

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/tcpcheck"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

// ErrIPv6Address: the address given to CheckIPv6 is not a global unicast IPv6 address.
var ErrIPv6Address = errors.New("fleet: not a global IPv6 address")

// CheckIPv6 (node.proto CheckIpv6): is this IPv6 address of the node reachable from outside, and is what answers there the node
// itself? It sends nothing but test packets and changes nothing on any host.
//
// The probes are those of the port check, sent to the address instead of the node's main one: the UDP delivery of every enabled
// UDP server (counted at the node, as the port check does) and the TLS identity of every TCP server of the node (the certificate,
// pin or REALITY chain that proves the connection reached Mistgate). The specs are built from the node's real row, so the
// names and pins are those of the main address; only the host that is dialled changes (portDial). Port 80 is left out: the
// HTTP-01 challenge goes to the name's A record, never to this address.
//
// Who sends: another node whose doctor says that outbound IPv6 works and that lists the capability, two at most per half, ordered
// as the port check orders them; the panel itself only after those and only when it has IPv6 and is not on the node's host.
// Only when no needed half has any sender may Globalping connect to one TLS port from two probes (IPv6Check.By = globalping):
// that proves the address answers a connection, not that it is this node's, and the result says so by its By. A sender that
// cannot run or a missing half fails closed; an address nobody could test is never passed.
//
// The whole node-side run holds the node's port-check gate (acquirePortCheck): the node has one UDP counter table. Nothing is
// written to port_check or tcp_check (those are the main address's); the check is returned and audited as node.ipv6_check.
func (f *Fleet) CheckIPv6(ctx context.Context, nodeID, address string) (store.IPv6Check, error) {
	addr, ok := store.UsableIPv6(address)
	if !ok {
		return store.IPv6Check{}, ErrIPv6Address
	}
	node, err := f.st.Node(ctx, nodeID)
	if err != nil {
		return store.IPv6Check{}, err
	}
	chk := store.IPv6Check{Addr: addr, At: f.now().Unix()}
	if node.State != "active" {
		chk.Code = "node_offline"
	} else {
		f.checkIPv6(ctx, node, &chk)
	}
	f.auditIPv6Check(ctx, node, chk)
	return chk, nil
}

// ipv6Sender is who sent the probes of a half: the node's id and its name now, or "panel".
type ipv6Sender struct{ id, name string }

func senderRef(s portSender) ipv6Sender {
	if s.panel {
		return ipv6Sender{store.IPv6ByPanel, store.IPv6ByPanel}
	}
	return ipv6Sender{s.node.ID, s.node.Name}
}

// ipv6Run is the part of a check that the node's own senders do.
type ipv6Run struct {
	udp, tcp   []store.IPv6Verdict
	udpBy      ipv6Sender
	tcpBy      ipv6Sender
	udpCode    string
	tcpCode    string
	udpSenders int
	tcpSenders int
	ranUDP     bool
	ranTCP     bool
	needUDP    bool
	needTCP    bool
}

func (f *Fleet) checkIPv6(ctx context.Context, node store.NodeRow, chk *store.IPv6Check) {
	specs, pins, err := f.tcpSpecsOf(ctx, node)
	if err != nil {
		f.log.Warn("list inbounds for the IPv6 check", "node", node.ID, "err", err)
		chk.Code = "failed"
		return
	}
	tcp, udp := ipv6CheckTargets(specs, pins)
	if len(tcp) == 0 && len(udp) == 0 {
		chk.Code = "nothing_to_check"
		return
	}

	lock, _, _, err := f.acquirePortCheck(ctx, node.ID)
	if err != nil {
		chk.Code = "failed"
		return
	}
	run, code := f.ipv6OnNodes(ctx, node, chk.Addr, tcp, udp)
	<-lock.gate // the gate is for the node's counter table: the wait for Globalping below does not need it
	if code != "" {
		chk.Code = code
		return
	}
	if run == nil {
		chk.Code = "failed"
		return
	}
	chk.Verdicts = append(append(chk.Verdicts, run.tcp...), run.udp...)
	by := run.tcpBy
	if by.id == "" {
		by = run.udpBy
	}
	chk.By, chk.Sender = by.id, by.name
	if run.udpSenders == 0 && run.tcpSenders == 0 {
		f.ipv6ByGlobalping(ctx, chk, tcp)
		return
	}
	if code := run.failureCode(); code != "" {
		chk.Code = code
		return
	}
	chk.Pass = ipv6Passed(chk.Verdicts)
}

func ipv6CheckTargets(specs []tcpSpec, pins map[string]string) ([]tcpTarget, []uint16) {
	var supported []tcpSpec
	for _, s := range specs {
		if protocols.IPv6Unsupported(s.Spec) == "" {
			supported = append(supported, s)
		}
	}
	var tcp []tcpTarget
	for _, t := range tcpTargets(supported, pins) {
		if t.Port != 80 {
			tcp = append(tcp, t)
		}
	}
	udp := ipv6UDPPorts(supported)
	return tcp, udp
}

func (r *ipv6Run) failureCode() string {
	if r.needUDP && r.udpSenders > 0 && !r.ranUDP {
		if r.udpCode != "" {
			return r.udpCode
		}
		return "failed"
	}
	if r.needTCP && r.tcpSenders > 0 && !r.ranTCP {
		if r.tcpCode != "" {
			return r.tcpCode
		}
		return "failed"
	}
	if (r.needUDP && r.udpSenders == 0) || (r.needTCP && r.tcpSenders == 0) {
		return "no_ipv6_verifier"
	}
	return ""
}

// ipv6UDPPorts are the listen ports of the node's enabled UDP servers, at most udpcheck.MaxPorts, in order.
func ipv6UDPPorts(specs []tcpSpec) []uint16 {
	var out []uint16
	for _, s := range specs {
		if p := s.Spec.Listen.Port; s.Spec.Listen.Network == "udp" && p != 0 && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	if len(out) > udpcheck.MaxPorts {
		out = out[:udpcheck.MaxPorts]
	}
	return out
}

// ipv6Passed: every TCP port is ours and every UDP port delivers.
func ipv6Passed(v []store.IPv6Verdict) bool {
	for _, x := range v {
		if (x.Net == "tcp" && x.Verdict != tcpcheck.VerdictOurs) || (x.Net == "udp" && x.Verdict != "ok") {
			return false
		}
	}
	return len(v) > 0
}

// ipv6OnNodes runs the halves with the nodes (and last the panel) that can send over IPv6. It records which halves had senders
// and which ran so the caller can preserve the verdicts of a half when the other did not run. A code is final (the node is not
// there to be checked).
func (f *Fleet) ipv6OnNodes(ctx context.Context, node store.NodeRow, addr string, tcp []tcpTarget, udp []uint16) (*ipv6Run, string) {
	liveRows, err := f.Live(ctx)
	if err != nil {
		f.log.Warn("read live nodes for the IPv6 check", "node", node.ID, "err", err)
		return nil, "failed"
	}
	live := liveRowsByID(liveRows)
	targetLive := live[node.ID]
	if !targetLive.Connected || (f.cfg.Remote == nil && f.session(node.ID) == nil) {
		return nil, "node_offline"
	}
	if len(udp) > 0 && !slices.Contains(targetLive.AgentCaps, capUDPCheck) {
		return nil, "agent_too_old"
	}
	nodes, err := f.st.Nodes(ctx, false)
	if err != nil {
		f.log.Warn("list nodes for the IPv6 check", "node", node.ID, "err", err)
		return nil, "failed"
	}
	v6, err := f.st.IPv6Doctors(ctx)
	if err != nil {
		f.log.Warn("read the doctor's IPv6 for the IPv6 check", "node", node.ID, "err", err)
		return nil, "failed"
	}
	senders := func(capability string) []portSender {
		var out []portSender
		for _, s := range f.portSenders(node, nodes, live, capability) {
			if v6[s.node.ID].State == store.IPv6OK {
				out = append(out, s)
			}
		}
		if len(out) > 2 {
			out = out[:2]
		}
		if f.panelMaySend(ctx, addr) {
			out = append(out, portSender{panel: true})
		}
		return out
	}

	run := &ipv6Run{needUDP: len(udp) > 0, needTCP: len(tcp) > 0}
	if run.needUDP {
		available := senders(capUDPCheck)
		run.udpSenders = len(available)
		run.udp, run.udpBy, run.ranUDP, run.udpCode = f.ipv6UDP(ctx, node, targetLive, available, addr, udp)
	}
	if run.needTCP {
		available := senders(capTCPCheck)
		run.tcpSenders = len(available)
		run.tcp, run.tcpBy, run.ranTCP, run.tcpCode = f.ipv6TCP(ctx, addr, available, tcp)
	}
	return run, ""
}

// panelMaySend: the panel sends from its own host, so only where that is a different host with an IPv6 route of its own.
func (f *Fleet) panelMaySend(ctx context.Context, addr string) bool {
	if !panelSendsUDP {
		return false
	}
	has := f.panelIPv6
	if has == nil {
		has = panelHasIPv6
	}
	if a, err := netip.ParseAddr(addr); err != nil || panelHasAddress(a) {
		return false
	}
	return has(ctx)
}

// ipv6UDP is the UDP half: the first sender that runs it. An inconclusive run (no port delivered) is asked once more of the
// next sender, as the port check does; its answer is "broken" for every port, which is a finding.
func (f *Fleet) ipv6UDP(ctx context.Context, node store.NodeRow, targetLive store.NodeLiveRow, senders []portSender, addr string, ports []uint16) (verdicts []store.IPv6Verdict, by ipv6Sender, ran bool, code string) {
	lastCode := "failed"
	for i, sender := range senders {
		out := f.runPortCheckAttempt(ctx, node, targetLive, sender, portDial{Host: addr}, ports)
		if len(out.checks) == 0 || (out.errorCode != "" && out.errorCode != "inconclusive") {
			if out.errorCode != "" && out.errorCode != "inconclusive" {
				lastCode = out.errorCode
			}
			if ctx.Err() != nil {
				return nil, ipv6Sender{}, false, "failed"
			}
			continue // busy, no route or a failed command: this sender did not run it
		}
		verdicts, by, ran = nil, senderRef(sender), true
		for _, c := range out.checks {
			verdicts = append(verdicts, store.IPv6Verdict{Net: "udp", Port: c.Port, Verdict: c.Verdict})
		}
		if out.errorCode != "inconclusive" || i == len(senders)-1 {
			break
		}
	}
	if len(senders) == 0 {
		lastCode = ""
	}
	if ran {
		lastCode = ""
	}
	return verdicts, by, ran, lastCode
}

// ipv6TCP is the TCP half: one sender probes every target; a sender that could not (busy, no route, failed) is replaced.
func (f *Fleet) ipv6TCP(ctx context.Context, addr string, senders []portSender, targets []tcpTarget) (verdicts []store.IPv6Verdict, by ipv6Sender, ran bool, code string) {
	lastCode := "failed"
	for _, sender := range senders {
		got, code := f.probeTCPTargets(ctx, addr, sender, targets, nil, nil)
		if code != "" {
			lastCode = code
			if ctx.Err() != nil {
				return nil, ipv6Sender{}, false, "failed"
			}
			continue
		}
		for _, t := range targets {
			v := got[t.Port]
			if !validTCPVerdict(v) {
				v = "failed"
			}
			verdicts = append(verdicts, store.IPv6Verdict{Net: "tcp", Port: t.Port, Verdict: v})
		}
		return verdicts, senderRef(sender), true, ""
	}
	if len(senders) == 0 {
		lastCode = ""
	}
	return nil, ipv6Sender{}, false, lastCode
}

// ipv6ByGlobalping is the last resort: one TLS port, two probes anywhere. A connection that opens says the address is
// routed to something that listens; it cannot say that it is this node.
func (f *Fleet) ipv6ByGlobalping(ctx context.Context, chk *store.IPv6Check, tcp []tcpTarget) {
	port := uint16(0)
	for _, t := range tcp {
		if t.Kind == tcpcheck.KindTLS {
			port = t.Port
			break
		}
	}
	if f.cfg.GlobalpingTCP6 == nil || port == 0 {
		chk.Code = "no_ipv6_verifier"
		return
	}
	answered, connected, err := f.cfg.GlobalpingTCP6(ctx, chk.Addr, port)
	if err != nil || answered == 0 {
		if err != nil && ctx.Err() == nil {
			f.log.Warn("IPv6 check through Globalping", "err", err)
		}
		chk.Code = "no_ipv6_verifier"
		return
	}
	verdict := "timeout"
	if connected > 0 {
		verdict = "reachable"
	}
	chk.By, chk.Sender = store.IPv6ByGlobalping, store.IPv6ByGlobalping
	chk.Verdicts = []store.IPv6Verdict{{Net: "tcp", Port: port, Verdict: verdict}}
	chk.Pass = connected > 0
}

func (f *Fleet) auditIPv6Check(ctx context.Context, node store.NodeRow, chk store.IPv6Check) {
	vs := make([]string, len(chk.Verdicts))
	for i, v := range chk.Verdicts {
		vs[i] = v.Net + "/" + strconv.Itoa(int(v.Port)) + "=" + v.Verdict
	}
	sort.Strings(vs)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	f.audit(ctx, "node.ipv6_check", map[string]string{
		"node_id": node.ID, "node": node.Name, "address": chk.Addr, "pass": strconv.FormatBool(chk.Pass),
		"by": chk.By, "code": chk.Code, "verdicts": strings.Join(vs, ","),
	})
}
