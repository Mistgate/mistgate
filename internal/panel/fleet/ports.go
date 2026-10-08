package fleet

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

const (
	capUDPCheck       = "udpcheck/1"
	udpCheckSendCount = 300
	udpCheckPPS       = 50
	udpCheckSize      = 1200
	// a port that lost this many percent more than the best port of its run is lossy even above 95 % (relativeLoss)
	udpCheckRelativeLossPct = 4
	udpCheckWait            = 12 * time.Second
	udpCheckStopWait        = 2 * time.Second
)

// Keep this list in step with access.twinPreferred.
var udpCheckPreferred = []uint16{8443, 4443, 2053, 2083, 2087, 2096}

var panelUDPSlots = make(chan struct{}, 4)

type portCheckOutcome struct {
	checks    []store.PortCheck
	sender    string
	errorCode string
}

// portCheckLock is one node's check gate. A caller that waited finds generation moved on and returns the run it waited
// for (last) instead of starting another.
type portCheckLock struct {
	gate       chan struct{}
	generation uint64
	last       portCheckOutcome
	waiters    int // callers blocked on gate (tests read it)
}

type portSender struct {
	node    store.NodeRow
	session *session
	panel   bool
}

type udpPortSettings struct {
	Port uint16 `json:"port"`
	Hop  struct {
		From uint16 `json:"from"`
		To   uint16 `json:"to"`
	} `json:"hop"`
}

// CheckPorts runs one bounded UDP delivery check and returns the sending node id (or "panel") and a stable result code.
func (f *Fleet) CheckPorts(ctx context.Context, nodeID string, requested []uint16) ([]store.PortCheck, string, string) {
	return f.checkPorts(ctx, nodeID, requested, "")
}

// CheckPortsAsSystem runs the same check for a panel-owned schedule and audits it as the system actor.
func (f *Fleet) CheckPortsAsSystem(ctx context.Context, nodeID string, requested []uint16) ([]store.PortCheck, string, string) {
	return f.checkPorts(ctx, nodeID, requested, "system")
}

func (f *Fleet) checkPorts(ctx context.Context, nodeID string, requested []uint16, auditActor string) ([]store.PortCheck, string, string) {
	node, err := f.st.Node(ctx, nodeID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			f.log.Warn("get node for UDP port check", "node", nodeID, "err", err)
		}
		return nil, "", "failed"
	}
	lock, generation, waited, err := f.acquirePortCheck(ctx, nodeID)
	if err != nil {
		out := portCheckOutcome{errorCode: "failed"}
		f.auditPortCheck(ctx, node, requested, out, auditActor)
		return nil, "", out.errorCode
	}
	defer func() { <-lock.gate }()

	if waited {
		f.portCheckMu.Lock()
		previous, completed := lock.last, lock.generation != generation
		f.portCheckMu.Unlock()
		if completed {
			out := f.completedPortCheck(ctx, nodeID, previous)
			f.auditPortCheck(ctx, node, requested, out, auditActor)
			return out.checks, out.sender, out.errorCode
		}
	}

	node, err = f.st.Node(ctx, nodeID)
	if err != nil {
		out := portCheckOutcome{errorCode: "failed"}
		f.finishPortCheck(lock, out)
		f.auditPortCheck(ctx, node, requested, out, auditActor)
		return nil, "", out.errorCode
	}
	out := f.runPortCheck(ctx, node, requested)
	f.finishPortCheck(lock, out)
	f.auditPortCheck(ctx, node, requested, out, auditActor)
	return out.checks, out.sender, out.errorCode
}

func (f *Fleet) acquirePortCheck(ctx context.Context, nodeID string) (*portCheckLock, uint64, bool, error) {
	f.portCheckMu.Lock()
	if f.portCheckLocks == nil {
		f.portCheckLocks = make(map[string]*portCheckLock)
	}
	lock := f.portCheckLocks[nodeID]
	if lock == nil {
		lock = &portCheckLock{gate: make(chan struct{}, 1)}
		f.portCheckLocks[nodeID] = lock
	}
	generation := lock.generation
	f.portCheckMu.Unlock()

	select {
	case lock.gate <- struct{}{}:
		return lock, generation, false, nil
	default:
	}
	f.changePortCheckWaiters(lock, 1)
	defer f.changePortCheckWaiters(lock, -1)
	select {
	case lock.gate <- struct{}{}:
		return lock, generation, true, nil
	case <-ctx.Done():
		return nil, generation, true, ctx.Err()
	}
}

