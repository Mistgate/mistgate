package fleet

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/tcpcheck"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

// The TCP half of the port check (design/vless.md section 7.4): does a connection to a TCP port of the node reach
// Mistgate itself? The target arms port 80 (the node's HTTP-01 listener answers one marked request), another party
// probes: a TLS handshake for the HTTPS ports, the marked GET for port 80.

const (
	capTCPCheck = "tcpcheck/1"
	// tcpMarkHold is how long the target keeps the mark; the probe follows at once and takes at most 8 s.
	tcpMarkHold = 30
	tcpMarkWait = 10 * time.Second
	// tcpProbeWait covers the 5 s connect and 8 s per port limits of the prober, in parallel, plus the name lookup.
	tcpProbeWait = 15 * time.Second

	tcpRoleACME  = "acme"
	tcpRoleSite  = "site"
	tcpRoleVLESS = "vless"
)

// tcpSpec is one enabled inbound of the node as the panel builds it for the agent.
type tcpSpec struct {
	Profile string
	Spec    plugin.InboundSpec
}

// tcpTarget is one TCP port to probe and what to say about it.
type tcpTarget struct {
	Port    uint16
	Role    string // acme | site | vless
	ALPN    bool   // the listener answers TLS-ALPN-01
	Profile string
	Kind    string // tcpcheck.KindHTTPMark | tcpcheck.KindTLS
	SNI     string
	Pin     string // lower-case hex sha256 of the leaf the node reported, "" = none
}

// tcpTargets lists the ports to probe on a node from its enabled inbounds, 80 first and then by port, at most
// tcpcheck.MaxTargets. pins maps an inbound id to the certificate pin the node reported. It is a pure function.
//
//   - 80 (acme): only when an inbound has an ACME certificate, the port the HTTP-01 challenge comes to.
//   - the Hysteria2 HTTPS site (role site), unless an enabled VLESS TCP inbound listens on the same port: the node closes
//     the site there, and the VLESS row says what answers.
//   - a VLESS TCP port (role vless): REALITY with another site is probed with the target's name (a valid chain proves our
//     REALITY forwarded the hello to that site), "self" and TLS with the node's own name.
func tcpTargets(specs []tcpSpec, pins map[string]string) []tcpTarget {
	sorted := slices.Clone(specs)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Spec.ID < sorted[j].Spec.ID })

	acme := false
	vlessPorts := make(map[uint16]bool)
	for _, s := range sorted {
		if s.Spec.TLS.Mode == plugin.TLSAcmeDomain || s.Spec.TLS.Mode == plugin.TLSAcmeIP {
			acme = true
		}
		if s.Spec.Protocol == "vless" && s.Spec.Listen.Network == "tcp" {
			vlessPorts[s.Spec.Listen.Port] = true
		}
	}

	byPort := make(map[uint16]tcpTarget)
	add := func(t tcpTarget) {
		if t.Port == 0 || t.Port == 80 || t.SNI == "" {
			return
		}
		if _, taken := byPort[t.Port]; !taken {
			byPort[t.Port] = t
		}
	}
	// the TLS-ALPN-01 answer only matters on 443 and only for an ACME certificate
	alpn := func(port uint16, tls plugin.TLS) bool {
		return port == 443 && (tls.Mode == plugin.TLSAcmeDomain || tls.Mode == plugin.TLSAcmeIP)
	}
	for _, s := range sorted {
		spec := s.Spec
		pin := pins[spec.ID]
		switch {
		case spec.Protocol == "vless" && spec.Listen.Network == "tcp":
			sni, ownName := vlessProbeName(spec)
			t := tcpTarget{Port: spec.Listen.Port, Role: tcpRoleVLESS, Profile: s.Profile, Kind: tcpcheck.KindTLS, SNI: sni, Pin: pin}
			if ownName {
				t.ALPN = alpn(t.Port, spec.TLS)
			}
			add(t)
		case spec.Protocol == "hysteria2":
			port := uint16(hy2SitePort(spec))
			if port == 0 || vlessPorts[port] {
				continue
			}
			add(tcpTarget{Port: port, Role: tcpRoleSite, ALPN: alpn(port, spec.TLS), Profile: s.Profile, Kind: tcpcheck.KindTLS,
				SNI: spec.TLS.ServerName, Pin: pin})
		}
	}

	out := make([]tcpTarget, 0, len(byPort)+1)
	if acme {
		out = append(out, tcpTarget{Port: 80, Role: tcpRoleACME, Kind: tcpcheck.KindHTTPMark})
	}
	rest := make([]tcpTarget, 0, len(byPort))
	for _, t := range byPort {
		rest = append(rest, t)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Port < rest[j].Port })
	out = append(out, rest...)
	if len(out) > tcpcheck.MaxTargets {
		out = out[:tcpcheck.MaxTargets]
	}
	return out
}