func (f *Fleet) changePortCheckWaiters(lock *portCheckLock, delta int) {
	f.portCheckMu.Lock()
	lock.waiters += delta
	f.portCheckMu.Unlock()
}

func (f *Fleet) finishPortCheck(lock *portCheckLock, outcome portCheckOutcome) {
	f.portCheckMu.Lock()
	lock.generation++
	lock.last = clonePortCheckOutcome(outcome)
	f.portCheckMu.Unlock()
}

func clonePortCheckOutcome(outcome portCheckOutcome) portCheckOutcome {
	outcome.checks = append([]store.PortCheck(nil), outcome.checks...)
	return outcome
}

func (f *Fleet) completedPortCheck(ctx context.Context, nodeID string, previous portCheckOutcome) portCheckOutcome {
	previous = clonePortCheckOutcome(previous)
	if len(previous.checks) == 0 {
		return previous
	}
	rows, err := f.st.PortChecks(ctx, nodeID)
	if err != nil {
		f.log.Warn("read completed UDP port check", "node", nodeID, "err", err)
		return previous
	}
	byPort := make(map[uint16]store.PortCheck, len(rows))
	for _, row := range rows {
		byPort[row.Port] = row
	}
	for i, checked := range previous.checks {
		if row, ok := byPort[checked.Port]; ok && row.Address == checked.Address && row.CheckedAt.Unix() == checked.CheckedAt.Unix() {
			previous.checks[i] = row
		}
	}
	return previous
}