// vlessProbeName is the TLS name to send to a VLESS listener, and whether it is the node's own name ("self" REALITY or a
// TLS front, where the node holds the certificate itself) rather than the name of another site REALITY forwards to.
func vlessProbeName(spec plugin.InboundSpec) (name string, own bool) {
	var settings struct {
		Security string `json:"security"`
		Reality  struct {
			Target      string   `json:"target"`
			ServerNames []string `json:"server_names"`
		} `json:"reality"`
	}
	if len(spec.Settings) > 0 && json.Unmarshal(spec.Settings, &settings) != nil {
		return "", false
	}
	if settings.Security == "tls" || (settings.Security == "reality" || settings.Security == "") && settings.Reality.Target == "self" {
		if spec.TLS.ServerName != "" {
			return spec.TLS.ServerName, true
		}
		if len(settings.Reality.ServerNames) > 0 {
			return settings.Reality.ServerNames[0], true
		}
		return "", true
	}
	if len(settings.Reality.ServerNames) > 0 {
		return settings.Reality.ServerNames[0], false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(settings.Reality.Target))
	if err != nil {
		return "", false
	}
	if _, err := netip.ParseAddr(host); err == nil { // an IP address is no name to send
		return "", false
	}
	return host, false
}

// hy2SitePort is the TCP port of a Hysteria2 inbound's HTTPS site: settings.masquerade.tcp_port, 443 when omitted, 0 = no
// site (the node's own rule, internal/node/hysteria2/settings.go).
func hy2SitePort(spec plugin.InboundSpec) int {
	if spec.Protocol != "hysteria2" || spec.TLS.Mode == 0 {
		return 0
	}
	var settings struct {
		Masquerade struct {
			TCPPort *int `json:"tcp_port"`
		} `json:"masquerade"`
	}
	if len(spec.Settings) > 0 && json.Unmarshal(spec.Settings, &settings) != nil {
		return 0
	}
	if settings.Masquerade.TCPPort != nil {
		return min(max(*settings.Masquerade.TCPPort, 0), 65535)
	}
	return 443
}

// runTCPCheck runs the TCP half for a node whose agent is connected. ran is false when there was nothing to do or nobody to
// send, in which case the stored rows stay as they were. The rows come back whether or not they could be stored.
func (f *Fleet) runTCPCheck(ctx context.Context, target store.NodeRow, allowPanel bool) (rows []store.TCPCheck, ran bool) {
	inbounds, err := f.st.FleetInbounds(ctx, target.ID, false)
	if err != nil {
		f.log.Warn("list inbounds for TCP port check", "node", target.ID, "err", err)
		return nil, false
	}
	var specs []tcpSpec
	pins := make(map[string]string)
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		spec, err := f.buildSpec(target, inbound)
		if err != nil {
			continue
		}
		specs = append(specs, tcpSpec{Profile: inbound.ProfileName, Spec: spec})
		if pin, ok := protocols.NormalizePin(inbound.CertPin); ok {
			pins[inbound.ID] = pin
		}
	}
	targets := tcpTargets(specs, pins)
	if len(targets) == 0 {
		return nil, false
	}

	liveRows, err := f.Live(ctx)
	if err != nil {
		f.log.Warn("read live nodes for TCP port check", "node", target.ID, "err", err)
		return nil, false
	}
	live := liveRowsByID(liveRows)
	targetLive := live[target.ID]
	if !targetLive.Connected {
		return nil, false
	}
	allNodes, err := f.st.Nodes(ctx, false)
	if err != nil {
		f.log.Warn("list nodes for TCP port check", "node", target.ID, "err", err)
		return nil, false
	}
	senders := f.portSenders(target, allNodes, live, capTCPCheck)
	if len(senders) > 2 {
		senders = senders[:2]
	}
	if len(senders) == 0 {
		// The VPS panel is the sender of last resort, but never against its own host.
		sameHost := false
		if address, err := udpcheck.Resolve(ctx, target.Address); err == nil {
			sameHost = panelHasAddress(address)
		}
		if !allowPanel || sameHost {
			return nil, false
		}
		senders = []portSender{{panel: true}}
	}

	// Port 80 needs the target to arm a mark first; a target that cannot, or has nothing serving, drops out of the probe.
	checkedAt := f.now().UTC()
	var token, answer [16]byte
	rows = make([]store.TCPCheck, 0, len(targets))
	probed := make([]tcpTarget, 0, len(targets))
	for _, t := range targets {
		if t.Kind != tcpcheck.KindHTTPMark {
			probed = append(probed, t)
			continue
		}
		verdict, reason := f.armTCPMark(ctx, target, targetLive, &token, &answer)
		switch {
		case verdict == "off":
			// the agent has no ACME name yet (the inbound is not applied): there is no port 80 to say anything about
		case verdict == "serving":
			probed = append(probed, t)
		default:
			rows = append(rows, tcpRow(target.ID, t, verdict, reason, "", checkedAt))
		}
	}

	var sender portSender
	var verdicts map[uint16]string
	errorCode := ""
	if len(probed) > 0 {
		// a second sender takes over when the first could not run the probe (busy, no route, lost)
		for i, candidate := range senders {
			sender = candidate
			verdicts, errorCode = f.probeTCPTargets(ctx, target, sender, probed, token[:], answer[:])
			if errorCode == "" || i == len(senders)-1 {
				break
			}
		}
	}
	for _, t := range probed {
		verdict, reason := "", errorCode
		if errorCode == "" {
			if verdict = verdicts[t.Port]; !validTCPVerdict(verdict) {
				verdict, reason = "", "failed"
			}
		}
		rows = append(rows, tcpRow(target.ID, t, verdict, reason, portSenderID(sender), checkedAt))
	}
	sort.Slice(rows, func(i, j int) bool { return tcpRowOrder(rows[i].Port) < tcpRowOrder(rows[j].Port) })

	// Nothing answered at all (every sender failed): keep the last stored result, the rows above only carry the reason.
	conclusive := false
	for _, row := range rows {
		conclusive = conclusive || row.Verdict != ""
	}
	if conclusive {
		if err := f.st.PutTCPChecks(ctx, target.ID, rows); err != nil {
			f.log.Warn("store TCP port checks", "node", target.ID, "err", err)
		}
	}
	return rows, true
}

func tcpRowOrder(port uint16) int {
	if port == 80 {
		return 0
	}
	return int(port)
}

func tcpRow(nodeID string, t tcpTarget, verdict, reason, sender string, at time.Time) store.TCPCheck {
	return store.TCPCheck{NodeID: nodeID, Port: t.Port, Role: t.Role, ALPN: t.ALPN, Profile: t.Profile,
		Verdict: verdict, Reason: reason, Sender: sender, CheckedAt: at}
}

func validTCPVerdict(v string) bool {
	switch v {
	case tcpcheck.VerdictOurs, tcpcheck.VerdictOther, tcpcheck.VerdictRefused, tcpcheck.VerdictTimeout:
		return true
	}
	return false
}