func (f *Fleet) auditPortCheck(ctx context.Context, node store.NodeRow, requested []uint16, outcome portCheckOutcome, actorOverride string) {
	ports := requested
	if len(outcome.checks) > 0 {
		ports = make([]uint16, 0, len(outcome.checks))
		for _, check := range outcome.checks {
			ports = append(ports, check.Port)
		}
	}
	values := make([]string, len(ports))
	for i, port := range ports {
		values[i] = strconv.Itoa(int(port))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	params := map[string]string{
		"node_id": node.ID, "node": node.Name, "ports": strings.Join(values, ","),
		"sender": outcome.sender, "error_code": outcome.errorCode,
	}
	if actorOverride == "" {
		f.audit(ctx, "node.ports_check", params)
		return
	}
	b, _ := json.Marshal(params)
	if err := f.st.Audit(ctx, f.now(), store.AuditEntry{Actor: actorOverride, Action: "node.ports_check", Params: string(b), Result: "ok"}); err != nil {
		f.log.Warn("audit", "action", "node.ports_check", "err", err)
	}
}

func (f *Fleet) runPortCheck(ctx context.Context, target store.NodeRow, requested []uint16) portCheckOutcome {
	return f.runPortCheckForEdition(ctx, target, requested, panelSendsUDP)
}

func (f *Fleet) runPortCheckForEdition(ctx context.Context, target store.NodeRow, requested []uint16, allowPanel bool) portCheckOutcome {
	liveRows, err := f.Live(ctx)
	if err != nil {
		f.log.Warn("read live nodes for UDP port check", "node", target.ID, "err", err)
		return portCheckOutcome{errorCode: "failed"}
	}
	live := liveRowsByID(liveRows)
	targetLive := live[target.ID]
	if !targetLive.Connected {
		return portCheckOutcome{errorCode: "node_offline"}
	}
	if !slices.Contains(targetLive.AgentCaps, capUDPCheck) {
		return portCheckOutcome{errorCode: "agent_too_old"}
	}
	targetSession := f.session(target.ID)
	if targetSession == nil {
		return portCheckOutcome{errorCode: "node_offline"}
	}

	allNodes, err := f.st.Nodes(ctx, false)
	if err != nil {
		f.log.Warn("list nodes for UDP port check", "node", target.ID, "err", err)
		return portCheckOutcome{errorCode: "failed"}
	}
	candidates := f.portSenders(target, allNodes, live)
	if len(candidates) == 0 && !allowPanel {
		return portCheckOutcome{errorCode: "no_sender"}
	}

	inbounds, err := f.st.FleetInbounds(ctx, target.ID, false)
	if err != nil {
		f.log.Warn("list inbounds for UDP port check", "node", target.ID, "err", err)
		return portCheckOutcome{errorCode: "failed"}
	}
	ports := udpCheckPorts(requested, inbounds)
	if len(ports) == 0 {
		return portCheckOutcome{errorCode: "failed"}
	}
	previous, err := f.st.PortChecks(ctx, target.ID)
	if err != nil {
		f.log.Warn("read previous UDP port checks", "node", target.ID, "err", err)
		return portCheckOutcome{errorCode: "failed"}
	}
	previousByPort := make(map[uint16]store.PortCheck, len(previous))
	for _, check := range previous {
		previousByPort[check.Port] = check
	}

	sameHost := false
	if address, err := udpcheck.Resolve(ctx, target.Address); err == nil {
		sameHost = panelHasAddress(address)
	}

	senders := candidates
	if len(senders) == 0 {
		senders = []portSender{{panel: true}}
	} else if len(senders) > 2 {
		senders = senders[:2]
	}
	var result portCheckOutcome
	for i, sender := range senders {
		result = f.runPortCheckAttempt(ctx, target, targetSession, sender, ports)
		if result.errorCode != "inconclusive" || i != 0 || len(senders) < 2 {
			break
		}
	}
	if sameHost && (result.errorCode == "" || result.errorCode == "inconclusive") {
		result.errorCode = "same_host"
	}
	if result.errorCode != "" && result.errorCode != "same_host" {
		return result
	}
	if result.errorCode == "same_host" {
		return result
	}

	for i := range result.checks {
		check := &result.checks[i]
		if check.Verdict != "ok" {
			check.BadAt = check.CheckedAt
		} else if old, ok := previousByPort[check.Port]; ok && old.Address == check.Address {
			check.BadAt = old.BadAt
		}
	}
	if err := f.st.PutPortChecks(ctx, result.checks); err != nil {
		f.log.Warn("store UDP port checks", "node", target.ID, "err", err)
		result.errorCode = "failed"
		return result
	}
	f.portLossyEvents(ctx, target, inbounds, result.checks, previousByPort)
	return result
}

func (f *Fleet) portSenders(target store.NodeRow, nodes []store.NodeRow, live map[string]store.NodeLiveRow) []portSender {
	var eligible []store.NodeRow
	sessions := make(map[string]*session)
	for _, node := range nodes {
		if node.ID == target.ID {
			continue
		}
		if current := live[node.ID]; !current.Connected || !slices.Contains(current.AgentCaps, capUDPCheck) {
			continue
		}
		sess := f.session(node.ID)
		if sess == nil {
			continue
		}
		eligible = append(eligible, node)
		sessions[node.ID] = sess
	}
	ordered := udpPortSenderOrder(target, eligible)
	candidates := make([]portSender, 0, len(ordered))
	for _, node := range ordered {
		candidates = append(candidates, portSender{node: node, session: sessions[node.ID]})
	}
	return candidates
}

func udpCheckPorts(requested []uint16, inbounds []store.FleetInboundRow) []uint16 {
	used := make([]udpPortSettings, 0, len(inbounds))
	for _, inbound := range inbounds {
		settings := udpPortSettings{}
		_ = json.Unmarshal([]byte(inbound.Settings), &settings)
		settings.Port = udpInboundPort(inbound)
		used = append(used, settings)
	}
	var out []uint16
	seen := make(map[uint16]bool, udpcheck.MaxPorts)
	nonAnchor := 0
	add := func(port uint16) {
		if port == 0 || seen[port] || (port != 443 && nonAnchor >= 7) {
			return
		}
		seen[port] = true
		out = append(out, port)
		if port != 443 {
			nonAnchor++
		}
	}
	if len(requested) > 0 && len(requested) <= 7 {
		for _, port := range requested {
			add(port)
		}
	} else {
		for _, inbound := range inbounds {
			if inbound.Enabled {
				add(udpInboundPort(inbound))
			}
		}
		for _, port := range udpCheckPreferred {
			if !udpPortUsed(port, used) {
				add(port)
			}
		}
	}
	add(443)
	return out
}

func udpPortUsed(port uint16, settings []udpPortSettings) bool {
	for _, current := range settings {
		if port == current.Port || (current.Hop.From != 0 && port >= current.Hop.From && port <= current.Hop.To) {
			return true
		}
	}
	return false
}

func (f *Fleet) runPortCheckAttempt(ctx context.Context, target store.NodeRow, targetSession *session, sender portSender, ports []uint16) portCheckOutcome {
	var tag [8]byte
	if _, err := rand.Read(tag[:]); err != nil {
		f.log.Warn("make UDP port check tag", "node", target.ID, "err", err)
		return portCheckOutcome{errorCode: "failed"}
	}
	portNumbers := make([]uint32, len(ports))
	for i, port := range ports {
		portNumbers[i] = uint32(port)
	}
	armed, err := targetSession.roundtrip(ctx, udpCheckWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpCount{UdpCount: &agentv1.UdpCount{
			RequestId: reqID, Tag: tag[:], Ports: portNumbers, HoldS: 30,
		}}}
	})
	if err != nil {
		return portCheckOutcome{sender: portSenderID(sender), errorCode: "failed"}
	}
	if armed == nil || !armed.Ok {
		return portCheckOutcome{sender: portSenderID(sender), errorCode: portCommandError(armed)}
	}

	family, sent, sendErrCode := f.sendPortCheck(ctx, target, sender, ports, tag)
	if sendErrCode == "" {
		if err := f.settlePortCheck(ctx); err != nil {
			sendErrCode = "failed"
		}
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), udpCheckStopWait)
	defer cancel()
	counts, stopErr := targetSession.roundtrip(stopCtx, udpCheckStopWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpCount{UdpCount: &agentv1.UdpCount{
			RequestId: reqID, Tag: tag[:], Stop: true,
		}}}
	})
	if stopErr != nil {
		return portCheckOutcome{sender: portSenderID(sender), errorCode: "failed"}
	}
	if counts == nil || !counts.Ok {
		return portCheckOutcome{sender: portSenderID(sender), errorCode: portCommandError(counts)}
	}
	if sendErrCode != "" {
		return portCheckOutcome{sender: portSenderID(sender), errorCode: sendErrCode}
	}
	if sent < 0 || sent > udpCheckSendCount || (family != "4" && family != "6") {
		return portCheckOutcome{sender: portSenderID(sender), errorCode: "failed"}
	}

	checkedAt := f.now().UTC()
	checks := make([]store.PortCheck, 0, len(ports))
	conclusive := false
	header := uint64(28)
	if family == "6" {
		header = 48
	}
	for _, port := range ports {
		packets, okPackets := udpCountParam(counts.Params, "p"+strconv.Itoa(int(port)))
		bytes, okBytes := udpCountParam(counts.Params, "b"+strconv.Itoa(int(port)))
		if !okPackets || !okBytes {
			return portCheckOutcome{sender: portSenderID(sender), errorCode: "failed"}
		}
		got := udpDelivered(packets, bytes, header, uint64(udpCheckSize), uint64(sent))
		verdict := udpPortVerdict(uint64(sent), got)
		if verdict == "ok" {
			conclusive = true
		}
		checks = append(checks, store.PortCheck{NodeID: target.ID, Port: port, Address: target.Address,
			Sent: uint32(sent), Got: uint32(got), Verdict: verdict, Sender: portSenderID(sender), CheckedAt: checkedAt})
	}
	if !conclusive {
		return portCheckOutcome{checks: checks, sender: portSenderID(sender), errorCode: "inconclusive"}
	}
	relativeLoss(checks)
	return portCheckOutcome{checks: checks, sender: portSenderID(sender)}
}

// relativeLoss marks lossy an "ok" port that lost clearly more than the best port of the same run: the ports share the
// sender, the path and the minute, so the difference is the port's. A hoster that filters one port often lets most of a
// slow test through (286 of 300 to a filtered port while every other port got 300), which the absolute 95 % misses.
// ponytail: 4 % of the packets (12 of 300), well above the noise between ports of one run on a clean path; calibrate.
func relativeLoss(checks []store.PortCheck) {
	var best uint32
	for _, c := range checks {
		best = max(best, c.Got)
	}
	for i, c := range checks {
		if c.Verdict == "ok" && uint64(best-c.Got)*100 >= uint64(c.Sent)*udpCheckRelativeLossPct {
			checks[i].Verdict = "lossy"
		}
	}
}

func (f *Fleet) sendPortCheck(ctx context.Context, target store.NodeRow, sender portSender, ports []uint16, tag [8]byte) (string, int, string) {
	if sender.panel {
		select {
		case panelUDPSlots <- struct{}{}:
		case <-ctx.Done():
			return "", 0, "failed"
		}
		defer func() { <-panelUDPSlots }()
		send := f.sendUDP
		if send == nil {
			send = udpcheck.Send
		}
		family, sent, err := send(ctx, target.Address, ports, tag, udpCheckSendCount, udpCheckPPS, udpCheckSize)
		if err != nil {
			return family, sent, udpSendErrorCode(err)
		}
		return family, sent, ""
	}
	result, err := sender.session.roundtrip(ctx, udpCheckWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpSend{UdpSend: &agentv1.UdpSend{
			RequestId: reqID, Host: target.Address, Ports: uint32Ports(ports), Tag: tag[:],
			Count: udpCheckSendCount, Pps: udpCheckPPS, Size: udpCheckSize,
		}}}
	})
	if err != nil {
		return "", 0, "failed"
	}
	if result == nil || !result.Ok {
		return "", 0, portCommandError(result)
	}
	family := result.Params["family"]
	sent, ok := udpSentParam(result.Params["sent"])
	if !ok || (family != "4" && family != "6") {
		return "", 0, "failed"
	}
	return family, sent, ""
}