// armTCPMark asks the target to answer one marked request on port 80. It returns "serving" when armed, "off" when the node
// has no ACME name, "busy" when another program holds port 80 (a verdict of the row), or "" with the reason for the row.
func (f *Fleet) armTCPMark(ctx context.Context, target store.NodeRow, live store.NodeLiveRow, token, answer *[16]byte) (verdict, reason string) {
	if !slices.Contains(live.AgentCaps, capTCPCheck) {
		return "", "agent_too_old"
	}
	if _, err := rand.Read(token[:]); err != nil {
		return "", "failed"
	}
	if _, err := rand.Read(answer[:]); err != nil {
		return "", "failed"
	}
	result, err := f.commandWithLive(ctx, target.ID, tcpMarkWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_HttpMark{HttpMark: &agentv1.HttpMark{
			RequestId: reqID, Token: token[:], Answer: answer[:], HoldS: tcpMarkHold,
		}}}
	}, &live)
	if err != nil {
		return "", "failed"
	}
	if result == nil || !result.Ok {
		return "", portCommandError(result)
	}
	switch state := result.Params["http01"]; state {
	case "serving", "off":
		return state, ""
	case "busy":
		return "busy", ""
	default:
		return "", "failed"
	}
}

// probeTCPTargets has one sender probe the targets and returns the verdict of each port, or an error code of the same
// words as the UDP part (busy, no_route, failed).
func (f *Fleet) probeTCPTargets(ctx context.Context, target store.NodeRow, sender portSender, targets []tcpTarget, token, answer []byte) (map[uint16]string, string) {
	probes := make([]tcpcheck.Target, len(targets))
	wire := make([]*agentv1.TcpTarget, len(targets))
	for i, t := range targets {
		probes[i] = tcpcheck.Target{Port: t.Port, Kind: t.Kind, SNI: t.SNI, PinSHA256: t.Pin}
		wire[i] = &agentv1.TcpTarget{Port: uint32(t.Port), Kind: t.Kind, Sni: t.SNI, PinSha256: t.Pin}
		if t.Kind == tcpcheck.KindHTTPMark {
			probes[i].Token, probes[i].Answer = token, answer
			wire[i].Token, wire[i].Answer = token, answer
		}
	}
	if sender.panel {
		probe := f.probeTCP
		if probe == nil {
			probe = tcpcheck.Probe
		}
		_, verdicts, err := probe(ctx, target.Address, probes)
		if err != nil {
			return nil, tcpProbeErrorCode(err)
		}
		return verdicts, ""
	}
	result, err := f.commandWithLive(ctx, sender.node.ID, tcpProbeWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_TcpProbe{TcpProbe: &agentv1.TcpProbe{
			RequestId: reqID, Host: target.Address, Targets: wire,
		}}}
	}, &sender.live)
	if err != nil {
		return nil, "failed"
	}
	if result == nil || !result.Ok {
		return nil, portCommandError(result)
	}
	verdicts := make(map[uint16]string, len(targets))
	for _, t := range targets {
		verdicts[t.Port] = result.Params["t"+strconv.Itoa(int(t.Port))]
	}
	return verdicts, ""
}

func tcpProbeErrorCode(err error) string {
	switch {
	case errors.Is(err, tcpcheck.ErrNoRoute), errors.Is(err, tcpcheck.ErrUnsupportedHost):
		return "no_route"
	default:
		return "failed"
	}
}

// tcpCheckMessages turns stored or fresh rows into the admin messages, naming the sending node.
func (f *Fleet) tcpCheckMessages(ctx context.Context, rows []store.TCPCheck) ([]*adminv1.TcpCheck, error) {
	var names map[string]string
	out := make([]*adminv1.TcpCheck, 0, len(rows))
	for _, row := range rows {
		sender := row.Sender
		if sender != "" && sender != "panel" {
			if names == nil {
				nodes, err := f.st.Nodes(ctx, true)
				if err != nil {
					return nil, err
				}
				names = make(map[string]string, len(nodes))
				for _, node := range nodes {
					names[node.ID] = node.Name
				}
			}
			if sender = names[row.Sender]; sender == "" {
				sender = "unknown"
			}
		}
		out = append(out, &adminv1.TcpCheck{Port: uint32(row.Port), Role: row.Role, Alpn: row.ALPN, Profile: row.Profile,
			Verdict: row.Verdict, Reason: row.Reason, Sender: sender, CheckedUnix: row.CheckedAt.Unix()})
	}
	return out, nil
}