func (f *Fleet) settlePortCheck(ctx context.Context) error {
	if f.settleUDP != nil {
		return f.settleUDP(ctx)
	}
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func udpPortVerdict(sent, got uint64) string {
	if sent == 0 {
		return ""
	}
	if got*100 >= sent*95 {
		return "ok"
	}
	if got*100 >= sent*70 {
		return "lossy"
	}
	return "broken"
}

func udpDelivered(packets, bytes, header, size, sent uint64) uint64 {
	if packets > math.MaxUint64/header {
		return 0
	}
	headerBytes := packets * header
	if bytes <= headerBytes || size == 0 {
		return 0
	}
	got := (bytes - headerBytes) / size
	return min(got, sent)
}

func udpCountParam(params map[string]string, key string) (uint64, bool) {
	raw, ok := params[key]
	if !ok || raw == "" {
		return 0, true
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	return value, err == nil
}

func udpSentParam(raw string) (int, bool) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > udpCheckSendCount {
		return 0, false
	}
	return value, true
}

func portCommandError(result *agentv1.CommandResult) string {
	if result == nil {
		return "failed"
	}
	switch strings.SplitN(result.Error, ":", 2)[0] {
	case "busy":
		return "busy"
	case "no_route", "unsupported_host":
		return "no_route"
	default:
		return "failed"
	}
}

func udpSendErrorCode(err error) string {
	switch {
	case errors.Is(err, udpcheck.ErrNoRoute), errors.Is(err, udpcheck.ErrUnsupportedHost):
		return "no_route"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "failed"
	default:
		return "failed"
	}
}

func uint32Ports(ports []uint16) []uint32 {
	out := make([]uint32, len(ports))
	for i, port := range ports {
		out[i] = uint32(port)
	}
	return out
}

func portSenderID(sender portSender) string {
	if sender.panel {
		return "panel"
	}
	return sender.node.ID
}

func panelHasAddress(address netip.Addr) bool {
	if address.IsLoopback() {
		return true
	}
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		var ip net.IP
		switch value := iface.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if parsed, ok := netip.AddrFromSlice(ip); ok && parsed.Unmap() == address.Unmap() {
			return true
		}
	}
	return false
}

func (f *Fleet) portLossyEvents(ctx context.Context, target store.NodeRow, inbounds []store.FleetInboundRow, checks []store.PortCheck, previous map[uint16]store.PortCheck) {
	checked := make(map[uint16]store.PortCheck, len(checks))
	for _, check := range checks {
		checked[check.Port] = check
	}
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		port := udpInboundPort(inbound)
		check, ok := checked[port]
		if !ok || check.Verdict == "ok" || previous[port].Bad(f.now()) {
			continue
		}
		params := map[string]string{
			"inbound": inbound.ID, "profile": inbound.ProfileName,
			"port": strconv.Itoa(int(port)), "sent": strconv.Itoa(int(check.Sent)),
			"got": strconv.Itoa(int(check.Got)), "sender": check.Sender,
		}
		if check.Sender != "panel" {
			if sender, err := f.st.Node(ctx, check.Sender); err == nil {
				params["sender"] = sender.Name
			}
		}
		err := f.st.InsertEvent(ctx, store.EventRow{Time: f.now(), Severity: 2, Code: "port_lossy", Source: "panel",
			NodeID: target.ID, InboundID: inbound.ID, Params: params})
		if err != nil {
			f.log.Warn("write UDP port loss event", "node", target.ID, "inbound", inbound.ID, "err", err)
		}
	}
}

func (f *Fleet) portCheckMessages(ctx context.Context, checks []store.PortCheck, sender, errorCode string) ([]*adminv1.PortCheck, string, error) {
	if len(checks) == 0 && sender == "" {
		return nil, "", nil
	}
	nodes, err := f.st.Nodes(ctx, true)
	if err != nil {
		return nil, "", err
	}
	names := make(map[string]string, len(nodes))
	for _, node := range nodes {
		names[node.ID] = node.Name
	}
	senderName := sender
	if sender != "" && sender != "panel" {
		senderName = names[sender]
		if senderName == "" {
			senderName = "unknown"
		}
	}
	out := make([]*adminv1.PortCheck, 0, len(checks))
	for _, check := range checks {
		rowSender := check.Sender
		if rowSender != "" && rowSender != "panel" {
			rowSender = names[rowSender]
			if rowSender == "" {
				rowSender = "unknown"
			}
		}
		badAt := int64(0)
		if !check.BadAt.IsZero() {
			badAt = check.BadAt.Unix()
		}
		reason := ""
		if check.Verdict == "" {
			reason = errorCode
		}
		out = append(out, &adminv1.PortCheck{NodeId: check.NodeID, Port: uint32(check.Port), Verdict: check.Verdict,
			Sent: check.Sent, Got: check.Got, CheckedUnix: check.CheckedAt.Unix(), BadUnix: badAt, Sender: rowSender, Reason: reason})
	}
	return out, senderName, nil
}

func udpInboundPort(inbound store.FleetInboundRow) uint16 {
	if inbound.PortOverride != 0 {
		return inbound.PortOverride
	}
	var settings udpPortSettings
	_ = json.Unmarshal([]byte(inbound.Settings), &settings)
	return settings.Port
}

func udpPortSenderOrder(target store.NodeRow, candidates []store.NodeRow) []store.NodeRow {
	out := append([]store.NodeRow(nil), candidates...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		aProvider := a.Provider != "" && !strings.EqualFold(a.Provider, target.Provider)
		bProvider := b.Provider != "" && !strings.EqualFold(b.Provider, target.Provider)
		if aProvider != bProvider {
			return aProvider
		}
		aCountry := a.CountryCode != "" && !strings.EqualFold(a.CountryCode, target.CountryCode)
		bCountry := b.CountryCode != "" && !strings.EqualFold(b.CountryCode, target.CountryCode)
		if aCountry != bCountry {
			return aCountry
		}
		if strings.ToLower(a.Name) != strings.ToLower(b.Name) {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.Name < b.Name
	})
	return out
}
